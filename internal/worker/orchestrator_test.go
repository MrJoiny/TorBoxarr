package worker_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/config"
	"github.com/mrjoiny/torboxarr/internal/files"
	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
	"github.com/mrjoiny/torboxarr/internal/worker"
)

type workerEnv struct {
	store  *store.Store
	layout *files.Layout
	mock   *torbox.MockClient
	tmpDir string
}

func newWorkerEnv(t *testing.T) *workerEnv {
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

	return &workerEnv{
		store:  st,
		layout: layout,
		mock:   &torbox.MockClient{},
		tmpDir: tmpDir,
	}
}

func (env *workerEnv) newOrchestrator(t *testing.T) *worker.Orchestrator {
	t.Helper()
	cfg := workerConfig(env.tmpDir)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	downloader := files.NewRangeDownloader(logger, 30*time.Second)
	return worker.NewOrchestrator(cfg, logger, env.store, env.layout, downloader, env.mock)
}

func workerConfig(tmpDir string) *config.Config {
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
	cfg.Auth.SABAPIKey = "sabapikey"
	cfg.Auth.SABNZBKey = "sabnzbkey"
	cfg.Auth.SessionTTL = 24 * time.Hour
	cfg.Compatibility.QBitVersion = "5.0.0"
	cfg.Compatibility.QBitWebAPI = "2.11.3"
	cfg.Compatibility.SABVersion = "4.5.1"
	cfg.Compatibility.DefaultCategory = "torboxarr"
	cfg.Workers.SubmitInterval = 5 * time.Second
	cfg.Workers.PollInterval = 30 * time.Second
	cfg.Workers.DownloadInterval = 5 * time.Second
	cfg.Workers.FinalizeInterval = 3 * time.Second
	cfg.Workers.RemoveInterval = 5 * time.Second
	cfg.Workers.PruneInterval = 12 * time.Hour
	cfg.Workers.SubmitRetryMin = 100 * time.Millisecond
	cfg.Workers.SubmitRetryMax = 1 * time.Second
	cfg.Workers.RemovedRetention = 30 * 24 * time.Hour
	cfg.Workers.BatchSize = 25
	return &cfg
}

