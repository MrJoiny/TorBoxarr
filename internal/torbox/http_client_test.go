package torbox_test

import (
	"encoding/json"
	"fmt"
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

func TestCreateTorrentTask_Success(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonEnvelope(map[string]any{
			"torrent_id": 42,
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
	if resp.RemoteHash != "abc123hash" {
		t.Errorf("RemoteHash = %q, want %q", resp.RemoteHash, "abc123hash")
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
