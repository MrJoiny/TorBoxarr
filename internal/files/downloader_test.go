package files_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/files"
	"github.com/mrjoiny/torboxarr/internal/store"
)

func TestDownload_FullFile(t *testing.T) {
	content := "hello download world 1234567890"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(content))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	tempPath := filepath.Join(tmpDir, "download.bin")

	dl := files.NewRangeDownloader(slog.Default(), 30*time.Second)
	part := &store.TransferPart{
		PartKey:   "file-0",
		SourceURL: server.URL + "/file.bin",
		TempPath:  tempPath,
	}

	var progressCalls int
	var firstDone, firstTotal int64
	err := dl.Download(context.Background(), part, func(done, total int64) error {
		progressCalls++
		if progressCalls == 1 {
			firstDone = done
			firstTotal = total
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	data, err := os.ReadFile(tempPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("file content = %q, want %q", string(data), content)
	}
	if progressCalls == 0 {
		t.Error("expected at least one progress callback")
	}
	if firstDone != 0 {
		t.Errorf("first progress done = %d, want 0", firstDone)
	}
	if firstTotal != int64(len(content)) {
		t.Errorf("first progress total = %d, want %d", firstTotal, len(content))
	}
	if part.BytesDone != int64(len(content)) {
		t.Errorf("BytesDone = %d, want %d", part.BytesDone, len(content))
	}
}

func TestDownload_Resume(t *testing.T) {
	fullContent := "0123456789ABCDEF"
	existing := "01234"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			t.Error("expected Range header for resume")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(fullContent))
			return
		}
		// Serve the remaining content
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 5-15/%d", len(fullContent)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte(fullContent[5:]))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	tempPath := filepath.Join(tmpDir, "resume.bin")

	// Write existing partial content
	if err := os.WriteFile(tempPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	dl := files.NewRangeDownloader(slog.Default(), 30*time.Second)
	part := &store.TransferPart{
		PartKey:   "file-0",
		SourceURL: server.URL + "/file.bin",
		TempPath:  tempPath,
	}

	err := dl.Download(context.Background(), part, func(done, total int64) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Download resume: %v", err)
	}

	data, err := os.ReadFile(tempPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != fullContent {
		t.Errorf("file content = %q, want %q", string(data), fullContent)
	}
}

func TestDownload_416_RangeNotSatisfiable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	tempPath := filepath.Join(tmpDir, "complete.bin")
	existingContent := "already complete"
	if err := os.WriteFile(tempPath, []byte(existingContent), 0o644); err != nil {
		t.Fatal(err)
	}

	dl := files.NewRangeDownloader(slog.Default(), 30*time.Second)
	part := &store.TransferPart{
		PartKey:   "file-0",
		SourceURL: server.URL + "/file.bin",
		TempPath:  tempPath,
	}

	err := dl.Download(context.Background(), part, func(done, total int64) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Download 416: %v", err)
	}

	// BytesDone should match existing file size
	if part.BytesDone != int64(len(existingContent)) {
		t.Errorf("BytesDone = %d, want %d", part.BytesDone, len(existingContent))
	}
}

func TestDownload_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	tempPath := filepath.Join(tmpDir, "error.bin")

	dl := files.NewRangeDownloader(slog.Default(), 30*time.Second)
	part := &store.TransferPart{
		PartKey:   "file-0",
		SourceURL: server.URL + "/file.bin",
		TempPath:  tempPath,
	}

	err := dl.Download(context.Background(), part, func(done, total int64) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}
