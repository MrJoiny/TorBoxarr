package api

import (
	"fmt"
	"html"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mrjoiny/torboxarr/internal/compat"
	"github.com/mrjoiny/torboxarr/internal/store"
)

func (s *Server) handleSABAPI(w http.ResponseWriter, r *http.Request) {
	// Extract mode and apikey from query parameters (no body parsing needed).
	q := r.URL.Query()
	mode := strings.ToLower(strings.TrimSpace(q.Get("mode")))
	apiKey := firstNonEmpty(q.Get("apikey"), q.Get("nzbkey"))

	// If mode or apikey not in query string and the body is not multipart,
	// do a cheap ParseForm() to extract them from a URL-encoded body.
	// Multipart bodies are intentionally NOT parsed before authentication.
	if mode == "" || apiKey == "" {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			_ = r.ParseForm() // errors are non-fatal here; we only need mode/apikey
			if mode == "" {
				mode = strings.ToLower(strings.TrimSpace(r.FormValue("mode")))
			}
			if apiKey == "" {
				apiKey = firstNonEmpty(r.FormValue("apikey"), r.FormValue("nzbkey"))
			}
		}
	}

	// Authenticate BEFORE any expensive multipart body parsing.
	if !s.sabAuth.Allow(mode, apiKey) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "API Key Incorrect"})
		return
	}

	// Parse body only for modes that accept POST data, and only after auth.
	// NOTE: if a new mode is added that reads r.FormValue(), it must be
	// included in this switch to ensure the body is parsed.
	switch mode {
	case "addurl", "addfile", "queue", "history", "set_config":
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			if err := r.ParseMultipartForm(2 << 20); err != nil { // 2 MB; NZBs are typically < 100 KB
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid multipart body"})
				return
			}
		} else if err := r.ParseForm(); err != nil { // idempotent; no-op if already called above
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid form body"})
			return
		}
	}

	switch mode {
	case "version":
		writeJSON(w, http.StatusOK, map[string]string{"version": s.cfg.Compatibility.SABVersion})
	case "auth":
		writeJSON(w, http.StatusOK, map[string]bool{"auth": true})
	case "get_config":
		s.handleSABGetConfig(w, r)
	case "get_cats":
		s.handleSABGetCats(w, r)
	case "get_scripts":
		s.handleSABGetScripts(w, r)
	case "set_config":
		s.handleSABSetConfig(w, r)
	case "addurl":
		s.handleSABAddURL(w, r)
	case "addfile":
		s.handleSABAddFile(w, r)
	case "queue":
		name := strings.ToLower(strings.TrimSpace(firstNonEmpty(r.FormValue("name"), r.URL.Query().Get("name"))))
		if name == "delete" {
			s.handleSABDeleteFromQueue(w, r)
			return
		}
		s.handleSABQueue(w, r)
	case "history":
		name := strings.ToLower(strings.TrimSpace(firstNonEmpty(r.FormValue("name"), r.URL.Query().Get("name"))))
		if name == "delete" {
			s.handleSABDeleteFromHistory(w, r)
			return
		}
		s.handleSABHistory(w, r)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported mode"})
	}
}

func (s *Server) handleSABGetConfig(w http.ResponseWriter, r *http.Request) {
	categories := s.sabCategoryConfigs(r)
	items := make([]map[string]any, 0, len(categories))
	for i, category := range categories {
		items = append(items, map[string]any{
			"name":     category.Name,
			"order":    i,
			"pp":       "3",
			"script":   "None",
			"dir":      category.Dir,
			"newzbin":  "",
			"priority": "-100",
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"config": map[string]any{
			"misc": map[string]any{
				"download_dir": s.cfg.Data.Staging,
				"complete_dir": s.cfg.Data.Completed,
				"dirscan_dir":  "",
				"script_dir":   "",
			},
			"categories": items,
			"servers":    []any{},
			"rss":        []any{},
			"sorters":    []any{},
			"scripts":    []string{"None"},

			// Keep flat fields too in case a client expects them directly.
			"download_dir": s.cfg.Data.Staging,
			"complete_dir": s.cfg.Data.Completed,
		},
	})
}

func (s *Server) handleSABGetCats(w http.ResponseWriter, r *http.Request) {
	categories := s.sabCategoryNames(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"categories": categories,
	})
}

func (s *Server) handleSABGetScripts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"scripts": []string{"None"},
	})
}

func (s *Server) handleSABSetConfig(w http.ResponseWriter, r *http.Request) {
	section := strings.ToLower(strings.TrimSpace(firstNonEmpty(r.FormValue("section"), r.URL.Query().Get("section"))))
	if section != "categories" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported section"})
		return
	}

	name := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		r.FormValue("keyword"),
		r.URL.Query().Get("keyword"),
		r.FormValue("name"),
		r.URL.Query().Get("name"),
	)))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "category name is required"})
		return
	}

	dir := strings.TrimSpace(firstNonEmpty(r.FormValue("dir"), r.URL.Query().Get("dir")))
	if name != "*" && dir == "" {
		dir = name
	}
	if dir != "" {
		if err := os.MkdirAll(filepath.Join(s.cfg.Data.Completed, dir), 0o755); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}

	s.handleSABGetConfig(w, r)
}

