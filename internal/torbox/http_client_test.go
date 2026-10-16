package torbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/torbox"
)

func newTestHTTPClient(t *testing.T, handler http.HandlerFunc) (*torbox.HTTPClient, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := torbox.NewHTTPClient(nil, server.URL, "test-token", "test-agent", 10*time.Second, nil, nil, nil)
	return client, server
}

func jsonEnvelope(data any) []byte {
	raw, _ := json.Marshal(data)
	env := map[string]any{
		"success": true,
		"data":    json.RawMessage(raw),
	}
	out, _ := json.Marshal(env)
	return out
}

func intPtr(v int) *int { return &v }

func TestCreateTorrentTask_Success(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{
			"torrent_id": 42,
			"id":         42,
			"hash":       "abc123hash",
			"name":       "Test Torrent",
		}))
	})

	resp, err := client.CreateTorrentTask(t.Context(), torbox.CreateTorrentTaskRequest{
		Magnet: "magnet:?xt=urn:btih:abc123",
		Name:   "Test Torrent",
	})
	if err != nil {
		t.Fatalf("CreateTorrentTask: %v", err)
	}
	if resp.RemoteID != "42" {
		t.Errorf("RemoteID = %q, want %q", resp.RemoteID, "42")
	}
	if !resp.ActiveIDExplicit {
		t.Error("ActiveIDExplicit = false, want true for torrent_id response")
	}
	if resp.RemoteHash != "abc123hash" {
		t.Errorf("RemoteHash = %q, want %q", resp.RemoteHash, "abc123hash")
	}
}

func TestCreateTorrentTask_GenericIDIsQueuedUntilActive(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{
			"id":   42,
			"hash": "queued-hash",
		}))
	})

	resp, err := client.CreateTorrentTask(t.Context(), torbox.CreateTorrentTaskRequest{
		Magnet: "magnet:?xt=urn:btih:queued",
	})
	if err != nil {
		t.Fatalf("CreateTorrentTask: %v", err)
	}
	if resp.RemoteID != "" {
		t.Errorf("RemoteID = %q, want empty until active confirmation", resp.RemoteID)
	}
	if resp.QueuedID != "42" {
		t.Errorf("QueuedID = %q, want %q", resp.QueuedID, "42")
	}
}

func TestCreateTorrentTask_UsesDocumentedMagnetField(t *testing.T) {
	var gotMagnet, gotMagnetLink string
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		gotMagnet = r.FormValue("magnet")
		gotMagnetLink = r.FormValue("magnet_link")
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{"torrent_id": 42}))
	})

	_, err := client.CreateTorrentTask(t.Context(), torbox.CreateTorrentTaskRequest{
		Magnet: "magnet:?xt=urn:btih:abc123",
		Name:   "Test Torrent",
	})
	if err != nil {
		t.Fatalf("CreateTorrentTask: %v", err)
	}
	if gotMagnet != "magnet:?xt=urn:btih:abc123" {
		t.Errorf("magnet = %q, want %q", gotMagnet, "magnet:?xt=urn:btih:abc123")
	}
	if gotMagnetLink != "" {
		t.Errorf("magnet_link = %q, want empty", gotMagnetLink)
	}
}

func TestCreateTorrentTask_UsesDocumentedFileField(t *testing.T) {
	payload := t.TempDir() + "/sample.torrent"
	if err := os.WriteFile(payload, []byte("torrent-bytes"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	var gotFileField bool
	var gotTorrentFileField bool
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		_, gotFileField = r.MultipartForm.File["file"]
		_, gotTorrentFileField = r.MultipartForm.File["torrent_file"]
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{"torrent_id": 77}))
	})

	_, err := client.CreateTorrentTask(t.Context(), torbox.CreateTorrentTaskRequest{
		PayloadPath: payload,
		Name:        "Uploaded Torrent",
	})
	if err != nil {
		t.Fatalf("CreateTorrentTask: %v", err)
	}
	if !gotFileField {
		t.Fatal("expected multipart file field 'file'")
	}
	if gotTorrentFileField {
		t.Fatal("did not expect multipart file field 'torrent_file'")
	}
}

