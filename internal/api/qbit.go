package api

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mrjoiny/torboxarr/internal/auth"
	"github.com/mrjoiny/torboxarr/internal/compat"
	"github.com/mrjoiny/torboxarr/internal/store"
)

func (s *Server) handleQBitLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Fails.", http.StatusBadRequest)
		return
	}
	sid, err := s.qbitAuth.Login(r.Context(), r.PostForm.Get("username"), r.PostForm.Get("password"))
	if err != nil {
		s.log.Warn("qbit login failed",
			"remote_addr", r.RemoteAddr,
			"username", r.PostForm.Get("username"),
			"error", err.Error(),
		)
		http.Error(w, "Fails.", http.StatusOK)
		return
	}
	auth.SetQBitCookie(w, sid, qbitCookieSecure(s.cfg.Server.BaseURL))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("Ok."))
}

func (s *Server) handleQBitLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.QBitSessionCookie); err == nil {
		_ = s.qbitAuth.Logout(r.Context(), cookie.Value)
	}
	auth.ClearQBitCookie(w, qbitCookieSecure(s.cfg.Server.BaseURL))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("Ok."))
}

func (s *Server) handleQBitVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(s.cfg.Compatibility.QBitVersion))
}

func (s *Server) handleQBitWebAPIVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(s.cfg.Compatibility.QBitWebAPI))
}

func (s *Server) handleQBitPreferences(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"save_path":            s.cfg.Data.Completed,
		"temp_path_enabled":    false,
		"start_paused_enabled": false,
	})
}

func (s *Server) handleQBitDefaultSavePath(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(s.cfg.Data.Completed))
}

func (s *Server) handleQBitCategories(w http.ResponseWriter, r *http.Request) {
	payload, err := s.qbitCategories(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleQBitCreateCategory(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	category := strings.TrimSpace(r.PostForm.Get("category"))
	if category == "" {
		http.Error(w, "category is required", http.StatusBadRequest)
		return
	}

	savePath := strings.TrimSpace(r.PostForm.Get("savePath"))
	if savePath == "" {
		savePath = filepath.Join(s.cfg.Data.Completed, category)
	}
	if err := os.MkdirAll(savePath, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleQBitTransferInfo(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListVisibleClientJobs(r.Context(), store.ClientKindQBit, "", 1000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jobs, err = s.withLocalTransferProgress(r.Context(), jobs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, compat.ProjectQBitTransferInfo(jobs))
}

func (s *Server) handleQBitAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(8 << 20); err != nil { // 8 MB; torrent files are typically < 1 MB
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	category := strings.TrimSpace(r.PostFormValue("category"))
	rename := strings.TrimSpace(r.PostFormValue("rename"))
	urlsRaw := strings.TrimSpace(r.PostFormValue("urls"))
	created := 0

	metadata := store.SubmissionMetadata{
		SavePath:     r.PostFormValue("savepath"),
		Rename:       rename,
		Tags:         splitCSV(r.PostFormValue("tags")),
		SkipChecking: parseBool(r.PostFormValue("skip_checking")),
		Paused:       parseBool(r.PostFormValue("paused")),
		RootFolder:   parseBool(r.PostFormValue("root_folder")),
	}

	if urlsRaw != "" {
		lines := strings.FieldsFunc(urlsRaw, func(r rune) bool {
			return r == '\n' || r == '\r'
		})
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			infoHash := extractInfoHash(line)
			displayName := rename
			if displayName == "" {
				displayName = firstNonEmpty(infoHash, line)
			}
			if _, _, err := s.enqueueSubmission(r.Context(), SubmissionRequest{
				SourceType:  store.SourceTypeTorrent,
				ClientKind:  store.ClientKindQBit,
				Category:    category,
				DisplayName: displayName,
				SourceURI:   line,
				InfoHash:    infoHash,
				Metadata:    metadata,
			}); err == nil {
				created++
			} else {
				s.log.Warn("qbit url submission failed",
					"url", line,
					"category", category,
					"error", err.Error(),
				)
			}
		}
	}

	if r.MultipartForm != nil {
		for _, header := range r.MultipartForm.File["torrents"] {
			file, err := header.Open()
			if err != nil {
				s.log.Warn("qbit file open failed",
					"filename", header.Filename,
					"error", err.Error(),
				)
				continue
			}
			_, _, err = s.enqueueSubmission(r.Context(), SubmissionRequest{
				SourceType:  store.SourceTypeTorrent,
				ClientKind:  store.ClientKindQBit,
				Category:    category,
				DisplayName: pick(rename, header.Filename),
				PayloadName: header.Filename,
				PayloadBody: file,
				Metadata: func() store.SubmissionMetadata {
					m := metadata
					m.UploadedFilename = header.Filename
					m.OriginalFilename = header.Filename
					return m
				}(),
			})
			file.Close()
			if err == nil {
				created++
			} else {
				s.log.Warn("qbit file submission failed",
					"filename", header.Filename,
					"category", category,
					"error", err.Error(),
				)
			}
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if created == 0 {
		_, _ = w.Write([]byte("Fails."))
		return
	}
	_, _ = w.Write([]byte("Ok."))
}

func (s *Server) handleQBitInfo(w http.ResponseWriter, r *http.Request) {
	category := strings.TrimSpace(r.URL.Query().Get("category"))
	jobs, err := s.store.ListVisibleClientJobs(r.Context(), store.ClientKindQBit, category, 1000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jobs, err = s.withLocalTransferProgress(r.Context(), jobs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	hashes := splitPipe(r.URL.Query().Get("hashes"))
	jobs = filterJobsByPublicIDs(jobs, hashes)
	sort.SliceStable(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})

	payload := make([]compat.QBitTorrentInfo, 0, len(jobs))
	for _, job := range jobs {
		payload = append(payload, compat.ProjectQBitTorrent(job))
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleQBitDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	hashes := splitPipe(r.PostForm.Get("hashes"))
	var lastErr error
	accepted := 0
	for _, hash := range hashes {
		if err := s.markRemovePending(r.Context(), hash); err != nil {
			s.log.Warn("qbit delete failed", "hash", hash, "error", err)
			lastErr = err
		} else {
			accepted++
		}
	}
	if accepted == 0 && lastErr != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func qbitCookieSecure(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	return err == nil && strings.EqualFold(parsed.Scheme, "https")
}

func splitPipe(v string) []string {
	raw := strings.Split(strings.TrimSpace(v), "|")
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

func (s *Server) qbitCategories(r *http.Request) (map[string]compat.QBitCategory, error) {
	payload := map[string]compat.QBitCategory{}

	entries, err := os.ReadDir(s.cfg.Data.Completed)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		category := strings.TrimSpace(entry.Name())
		if category == "" {
			continue
		}
		payload[category] = compat.ProjectQBitCategory(category, filepath.Join(s.cfg.Data.Completed, category))
	}

	jobs, err := s.store.ListVisibleClientJobs(r.Context(), store.ClientKindQBit, "", 1000)
	if err != nil {
		return nil, err
	}
	for _, job := range jobs {
		if _, ok := payload[job.Category]; ok {
			continue
		}
		payload[job.Category] = compat.ProjectQBitCategory(job.Category, filepath.Join(s.cfg.Data.Completed, job.Category))
	}

	if len(payload) == 0 {
		category := s.cfg.Compatibility.DefaultCategory
		payload[category] = compat.ProjectQBitCategory(category, filepath.Join(s.cfg.Data.Completed, category))
	}

	return payload, nil
}
