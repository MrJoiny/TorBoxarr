package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/api"
	"github.com/mrjoiny/torboxarr/internal/auth"
	"github.com/mrjoiny/torboxarr/internal/config"
	"github.com/mrjoiny/torboxarr/internal/files"
	"github.com/mrjoiny/torboxarr/internal/store"
)

type testEnv struct {
	router  http.Handler
	store   *store.Store
	qbitMgr *auth.QBitSessionManager
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWithBaseURL(t, "http://localhost")
}

func newTestEnvWithBaseURL(t *testing.T, baseURL string) *testEnv {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, ":memory:", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // :memory: creates a separate DB per connection
	if err := store.RunMigrationsFS(db, store.EmbeddedMigrations); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db)

	tmpDir := t.TempDir()
	layout := files.NewLayout(
		tmpDir,
		filepath.Join(tmpDir, "staging"),
		filepath.Join(tmpDir, "completed"),
		filepath.Join(tmpDir, "payloads"),
	)
	_ = layout.Ensure()

	cfg := testConfig(tmpDir)
	cfg.Server.BaseURL = baseURL
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	qbitMgr := auth.NewQBitSessionManager(st, cfg.Auth.QBitUsername, cfg.Auth.QBitPassword, cfg.Auth.SessionTTL)
	sabAuth := auth.NewSABAuth(cfg.Auth.SABAPIKey, cfg.Auth.SABNZBKey)

	srv := api.NewServer(cfg, logger, st, layout, qbitMgr, sabAuth)

	return &testEnv{
		router:  srv.Router(),
		store:   st,
		qbitMgr: qbitMgr,
	}
}

func testConfig(tmpDir string) *config.Config {
	var cfg config.Config
	cfg.Server.Address = ":0"
	cfg.Server.BaseURL = "http://localhost"
	cfg.Logging.Level = "ERROR"
	cfg.Database.Path = ":memory:"
	cfg.Database.BusyTimeout = 5 * time.Second
	cfg.Data.Root = tmpDir
	cfg.Data.Staging = filepath.Join(tmpDir, "staging")
	cfg.Data.Completed = filepath.Join(tmpDir, "completed")
	cfg.Data.Payloads = filepath.Join(tmpDir, "payloads")
	cfg.TorBox.BaseURL = "https://api.torbox.app/v1"
	cfg.TorBox.APIToken = "test-token"
	cfg.TorBox.UserAgent = "test-agent"
	cfg.TorBox.RequestTimeout = 30 * time.Second
	cfg.Auth.QBitUsername = "admin"
	cfg.Auth.QBitPassword = "password"
	cfg.Auth.SABAPIKey = "sabapikey123"
	cfg.Auth.SABNZBKey = "sabnzbkey456"
	cfg.Auth.SessionTTL = 24 * time.Hour
	cfg.Compatibility.QBitVersion = "5.0.0"
	cfg.Compatibility.QBitWebAPI = "2.11.3"
	cfg.Compatibility.SABVersion = "4.5.1"
	cfg.Compatibility.DefaultCategory = "torboxarr"
	return &cfg
}