func TestCreateUsenetTask_ExplicitZeroPostProcessingSent(t *testing.T) {
	var gotValues []string
	var gotField bool
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		gotValues, gotField = r.MultipartForm.Value["post_processing"]
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(42))
	})

	_, err := client.CreateUsenetTask(t.Context(), torbox.CreateUsenetTaskRequest{
		Link:           "https://example.invalid/test.nzb",
		Name:           "Test NZB",
		PostProcessing: intPtr(0),
	})
	if err != nil {
		t.Fatalf("CreateUsenetTask: %v", err)
	}
	if !gotField {
		t.Fatal("expected multipart field 'post_processing'")
	}
	if len(gotValues) != 1 || gotValues[0] != "0" {
		t.Fatalf("post_processing = %v, want [0]", gotValues)
	}
}

func TestCreateUsenetTask_OmitsPostProcessingWhenUnset(t *testing.T) {
	var gotField bool
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		_, gotField = r.MultipartForm.Value["post_processing"]
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(42))
	})

	_, err := client.CreateUsenetTask(t.Context(), torbox.CreateUsenetTaskRequest{
		Link: "https://example.invalid/test.nzb",
		Name: "Test NZB",
	})
	if err != nil {
		t.Fatalf("CreateUsenetTask: %v", err)
	}
	if gotField {
		t.Fatal("did not expect multipart field 'post_processing'")
	}
}

func TestCreateTorrentTask_RateLimit(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("rate limited"))
	})

	_, err := client.CreateTorrentTask(t.Context(), torbox.CreateTorrentTaskRequest{
		Magnet: "magnet:?xt=urn:btih:abc",
	})
	if err == nil {
		t.Fatal("expected error for 429 response")
	}
	if !torbox.IsRetryable(err) {
		t.Errorf("expected retryable error, got: %v", err)
	}
}

func TestCreateTorrentTask_ServerError(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	})

	_, err := client.CreateTorrentTask(t.Context(), torbox.CreateTorrentTaskRequest{
		Magnet: "magnet:?xt=urn:btih:abc",
	})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !torbox.IsRetryable(err) {
		t.Errorf("expected retryable error for 500, got: %v", err)
	}
}

func TestCreateTorrentTask_ClientError(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	})

	_, err := client.CreateTorrentTask(t.Context(), torbox.CreateTorrentTaskRequest{
		Magnet: "magnet:?xt=urn:btih:abc",
	})
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if torbox.IsRetryable(err) {
		t.Errorf("400 error should NOT be retryable, got: %v", err)
	}
}

func TestGetTaskStatus_Found(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{
			"torrent_id":        99,
			"name":              "My Torrent",
			"download_state":    "downloading",
			"progress":          0.75,
			"size":              2048,
			"downloaded":        1536,
			"download_present":  true,
			"download_finished": true,
			"hash":              "hashvalue",
		}))
	})

	status, err := client.GetTaskStatus(t.Context(), "torrent", "99")
	if err != nil {
		t.Fatalf("GetTaskStatus: %v", err)
	}
	if status == nil {
		t.Fatal("expected non-nil status")
	}
	if status.RemoteID != "99" {
		t.Errorf("RemoteID = %q, want %q", status.RemoteID, "99")
	}
	if status.Name != "My Torrent" {
		t.Errorf("Name = %q, want %q", status.Name, "My Torrent")
	}
	if status.Progress != 0.75 {
		t.Errorf("Progress = %f, want 0.75", status.Progress)
	}
	if !status.DownloadPresent {
		t.Error("expected DownloadPresent = true")
	}
	if !status.DownloadFinished {
		t.Error("expected DownloadFinished = true")
	}
	if !status.DownloadReady {
		t.Error("expected DownloadReady = true")
	}
	if status.Hash != "hashvalue" {
		t.Errorf("Hash = %q, want %q", status.Hash, "hashvalue")
	}
}

