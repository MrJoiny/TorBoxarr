package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/files"
	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
)

func TestExpiredDownloadLinkSurvivesTransientRefreshFailure(t *testing.T) {
	env := newQueuedTestEnv(t)
	ctx := context.Background()
	stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer stale.Close()
	fresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer fresh.Close()
	calls := 0
	env.mock.GetDownloadLinksFn = func(context.Context, string, string) ([]torbox.DownloadAsset, error) {
		calls++
		if calls == 2 || calls == 3 {
			return nil, torbox.MarkRetryable(context.DeadlineExceeded)
		}
		link := stale.URL
		if calls > 3 {
			link = fresh.URL
		}
		return []torbox.DownloadAsset{{FileID: "1", URL: link, RelativePath: "episode.mkv", Size: 7}}, nil
	}
	job := queuedTestJob("refresh-outage", store.StateLocalDownloadPending)
	job.RemoteID = stringPtr("123")
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 4; attempt++ {
		var err error
		job, err = env.store.GetJobByID(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := env.orch.processDownloadJob(ctx, job); err != nil {
			t.Fatal(err)
		}
		got, err := env.store.GetJobByID(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 0 && (got.State != store.StateLocalDownloading || got.NextRunAt == nil) {
			t.Fatalf("state = %s, next run = %v, want recovery pending after refresh outage", got.State, got.NextRunAt)
		}
		if attempt == 3 && got.State != store.StateLocalVerify {
			t.Fatalf("state = %s, want verified-stage transfer after link recovery", got.State)
		}
	}
}

func TestSmallResumedProgressResetsRetryBudget(t *testing.T) {
	env := newQueuedTestEnv(t)
	ctx := context.Background()
	staging := filepath.Join(env.orch.layout.Staging, "small-progress")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(staging, "episode.mkv")
	if err := os.WriteFile(path, make([]byte, 64), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "192")
		w.Header().Set("Content-Range", "bytes 64-255/256")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(make([]byte, 32))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	job := queuedTestJob("small-progress", store.StateLocalDownloading)
	job.RemoteID, job.StagingPath = stringPtr("123"), &staging
	job.BytesTotal, job.RetryCount = 256, 2
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	part := &store.TransferPart{JobID: job.ID, PartKey: "1", SourceURL: server.URL, TempPath: path,
		RelativePath: "episode.mkv", ContentLength: 256, BytesDone: 64, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := env.store.UpsertTransferPart(ctx, part); err != nil {
		t.Fatal(err)
	}
	bounded, cancelTimeout := context.WithTimeout(ctx, 3*time.Second)
	defer cancelTimeout()
	transferCtx, cancelIdle := context.WithCancelCause(bounded)
	defer cancelIdle(nil)
	observed := make(chan struct{})
	go func() {
		defer close(observed)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-transferCtx.Done():
				return
			case <-ticker.C:
				if info, err := os.Stat(path); err == nil && info.Size() == 96 {
					cancelIdle(files.ErrDownloadIdleTimeout)
					return
				}
			}
		}
	}()
	if err := env.orch.processDownloadJob(transferCtx, job); err != nil {
		t.Fatal(err)
	}
	<-observed
	if job.RetryCount != 1 {
		t.Fatalf("retries = %d, want one idle failure after actual resumed progress", job.RetryCount)
	}
}