func waitFor(t *testing.T, desc string, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if condition() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for: %s", desc)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

//go:fix inline
func strPtr(s string) *string { return new(s) }

func makeWorkerJob(id, publicID string, state store.JobState, sourceType store.SourceType) *store.Job {
	now := time.Now().UTC()
	return &store.Job{
		ID:            id,
		PublicID:      publicID,
		SourceType:    sourceType,
		ClientKind:    store.ClientKindQBit,
		Category:      "movies",
		State:         state,
		SubmissionKey: "key-" + id,
		DisplayName:   "Test " + id,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

// ─── ReconcileStartup ───────────────────────────────────────────────────────

func TestReconcileStartup(t *testing.T) {
	env := newWorkerEnv(t)
	ctx := context.Background()

	// Create a sleeping job that needs wakeup
	job := makeWorkerJob("recon-001", "pub-recon-001", store.StateSubmitPending, store.SourceTypeTorrent)
	job.NextRunAt = nil // sleeping
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	// Create staging dir that should be cleaned (orphan)
	orphanDir := filepath.Join(env.tmpDir, "staging", "orphan-dir")
	if err := os.MkdirAll(orphanDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create staging dir for the valid job
	validDir := filepath.Join(env.tmpDir, "staging", "recon-001")
	if err := os.MkdirAll(validDir, 0o755); err != nil {
		t.Fatal(err)
	}

	orch := env.newOrchestrator(t)
	// Start will call reconcileStartup (which needs a live context),
	// then start loops. Cancel immediately after so the loops exit.
	startCtx, cancel := context.WithCancel(ctx)
	if err := orch.Start(startCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancel() // cancel after reconciliation is done so loops exit
	orch.Wait()

	// Verify the sleeping job was woken up
	got, err := env.store.GetJobByID(ctx, "recon-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.NextRunAt == nil {
		t.Error("expected sleeping job to be woken (NextRunAt set)")
	}

	// Verify orphan staging dir was cleaned
	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Error("orphan staging dir should have been removed")
	}

	// Verify valid staging dir still exists
	if _, err := os.Stat(validDir); os.IsNotExist(err) {
		t.Error("valid staging dir should still exist")
	}
}

// ─── Submit Job Tests (using Start with immediate cancel) ───────────────────

func TestProcessSubmitJob_Torrent(t *testing.T) {
	env := newWorkerEnv(t)
	ctx := context.Background()

	env.mock.CreateTorrentTaskFn = func(ctx context.Context, req torbox.CreateTorrentTaskRequest) (*torbox.CreateTaskResponse, error) {
		return &torbox.CreateTaskResponse{
			RemoteID:    "remote-123",
			RemoteHash:  "hash-abc",
			DisplayName: "Remote Name",
		}, nil
	}

	job := makeWorkerJob("sub-001", "pub-sub-001", store.StateSubmitPending, store.SourceTypeTorrent)
	job.SourceURI = strPtr("magnet:?xt=urn:btih:abc")
	past := time.Now().UTC().Add(-1 * time.Minute)
	job.NextRunAt = &past
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	orch := env.newOrchestrator(t)
	startCtx, cancel := context.WithCancel(ctx)
	if err := orch.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job sub-001 reaches StateRemoteActive", 5*time.Second, func() bool {
		got, _ := env.store.GetJobByID(ctx, "sub-001")
		return got != nil && got.State == store.StateRemoteActive
	})
	cancel()
	orch.Wait()

	got, err := env.store.GetJobByID(ctx, "sub-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.RemoteID == nil || *got.RemoteID != "remote-123" {
		t.Errorf("RemoteID = %v, want %q", got.RemoteID, "remote-123")
	}
}

func TestProcessSubmitJob_Retry(t *testing.T) {
	env := newWorkerEnv(t)
	ctx := context.Background()

	env.mock.CreateTorrentTaskFn = func(ctx context.Context, req torbox.CreateTorrentTaskRequest) (*torbox.CreateTaskResponse, error) {
		return nil, torbox.MarkRetryable(fmt.Errorf("rate limited"))
	}

	job := makeWorkerJob("retry-001", "pub-retry-001", store.StateSubmitPending, store.SourceTypeTorrent)
	job.SourceURI = strPtr("magnet:?xt=urn:btih:abc")
	past := time.Now().UTC().Add(-1 * time.Minute)
	job.NextRunAt = &past
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	orch := env.newOrchestrator(t)
	startCtx, cancel := context.WithCancel(ctx)
	if err := orch.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job retry-001 reaches StateSubmitRetry", 5*time.Second, func() bool {
		got, _ := env.store.GetJobByID(ctx, "retry-001")
		return got != nil && got.State == store.StateSubmitRetry
	})
	cancel()
	orch.Wait()

	got, err := env.store.GetJobByID(ctx, "retry-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", got.RetryCount)
	}
}

func TestProcessSubmitJob_PermanentFailure(t *testing.T) {
	env := newWorkerEnv(t)
	ctx := context.Background()

	env.mock.CreateTorrentTaskFn = func(ctx context.Context, req torbox.CreateTorrentTaskRequest) (*torbox.CreateTaskResponse, error) {
		return nil, fmt.Errorf("permanently failed")
	}

	job := makeWorkerJob("fail-001", "pub-fail-001", store.StateSubmitPending, store.SourceTypeTorrent)
	job.SourceURI = strPtr("magnet:?xt=urn:btih:abc")
	past := time.Now().UTC().Add(-1 * time.Minute)
	job.NextRunAt = &past
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	orch := env.newOrchestrator(t)
	startCtx, cancel := context.WithCancel(ctx)
	if err := orch.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job fail-001 reaches StateRemoteFailed", 5*time.Second, func() bool {
		got, _ := env.store.GetJobByID(ctx, "fail-001")
		return got != nil && got.State == store.StateRemoteFailed
	})
	cancel()
	orch.Wait()
}

// ─── Remove Job ─────────────────────────────────────────────────────────────