func TestGetQueuedStatus_GenericIDIsNotActiveID(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/queued/getqueued" {
			t.Errorf("path = %q, want queued endpoint", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{
			"id":   42,
			"hash": "queued-hash",
			"name": "Queued task",
		}))
	})

	status, err := client.GetQueuedStatus(t.Context(), "torrent", "42")
	if err != nil {
		t.Fatalf("GetQueuedStatus: %v", err)
	}
	if status == nil {
		t.Fatal("expected queued status")
	}
	if status.QueuedID != "42" {
		t.Errorf("QueuedID = %q, want %q", status.QueuedID, "42")
	}
	if status.RemoteID != "" {
		t.Errorf("RemoteID = %q, want empty for queued item", status.RemoteID)
	}
}

func TestFindQueuedTaskFallsBackToFullListAfterIDError(t *testing.T) {
	var calls int
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("id") != "" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"success":false,"error":"DATABASE_ERROR"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{
			"id":   42,
			"hash": "queued-hash",
		}))
	})

	status, err := client.FindQueuedTask(t.Context(), "torrent", "42", "", "queued-hash")
	if err != nil {
		t.Fatalf("FindQueuedTask: %v", err)
	}
	if calls != 2 {
		t.Fatalf("request count = %d, want 2", calls)
	}
	if status == nil || status.QueuedID != "42" || status.RemoteID != "" {
		t.Fatalf("status = %#v, want queued ID 42 without active ID", status)
	}
}

func TestFindQueuedTaskUsesUsenetQueueTypeAndAuthID(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("type"); got != "usenet" {
			t.Errorf("type = %q, want usenet", got)
		}
		if got := r.URL.Query().Get("id"); got != "" {
			t.Errorf("id = %q, want empty for Usenet queue lookup", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{
			"id":      7,
			"auth_id": "auth-7",
			"hash":    "usenet-hash",
			"name":    "Queued NZB",
		}))
	})

	status, err := client.FindQueuedTask(t.Context(), "nzb", "", "auth-7", "usenet-hash")
	if err != nil {
		t.Fatalf("FindQueuedTask: %v", err)
	}
	if status == nil || status.QueueAuthID != "auth-7" {
		t.Fatalf("status = %#v, want auth ID auth-7", status)
	}
}

func TestFindActiveTaskFallsBackToFullListAfterIDError(t *testing.T) {
	var calls int
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("id") != "" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"success":false,"error":"DATABASE_ERROR"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{
			"torrent_id":     99,
			"hash":           "active-hash",
			"download_state": "downloading",
		}))
	})

	status, err := client.FindActiveTask(t.Context(), "torrent", "42", "", "active-hash")
	if err != nil {
		t.Fatalf("FindActiveTask: %v", err)
	}
	if calls != 2 {
		t.Fatalf("request count = %d, want 2", calls)
	}
	if status == nil || status.RemoteID != "99" {
		t.Fatalf("status = %#v, want active remote ID 99", status)
	}
}

func TestFindActiveTaskFullListCanConfirmExplicitIDAfterIDError(t *testing.T) {
	var calls int
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("id") != "" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"success":false,"error":"DATABASE_ERROR"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{
			"torrent_id":     42,
			"hash":           "active-hash",
			"download_state": "downloading",
		}))
	})

	status, err := client.FindActiveTask(t.Context(), "torrent", "42", "", "active-hash")
	if err != nil {
		t.Fatalf("FindActiveTask: %v", err)
	}
	if calls != 2 {
		t.Fatalf("request count = %d, want 2", calls)
	}
	if status == nil || status.RemoteID != "42" {
		t.Fatalf("status = %#v, want active remote ID 42", status)
	}
}

func TestFindActiveTaskMatchesHashWithoutRemoteID(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("id"); got != "" {
			t.Errorf("id = %q, want empty for hash lookup", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{
			"torrent_id":     99,
			"hash":           "hash-only",
			"download_state": "downloading",
		}))
	})

	status, err := client.FindActiveTask(t.Context(), "torrent", "", "", "hash-only")
	if err != nil {
		t.Fatalf("FindActiveTask: %v", err)
	}
	if status == nil || status.RemoteID != "99" {
		t.Fatalf("status = %#v, want active remote ID 99", status)
	}
}

