package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
)

func TestUncertainSubmissionReconcilesNon521Timeout(t *testing.T) {
	env := newQueuedTestEnv(t)
	ctx := context.Background()
	job := queuedTestJob("uncertain-timeout", store.StateSubmitPending)
	job.InfoHash = stringPtr("owned")
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	creates := 0
	env.mock.CreateTorrentTaskFn = func(context.Context, torbox.CreateTorrentTaskRequest) (*torbox.CreateTaskResponse, error) {
		creates++
		return nil, torbox.MarkRetryable(context.DeadlineExceeded)
	}
	if err := env.orch.processSubmitJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	job, _ = env.store.GetJobByID(ctx, job.ID)
	if !job.Metadata.SubmissionUncertain {
		t.Fatal("uncertainty was not persisted")
	}
	env.mock.FindActiveTaskByIdentityFn = func(context.Context, string, string, string, string, string) (*torbox.TaskStatus, error) {
		return &torbox.TaskStatus{RemoteID: "42", Hash: "owned"}, nil
	}
	if err := env.orch.processSubmitJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	got, err := env.store.GetJobByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if creates != 1 || got.State != store.StateRemoteActive || got.Metadata.SubmissionUncertain {
		t.Fatalf("creates = %d, state = %s, uncertainty = %v", creates, got.State, got.Metadata.SubmissionUncertain)
	}
}

func TestSubmissionReconciliationDoesNotCreateAfterRemoval(t *testing.T) {
	env := newQueuedTestEnv(t)
	ctx := context.Background()
	job := queuedTestJob("reconcile-removal", store.StateSubmitRetry)
	job.InfoHash = stringPtr("owned")
	job.Metadata.SubmissionUncertain = true
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	env.mock.FindQueuedTaskFn = func(context.Context, string, string, string, string) (*torbox.TaskStatus, error) {
		current, err := env.store.GetJobByID(ctx, job.ID)
		if err != nil {
			return nil, err
		}
		current.DeleteRequested = true
		if err := env.store.UpdateJobState(ctx, current, store.StateRemovePending, "remove during reconciliation"); err != nil {
			return nil, err
		}
		return &torbox.TaskStatus{QueuedID: "12", Hash: "owned"}, nil
	}
	creates := 0
	env.mock.CreateTorrentTaskFn = func(context.Context, torbox.CreateTorrentTaskRequest) (*torbox.CreateTaskResponse, error) {
		creates++
		return &torbox.CreateTaskResponse{RemoteID: "99"}, nil
	}
	if err := env.orch.processSubmitJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	got, err := env.store.GetJobByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if creates != 0 || got.State != store.StateRemovePending || deref(got.QueuedID) != "12" {
		t.Fatalf("creates = %d, state = %s, queue ID = %s", creates, got.State, deref(got.QueuedID))
	}
}