func TestProcessRemoveJob(t *testing.T) {
	env := newWorkerEnv(t)
	ctx := context.Background()

	staging := filepath.Join(env.tmpDir, "staging", "rem-001")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "file.bin"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	job := makeWorkerJob("rem-001", "pub-rem-001", store.StateRemovePending, store.SourceTypeTorrent)
	job.RemoteID = strPtr("remote-rem")
	job.StagingPath = &staging
	past := time.Now().UTC().Add(-1 * time.Minute)
	job.NextRunAt = &past
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	orch := env.newOrchestrator(t)
	startCtx, cancel := context.WithCancel(ctx)
	if err := orch.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job rem-001 reaches StateRemoved", 5*time.Second, func() bool {
		got, _ := env.store.GetJobByID(ctx, "rem-001")
		return got != nil && got.State == store.StateRemoved
	})
	cancel()
	orch.Wait()

	got, err := env.store.GetJobByID(ctx, "rem-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.RemoteID == nil || *got.RemoteID != "remote-rem" {
		t.Fatalf("RemoteID = %v, want retained remote id %q", got.RemoteID, "remote-rem")
	}
	if got.StagingPath != nil {
		t.Fatalf("StagingPath = %v, want nil after local cleanup", got.StagingPath)
	}
	if got.NextRunAt != nil {
		t.Fatalf("NextRunAt = %v, want nil after removal", got.NextRunAt)
	}
	if got.ErrorMessage != nil {
		t.Fatalf("ErrorMessage = %v, want nil after removal", got.ErrorMessage)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Error("staging dir should have been removed")
	}
}

// ─── Progress Tracking (applyActiveStatus) ──────────────────────────────────

func TestProgressBytes_IncreasingUpdates(t *testing.T) {
	env := newWorkerEnv(t)
	ctx := context.Background()

	pollCount := 0
	env.mock.GetTaskStatusFn = func(ctx context.Context, sourceType, remoteID string) (*torbox.TaskStatus, error) {
		pollCount++
		switch pollCount {
		case 1:
			return &torbox.TaskStatus{
				RemoteID:   "remote-prog-001",
				State:      "downloading",
				BytesTotal: 1000,
				BytesDone:  200,
			}, nil
		default:
			return &torbox.TaskStatus{
				RemoteID:   "remote-prog-001",
				State:      "downloading",
				BytesTotal: 1000,
				BytesDone:  600,
			}, nil
		}
	}

	job := makeWorkerJob("prog-001", "pub-prog-001", store.StateRemoteActive, store.SourceTypeTorrent)
	job.RemoteID = strPtr("remote-prog-001")
	past := time.Now().UTC().Add(-1 * time.Minute)
	job.NextRunAt = &past
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	orch := env.newOrchestrator(t)
	startCtx, cancel := context.WithCancel(ctx)
	if err := orch.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "BytesDone == 200 after first poll", 5*time.Second, func() bool {
		got, _ := env.store.GetJobByID(ctx, "prog-001")
		return got != nil && got.BytesDone == 200
	})
	cancel()
	orch.Wait()

	got, err := env.store.GetJobByID(ctx, "prog-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.BytesTotal != 1000 {
		t.Errorf("BytesTotal = %d, want 1000", got.BytesTotal)
	}

	// Trigger second poll by setting NextRunAt in the past
	past2 := time.Now().UTC().Add(-1 * time.Minute)
	got.NextRunAt = &past2
	if err := env.store.UpdateJob(ctx, got); err != nil {
		t.Fatal(err)
	}

	orch2 := env.newOrchestrator(t)
	startCtx2, cancel2 := context.WithCancel(ctx)
	if err := orch2.Start(startCtx2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "BytesDone == 600 after second poll", 5*time.Second, func() bool {
		got, _ := env.store.GetJobByID(ctx, "prog-001")
		return got != nil && got.BytesDone == 600
	})
	cancel2()
	orch2.Wait()
}