func (env *testEnv) loginQBit(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	sid, err := env.qbitMgr.Login(ctx, "admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

func (env *testEnv) qbitRequest(t *testing.T, method, path string, body io.Reader, contentType, sid string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if sid != "" {
		req.AddCookie(&http.Cookie{Name: auth.QBitSessionCookie, Value: sid})
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	return rec
}

// qbitMultipartAdd builds a multipart/form-data POST for /api/v2/torrents/add.
func (env *testEnv) qbitMultipartAdd(t *testing.T, sid string, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	w.Close()
	return env.qbitRequest(t, "POST", "/api/v2/torrents/add", &buf, w.FormDataContentType(), sid)
}

// ─── Health ─────────────────────────────────────────────────────────────────

func TestHealthEndpoint(t *testing.T) {
	env := newTestEnv(t)
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["ok"] != true {
		t.Errorf("ok = %v, want true", resp["ok"])
	}
}

// ─── QBit Login ──────────────────────────────────────────────────────────────

func TestQBitLogin_Success(t *testing.T) {
	env := newTestEnv(t)

	form := url.Values{"username": {"admin"}, "password": {"password"}}
	req := httptest.NewRequest("POST", "/api/v2/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if body != "Ok." {
		t.Errorf("body = %q, want %q", body, "Ok.")
	}
	// Should have SID cookie
	cookies := rec.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == "SID" && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Error("expected SID cookie in response")
	}
	for _, c := range cookies {
		if c.Name == "SID" && c.Secure {
			t.Error("expected insecure SID cookie for plain HTTP base URL")
		}
	}
}

func TestQBitLogin_BadCreds(t *testing.T) {
	env := newTestEnv(t)

	form := url.Values{"username": {"admin"}, "password": {"wrong"}}
	req := httptest.NewRequest("POST", "/api/v2/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	body := strings.TrimSpace(rec.Body.String())
	if body != "Fails." {
		t.Errorf("body = %q, want %q", body, "Fails.")
	}
}

func TestQBitLogin_MethodNotAllowed(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest("GET", "/api/v2/auth/login?username=admin&password=password", nil)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestQBitLogin_SetsSecureCookieForHTTPSBaseURL(t *testing.T) {
	env := newTestEnvWithBaseURL(t, "https://torboxarr.example.com")

	form := url.Values{"username": {"admin"}, "password": {"password"}}
	req := httptest.NewRequest("POST", "/api/v2/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	cookies := rec.Result().Cookies()
	foundSecure := false
	for _, c := range cookies {
		if c.Name == "SID" && c.Secure {
			foundSecure = true
		}
	}
	if !foundSecure {
		t.Error("expected secure SID cookie for HTTPS base URL")
	}
}

// ─── QBit Add URL ────────────────────────────────────────────────────────────

func TestQBitAdd_URL(t *testing.T) {
	env := newTestEnv(t)
	sid := env.loginQBit(t)

	rec := env.qbitMultipartAdd(t, sid, map[string]string{
		"urls":     "magnet:?xt=urn:btih:aaaa1111bbbb2222cccc3333dddd4444eeee5555",
		"category": "movies",
	})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if body != "Ok." {
		t.Errorf("body = %q, want %q", body, "Ok.")
	}

	// Verify job was created in DB
	jobs, err := env.store.ListVisibleClientJobs(context.Background(), store.ClientKindQBit, "movies", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	if jobs[0].Category != "movies" {
		t.Errorf("Category = %q, want %q", jobs[0].Category, "movies")
	}
}

func TestQBitAdd_Unauthenticated(t *testing.T) {
	env := newTestEnv(t)

	rec := env.qbitMultipartAdd(t, "", map[string]string{"urls": "magnet:?xt=urn:btih:abc"})

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestQBitAdd_MethodNotAllowed(t *testing.T) {
	env := newTestEnv(t)
	sid := env.loginQBit(t)

	rec := env.qbitRequest(t, "GET", "/api/v2/torrents/add?urls=magnet:?xt=urn:btih:abc", nil, "", sid)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// ─── QBit Info ───────────────────────────────────────────────────────────────

func TestQBitInfo(t *testing.T) {
	env := newTestEnv(t)
	sid := env.loginQBit(t)

	// Add a job first via multipart
	env.qbitMultipartAdd(t, sid, map[string]string{
		"urls":     "magnet:?xt=urn:btih:aaaa1111bbbb2222cccc3333dddd4444eeee5555",
		"category": "tv",
	})

	// Fetch info
	rec := env.qbitRequest(t, "GET", "/api/v2/torrents/info", nil, "", sid)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var torrents []json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&torrents); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(torrents) != 1 {
		t.Errorf("got %d torrents, want 1", len(torrents))
	}
}

// ─── QBit Delete ────────────────────────────────────────────────────────────

func TestQBitCreateCategory_VisibleInCategories(t *testing.T) {
	env := newTestEnv(t)
	sid := env.loginQBit(t)

	before := env.qbitRequest(t, "GET", "/api/v2/torrents/categories", nil, "", sid)
	if before.Code != http.StatusOK {
		t.Fatalf("initial categories status = %d, want %d", before.Code, http.StatusOK)
	}

	form := url.Values{"category": {"sonarr"}}
	create := env.qbitRequest(t, "POST", "/api/v2/torrents/createCategory", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", sid)
	if create.Code != http.StatusOK {
		t.Fatalf("createCategory status = %d, want %d", create.Code, http.StatusOK)
	}

	after := env.qbitRequest(t, "GET", "/api/v2/torrents/categories", nil, "", sid)
	if after.Code != http.StatusOK {
		t.Fatalf("updated categories status = %d, want %d", after.Code, http.StatusOK)
	}

	var categories map[string]struct {
		Name     string `json:"name"`
		SavePath string `json:"savePath"`
	}
	if err := json.NewDecoder(after.Body).Decode(&categories); err != nil {
		t.Fatalf("decode categories: %v", err)
	}

	category, ok := categories["sonarr"]
	if !ok {
		keys := make([]string, 0, len(categories))
		for key := range categories {
			keys = append(keys, key)
		}
		t.Fatalf("expected created category in response, got keys: %v", keys)
	}
	if category.Name != "sonarr" {
		t.Errorf("category name = %q, want %q", category.Name, "sonarr")
	}
	if category.SavePath == "" {
		t.Error("expected category savePath to be populated")
	}
}

func TestQBitCreateCategory_MethodNotAllowed(t *testing.T) {
	env := newTestEnv(t)
	sid := env.loginQBit(t)

	rec := env.qbitRequest(t, "GET", "/api/v2/torrents/createCategory?category=sonarr", nil, "", sid)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestQBitDelete(t *testing.T) {
	env := newTestEnv(t)
	sid := env.loginQBit(t)

	// Add a job via multipart
	env.qbitMultipartAdd(t, sid, map[string]string{
		"urls":     "magnet:?xt=urn:btih:aaaa1111bbbb2222cccc3333dddd4444eeee5555",
		"category": "movies",
	})

	// Get the job's public ID
	jobs, _ := env.store.ListVisibleClientJobs(context.Background(), store.ClientKindQBit, "", 100)
	if len(jobs) == 0 {
		t.Fatal("no jobs found")
	}
	publicID := jobs[0].PublicID

	// Delete (form-urlencoded is fine for delete)
	delForm := url.Values{"hashes": {publicID}}
	rec := env.qbitRequest(t, "POST", "/api/v2/torrents/delete", strings.NewReader(delForm.Encode()), "application/x-www-form-urlencoded", sid)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Verify state
	job, _ := env.store.GetJobByPublicID(context.Background(), publicID)
	if job.State != store.StateRemovePending {
		t.Errorf("State = %q, want %q", job.State, store.StateRemovePending)
	}
}

func TestQBitDelete_MethodNotAllowed(t *testing.T) {
	env := newTestEnv(t)
	sid := env.loginQBit(t)

	rec := env.qbitRequest(t, "GET", "/api/v2/torrents/delete?hashes=abc", nil, "", sid)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestQBitLogout(t *testing.T) {
	env := newTestEnvWithBaseURL(t, "https://torboxarr.example.com")
	sid := env.loginQBit(t)

	rec := env.qbitRequest(t, "POST", "/api/v2/auth/logout", nil, "", sid)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	foundCleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == "SID" && c.MaxAge == -1 && c.Secure {
			foundCleared = true
		}
	}
	if !foundCleared {
		t.Error("expected cleared secure SID cookie on logout")
	}
}

func TestQBitLogout_MethodNotAllowed(t *testing.T) {
	env := newTestEnv(t)

	rec := env.qbitRequest(t, "GET", "/api/v2/auth/logout", nil, "", "")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// ─── SAB API ────────────────────────────────────────────────────────────────

func TestSABVersion(t *testing.T) {
	env := newTestEnv(t)
	req := httptest.NewRequest("GET", "/sabnzbd/api?mode=version&apikey=sabapikey123", nil)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["version"] != "4.5.1" {
		t.Errorf("version = %q, want %q", resp["version"], "4.5.1")
	}
}

func TestSABAddURL(t *testing.T) {
	env := newTestEnv(t)

	params := url.Values{
		"mode":   {"addurl"},
		"apikey": {"sabapikey123"},
		"name":   {"https://example.com/test.nzb"},
		"cat":    {"tv"},
	}
	req := httptest.NewRequest("GET", "/sabnzbd/api?"+params.Encode(), nil)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != true {
		t.Errorf("status = %v, want true", resp["status"])
	}

	// Verify job in DB
	jobs, err := env.store.ListVisibleClientJobs(context.Background(), store.ClientKindSAB, "tv", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d SAB jobs, want 1", len(jobs))
	}
}

func TestSABAddURL_NoAuth(t *testing.T) {
	env := newTestEnv(t)

	params := url.Values{
		"mode": {"addurl"},
		"name": {"https://example.com/test.nzb"},
	}
	req := httptest.NewRequest("GET", "/sabnzbd/api?"+params.Encode(), nil)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d for missing API key", rec.Code, http.StatusForbidden)
	}
}

func TestSABQueue(t *testing.T) {
	env := newTestEnv(t)

	// Add a job via SAB API
	params := url.Values{
		"mode":   {"addurl"},
		"apikey": {"sabapikey123"},
		"name":   {"https://example.com/queue-test.nzb"},
	}
	req := httptest.NewRequest("GET", "/sabnzbd/api?"+params.Encode(), nil)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	// Fetch queue
	queueParams := url.Values{
		"mode":   {"queue"},
		"apikey": {"sabapikey123"},
	}
	req = httptest.NewRequest("GET", "/sabnzbd/api?"+queueParams.Encode(), nil)
	rec = httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]json.RawMessage
	json.NewDecoder(rec.Body).Decode(&resp)
	if _, ok := resp["queue"]; !ok {
		t.Error("expected 'queue' key in response")
	}
}

func TestSABHistory(t *testing.T) {
	env := newTestEnv(t)

	histParams := url.Values{
		"mode":   {"history"},
		"apikey": {"sabapikey123"},
	}
	req := httptest.NewRequest("GET", "/sabnzbd/api?"+histParams.Encode(), nil)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]json.RawMessage
	json.NewDecoder(rec.Body).Decode(&resp)
	if _, ok := resp["history"]; !ok {
		t.Error("expected 'history' key in response")
	}
}

// ─── QBit Delete Error Path ─────────────────────────────────────────────────

func TestQBitDelete_AllFail(t *testing.T) {
	env := newTestEnv(t)
	sid := env.loginQBit(t)

	// Add a job so there's something to delete
	env.qbitMultipartAdd(t, sid, map[string]string{
		"urls":     "magnet:?xt=urn:btih:aaaa1111bbbb2222cccc3333dddd4444eeee5555",
		"category": "movies",
	})

	jobs, _ := env.store.ListVisibleClientJobs(context.Background(), store.ClientKindQBit, "", 100)
	if len(jobs) == 0 {
		t.Fatal("no jobs found")
	}
	publicID := jobs[0].PublicID

	// Drop the jobs table to force errors in markRemovePending
	// (auth uses qbit_sessions which remains intact)
	env.store.DB().ExecContext(context.Background(), "DROP TABLE job_events")
	env.store.DB().ExecContext(context.Background(), "DROP TABLE transfer_parts")
	env.store.DB().ExecContext(context.Background(), "DROP TABLE jobs")

	delForm := url.Values{"hashes": {publicID}}
	rec := env.qbitRequest(t, "POST", "/api/v2/torrents/delete", strings.NewReader(delForm.Encode()), "application/x-www-form-urlencoded", sid)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// ─── SAB Delete From Queue ──────────────────────────────────────────────────

func TestSABDeleteFromQueue(t *testing.T) {
	env := newTestEnv(t)

	// Add a job via SAB API
	addParams := url.Values{
		"mode":   {"addurl"},
		"apikey": {"sabapikey123"},
		"name":   {"https://example.com/queue-delete-test.nzb"},
		"cat":    {"tv"},
	}
	req := httptest.NewRequest("GET", "/sabnzbd/api?"+addParams.Encode(), nil)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("add status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Get the job's public ID
	jobs, _ := env.store.ListVisibleClientJobs(context.Background(), store.ClientKindSAB, "", 100)
	if len(jobs) == 0 {
		t.Fatal("no SAB jobs found")
	}
	nzoID := "TBOX-" + jobs[0].PublicID

	// Delete from queue
	delParams := url.Values{
		"mode":   {"queue"},
		"name":   {"delete"},
		"value":  {nzoID},
		"apikey": {"sabapikey123"},
	}
	req = httptest.NewRequest("GET", "/sabnzbd/api?"+delParams.Encode(), nil)
	rec = httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != true {
		t.Errorf("status = %v, want true", resp["status"])
	}

	// Verify state changed
	job, _ := env.store.GetJobByPublicID(context.Background(), jobs[0].PublicID)
	if job.State != store.StateRemovePending {
		t.Errorf("State = %q, want %q", job.State, store.StateRemovePending)
	}
}

// ─── SAB Delete From History ────────────────────────────────────────────────

func TestSABDeleteFromHistory(t *testing.T) {
	env := newTestEnv(t)

	// Add a job via SAB API
	addParams := url.Values{
		"mode":   {"addurl"},
		"apikey": {"sabapikey123"},
		"name":   {"https://example.com/history-delete-test.nzb"},
		"cat":    {"movies"},
	}
	req := httptest.NewRequest("GET", "/sabnzbd/api?"+addParams.Encode(), nil)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("add status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Get the job's public ID
	jobs, _ := env.store.ListVisibleClientJobs(context.Background(), store.ClientKindSAB, "", 100)
	if len(jobs) == 0 {
		t.Fatal("no SAB jobs found")
	}
	nzoID := "TBOX-" + jobs[0].PublicID

	// Delete from history
	delParams := url.Values{
		"mode":   {"history"},
		"name":   {"delete"},
		"value":  {nzoID},
		"apikey": {"sabapikey123"},
	}
	req = httptest.NewRequest("GET", "/sabnzbd/api?"+delParams.Encode(), nil)
	rec = httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != true {
		t.Errorf("status = %v, want true", resp["status"])
	}

	// Verify state changed
	job, _ := env.store.GetJobByPublicID(context.Background(), jobs[0].PublicID)
	if job.State != store.StateRemovePending {
		t.Errorf("State = %q, want %q", job.State, store.StateRemovePending)
	}
}