func (s *Server) handleSABConfigCategories(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/config/categories" && r.URL.Path != "/config/categories/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	categories := s.sabCategoryConfigs(r)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	var b strings.Builder
	b.WriteString("<!doctype html><html><head><meta charset=\"utf-8\"><title>SABnzbd Categories</title></head><body>")
	b.WriteString("<h1>SABnzbd Categories</h1>")
	b.WriteString("<p>This compatibility view exists so Sonarr's SABnzbd help link does not 404. Category validation uses <code>mode=get_config</code>, not this page.</p>")
	b.WriteString("<p>Current categories:</p><ul>")
	for _, category := range categories {
		b.WriteString("<li><strong>")
		b.WriteString(html.EscapeString(category.Name))
		b.WriteString("</strong>")
		if category.Dir == "" {
			b.WriteString(" - default path")
		} else {
			b.WriteString(" - ")
			b.WriteString(html.EscapeString(filepath.Join(s.cfg.Data.Completed, category.Dir)))
		}
		b.WriteString("</li>")
	}
	b.WriteString("</ul>")
	b.WriteString("<p>To add a custom category, create a subfolder under <code>")
	b.WriteString(html.EscapeString(s.cfg.Data.Completed))
	b.WriteString("</code> or call <code>/sabnzbd/api?mode=set_config&amp;section=categories&amp;name=YOUR_CATEGORY&amp;apikey=...</code>.</p>")
	b.WriteString("</body></html>")
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) handleSABAddURL(w http.ResponseWriter, r *http.Request) {
	category := firstNonEmpty(r.FormValue("cat"), r.URL.Query().Get("cat"))
	link := firstNonEmpty(r.FormValue("name"), r.URL.Query().Get("name"), r.FormValue("url"), r.URL.Query().Get("url"))
	name := firstNonEmpty(r.FormValue("nzbname"), r.URL.Query().Get("nzbname"), r.FormValue("title"), r.URL.Query().Get("title"))
	postProcessing := parseInt(firstNonEmpty(r.FormValue("pp"), r.URL.Query().Get("pp")), -1)
	password := firstNonEmpty(r.FormValue("password"), r.URL.Query().Get("password"))

	job, _, err := s.enqueueSubmission(r.Context(), SubmissionRequest{
		SourceType:  store.SourceTypeNZB,
		ClientKind:  store.ClientKindSAB,
		Category:    category,
		DisplayName: name,
		SourceURI:   link,
		Metadata: store.SubmissionMetadata{
			PostProcessing: postProcessing,
			Password:       password,
		},
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"status": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, compat.SABAddResponse{Status: true, NzoIDs: []string{compat.SABNZOID(job.PublicID)}})
}

func (s *Server) handleSABAddFile(w http.ResponseWriter, r *http.Request) {
	category := firstNonEmpty(r.FormValue("cat"), r.URL.Query().Get("cat"))
	postProcessing := parseInt(firstNonEmpty(r.FormValue("pp"), r.URL.Query().Get("pp")), -1)
	password := firstNonEmpty(r.FormValue("password"), r.URL.Query().Get("password"))
	jobName := firstNonEmpty(r.FormValue("nzbname"), r.URL.Query().Get("nzbname"))

	var ids []string
	if r.MultipartForm != nil {
		for _, header := range multipartFiles(r.MultipartForm, "nzbfile", "name", "file") {
			file, err := header.Open()
			if err != nil {
				s.log.Warn("sab file open failed",
					"filename", header.Filename,
					"error", err.Error(),
				)
				continue
			}
			displayName := header.Filename
			if jobName != "" {
				displayName = jobName
			}
			job, _, err := s.enqueueSubmission(r.Context(), SubmissionRequest{
				SourceType:  store.SourceTypeNZB,
				ClientKind:  store.ClientKindSAB,
				Category:    category,
				DisplayName: displayName,
				PayloadName: header.Filename,
				PayloadBody: file,
				Metadata: store.SubmissionMetadata{
					UploadedFilename: header.Filename,
					OriginalFilename: header.Filename,
					PostProcessing:   postProcessing,
					Password:         password,
				},
			})
			file.Close()
			if err == nil {
				ids = append(ids, compat.SABNZOID(job.PublicID))
			} else {
				s.log.Warn("sab file submission failed",
					"filename", header.Filename,
					"category", category,
					"error", err.Error(),
				)
			}
		}
	}

	if len(ids) == 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"status": false, "error": "no nzb files accepted"})
		return
	}
	writeJSON(w, http.StatusOK, compat.SABAddResponse{Status: true, NzoIDs: ids})
}

