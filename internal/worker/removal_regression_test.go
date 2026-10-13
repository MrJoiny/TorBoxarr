package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
)

func TestUpstreamRemoveDisabledKeepsStructuredOutagesRetryable(t *testing.T) {
	for _, phase := range []store.JobState{store.StateSubmitPending, store.StateRemoteActive} {
		t.Run(string(phase), func(t *testing.T) {
			env := newRemoveTestEnv(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"success":false,"error":"DATABASE_ERROR"}`))
			}))
			defer server.Close()
			env.orch.torbox = torbox.NewHTTPClient(nil, server.URL, "test", "test", time.Second, nil, nil, nil)
			job := &store.Job{ID: "outage", PublicID: "outage", SubmissionKey: "outage", State: phase,
				SourceType: store.SourceTypeTorrent, ClientKind: store.ClientKindQBit,
				RemoteID: ptr("123"), SourceURI: ptr("magnet:?xt=urn:btih:abc"),
				CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			ctx := context.Background()
			if err := env.store.CreateJob(ctx, job); err != nil {
				t.Fatal(err)
			}
			var err error
			want := phase
			if phase == store.StateSubmitPending {
				err = env.orch.processSubmitJob(ctx, job)
				want = store.StateSubmitRetry
			} else {
				err = env.orch.processPollJob(ctx, job)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := env.store.GetJobByID(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != want || got.NextRunAt == nil {
				t.Fatalf("state = %s, next run = %v, want %s with a retry", got.State, got.NextRunAt, want)
			}
		})
	}
}

func TestRemoveDuringSubmissionCleansLateUpstreamTask(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true
	ctx := context.Background()
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	env.mock.CreateTorrentTaskFn = func(context.Context, torbox.CreateTorrentTaskRequest) (*torbox.CreateTaskResponse, error) {
		close(started)
		<-release
		return &torbox.CreateTaskResponse{RemoteID: "731"}, nil
	}
	job := &store.Job{ID: "late-create", PublicID: "late-create", SubmissionKey: "late-create",
		State: store.StateSubmitPending, SourceType: store.SourceTypeTorrent, ClientKind: store.ClientKindQBit,
		SourceURI: ptr("magnet:?xt=urn:btih:abc"), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	go func() { finished <- env.orch.runSubmitter(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("submitter did not start")
	}
	if err := env.store.MarkJobRemovePending(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := env.orch.runRemover(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := env.store.GetJobByID(ctx, job.ID)
	if got.State != store.StateRemovePending {
		t.Fatalf("state = %s while submitter owns claim", got.State)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	var deleted string
	env.mock.DeleteTaskFn = func(_ context.Context, _, id string) error { deleted = id; return nil }
	if err := env.orch.runRemover(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = env.store.GetJobByID(ctx, job.ID)
	if deleted != "731" || got.State != store.StateRemoved {
		t.Fatalf("deleted = %q, state = %s, want late task deleted and local job removed", deleted, got.State)
	}
}

func TestRemoveCancelsStalledLocalTransfer(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true
	env.orch.cfg.Workers.DownloadInterval = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	env.mock.GetDownloadLinksFn = func(context.Context, string, string) ([]torbox.DownloadAsset, error) {
		return []torbox.DownloadAsset{{URL: server.URL, FileID: "1", RelativePath: "episode.mkv", Size: 100}}, nil
	}
	staging := filepath.Join(env.dir, "staging", "cancel-transfer")
	job := &store.Job{ID: "cancel-transfer", PublicID: "cancel-transfer", SubmissionKey: "cancel-transfer",
		State: store.StateLocalDownloadPending, SourceType: store.SourceTypeTorrent, ClientKind: store.ClientKindQBit,
		RemoteID: ptr("931"), StagingPath: &staging, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- env.orch.runDownloader(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("download did not start")
	}
	if err := env.store.MarkJobRemovePending(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := env.orch.runRemover(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("removal did not cancel stalled download")
	}
	if err := env.orch.runRemover(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := env.store.GetJobByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemoved {
		t.Fatalf("state = %s, want removed", got.State)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging directory remains: %v", err)
	}
}

func TestRemoveAlreadyAbsentHTTPTaskDoesNotWarn(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error":"ITEM_NOT_FOUND"}`))
	}))
	defer server.Close()
	env.orch.torbox = torbox.NewHTTPClient(nil, server.URL, "test", "test", time.Second, nil, nil, nil)
	job := env.insertRemovePendingJob(t, "already-absent-http", "123", store.SourceTypeTorrent)
	if err := env.orch.processRemoveJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	got, err := env.store.GetJobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemoved || got.ErrorMessage != nil || got.Metadata.ActiveRemoval.Outcome != store.UpstreamRemovalAbsent {
		t.Fatalf("state = %s, error = %v, outcome = %s", got.State, got.ErrorMessage, got.Metadata.ActiveRemoval.Outcome)
	}
}