func TestFindActiveTaskFallsBackToAuthIDAfterEmptyIDResult(t *testing.T) {
	var calls int
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("id") != "" {
			w.Write(jsonEnvelope([]any{}))
			return
		}
		w.Write(jsonEnvelope(map[string]any{
			"usenet_id":      99,
			"auth_id":        "auth-only",
			"download_state": "downloading",
		}))
	})

	status, err := client.FindActiveTask(t.Context(), "nzb", "42", "auth-only", "")
	if err != nil {
		t.Fatalf("FindActiveTask: %v", err)
	}
	if calls != 2 {
		t.Fatalf("request count = %d, want 2", calls)
	}
	if status == nil || status.RemoteID != "99" {
		t.Fatalf("status = %#v, want active remote ID 99", status)
	}
}

func TestGetTaskStatus_DownloadFinishedWithoutPresentNotReady(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{
			"torrent_id":        88,
			"name":              "Finished But Not Present",
			"download_state":    "completed",
			"download_present":  false,
			"download_finished": true,
			"progress":          1.0,
		}))
	})

	status, err := client.GetTaskStatus(t.Context(), "torrent", "88")
	if err != nil {
		t.Fatalf("GetTaskStatus: %v", err)
	}
	if status == nil {
		t.Fatal("expected non-nil status")
	}
	if !status.DownloadFinished {
		t.Error("expected DownloadFinished = true")
	}
	if status.DownloadPresent {
		t.Error("expected DownloadPresent = false")
	}
	if status.DownloadReady {
		t.Error("DownloadReady should be false when download_present is false")
	}
}

func TestGetTaskStatus_ExpiredFinishedWithoutPresentNotReady(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{
			"torrent_id":        89,
			"name":              "Expired Download",
			"download_state":    "expired",
			"download_present":  false,
			"download_finished": true,
			"progress":          1.0,
		}))
	})

	status, err := client.GetTaskStatus(t.Context(), "torrent", "89")
	if err != nil {
		t.Fatalf("GetTaskStatus: %v", err)
	}
	if status == nil {
		t.Fatal("expected non-nil status")
	}
	if status.DownloadReady {
		t.Error("DownloadReady should be false for expired content without download_present")
	}
}

func TestGetDownloadLinks(t *testing.T) {
	callCount := 0
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		if callCount == 1 {
			// First call: mylist — return item with files
			w.Write(jsonEnvelope(map[string]any{
				"torrent_id": 99,
				"name":       "My Torrent",
				"files": []map[string]any{
					{"id": "f1", "name": "movie.mkv", "short_name": "movie.mkv", "size": 1024},
				},
			}))
		} else {
			// Second call: requestdl — return link
			w.Write(jsonEnvelope("https://cdn.example.com/download/movie.mkv"))
		}
	})

	assets, err := client.GetDownloadLinks(t.Context(), "torrent", "99")
	if err != nil {
		t.Fatalf("GetDownloadLinks: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("got %d assets, want 1", len(assets))
	}
	if assets[0].FileID != "f1" {
		t.Errorf("FileID = %q, want %q", assets[0].FileID, "f1")
	}
	if assets[0].URL != "https://cdn.example.com/download/movie.mkv" {
		t.Errorf("URL = %q, want download URL", assets[0].URL)
	}
}

// ─── Retryable Error Tests ─────────────────────────────────────────────────

func TestIsRetryable(t *testing.T) {
	regular := fmt.Errorf("regular error")
	if torbox.IsRetryable(regular) {
		t.Error("regular error should not be retryable")
	}

	retryable := torbox.MarkRetryable(fmt.Errorf("transient"))
	if !torbox.IsRetryable(retryable) {
		t.Error("MarkRetryable error should be retryable")
	}

	// MarkRetryable(nil) should return nil
	if torbox.MarkRetryable(nil) != nil {
		t.Error("MarkRetryable(nil) should return nil")
	}

	// Double-wrapping should not double up
	double := torbox.MarkRetryable(retryable)
	if double != retryable {
		t.Error("double-wrapping should return the original")
	}
}

