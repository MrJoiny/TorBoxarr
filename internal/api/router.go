package api

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mrjoiny/torboxarr/internal/auth"
	"github.com/mrjoiny/torboxarr/internal/config"
	"github.com/mrjoiny/torboxarr/internal/files"
	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/util"
)

type Server struct {
	cfg      *config.Config
	log      *slog.Logger
	store    *store.Store
	layout   *files.Layout
	qbitAuth *auth.QBitSessionManager
	sabAuth  *auth.SABAuth
}

type SubmissionRequest struct {
	SourceType  store.SourceType
	ClientKind  store.ClientKind
	Category    string
	DisplayName string
	SourceURI   string
	InfoHash    string
	PayloadName string
	PayloadBody io.Reader
	Metadata    store.SubmissionMetadata
}

func NewServer(cfg *config.Config, log *slog.Logger, st *store.Store, layout *files.Layout, qbitAuth *auth.QBitSessionManager, sabAuth *auth.SABAuth) *Server {
	return &Server{
		cfg:      cfg,
		log:      log,
		store:    st,
		layout:   layout,
		qbitAuth: qbitAuth,
		sabAuth:  sabAuth,
	}
}

func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", getOnly(http.HandlerFunc(s.handleHealth)))

	mux.Handle("/api/v2/auth/login", postOnly(http.HandlerFunc(s.handleQBitLogin)))
	mux.Handle("/api/v2/auth/logout", postOnly(http.HandlerFunc(s.handleQBitLogout)))
	mux.Handle("/api/v2/app/version", getOnly(s.qbitProtected(http.HandlerFunc(s.handleQBitVersion))))
	mux.Handle("/api/v2/app/webapiVersion", getOnly(s.qbitProtected(http.HandlerFunc(s.handleQBitWebAPIVersion))))
	mux.Handle("/api/v2/app/preferences", getOnly(s.qbitProtected(http.HandlerFunc(s.handleQBitPreferences))))
	mux.Handle("/api/v2/app/defaultSavePath", getOnly(s.qbitProtected(http.HandlerFunc(s.handleQBitDefaultSavePath))))
	mux.Handle("/api/v2/torrents/add", postOnly(s.qbitProtected(http.HandlerFunc(s.handleQBitAdd))))
	mux.Handle("/api/v2/torrents/info", getOnly(s.qbitProtected(http.HandlerFunc(s.handleQBitInfo))))
	mux.Handle("/api/v2/torrents/delete", postOnly(s.qbitProtected(http.HandlerFunc(s.handleQBitDelete))))
	mux.Handle("/api/v2/torrents/categories", getOnly(s.qbitProtected(http.HandlerFunc(s.handleQBitCategories))))
	mux.Handle("/api/v2/torrents/createCategory", postOnly(s.qbitProtected(http.HandlerFunc(s.handleQBitCreateCategory))))
	mux.Handle("/api/v2/transfer/info", getOnly(s.qbitProtected(http.HandlerFunc(s.handleQBitTransferInfo))))

	mux.HandleFunc("/api", s.handleSABAPI)
	mux.HandleFunc("/sabnzbd/api", s.handleSABAPI)
	mux.HandleFunc("/config/categories", s.handleSABConfigCategories)
	mux.HandleFunc("/config/categories/", s.handleSABConfigCategories)

	return withLogging(s.log, mux)
}