func TestProgressBytes_TotalChanges(t *testing.T) {
	env := newWorkerEnv(t)
	ctx := context.Background()

	pollCount := 0
	env.mock.GetTaskStatusFn = func(ctx context.Context, sourceType, remoteID string) (*torbox.TaskStatus, error) {
		pollCount++
		switch pollCount {
		case 1:
			return &torbox.TaskStatus{
				RemoteID:   "remote-total-001",
				State:      "downloading",
				BytesTotal: 1000,
				BytesDone:  100,
			}, nil
		default:
			return &torbox.TaskStatus{
				RemoteID:   "remote-total-001",
				State:      "downloading",
				BytesTotal: 2000, // total changed (e.g. metadata correction)
				BytesDone:  500,
			}, nil
		}
	}

	job := makeWorkerJob("total-001", "pub-total-001", store.StateRemoteActive, store.SourceTypeTorrent)
	job.RemoteID = strPtr("remote-total-001")
	past := time.Now().UTC().Add(-1 * time.Minute)
	job.NextRunAt = &past
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	orch := env.newOrchestrator(t)
	startCtx, cancel := context.WithCancel(ctx)
	if err := orch.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "BytesTotal == 1000 after first poll", 5*time.Second, func() bool {
		got, _ := env.store.GetJobByID(ctx, "total-001")
		return got != nil && got.BytesTotal == 1000
	})
	cancel()
	orch.Wait()

	got, _ := env.store.GetJobByID(ctx, "total-001")

	past2 := time.Now().UTC().Add(-1 * time.Minute)
	got.NextRunAt = &past2
	_ = env.store.UpdateJob(ctx, got)

	orch2 := env.newOrchestrator(t)
	startCtx2, cancel2 := context.WithCancel(ctx)
	_ = orch2.Start(startCtx2)
	waitFor(t, "BytesTotal == 2000 after second poll", 5*time.Second, func() bool {
		got, _ := env.store.GetJobByID(ctx, "total-001")
		return got != nil && got.BytesTotal == 2000
	})
	cancel2()
	orch2.Wait()
}

func TestProgressBytes_ZeroStaysZero(t *testing.T) {
	env := newWorkerEnv(t)
	ctx := context.Background()

	env.mock.GetTaskStatusFn = func(ctx context.Context, sourceType, remoteID string) (*torbox.TaskStatus, error) {
		return &torbox.TaskStatus{
			RemoteID:   "remote-zero-001",
			State:      "downloading",
			BytesTotal: 0,
			BytesDone:  0,
		}, nil
	}

	job := makeWorkerJob("zero-001", "pub-zero-001", store.StateRemoteActive, store.SourceTypeTorrent)
	job.RemoteID = strPtr("remote-zero-001")
	past := time.Now().UTC().Add(-1 * time.Minute)
	job.NextRunAt = &past
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	orch := env.newOrchestrator(t)
	startCtx, cancel := context.WithCancel(ctx)
	if err := orch.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	// Zero bytes: just wait for the poller to have processed (UpdatedAt changes)
	waitFor(t, "job zero-001 processed by poller", 5*time.Second, func() bool {
		got, _ := env.store.GetJobByID(ctx, "zero-001")
		return got != nil && got.UpdatedAt.After(past)
	})
	cancel()
	orch.Wait()

	got, err := env.store.GetJobByID(ctx, "zero-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.BytesTotal != 0 {
		t.Errorf("BytesTotal = %d, want 0", got.BytesTotal)
	}
	if got.BytesDone != 0 {
		t.Errorf("BytesDone = %d, want 0", got.BytesDone)
	}
}

func TestProcessPollJob_DownloadFinishedWithoutPresentStaysRemoteActive(t *testing.T) {
	env := newWorkerEnv(t)
	ctx := context.Background()

	env.mock.GetTaskStatusFn = func(ctx context.Context, sourceType, remoteID string) (*torbox.TaskStatus, error) {
		return &torbox.TaskStatus{
			RemoteID:         "remote-not-ready-001",
			State:            "processing",
			DownloadPresent:  false,
			DownloadFinished: true,
			DownloadReady:    false,
		}, nil
	}

	job := makeWorkerJob("not-ready-001", "pub-not-ready-001", store.StateRemoteActive, store.SourceTypeNZB)
	job.RemoteID = strPtr("remote-not-ready-001")
	past := time.Now().UTC().Add(-1 * time.Minute)
	job.NextRunAt = &past
	initialUpdatedAt := job.UpdatedAt
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	orch := env.newOrchestrator(t)
	startCtx, cancel := context.WithCancel(ctx)
	if err := orch.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job not-ready-001 processed by poller", 5*time.Second, func() bool {
		got, _ := env.store.GetJobByID(ctx, "not-ready-001")
		return got != nil && got.UpdatedAt.After(initialUpdatedAt) && got.NextRunAt != nil
	})
	cancel()
	orch.Wait()

	got, err := env.store.GetJobByID(ctx, "not-ready-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemoteActive {
		t.Fatalf("State = %s, want %s", got.State, store.StateRemoteActive)
	}
	if got.NextRunAt == nil || !got.NextRunAt.After(past) {
		t.Fatal("expected NextRunAt to be rescheduled for another poll")
	}
}