func TestRequireRemoteID(t *testing.T) {
	if err := torbox.RequireRemoteID(""); err == nil {
		t.Error("expected error for empty remote ID")
	}
	if err := torbox.RequireRemoteID("123"); err != nil {
		t.Errorf("unexpected error for valid remote ID: %v", err)
	}
}

// ─── Failure Detection Tests ───────────────────────────────────────────────

func TestGetTaskStatus_FailedButDownloadPresent(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{
			"torrent_id":        55,
			"name":              "Failed But Ready",
			"download_state":    "failed",
			"download_present":  true,
			"download_finished": true,
			"progress":          1.0,
			"hash":              "abc",
		}))
	})

	status, err := client.GetTaskStatus(t.Context(), "torrent", "55")
	if err != nil {
		t.Fatalf("GetTaskStatus: %v", err)
	}
	if status == nil {
		t.Fatal("expected non-nil status")
	}
	if status.Failed {
		t.Error("Failed should be false when download_present is true")
	}
	if !status.DownloadReady {
		t.Error("DownloadReady should be true when download_present is true")
	}
}

func TestGetTaskStatus_FailedFinishedWithoutPresentRemainsFailed(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{
			"torrent_id":        56,
			"name":              "Failed And Not Present",
			"download_state":    "failed (repair failed)",
			"download_present":  false,
			"download_finished": true,
			"progress":          1.0,
			"hash":              "def",
		}))
	})

	status, err := client.GetTaskStatus(t.Context(), "torrent", "56")
	if err != nil {
		t.Fatalf("GetTaskStatus: %v", err)
	}
	if status == nil {
		t.Fatal("expected non-nil status")
	}
	if status.DownloadReady {
		t.Error("DownloadReady should be false when download_present is false")
	}
	if !status.Failed {
		t.Error("Failed should remain true when download_present is false")
	}
}

func TestGetTaskStatus_ExpandedFailureMarkers(t *testing.T) {
	markers := []string{"aborted", "cancelled", "canceled", "cannot be completed", "repair failed", "incomplete"}
	for _, marker := range markers {
		t.Run(marker, func(t *testing.T) {
			client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write(jsonEnvelope(map[string]any{
					"torrent_id":       77,
					"name":             "Terminal " + marker,
					"download_state":   marker,
					"download_present": false,
					"progress":         0.0,
					"hash":             "xyz",
				}))
			})

			status, err := client.GetTaskStatus(t.Context(), "torrent", "77")
			if err != nil {
				t.Fatalf("GetTaskStatus: %v", err)
			}
			if status == nil {
				t.Fatal("expected non-nil status")
			}
			if !status.Failed {
				t.Errorf("state %q should be detected as Failed", marker)
			}
		})
	}
}

func TestCreateTorrentTask_SeedAndAllowZip(t *testing.T) {
	var gotSeed, gotAllowZip string
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		gotSeed = r.FormValue("seed")
		gotAllowZip = r.FormValue("allow_zip")
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{"torrent_id": 1}))
	})

	_, err := client.CreateTorrentTask(t.Context(), torbox.CreateTorrentTaskRequest{
		Magnet:   "magnet:?xt=urn:btih:test",
		Name:     "Test",
		Seed:     2,
		AllowZip: true,
	})
	if err != nil {
		t.Fatalf("CreateTorrentTask: %v", err)
	}
	if gotSeed != "2" {
		t.Errorf("seed = %q, want %q", gotSeed, "2")
	}
	if gotAllowZip != "true" {
		t.Errorf("allow_zip = %q, want %q", gotAllowZip, "true")
	}
}