func (s *Server) qbitProtected(next http.Handler) http.Handler {
	return s.qbitAuth.Middleware(next)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"time": time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) enqueueSubmission(ctx context.Context, req SubmissionRequest) (*store.Job, bool, error) {
	category := strings.TrimSpace(req.Category)
	if category == "" {
		category = s.cfg.Compatibility.DefaultCategory
	}

	jobID, err := util.RandomHex(16)
	if err != nil {
		return nil, false, err
	}
	publicID := util.SHA1Hex(jobID)

	payloadRef := ""
	payloadDigest := ""
	if req.PayloadBody != nil {
		name := req.PayloadName
		if name == "" {
			name = req.DisplayName
		}
		payloadRef, payloadDigest, err = s.layout.SavePayload(jobID, name, req.PayloadBody)
		if err != nil {
			return nil, false, err
		}
	}

	sourceURI := strings.TrimSpace(req.SourceURI)
	infoHash := normalizeInfoHash(req.InfoHash)
	submissionKey := submissionFingerprint(req.SourceType, req.ClientKind, category, sourceURI, infoHash, payloadDigest)
	active, err := s.store.FindActiveBySubmissionKey(ctx, submissionKey)
	if err != nil {
		return nil, false, err
	}
	if active != nil {
		if payloadRef != "" {
			_ = s.layout.RemovePath(filepath.Dir(payloadRef))
		}
		s.log.Info("duplicate submission ignored",
			"job_id", active.ID,
			"public_id", active.PublicID,
			"client", req.ClientKind,
			"source_type", req.SourceType,
			"category", category,
		)
		return active, true, nil
	}

	stagingPath := s.layout.StagingPathForJob(jobID)
	now := time.Now().UTC()
	displayName := deriveDisplayName(req.DisplayName, sourceURI, req.PayloadName, infoHash, jobID)

	job := &store.Job{
		ID:            jobID,
		PublicID:      publicID,
		SourceType:    req.SourceType,
		ClientKind:    req.ClientKind,
		Category:      category,
		State:         store.StateAccepted,
		SubmissionKey: submissionKey,
		DisplayName:   displayName,
		BytesTotal:    0,
		BytesDone:     0,
		Metadata:      req.Metadata,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if sourceURI != "" {
		job.SourceURI = &sourceURI
	}
	if infoHash != "" {
		job.InfoHash = &infoHash
	}
	if payloadRef != "" {
		job.PayloadRef = &payloadRef
	}
	job.StagingPath = &stagingPath

	if err := s.store.CreateJob(ctx, job); err != nil {
		return nil, false, err
	}
	s.log.Info("job accepted",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"client", job.ClientKind,
		"source_type", job.SourceType,
		"category", job.Category,
		"display_name", job.DisplayName,
		"has_payload", job.PayloadRef != nil,
		"has_source_uri", job.SourceURI != nil,
	)
	job.NextRunAt = &now
	if err := s.store.UpdateJobState(ctx, job, store.StateSubmitPending, "queued for remote submission"); err != nil {
		return nil, false, err
	}
	s.log.Debug("job queued for remote submission",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"next_run_at", job.NextRunAt.Format(time.RFC3339Nano),
	)
	return job, false, nil
}

func (s *Server) markRemovePending(ctx context.Context, publicID string) error {
	job, err := s.store.GetJobByPublicID(ctx, publicID)
	if err != nil {
		return err
	}
	if job == nil {
		return nil
	}
	if job.State == store.StateRemoved || job.State == store.StateRemovePending {
		return nil
	}
	job.DeleteRequested = true
	now := time.Now().UTC()
	job.NextRunAt = &now
	job.UpdatedAt = now
	s.log.Info("job marked for removal", "job_id", job.ID, "public_id", job.PublicID, "state", job.State)
	return s.store.UpdateJobState(ctx, job, store.StateRemovePending, "remove requested via Arr-compatible API")
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func submissionFingerprint(sourceType store.SourceType, clientKind store.ClientKind, category, sourceURI, infoHash, payloadDigest string) string {
	parts := []string{
		string(sourceType),
		string(clientKind),
		strings.TrimSpace(category),
		normalizeSourceURI(sourceURI),
		normalizeInfoHash(infoHash),
		strings.TrimSpace(payloadDigest),
	}
	return util.SHA1Hex(strings.Join(parts, "|"))
}

func normalizeSourceURI(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	parsed, err := url.Parse(v)
	if err != nil {
		return strings.ToLower(v)
	}
	parsed.Fragment = ""
	values := parsed.Query()
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := url.Values{}
	for _, key := range keys {
		items := append([]string(nil), values[key]...)
		sort.Strings(items)
		for _, item := range items {
			out.Add(key, item)
		}
	}
	parsed.RawQuery = out.Encode()
	return strings.ToLower(parsed.String())
}

func normalizeInfoHash(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func extractInfoHash(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}

	if parsed, err := url.Parse(source); err == nil && strings.EqualFold(parsed.Scheme, "magnet") {
		for _, xt := range parsed.Query()["xt"] {
			const prefix = "urn:btih:"
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(xt)), prefix) {
				return normalizeBTIH(xt[len(prefix):])
			}
		}
	}

	return normalizeBTIH(source)
}

func normalizeBTIH(v string) string {
	v = strings.TrimSpace(v)
	switch len(v) {
	case 40:
		normalized := strings.ToLower(v)
		if _, err := hex.DecodeString(normalized); err == nil {
			return normalized
		}
	case 32:
		decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(v))
		if err == nil {
			return strings.ToLower(hex.EncodeToString(decoded))
		}
	}
	return ""
}

func deriveDisplayName(displayName, sourceURI, payloadName, infoHash, fallback string) string {
	switch {
	case strings.TrimSpace(displayName) != "":
		return strings.TrimSpace(displayName)
	case strings.TrimSpace(payloadName) != "":
		return strings.TrimSpace(payloadName)
	case strings.TrimSpace(infoHash) != "":
		return strings.TrimSpace(infoHash)
	case strings.TrimSpace(sourceURI) != "":
		return strings.TrimSpace(sourceURI)
	default:
		return fallback
	}
}

func withLogging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Debug("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"query", sanitizeQuery(r.URL.Query()),
			"status", rec.status,
			"bytes", rec.bytes,
			"remote_addr", r.RemoteAddr,
			"duration", time.Since(started).String(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += n
	return n, err
}

func sanitizeQuery(values url.Values) map[string]string {
	out := make(map[string]string, len(values))
	for key, items := range values {
		switch strings.ToLower(key) {
		case "apikey", "nzbkey", "token", "username", "password", "pass":
			out[key] = "[redacted]"
		default:
			out[key] = strings.Join(items, ",")
		}
	}
	return out
}

func filterJobsByPublicIDs(jobs []*store.Job, hashes []string) []*store.Job {
	if len(hashes) == 0 {
		return jobs
	}
	set := map[string]struct{}{}
	for _, hash := range hashes {
		set[hash] = struct{}{}
	}
	filtered := make([]*store.Job, 0, len(jobs))
	for _, job := range jobs {
		if _, ok := set[job.PublicID]; ok {
			filtered = append(filtered, job)
		}
	}
	return filtered
}

func requireMethod(method string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func getOnly(next http.Handler) http.Handler {
	return requireMethod(http.MethodGet, next)
}

func postOnly(next http.Handler) http.Handler {
	return requireMethod(http.MethodPost, next)
}
