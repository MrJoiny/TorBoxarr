package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/config"
	"github.com/mrjoiny/torboxarr/internal/files"
	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
)

// removeTestEnv bundles a configured Orchestrator with a controllable mock client
// and an in-memory store for exercising processRemoveJob / runRemover.
type removeTestEnv struct {
	orch  *Orchestrator
	store *store.Store
	mock  *torbox.MockClient
	dir   string
}

func newRemoveTestEnv(t *testing.T) *removeTestEnv {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, ":memory:", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := store.RunMigrationsFS(db, store.EmbeddedMigrations); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db)

	dir := t.TempDir()
	layout := files.NewLayout(dir, filepath.Join(dir, "staging"), filepath.Join(dir, "completed"), filepath.Join(dir, "payloads"))
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.Logging.Level = "ERROR"
	cfg.Database.Path = ":memory:"
	cfg.Data.Root = dir
	cfg.Data.Staging = filepath.Join(dir, "staging")
	cfg.Data.Completed = filepath.Join(dir, "completed")
	cfg.Data.Payloads = filepath.Join(dir, "payloads")
	cfg.TorBox.BaseURL = "https://api.torbox.app/v1"
	cfg.TorBox.APIToken = "test-token"
	cfg.Auth.QBitUsername = "admin"
	cfg.Auth.QBitPassword = "password"
	cfg.Auth.SABAPIKey = "sabapikey"
	cfg.Auth.SABNZBKey = "sabnzbkey"
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

	mock := &torbox.MockClient{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	downloader := files.NewRangeDownloader(logger, 30*time.Second)
	orch := NewOrchestrator(cfg, logger, st, layout, downloader, mock)

	return &removeTestEnv{orch: orch, store: st, mock: mock, dir: dir}
}