func TestRequestDownloadLink_IncludesToken(t *testing.T) {
	var gotToken string
	callCount := 0
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			// mylist call
			w.Write(jsonEnvelope(map[string]any{
				"torrent_id": 99,
				"files": []map[string]any{
					{"id": "f1", "name": "file.mkv", "size": 100},
				},
			}))
		} else {
			// requestdl call
			gotToken = r.URL.Query().Get("token")
			w.Write(jsonEnvelope("https://cdn.example.com/dl"))
		}
	})

	_, err := client.GetDownloadLinks(t.Context(), "torrent", "99")
	if err != nil {
		t.Fatalf("GetDownloadLinks: %v", err)
	}
	if gotToken != "test-token" {
		t.Errorf("token = %q, want %q", gotToken, "test-token")
	}
}

func TestDeleteQueuedTask_RequestContract(t *testing.T) {
	for _, sourceType := range []string{"torrent", "nzb"} {
		t.Run(sourceType, func(t *testing.T) {
			client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("method = %s, want POST", r.Method)
				}
				if r.URL.Path != "/api/queued/controlqueued" {
					t.Errorf("path = %q, want /api/queued/controlqueued", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
					t.Errorf("authorization = %q, want bearer token", got)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				var payload map[string]any
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatalf("decode body %q: %v", body, err)
				}
				if payload["operation"] != "delete" {
					t.Errorf("operation = %v, want delete", payload["operation"])
				}
				if got, ok := payload["queued_id"].(float64); !ok || got != 42 {
					t.Errorf("queued_id = %#v, want JSON number 42", payload["queued_id"])
				}
				if _, ok := payload["all"]; ok {
					t.Error("queued delete must not include account-wide all option")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(jsonEnvelope(nil))
			})

			if err := client.DeleteQueuedTask(t.Context(), sourceType, "42"); err != nil {
				t.Fatalf("DeleteQueuedTask: %v", err)
			}
		})
	}
}

func TestDeleteTask_RequestContractUsesNumericIDs(t *testing.T) {
	tests := []struct {
		name       string
		sourceType string
		path       string
		field      string
	}{
		{name: "torrent", sourceType: "torrent", path: "/api/torrents/controltorrent", field: "torrent_id"},
		{name: "usenet", sourceType: "nzb", path: "/api/usenet/controlusenetdownload", field: "usenet_id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.path {
					t.Errorf("path = %q, want %q", r.URL.Path, test.path)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(body, []byte(`"`+test.field+`":"`)) {
					t.Errorf("%s must be sent as a JSON number: %s", test.field, body)
				}
				var payload map[string]any
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatal(err)
				}
				if got, ok := payload[test.field].(float64); !ok || got != 77 {
					t.Errorf("%s = %#v, want JSON number 77", test.field, payload[test.field])
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(jsonEnvelope(nil))
			})

			if err := client.DeleteTask(t.Context(), test.sourceType, "77"); err != nil {
				t.Fatalf("DeleteTask: %v", err)
			}
		})
	}
}

func TestDeleteOperationsRejectInvalidIDsWithoutHTTP(t *testing.T) {
	var calls int
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jsonEnvelope(nil))
	})

	for _, id := range []string{"", "-1", "not-a-number"} {
		if err := client.DeleteTask(t.Context(), "torrent", id); err == nil {
			t.Errorf("DeleteTask(%q) returned nil", id)
		}
		if err := client.DeleteQueuedTask(t.Context(), "torrent", id); err == nil {
			t.Errorf("DeleteQueuedTask(%q) returned nil", id)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid IDs caused %d HTTP calls, want 0", calls)
	}
}

func TestDeleteTaskCanceledBeforeLimiterDoesNotSend(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls++
	}))
	t.Cleanup(server.Close)

	bucket := torbox.NewTokenBucket(1, time.Hour)
	t.Cleanup(bucket.Stop)
	if err := bucket.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	client := torbox.NewHTTPClient(nil, server.URL, "test-token", "test-agent", 10*time.Second, nil, bucket, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := client.DeleteTask(ctx, "torrent", "42")
	if !torbox.IsRequestNotSent(err) {
		t.Fatalf("DeleteTask error = %v, want RequestNotSentError", err)
	}
	if !torbox.IsRetryable(err) {
		t.Fatalf("DeleteTask error = %v, want retryable for non-removal callers", err)
	}
	if calls != 0 {
		t.Fatalf("canceled limiter caused %d HTTP calls, want 0", calls)
	}
}