func TestRemovePromotionPreservesAccumulatedBudget(t *testing.T) {
	env := newRemoveTestEnv(t)
	env.orch.cfg.UpstreamRemove = true
	ctx := context.Background()
	job := env.insertRemovePendingJob(t, "promotion-budget", "", store.SourceTypeTorrent)
	job.QueuedID, job.RemoteHash = ptr("12"), ptr("owned-hash")
	job.Metadata.ActiveRemoval.Attempts = 4
	if err := env.store.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	calls := 0
	env.mock.FindActiveTaskByIdentityFn = func(context.Context, string, string, string, string, string) (*torbox.TaskStatus, error) {
		calls++
		if calls == 1 {
			return nil, nil
		}
		return &torbox.TaskStatus{RemoteID: "34", Hash: "owned-hash"}, nil
	}
	env.mock.FindQueuedTaskFn = func(context.Context, string, string, string, string) (*torbox.TaskStatus, error) {
		return &torbox.TaskStatus{QueuedID: "12", RemoteID: "34", Hash: "owned-hash"}, nil
	}
	env.mock.DeleteTaskFn = func(context.Context, string, string) error { return torbox.MarkRetryable(context.DeadlineExceeded) }
	if err := env.orch.processRemoveJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	got, err := env.store.GetJobByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemoved || got.Metadata.ActiveRemoval.Attempts != 5 || got.Metadata.ActiveRemoval.Outcome != store.UpstreamRemovalExhausted {
		t.Fatalf("state = %s, attempts = %d, outcome = %s", got.State, got.Metadata.ActiveRemoval.Attempts, got.Metadata.ActiveRemoval.Outcome)
	}
}

func TestRemoveRechecksActiveAfterQueuedPromotion(t *testing.T) {
	for _, duringDelete := range []bool{false, true} {
		t.Run(map[bool]string{false: "between-lookups", true: "during-delete"}[duringDelete], func(t *testing.T) {
			env := newRemoveTestEnv(t)
			env.orch.cfg.UpstreamRemove = true
			ctx := context.Background()
			job := env.insertRemovePendingJob(t, "promotion-race", "", store.SourceTypeTorrent)
			job.QueuedID, job.RemoteHash = ptr("12"), ptr("owned-hash")
			if err := env.store.UpdateJob(ctx, job); err != nil {
				t.Fatal(err)
			}
			activeCalls := 0
			env.mock.FindActiveTaskByIdentityFn = func(context.Context, string, string, string, string, string) (*torbox.TaskStatus, error) {
				activeCalls++
				if activeCalls == 1 {
					return nil, nil
				}
				return &torbox.TaskStatus{RemoteID: "34", Hash: "owned-hash"}, nil
			}
			env.mock.FindQueuedTaskFn = func(context.Context, string, string, string, string) (*torbox.TaskStatus, error) {
				if duringDelete {
					return &torbox.TaskStatus{QueuedID: "12", Hash: "owned-hash"}, nil
				}
				return nil, nil
			}
			env.mock.DeleteQueuedTaskFn = func(context.Context, string, string) error {
				return &torbox.ErrTorboxAbsent{Err: context.Canceled}
			}
			var deleted string
			env.mock.DeleteTaskFn = func(_ context.Context, _, id string) error { deleted = id; return nil }
			if err := env.orch.processRemoveJob(ctx, job); err != nil {
				t.Fatal(err)
			}
			got, err := env.store.GetJobByID(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if deleted != "34" || got.State != store.StateRemoved || got.Metadata.ActiveRemoval.Outcome != store.UpstreamRemovalDeleted {
				t.Fatalf("deleted = %q, state = %s, active outcome = %s", deleted, got.State, got.Metadata.ActiveRemoval.Outcome)
			}
		})
	}
}
