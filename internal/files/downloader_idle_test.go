package files

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
)

func TestDownloadIdleTimeout(t *testing.T) {
	for _, headers := range []bool{false, true} {
		name := "before headers"
		if headers {
			name = "during body"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if headers {
					w.Header().Set("Content-Length", "100")
					_, _ = w.Write([]byte("partial"))
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			dl := NewRangeDownloader(slog.New(slog.NewTextHandler(io.Discard, nil)), 0)
			dl.idleTimeout = 100 * time.Millisecond
			part := &store.TransferPart{SourceURL: server.URL, TempPath: filepath.Join(t.TempDir(), "part")}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := dl.Download(ctx, part, func(done, total int64) error { return nil })
			if !errors.Is(err, ErrDownloadIdleTimeout) {
				t.Fatalf("Download = %v, want idle timeout", err)
			}
			if headers && part.BytesDone != int64(len("partial")) {
				t.Fatalf("partial progress = %d, want 7", part.BytesDone)
			}
		})
	}
}

func TestDownloadIdleTimeoutResetsOnSmallReads(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "20")
		for i := 0; i < 20; i++ {
			_, _ = w.Write([]byte("x"))
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}))
	defer server.Close()
	dl := NewRangeDownloader(slog.New(slog.NewTextHandler(io.Discard, nil)), 0)
	dl.idleTimeout = 150 * time.Millisecond
	part := &store.TransferPart{SourceURL: server.URL, TempPath: filepath.Join(t.TempDir(), "part")}
	if err := dl.Download(context.Background(), part, func(done, total int64) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if part.BytesDone != 20 {
		t.Fatalf("BytesDone = %d, want 20", part.BytesDone)
	}
}