func (s *Server) handleSABQueue(w http.ResponseWriter, r *http.Request) {
	category := strings.TrimSpace(firstNonEmpty(r.FormValue("cat"), r.URL.Query().Get("cat"), r.FormValue("category"), r.URL.Query().Get("category")))
	if category == "*" {
		category = ""
	}
	jobs, err := s.store.ListVisibleClientJobs(r.Context(), store.ClientKindSAB, category, 1000)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, compat.ProjectSABQueue(s.cfg.Compatibility.SABVersion, jobs))
}

func (s *Server) handleSABHistory(w http.ResponseWriter, r *http.Request) {
	category := strings.TrimSpace(firstNonEmpty(r.FormValue("cat"), r.URL.Query().Get("cat"), r.FormValue("category"), r.URL.Query().Get("category")))
	if category == "*" {
		category = ""
	}
	jobs, err := s.store.ListVisibleClientJobs(r.Context(), store.ClientKindSAB, category, 1000)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, compat.ProjectSABHistory(s.cfg.Compatibility.SABVersion, jobs))
}

func (s *Server) handleSABDeleteFromQueue(w http.ResponseWriter, r *http.Request) {
	value := firstNonEmpty(r.FormValue("value"), r.URL.Query().Get("value"))
	ids := splitCSV(value)
	for _, id := range ids {
		if err := s.markRemovePending(r.Context(), compat.NormalizeSABNZOID(id)); err != nil {
			s.log.Warn("sab queue delete failed", "nzo_id", id, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": true, "nzo_ids": ids})
}

func (s *Server) handleSABDeleteFromHistory(w http.ResponseWriter, r *http.Request) {
	value := firstNonEmpty(r.FormValue("value"), r.URL.Query().Get("value"))
	ids := splitCSV(value)
	for _, id := range ids {
		if err := s.markRemovePending(r.Context(), compat.NormalizeSABNZOID(id)); err != nil {
			s.log.Warn("sab history delete failed", "nzo_id", id, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": true, "nzo_ids": ids})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func splitCSV(v string) []string {
	raw := strings.Split(strings.TrimSpace(v), ",")
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" || item == "all" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func pick(values ...string) string {
	return firstNonEmpty(values...)
}

func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseInt(v string, fallback int) int {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	var out int
	if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &out); err == nil {
		return out
	}
	return fallback
}

func multipartFiles(form *multipart.Form, keys ...string) []*multipart.FileHeader {
	if form == nil {
		return nil
	}
	var files []*multipart.FileHeader
	for _, key := range keys {
		files = append(files, form.File[key]...)
	}
	return files
}

type sabCategoryConfig struct {
	Name string
	Dir  string
}

func (s *Server) sabCategoryNames(r *http.Request) []string {
	configs := s.sabCategoryConfigs(r)
	names := make([]string, 0, len(configs))
	for _, category := range configs {
		names = append(names, category.Name)
	}
	return names
}

func (s *Server) sabCategoryConfigs(r *http.Request) []sabCategoryConfig {
	jobs, err := s.store.ListVisibleClientJobs(r.Context(), store.ClientKindSAB, "", 1000)
	if err != nil {
		return s.defaultSABCategories()
	}

	seen := map[string]sabCategoryConfig{}
	add := func(name, dir string) {
		name = strings.TrimSpace(name)
		dir = strings.TrimSpace(dir)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = sabCategoryConfig{Name: name, Dir: dir}
	}

	for _, category := range s.defaultSABCategories() {
		add(category.Name, category.Dir)
	}

	entries, err := os.ReadDir(s.cfg.Data.Completed)
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := strings.TrimSpace(entry.Name())
			if name == "" {
				continue
			}
			add(name, name)
		}
	}

	for _, job := range jobs {
		add(job.Category, job.Category)
	}

	categories := make([]sabCategoryConfig, 0, len(seen))
	defaultCategory, ok := seen["*"]
	if ok {
		categories = append(categories, defaultCategory)
		delete(seen, "*")
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		categories = append(categories, seen[name])
	}
	return categories
}

func (s *Server) defaultSABCategories() []sabCategoryConfig {
	defaultCategory := strings.TrimSpace(s.cfg.Compatibility.DefaultCategory)
	categories := []sabCategoryConfig{
		{Name: "*"},
		{Name: "movies", Dir: "movies"},
		{Name: "tv", Dir: "tv"},
		{Name: "audio", Dir: "audio"},
		{Name: "software", Dir: "software"},
	}
	if defaultCategory == "" || defaultCategory == "*" {
		return categories
	}
	return append(categories, sabCategoryConfig{Name: defaultCategory, Dir: defaultCategory})
}