// insertRemovePendingJob creates a remove_pending job with local files on disk
// and (optionally) an upstream remote id. Returns the created job.
func (e *removeTestEnv) insertRemovePendingJob(t *testing.T, id, remoteID string, sourceType store.SourceType) *store.Job {
	t.Helper()
	ctx := context.Background()

	completedDir := filepath.Join(e.dir, "completed", id)
	if err := os.MkdirAll(completedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	completedPath := filepath.Join(completedDir, "file.mkv")
	if err := os.WriteFile(completedPath, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	job := &store.Job{
		ID:            id,
		PublicID:      "pub-" + id,
		SourceType:    sourceType,
		ClientKind:    store.ClientKindQBit,
		Category:      "movies",
		State:         store.StateRemovePending,
		SubmissionKey: "key-" + id,
		DisplayName:   "Test " + id,
		CompletedPath: &completedPath,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if remoteID != "" {
		job.RemoteID = ptr(remoteID)
	}
	if err := e.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	return job
}

// insertRemovePendingJobAt is like insertRemovePendingJob but uses an explicit
// completed file path so tests can assert local deletion afterwards.
func (e *removeTestEnv) insertRemovePendingJobAt(t *testing.T, id, remoteID string, sourceType store.SourceType, completedPath string) *store.Job {
	t.Helper()
	ctx := context.Background()

	completedDir := filepath.Dir(completedPath)
	if err := os.MkdirAll(completedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(completedPath, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	job := &store.Job{
		ID:            id,
		PublicID:      "pub-" + id,
		SourceType:    sourceType,
		ClientKind:    store.ClientKindQBit,
		Category:      "movies",
		State:         store.StateRemovePending,
		SubmissionKey: "key-" + id,
		DisplayName:   "Test " + id,
		CompletedPath: &completedPath,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if remoteID != "" {
		job.RemoteID = ptr(remoteID)
	}
	if err := e.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestProcessRemoveJob_UpstreamDeleteTorrent(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true

	var mu sync.Mutex
	var calls []string
	env.mock.DeleteTaskFn = func(_ context.Context, sourceType, remoteID string) error {
		mu.Lock()
		calls = append(calls, sourceType+":"+remoteID)
		mu.Unlock()
		return nil
	}

	completedPath := filepath.Join(env.dir, "completed", "t1", "file.mkv")
	env.insertRemovePendingJobAt(t, "t1", "57356712", store.SourceTypeTorrent, completedPath)

	ctx := context.Background()
	jobs, err := env.store.ClaimJobsDue(ctx, "remover", []store.JobState{store.StateRemovePending}, time.Now().UTC(), 25)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if err := env.orch.processRemoveJob(ctx, job); err != nil {
			t.Fatalf("processRemoveJob: %v", err)
		}
		env.orch.releaseJobClaim(ctx, "remover", job.ID)
	}

	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "torrent:57356712" {
		t.Fatalf("expected single torrent upstream delete, got %v", got)
	}

	got2, err := env.store.GetJobByID(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got2.State != store.StateRemoved {
		t.Errorf("expected StateRemoved, got %s", got2.State)
	}
	if _, err := os.Stat(completedPath); !os.IsNotExist(err) {
		t.Error("completed file should have been removed locally")
	}
}

func TestProcessRemoveJob_UpstreamDeleteUsenet(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true

	var mu sync.Mutex
	var calls []string
	env.mock.DeleteTaskFn = func(_ context.Context, sourceType, remoteID string) error {
		mu.Lock()
		calls = append(calls, sourceType+":"+remoteID)
		mu.Unlock()
		return nil
	}

	env.insertRemovePendingJob(t, "u1", "88123455", store.SourceTypeNZB)

	ctx := context.Background()
	jobs, err := env.store.ClaimJobsDue(ctx, "remover", []store.JobState{store.StateRemovePending}, time.Now().UTC(), 25)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if err := env.orch.processRemoveJob(ctx, job); err != nil {
			t.Fatalf("processRemoveJob: %v", err)
		}
		env.orch.releaseJobClaim(ctx, "remover", job.ID)
	}

	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "nzb:88123455" {
		t.Fatalf("expected single usenet upstream delete, got %v", got)
	}
}

func TestProcessRemoveJob_DisabledSkipsUpstream(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = false

	called := false
	env.mock.DeleteTaskFn = func(_ context.Context, _, _ string) error {
		called = true
		return nil
	}
	env.mock.DeleteQueuedTaskFn = env.mock.DeleteTaskFn
	env.mock.FindActiveTaskByIdentityFn = func(context.Context, string, string, string, string, string) (*torbox.TaskStatus, error) {
		t.Fatal("disabled removal must not look up active tasks")
		return nil, nil
	}
	env.mock.FindQueuedTaskFn = func(context.Context, string, string, string, string) (*torbox.TaskStatus, error) {
		t.Fatal("disabled removal must not look up queued tasks")
		return nil, nil
	}

	env.insertRemovePendingJob(t, "d1", "57356712", store.SourceTypeTorrent)

	ctx := context.Background()
	jobs, err := env.store.ClaimJobsDue(ctx, "remover", []store.JobState{store.StateRemovePending}, time.Now().UTC(), 25)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if err := env.orch.processRemoveJob(ctx, job); err != nil {
			t.Fatalf("processRemoveJob: %v", err)
		}
		env.orch.releaseJobClaim(ctx, "remover", job.ID)
	}

	if called {
		t.Error("upstream delete must not be called when TORBOXARR_UPSTREAM_REMOVE is false")
	}
	got, err := env.store.GetJobByID(ctx, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemoved {
		t.Errorf("expected StateRemoved, got %s", got.State)
	}
}

func TestProcessRemoveJob_UpstreamFailureStillCleansLocally(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true

	env.mock.DeleteTaskFn = func(_ context.Context, _, _ string) error {
		return errors.New("torbox 500")
	}

	env.insertRemovePendingJob(t, "f1", "57356712", store.SourceTypeTorrent)

	ctx := context.Background()
	jobs, err := env.store.ClaimJobsDue(ctx, "remover", []store.JobState{store.StateRemovePending}, time.Now().UTC(), 25)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if err := env.orch.processRemoveJob(ctx, job); err != nil {
			t.Fatalf("processRemoveJob: %v", err)
		}
		env.orch.releaseJobClaim(ctx, "remover", job.ID)
	}

	got, err := env.store.GetJobByID(ctx, "f1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemoved {
		t.Errorf("local cleanup must still proceed on upstream failure; state=%s", got.State)
	}
	if _, err := os.Stat(filepath.Join(env.dir, "completed", "f1", "file.mkv")); !os.IsNotExist(err) {
		t.Error("completed file should have been removed locally despite upstream failure")
	}
}

func TestProcessRemoveJob_UpstreamDeleteRetriesThenEscalates(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true

	var deleteCalls int
	env.mock.DeleteTaskFn = func(_ context.Context, _, _ string) error {
		deleteCalls++
		return torbox.MarkRetryable(errors.New("torbox 500 DATABASE_ERROR"))
	}

	env.insertRemovePendingJob(t, "r1", "57356712", store.SourceTypeTorrent)

	ctx := context.Background()
	// Simulate the remover ticking repeatedly while the upstream API is down.
	// Attempts 1-4 retain local payloads and schedule another run; attempt 5
	// escalates and proceeds with local cleanup.
	for attempt := 1; attempt <= maxUpstreamDeleteAttempts; attempt++ {
		job, err := env.store.GetJobByID(ctx, "r1")
		if err != nil {
			t.Fatal(err)
		}
		err = env.orch.processRemoveJob(ctx, job)
		if err != nil {
			t.Fatalf("attempt %d: processRemoveJob: %v", attempt, err)
		}
		got, err := env.store.GetJobByID(ctx, "r1")
		if err != nil {
			t.Fatal(err)
		}
		if attempt < maxUpstreamDeleteAttempts {
			if got.State != store.StateRemovePending {
				t.Fatalf("attempt %d: state=%s, want remove_pending", attempt, got.State)
			}
			if _, err := os.Stat(filepath.Join(env.dir, "completed", "r1", "file.mkv")); err != nil {
				t.Fatalf("attempt %d: local payload should remain: %v", attempt, err)
			}
		} else if got.State != store.StateRemoved {
			t.Fatalf("attempt %d: state=%s, want removed", attempt, got.State)
		}
	}

	if deleteCalls != maxUpstreamDeleteAttempts {
		t.Errorf("expected %d delete calls, got %d", maxUpstreamDeleteAttempts, deleteCalls)
	}

	got, err := env.store.GetJobByID(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemoved {
		t.Errorf("after max attempts the job must finalize locally; state=%s", got.State)
	}
	if got.Metadata.ActiveRemoval.Attempts != maxUpstreamDeleteAttempts {
		t.Errorf("expected active removal attempts=%d, got %d", maxUpstreamDeleteAttempts, got.Metadata.ActiveRemoval.Attempts)
	}
	if got.Metadata.ActiveRemoval.Outcome != store.UpstreamRemovalExhausted {
		t.Errorf("active removal outcome=%q, want %q", got.Metadata.ActiveRemoval.Outcome, store.UpstreamRemovalExhausted)
	}
	if _, err := os.Stat(filepath.Join(env.dir, "completed", "r1", "file.mkv")); !os.IsNotExist(err) {
		t.Error("completed file should have been removed locally after escalation")
	}
}

func TestProcessRemoveJob_QueuedJobNoRemoteIDSkipsUpstream(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true

	var activeCalls, queuedCalls int
	env.mock.DeleteTaskFn = func(_ context.Context, _, _ string) error {
		activeCalls++
		return nil
	}
	env.mock.DeleteQueuedTaskFn = func(_ context.Context, _, queuedID string) error {
		queuedCalls++
		if queuedID != "42" {
			t.Errorf("queued id = %q, want 42", queuedID)
		}
		return nil
	}

	// Queued job: only a queued_id, no remote_id yet.
	job := env.insertRemovePendingJob(t, "q1", "", store.SourceTypeTorrent)
	job.QueuedID = ptr("42")

	ctx := context.Background()
	if err := env.store.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	jobs, err := env.store.ClaimJobsDue(ctx, "remover", []store.JobState{store.StateRemovePending}, time.Now().UTC(), 25)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if err := env.orch.processRemoveJob(ctx, j); err != nil {
			t.Fatalf("processRemoveJob: %v", err)
		}
		env.orch.releaseJobClaim(ctx, "remover", j.ID)
	}

	if activeCalls != 0 {
		t.Errorf("active delete calls = %d, want 0 for queued-only job", activeCalls)
	}
	if queuedCalls != 1 {
		t.Errorf("queued delete calls = %d, want 1", queuedCalls)
	}
	got, err := env.store.GetJobByID(ctx, "q1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemoved {
		t.Errorf("expected StateRemoved for queued job, got %s", got.State)
	}
}

// TestRunRemover_ConcurrentClaims verifies that when two remover workers run
// concurrently against the same pending job, the job is claimed and the upstream
// delete is invoked exactly once (ClaimJobsDue serializes via claimed_by).
func TestRunRemover_ConcurrentClaims(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true

	var mu sync.Mutex
	var calls int
	env.mock.DeleteTaskFn = func(_ context.Context, _, _ string) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil
	}

	env.insertRemovePendingJob(t, "c1", "57356712", store.SourceTypeTorrent)

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = env.orch.runRemover(ctx)
		}()
	}
	wg.Wait()

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("expected upstream delete exactly once across concurrent workers, got %d", got)
	}
	job, err := env.store.GetJobByID(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != store.StateRemoved {
		t.Errorf("expected StateRemoved, got %s", job.State)
	}
}

func TestRunRemover_RecoversClaimThatExpiresAfterStartup(t *testing.T) {
	env := newRemoveTestEnv(t)
	ctx := context.Background()
	job := env.insertRemovePendingJob(t, "expired-remover", "123", store.SourceTypeTorrent)
	claimedAt := time.Now().UTC().Add(-6 * time.Minute)
	if _, err := env.store.ClaimJobsDue(ctx, "remover-crashed", []store.JobState{store.StateRemovePending}, claimedAt, 1); err != nil {
		t.Fatal(err)
	}
	// Startup occurred before the claim's lease expired and kept it intact.
	if err := env.store.RecoverClaims(ctx, claimedAt.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := env.orch.runRemover(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := env.store.GetJobByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemoved || got.ClaimedBy != nil {
		t.Fatalf("state = %s, claim = %v, want removed and released", got.State, got.ClaimedBy)
	}
}

func TestProcessRemoveJob_BothViewsDeleteIndependently(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true
	var operations []string
	var activeCalls, queuedCalls int
	env.mock.FindActiveTaskFn = func(_ context.Context, _, remoteID, _, _ string) (*torbox.TaskStatus, error) {
		operations = append(operations, "lookup-active")
		return &torbox.TaskStatus{RemoteID: remoteID}, nil
	}
	env.mock.FindQueuedTaskFn = func(_ context.Context, _, queuedID, _, _ string) (*torbox.TaskStatus, error) {
		operations = append(operations, "lookup-queued")
		return &torbox.TaskStatus{QueuedID: queuedID}, nil
	}
	env.mock.DeleteTaskFn = func(_ context.Context, _, _ string) error {
		operations = append(operations, "delete-active")
		activeCalls++
		return nil
	}
	env.mock.DeleteQueuedTaskFn = func(_ context.Context, _, _ string) error {
		operations = append(operations, "delete-queued")
		queuedCalls++
		return nil
	}

	job := env.insertRemovePendingJob(t, "both-views", "101", store.SourceTypeTorrent)
	job.QueuedID = ptr("202")
	if err := env.store.UpdateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := env.orch.processRemoveJob(context.Background(), job); err != nil {
		t.Fatalf("processRemoveJob: %v", err)
	}

	if activeCalls != 1 || queuedCalls != 1 {
		t.Fatalf("delete calls = active:%d queued:%d, want 1 each", activeCalls, queuedCalls)
	}
	wantOperations := []string{"lookup-active", "lookup-queued", "delete-active", "delete-queued"}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Fatalf("operations = %v, want %v", operations, wantOperations)
	}
	got, err := env.store.GetJobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata.ActiveRemoval.Outcome != store.UpstreamRemovalDeleted || got.Metadata.QueuedRemoval.Outcome != store.UpstreamRemovalDeleted {
		t.Fatalf("outcomes = active:%q queued:%q, want deleted for both", got.Metadata.ActiveRemoval.Outcome, got.Metadata.QueuedRemoval.Outcome)
	}
}

func TestProcessRemoveJob_PartialSuccessRetriesOnlyRemainingView(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true
	var activeCalls, queuedCalls int
	env.mock.FindActiveTaskFn = func(_ context.Context, _, remoteID, _, _ string) (*torbox.TaskStatus, error) {
		return &torbox.TaskStatus{RemoteID: remoteID}, nil
	}
	env.mock.FindQueuedTaskFn = func(_ context.Context, _, queuedID, _, _ string) (*torbox.TaskStatus, error) {
		return &torbox.TaskStatus{QueuedID: queuedID}, nil
	}
	env.mock.DeleteTaskFn = func(_ context.Context, _, _ string) error {
		activeCalls++
		return nil
	}
	env.mock.DeleteQueuedTaskFn = func(_ context.Context, _, _ string) error {
		queuedCalls++
		return torbox.MarkRetryable(errors.New("queue unavailable"))
	}

	job := env.insertRemovePendingJob(t, "partial-view", "301", store.SourceTypeTorrent)
	job.QueuedID = ptr("302")
	if err := env.store.UpdateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := env.orch.processRemoveJob(context.Background(), job); err != nil {
		t.Fatalf("first processRemoveJob: %v", err)
	}
	if activeCalls != 1 || queuedCalls != 1 {
		t.Fatalf("first delete calls = active:%d queued:%d, want 1 each", activeCalls, queuedCalls)
	}
	got, err := env.store.GetJobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemovePending {
		t.Fatalf("first state = %s, want remove_pending", got.State)
	}

	env.mock.DeleteQueuedTaskFn = func(_ context.Context, _, _ string) error {
		queuedCalls++
		return nil
	}
	if err := env.orch.processRemoveJob(context.Background(), got); err != nil {
		t.Fatalf("second processRemoveJob: %v", err)
	}
	if activeCalls != 1 || queuedCalls != 2 {
		t.Fatalf("second delete calls = active:%d queued:%d, want active once and queued twice", activeCalls, queuedCalls)
	}
	got, err = env.store.GetJobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemoved {
		t.Fatalf("second state = %s, want removed", got.State)
	}
}

func TestProcessRemoveJob_LookupFailureDoesNotBlockOtherView(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true
	var activeDeletes, queuedDeletes int
	env.mock.FindActiveTaskFn = func(context.Context, string, string, string, string) (*torbox.TaskStatus, error) {
		return nil, errors.New("active lookup unavailable")
	}
	env.mock.FindQueuedTaskFn = func(_ context.Context, _, queuedID, _, _ string) (*torbox.TaskStatus, error) {
		return &torbox.TaskStatus{QueuedID: queuedID}, nil
	}
	env.mock.DeleteTaskFn = func(context.Context, string, string) error {
		activeDeletes++
		return nil
	}
	env.mock.DeleteQueuedTaskFn = func(context.Context, string, string) error {
		queuedDeletes++
		return nil
	}

	job := env.insertRemovePendingJob(t, "lookup-partial", "401", store.SourceTypeTorrent)
	job.QueuedID = ptr("402")
	if err := env.store.UpdateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := env.orch.processRemoveJob(context.Background(), job); err != nil {
		t.Fatalf("processRemoveJob: %v", err)
	}

	if activeDeletes != 0 || queuedDeletes != 1 {
		t.Fatalf("delete calls = active:%d queued:%d, want active 0 and queued 1", activeDeletes, queuedDeletes)
	}
	got, err := env.store.GetJobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemovePending {
		t.Fatalf("state = %s, want remove_pending", got.State)
	}
	if got.Metadata.QueuedRemoval.Outcome != store.UpstreamRemovalDeleted {
		t.Fatalf("queued outcome = %q, want deleted", got.Metadata.QueuedRemoval.Outcome)
	}
	if got.Metadata.ActiveRemoval.Attempts != 0 || got.Metadata.ActiveRemoval.Outcome != "" {
		t.Fatalf("active progress = %#v, want unresolved with no delete attempt", got.Metadata.ActiveRemoval)
	}
}

func TestProcessRemoveJob_QueuedPromotionRefreshesActiveView(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true
	var activeLookups, activeDeletes int
	env.mock.FindActiveTaskFn = func(_ context.Context, _, remoteID, _, _ string) (*torbox.TaskStatus, error) {
		activeLookups++
		if remoteID == "901" {
			return &torbox.TaskStatus{RemoteID: remoteID}, nil
		}
		return nil, nil
	}
	env.mock.FindQueuedTaskFn = func(_ context.Context, _, queuedID, _, _ string) (*torbox.TaskStatus, error) {
		return &torbox.TaskStatus{QueuedID: queuedID, RemoteID: "901"}, nil
	}
	env.mock.DeleteTaskFn = func(_ context.Context, _, remoteID string) error {
		activeDeletes++
		if remoteID != "901" {
			t.Fatalf("active delete id = %q, want 901", remoteID)
		}
		return nil
	}

	job := env.insertRemovePendingJob(t, "queued-promotion", "", store.SourceTypeTorrent)
	job.QueuedID = ptr("902")
	if err := env.store.UpdateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := env.orch.processRemoveJob(context.Background(), job); err != nil {
		t.Fatalf("processRemoveJob: %v", err)
	}
	if activeLookups != 2 || activeDeletes != 1 {
		t.Fatalf("active calls = lookups:%d deletes:%d, want 2 and 1", activeLookups, activeDeletes)
	}
}

func TestProcessRemoveJob_LookupFailuresAreBounded(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true
	env.mock.FindActiveTaskFn = func(context.Context, string, string, string, string) (*torbox.TaskStatus, error) {
		return nil, errors.New("lookup unavailable")
	}

	job := env.insertRemovePendingJob(t, "lookup-exhausted", "903", store.SourceTypeTorrent)
	for attempt := 1; attempt <= maxUpstreamReconciliationFailures; attempt++ {
		if err := env.orch.processRemoveJob(context.Background(), job); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		var err error
		job, err = env.store.GetJobByID(context.Background(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if job.State != store.StateRemoved || job.Metadata.ActiveRemoval.Outcome != store.UpstreamRemovalExhausted {
		t.Fatalf("state = %s outcome = %q, want removed/exhausted", job.State, job.Metadata.ActiveRemoval.Outcome)
	}
	if job.Metadata.ActiveRemoval.Attempts != 0 || job.Metadata.ActiveRemoval.ReconciliationFailures != maxUpstreamReconciliationFailures {
		t.Fatalf("progress = %#v, want zero delete attempts and bounded reconciliation failures", job.Metadata.ActiveRemoval)
	}
}

func TestProcessRemoveJob_AlreadyAbsentDoesNotWarn(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true
	env.mock.FindActiveTaskFn = func(context.Context, string, string, string, string) (*torbox.TaskStatus, error) {
		return nil, nil
	}
	env.mock.FindQueuedTaskFn = func(context.Context, string, string, string, string) (*torbox.TaskStatus, error) {
		return nil, nil
	}
	env.mock.DeleteTaskFn = func(context.Context, string, string) error {
		t.Fatal("active delete must not be called for an absent task")
		return nil
	}
	env.mock.DeleteQueuedTaskFn = func(context.Context, string, string) error {
		t.Fatal("queued delete must not be called for an absent task")
		return nil
	}

	job := env.insertRemovePendingJob(t, "absent-view", "401", store.SourceTypeTorrent)
	job.QueuedID = ptr("402")
	if err := env.store.UpdateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := env.orch.processRemoveJob(context.Background(), job); err != nil {
		t.Fatalf("processRemoveJob: %v", err)
	}
	got, err := env.store.GetJobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ErrorMessage != nil {
		t.Fatalf("ErrorMessage = %v, want nil for already-absent cleanup", got.ErrorMessage)
	}
	if got.Metadata.ActiveRemoval.Outcome != store.UpstreamRemovalAbsent || got.Metadata.QueuedRemoval.Outcome != store.UpstreamRemovalAbsent {
		t.Fatalf("outcomes = active:%q queued:%q, want absent for both", got.Metadata.ActiveRemoval.Outcome, got.Metadata.QueuedRemoval.Outcome)
	}
}

func TestProcessRemoveJob_DefinitiveRejectionPersistsWarning(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true
	env.mock.FindActiveTaskFn = func(_ context.Context, _, remoteID, _, _ string) (*torbox.TaskStatus, error) {
		return &torbox.TaskStatus{RemoteID: remoteID}, nil
	}
	env.mock.DeleteTaskFn = func(context.Context, string, string) error {
		return errors.New("bad request")
	}

	job := env.insertRemovePendingJob(t, "reject-view", "501", store.SourceTypeTorrent)
	if err := env.orch.processRemoveJob(context.Background(), job); err != nil {
		t.Fatalf("processRemoveJob: %v", err)
	}
	got, err := env.store.GetJobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemoved {
		t.Fatalf("state = %s, want removed", got.State)
	}
	if got.ErrorMessage == nil || !strings.Contains(*got.ErrorMessage, "active") {
		t.Fatalf("ErrorMessage = %v, want active cleanup warning", got.ErrorMessage)
	}
	if got.Metadata.ActiveRemoval.Outcome != store.UpstreamRemovalRejected {
		t.Fatalf("active outcome = %q, want rejected", got.Metadata.ActiveRemoval.Outcome)
	}
}

func TestProcessRemoveJob_RequestNotSentDoesNotConsumeAttempt(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true
	env.mock.DeleteTaskFn = func(context.Context, string, string) error {
		return &torbox.RequestNotSentError{Err: context.Canceled}
	}

	job := env.insertRemovePendingJob(t, "not-sent-view", "601", store.SourceTypeTorrent)
	if err := env.orch.processRemoveJob(context.Background(), job); err != nil {
		t.Fatalf("processRemoveJob: %v", err)
	}
	got, err := env.store.GetJobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemovePending {
		t.Fatalf("state = %s, want remove_pending", got.State)
	}
	if got.Metadata.ActiveRemoval.Attempts != 0 {
		t.Fatalf("active attempts = %d, want 0", got.Metadata.ActiveRemoval.Attempts)
	}
	if _, err := os.Stat(filepath.Join(env.dir, "completed", job.ID, "file.mkv")); err != nil {
		t.Fatalf("local payload should remain: %v", err)
	}
}

func TestLegacyRemovalProgressClearsLegacyCounter(t *testing.T) {
	job := &store.Job{Metadata: store.SubmissionMetadata{
		UpstreamDeleteAttempts: 4,
		ActiveRemoval: store.UpstreamRemovalProgress{
			Attempts: 2,
		},
	}}

	legacyRemovalProgress(job)

	if job.Metadata.UpstreamDeleteAttempts != 0 {
		t.Fatalf("legacy attempts = %d, want 0", job.Metadata.UpstreamDeleteAttempts)
	}
	if job.Metadata.ActiveRemoval.Attempts != 2 {
		t.Fatalf("active attempts = %d, want existing value 2", job.Metadata.ActiveRemoval.Attempts)
	}
}