func TestHTTPErrorClassification(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		retryable bool
		logical   bool
	}{
		{name: "structured 500", status: http.StatusInternalServerError, body: `{"success":false,"error":"DATABASE_ERROR"}`, retryable: true, logical: true},
		{name: "plain 500", status: http.StatusInternalServerError, body: "outage", retryable: true},
		{name: "request timeout", status: http.StatusRequestTimeout, body: "timeout", retryable: true},
		{name: "structured 429", status: http.StatusTooManyRequests, body: `{"success":false,"error":"RATE_LIMIT"}`, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			})
			_, err := client.GetTaskStatus(t.Context(), "torrent", "1")
			if err == nil {
				t.Fatal("expected error")
			}
			if got := torbox.IsRetryable(err); got != test.retryable {
				t.Errorf("retryable = %v, want %v", got, test.retryable)
			}
			if got := torbox.IsTorboxLogical(err); got != test.logical {
				t.Errorf("logical = %v, want %v", got, test.logical)
			}
		})
	}
}

func TestStructuredErrorSemanticClassification(t *testing.T) {
	tests := []struct {
		code        string
		wantAbsent  bool
		wantStatus  int
		wantLogical bool
	}{
		{code: "ITEM_NOT_FOUND", wantAbsent: true},
		{code: "INVALID_OPTION", wantStatus: http.StatusBadRequest},
		{code: "MISSING_REQUIRED_OPTION", wantStatus: http.StatusBadRequest},
		{code: "BAD_TOKEN", wantStatus: http.StatusUnauthorized},
		{code: "DATABASE_ERROR", wantLogical: true},
		{code: "UNKNOWN_ERROR", wantLogical: true},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"success":false,"error":%q}`, test.code)
			})
			err := client.DeleteTask(t.Context(), "torrent", "1")
			if got := torbox.IsTorboxAbsent(err); got != test.wantAbsent {
				t.Fatalf("absent = %v, want %v; err = %v", got, test.wantAbsent, err)
			}
			if test.wantStatus != 0 && !torbox.IsHTTPStatus(err, test.wantStatus) {
				t.Fatalf("error = %v, want HTTP status %d", err, test.wantStatus)
			}
			if got := torbox.IsTorboxLogical(err); got != test.wantLogical {
				t.Fatalf("logical = %v, want %v; err = %v", got, test.wantLogical, err)
			}
		})
	}
}

func TestDeleteTaskGenericNotFoundIsNotAbsence(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("endpoint not found"))
	})
	if err := client.DeleteTask(t.Context(), "torrent", "1"); err == nil || torbox.IsTorboxAbsent(err) {
		t.Fatalf("DeleteTask error = %v, want non-absence error", err)
	} else if !torbox.IsRetryable(err) {
		t.Fatalf("DeleteTask error = %v, want retryable uncertainty", err)
	}
	if err := client.DeleteQueuedTask(t.Context(), "torrent", "1"); err == nil || torbox.IsTorboxAbsent(err) {
		t.Fatalf("DeleteQueuedTask error = %v, want non-absence error", err)
	} else if !torbox.IsRetryable(err) {
		t.Fatalf("DeleteQueuedTask error = %v, want retryable uncertainty", err)
	}
}

func TestFindActiveTaskByIdentityRefreshesStaleID(t *testing.T) {
	var calls int
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("id") != "" {
			_, _ = w.Write(jsonEnvelope([]map[string]any{}))
			return
		}
		_, _ = w.Write(jsonEnvelope([]map[string]any{{
			"torrent_id": "123",
			"queued_id":  "42",
			"hash":       "exact-hash",
			"name":       "Recovered task",
		}}))
	})

	status, err := client.FindActiveTaskByIdentity(t.Context(), "torrent", "999", "42", "", "exact-hash")
	if err != nil {
		t.Fatalf("FindActiveTaskByIdentity: %v", err)
	}
	if status == nil || status.RemoteID != "123" {
		t.Fatalf("status = %#v, want refreshed remote id 123", status)
	}
	if calls != 2 {
		t.Fatalf("lookup calls = %d, want stale-id lookup plus full-list fallback", calls)
	}
}

func TestFindQueuedTaskRefreshesStaleID(t *testing.T) {
	var calls int
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("id") != "" {
			_, _ = w.Write(jsonEnvelope([]map[string]any{}))
			return
		}
		_, _ = w.Write(jsonEnvelope([]map[string]any{{
			"id":   "321",
			"hash": "exact-hash",
			"name": "Recovered queued task",
		}}))
	})

	status, err := client.FindQueuedTask(t.Context(), "torrent", "999", "", "exact-hash")
	if err != nil {
		t.Fatalf("FindQueuedTask: %v", err)
	}
	if status == nil || status.QueuedID != "321" {
		t.Fatalf("status = %#v, want refreshed queue id 321", status)
	}
	if calls != 2 {
		t.Fatalf("lookup calls = %d, want stale-id lookup plus full-list fallback", calls)
	}
}

func TestGetQueuedStatusPreservesExplicitActiveID(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jsonEnvelope(map[string]any{
			"id":         42,
			"torrent_id": 99,
			"hash":       "exact-hash",
		}))
	})

	status, err := client.GetQueuedStatus(t.Context(), "torrent", "42")
	if err != nil {
		t.Fatalf("GetQueuedStatus: %v", err)
	}
	if status == nil || status.QueuedID != "42" || status.RemoteID != "99" {
		t.Fatalf("status = %#v, want queued id 42 and explicit active id 99", status)
	}
}

func TestGetQueuedStatusDoesNotTreatGenericIDAsActive(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jsonEnvelope(map[string]any{"id": 42}))
	})

	status, err := client.GetQueuedStatus(t.Context(), "torrent", "42")
	if err != nil {
		t.Fatalf("GetQueuedStatus: %v", err)
	}
	if status == nil || status.QueuedID != "42" || status.RemoteID != "" {
		t.Fatalf("status = %#v, want queue-only id 42", status)
	}
}

func TestFindActiveTaskRejectsHashConflict(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jsonEnvelope(map[string]any{
			"torrent_id": "123",
			"hash":       "different-hash",
		}))
	})

	_, err := client.FindActiveTaskByIdentity(t.Context(), "torrent", "123", "", "", "expected-hash")
	if !torbox.IsTorboxIdentityConflict(err) {
		t.Fatalf("error = %v, want identity conflict", err)
	}
}

func TestStructured521KeepsStatusAndRetryability(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(torbox.StatusTorboxOriginDown)
		_, _ = w.Write([]byte(`{"success":false,"error":"DATABASE_ERROR"}`))
	})
	_, err := client.CreateTorrentTask(t.Context(), torbox.CreateTorrentTaskRequest{Magnet: "magnet:?xt=urn:btih:test"})
	if !torbox.IsHTTPStatus(err, torbox.StatusTorboxOriginDown) || !torbox.IsRetryable(err) {
		t.Fatalf("error = %v, want retryable HTTP 521", err)
	}
}

func Test404WithoutConfirmedAbsenceRemainsInconclusive(t *testing.T) {
	for _, body := range []string{"Not Found", `{"success":false,"error":"DATABASE_ERROR"}`} {
		t.Run(body, func(t *testing.T) {
			client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(body))
			})
			_, err := client.FindQueuedTask(t.Context(), "torrent", "12", "", "owned")
			if err == nil || torbox.IsTorboxAbsent(err) || !torbox.IsRetryable(err) {
				t.Fatalf("error = %v, want retryable inconclusive error", err)
			}
		})
	}
}