func TestSubmissionRecoveryWithOnlyRemoteIDUsesActiveView(t *testing.T) {
	env := newQueuedTestEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/usenet/mylist" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":123,"download_state":"downloading"}}`))
	}))
	defer server.Close()
	env.orch.torbox = torbox.NewHTTPClient(nil, server.URL, "test", "test", time.Second, nil, nil, nil)
	job := queuedTestJob("remote-only", store.StateSubmitRetry)
	job.SourceType, job.RemoteID, job.Metadata.SubmissionUncertain = store.SourceTypeNZB, stringPtr("123"), true
	if err := env.store.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := env.orch.processSubmitJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	got, err := env.store.GetJobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemoteActive {
		t.Fatalf("state = %s, want remote_active", got.State)
	}
}

func TestUnidentifiableUncertainSubmissionDoesNotBlindlyRetry(t *testing.T) {
	env := newQueuedTestEnv(t)
	job := queuedTestJob("unknown-outcome", store.StateSubmitRetry)
	job.Metadata.SubmissionUncertain = true
	if err := env.store.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	env.mock.CreateTorrentTaskFn = func(context.Context, torbox.CreateTorrentTaskRequest) (*torbox.CreateTaskResponse, error) {
		t.Error("unidentified uncertain submission was sent again")
		return nil, context.Canceled
	}
	if err := env.orch.processSubmitJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	got, err := env.store.GetJobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Metadata.SubmissionUncertain || got.NextRunAt == nil || got.ErrorMessage == nil {
		t.Fatalf("uncertain submission lost its pending reconciliation state: %#v", got)
	}
}

func TestSubmissionUncertaintySurvivesDatabaseReopen(t *testing.T) {
	env := newQueuedTestEnv(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recovery.db")
	db, err := store.Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RunMigrationsFS(db, store.EmbeddedMigrations); err != nil {
		db.Close()
		t.Fatal(err)
	}
	st := store.New(db)
	job := queuedTestJob("restart-uncertain", store.StateSubmitPending)
	job.InfoHash = stringPtr("owned")
	if err := st.CreateJob(ctx, job); err != nil {
		db.Close()
		t.Fatal(err)
	}
	env.mock.CreateTorrentTaskFn = func(context.Context, torbox.CreateTorrentTaskRequest) (*torbox.CreateTaskResponse, error) {
		persisted, err := st.GetJobByID(ctx, job.ID)
		if err != nil {
			return nil, err
		}
		if !persisted.Metadata.SubmissionUncertain {
			t.Error("create started without durable uncertainty intent")
		}
		return nil, torbox.MarkRetryable(context.DeadlineExceeded)
	}
	orch := NewOrchestrator(env.orch.cfg, env.orch.log, st, env.orch.layout, env.orch.downloader, env.mock)
	if err := orch.processSubmitJob(ctx, job); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st = store.New(db)
	job, err = st.GetJobByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !job.Metadata.SubmissionUncertain {
		t.Fatal("uncertainty was lost on database reopen")
	}
	env.mock.CreateTorrentTaskFn = func(context.Context, torbox.CreateTorrentTaskRequest) (*torbox.CreateTaskResponse, error) {
		t.Error("restart sent a duplicate create")
		return nil, context.Canceled
	}
	env.mock.FindActiveTaskByIdentityFn = func(context.Context, string, string, string, string, string) (*torbox.TaskStatus, error) {
		return &torbox.TaskStatus{RemoteID: "42", Hash: "owned"}, nil
	}
	orch = NewOrchestrator(env.orch.cfg, env.orch.log, st, env.orch.layout, env.orch.downloader, env.mock)
	if err := orch.processSubmitJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetJobByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRemoteActive || got.Metadata.SubmissionUncertain {
		t.Fatalf("recovered state = %s, uncertainty = %v", got.State, got.Metadata.SubmissionUncertain)
	}
}

func TestSubmissionNotSentCanRetryWithoutIdentity(t *testing.T) {
	env := newQueuedTestEnv(t)
	ctx := context.Background()
	job := queuedTestJob("not-sent", store.StateSubmitPending)
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	creates := 0
	env.mock.CreateTorrentTaskFn = func(context.Context, torbox.CreateTorrentTaskRequest) (*torbox.CreateTaskResponse, error) {
		creates++
		if creates == 1 {
			return nil, &torbox.RequestNotSentError{Err: context.DeadlineExceeded}
		}
		return &torbox.CreateTaskResponse{RemoteID: "42"}, nil
	}
	if err := env.orch.processSubmitJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	job, err := env.store.GetJobByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != store.StateSubmitRetry || job.Metadata.SubmissionUncertain {
		t.Fatalf("state = %s, uncertainty = %v", job.State, job.Metadata.SubmissionUncertain)
	}
	if err := env.orch.processSubmitJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	got, err := env.store.GetJobByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if creates != 2 || got.State != store.StateRemoteActive {
		t.Fatalf("creates = %d, state = %s", creates, got.State)
	}
}

func TestProcessPollJob_HTTPConfirmedAbsenceReachesLimit(t *testing.T) {
	for _, state := range []store.JobState{store.StateRemoteQueued, store.StateRemoteActive} {
		for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusInternalServerError} {
			t.Run(string(state)+"/"+http.StatusText(status), func(t *testing.T) {
				env := newQueuedTestEnv(t)
				queueRequests, activeRequests := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/queued/getqueued":
						queueRequests++
					case "/api/torrents/mylist":
						activeRequests++
					default:
						t.Errorf("unexpected request %s", r.URL.Path)
					}
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"success":false,"error":"ITEM_NOT_FOUND","detail":"No matching task"}`))
				}))
				defer server.Close()
				env.orch.torbox = torbox.NewHTTPClient(nil, server.URL, "test", "test", time.Second, nil, nil, nil)
				ctx := context.Background()
				job := queuedTestJob("confirmed-absence", state)
				job.QueuedID, job.RemoteHash = stringPtr("12"), stringPtr("owned")
				if state == store.StateRemoteActive {
					job.RemoteID = stringPtr("42")
				}
				if err := env.store.CreateJob(ctx, job); err != nil {
					t.Fatal(err)
				}
				for attempt := 1; attempt <= env.orch.cfg.Workers.RemoteAbsenceAttempts; attempt++ {
					if err := env.orch.processPollJob(ctx, job); err != nil {
						t.Fatal(err)
					}
					var err error
					job, err = env.store.GetJobByID(ctx, job.ID)
					if err != nil {
						t.Fatal(err)
					}
					if job.Metadata.PollAttempts != attempt {
						t.Fatalf("absence attempts = %d, want %d", job.Metadata.PollAttempts, attempt)
					}
					if attempt < env.orch.cfg.Workers.RemoteAbsenceAttempts && (job.State != state || job.NextRunAt == nil) {
						t.Fatalf("job failed before absence limit: %#v", job)
					}
				}
				if job.State != store.StateRemoteFailed || job.NextRunAt != nil || queueRequests == 0 || activeRequests == 0 {
					t.Fatalf("state = %s, next run = %v, queue requests = %d, active requests = %d", job.State, job.NextRunAt, queueRequests, activeRequests)
				}
			})
		}
	}
}