func TestProcessPollJob_DownloadPresentTransitionsToLocalDownloadPending(t *testing.T) {
	env := newWorkerEnv(t)
	ctx := context.Background()

	env.mock.GetTaskStatusFn = func(ctx context.Context, sourceType, remoteID string) (*torbox.TaskStatus, error) {
		return &torbox.TaskStatus{
			RemoteID:         "remote-ready-001",
			State:            "completed",
			DownloadPresent:  true,
			DownloadFinished: true,
			DownloadReady:    true,
		}, nil
	}
	env.mock.GetDownloadLinksFn = func(ctx context.Context, sourceType, remoteID string) ([]torbox.DownloadAsset, error) {
		return nil, torbox.MarkRetryable(fmt.Errorf("skip local download in poll transition test"))
	}

	job := makeWorkerJob("ready-001", "pub-ready-001", store.StateRemoteActive, store.SourceTypeNZB)
	job.RemoteID = strPtr("remote-ready-001")
	past := time.Now().UTC().Add(-1 * time.Minute)
	job.NextRunAt = &past
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	orch := env.newOrchestrator(t)
	startCtx, cancel := context.WithCancel(ctx)
	if err := orch.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job ready-001 records local download pending transition", 5*time.Second, func() bool {
		var count int
		err := env.store.DB().QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM job_events
			WHERE job_id = ?
			  AND to_state = ?
		`, "ready-001", string(store.StateLocalDownloadPending)).Scan(&count)
		return err == nil && count > 0
	})
	cancel()
	orch.Wait()
}

func TestProgressBytes_MonotonicGuard(t *testing.T) {
	env := newWorkerEnv(t)
	ctx := context.Background()

	pollCount := 0
	env.mock.GetTaskStatusFn = func(ctx context.Context, sourceType, remoteID string) (*torbox.TaskStatus, error) {
		pollCount++
		switch pollCount {
		case 1:
			return &torbox.TaskStatus{
				RemoteID:   "remote-mono-001",
				State:      "downloading",
				BytesTotal: 1000,
				BytesDone:  500,
			}, nil
		default:
			// Simulate API glitch reporting lower BytesDone
			return &torbox.TaskStatus{
				RemoteID:   "remote-mono-001",
				State:      "downloading",
				BytesTotal: 1000,
				BytesDone:  200, // regressed
			}, nil
		}
	}

	job := makeWorkerJob("mono-001", "pub-mono-001", store.StateRemoteActive, store.SourceTypeTorrent)
	job.RemoteID = strPtr("remote-mono-001")
	past := time.Now().UTC().Add(-1 * time.Minute)
	job.NextRunAt = &past
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	orch := env.newOrchestrator(t)
	startCtx, cancel := context.WithCancel(ctx)
	if err := orch.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "BytesDone == 500 after first poll", 5*time.Second, func() bool {
		got, _ := env.store.GetJobByID(ctx, "mono-001")
		return got != nil && got.BytesDone == 500
	})
	cancel()
	orch.Wait()

	got, _ := env.store.GetJobByID(ctx, "mono-001")

	past2 := time.Now().UTC().Add(-1 * time.Minute)
	got.NextRunAt = &past2
	_ = env.store.UpdateJob(ctx, got)

	orch2 := env.newOrchestrator(t)
	startCtx2, cancel2 := context.WithCancel(ctx)
	_ = orch2.Start(startCtx2)
	// The regressed poll (BytesDone=200) should be clamped to 500 by the monotonic guard.
	// We wait for UpdatedAt to change to confirm the poller ran.
	updatedBefore := got.UpdatedAt
	waitFor(t, "mono-001 processed by second poller", 5*time.Second, func() bool {
		got, _ := env.store.GetJobByID(ctx, "mono-001")
		return got != nil && got.UpdatedAt.After(updatedBefore)
	})
	cancel2()
	orch2.Wait()

	got2, _ := env.store.GetJobByID(ctx, "mono-001")
	// BytesDone should NOT have regressed to 200
	if got2.BytesDone != 500 {
		t.Errorf("BytesDone after regressed poll = %d, want 500 (monotonic guard)", got2.BytesDone)
	}
}
