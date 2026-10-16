package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mrjoiny/torboxarr/internal/config"
	"github.com/mrjoiny/torboxarr/internal/files"
	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
)

type Orchestrator struct {
	cfg        *config.Config
	log        *slog.Logger
	store      *store.Store
	layout     *files.Layout
	downloader *files.RangeDownloader
	torbox     torbox.Client
	claimToken string
	wg         sync.WaitGroup
}

func NewOrchestrator(cfg *config.Config, log *slog.Logger, st *store.Store, layout *files.Layout, downloader *files.RangeDownloader, client torbox.Client) *Orchestrator {
	return &Orchestrator{
		cfg:        cfg,
		log:        log,
		store:      st,
		layout:     layout,
		downloader: downloader,
		torbox:     client,
		claimToken: strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36),
	}
}

func (o *Orchestrator) Start(ctx context.Context) error {
	if err := o.reconcileStartup(ctx); err != nil {
		return err
	}

	o.log.Debug("starting worker loops")
	startWorker := func(name string, interval time.Duration, fn func(context.Context) error) {
		o.wg.Go(func() {
			o.runLoop(ctx, name, interval, fn)
		})
	}
	startWorker("submitter", o.cfg.Workers.SubmitInterval, o.runSubmitter)
	startWorker("poller", o.cfg.Workers.PollInterval, o.runPoller)
	startWorker("downloader", o.cfg.Workers.DownloadInterval, o.runDownloader)
	startWorker("finalizer", o.cfg.Workers.FinalizeInterval, o.runFinalizer)
	startWorker("remover", o.cfg.Workers.RemoveInterval, o.runRemover)
	startWorker("pruner", o.cfg.Workers.PruneInterval, o.runPruner)
	return nil
}

// Wait blocks until all worker goroutines have exited.
func (o *Orchestrator) Wait() {
	o.wg.Wait()
}

func (o *Orchestrator) runLoop(ctx context.Context, name string, every time.Duration, fn func(context.Context) error) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	var running atomic.Bool

	execute := func() {
		if !running.CompareAndSwap(false, true) {
			o.log.Debug("worker tick skipped, previous still running", "worker", name)
			return
		}
		defer running.Store(false)

		if err := fn(ctx); err != nil {
			o.log.Error("worker iteration failed", "worker", name, "error", err)
		}
	}

	o.log.Debug("worker loop started", "worker", name, "interval", every.String())
	execute()

	for {
		select {
		case <-ctx.Done():
			o.log.Debug("worker loop stopped", "worker", name)
			return
		case <-ticker.C:
			execute()
		}
	}
}

func (o *Orchestrator) reconcileStartup(ctx context.Context) error {
	if err := o.store.RecoverClaims(ctx, time.Now().UTC().Add(-o.removerClaimLease())); err != nil {
		return fmt.Errorf("release stale claims: %w", err)
	}
	if err := o.recoverFailedQueuedJobs(ctx); err != nil {
		return fmt.Errorf("recover failed queued jobs: %w", err)
	}
	if err := o.recoverHistorical521Failures(ctx); err != nil {
		return fmt.Errorf("recover historical 521 failures: %w", err)
	}

	jobs, err := o.store.ListOpenJobs(ctx)
	if err != nil {
		return err
	}
	o.log.Info("reconciling startup jobs", "count", len(jobs))
	validIDs := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		o.log.Debug("reconciling job", "job_id", job.ID, "public_id", job.PublicID, "state", job.State)
		validIDs[job.ID] = struct{}{}
		if job.StagingPath == nil {
			staging := o.layout.StagingPathForJob(job.ID)
			job.StagingPath = &staging
		}
		if job.State == store.StateCompleted || job.State == store.StateRemovePending {
			if job.CompletedPath != nil {
				if _, err := os.Stat(*job.CompletedPath); err == nil {
					continue
				}
			}
		}
		if job.CompletedPath != nil {
			if _, err := os.Stat(*job.CompletedPath); err == nil && job.State != store.StateCompleted && job.State != store.StateRemovePending {
				job.UpdatedAt = time.Now().UTC()
				if err := o.store.UpdateJobState(ctx, job, store.StateCompleted, "startup reconciliation promoted completed path"); err != nil {
					o.log.Error("reconciliation state update failed", "job_id", job.ID, "error", err)
				}
				continue
			}
		}
		if job.StagingPath != nil {
			if _, err := os.Stat(*job.StagingPath); err == nil {
				if job.State == store.StateLocalDownloadPending || job.State == store.StateLocalDownloading || job.State == store.StateLocalVerify {
					parts, _ := o.store.ListTransferParts(ctx, job.ID)
					if allPartsCompleted(parts) && len(parts) > 0 {
						now := time.Now().UTC()
						job.NextRunAt = &now
						job.UpdatedAt = now
						o.log.Info("startup reconciliation advanced job to verify", "job_id", job.ID, "public_id", job.PublicID)
						if err := o.store.UpdateJobState(ctx, job, store.StateLocalVerify, "startup reconciliation detected complete staging payload"); err != nil {
							o.log.Error("reconciliation state update failed", "job_id", job.ID, "error", err)
						}
						continue
					}
				}
			}
		}
		if needsWakeup(job.State) && job.NextRunAt == nil {
			now := time.Now().UTC()
			job.NextRunAt = &now
			job.UpdatedAt = now
			o.log.Debug("startup reconciliation woke sleeping job", "job_id", job.ID, "public_id", job.PublicID)
			if err := o.store.UpdateJob(ctx, job); err != nil {
				o.log.Error("reconciliation job update failed", "job_id", job.ID, "error", err)
			}
		}
	}
	_, err = o.layout.RemoveOrphanStagingDirs(validIDs)
	return err
}

func (o *Orchestrator) removerClaimLease() time.Duration {
	return max(5*time.Minute, o.cfg.TorBox.RequestTimeout*time.Duration(max(o.cfg.Workers.BatchSize, 1)*8))
}

// recoverHistorical521Failures gives jobs failed by an older build one
// bounded recovery sequence. The recorded failure counts as the first create
// attempt, so the first reopened attempt uses the 15-minute stage.
func (o *Orchestrator) recoverHistorical521Failures(ctx context.Context) error {
	jobs, err := o.store.ListRemoteFailed521(ctx)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.DeleteRequested || deref(job.SourceURI) == "" || job.Metadata.Torbox521Attempts >= maxTorbox521Attempts {
			continue
		}
		if job.Metadata.Torbox521Attempts == 0 {
			job.Metadata.Torbox521Attempts = 1
		}
		nextRun := time.Now().UTC().Add(torbox521RetryDelays[0])
		job.NextRunAt = &nextRun
		job.UpdatedAt = time.Now().UTC()
		message := fmt.Sprintf("historical HTTP 521 failure scheduled for retry %d of %d", job.Metadata.Torbox521Attempts+1, maxTorbox521Attempts)
		ok, updateErr := o.store.UpdateJobStateIfCurrent(ctx, job, store.StateRemoteFailed, store.StateSubmitRetry, message)
		if updateErr != nil {
			return updateErr
		}
		if ok {
			o.log.Info("scheduled historical HTTP 521 failure for retry",
				"job_id", job.ID,
				"public_id", job.PublicID,
				"next_run_at", nextRun.Format(time.RFC3339Nano),
			)
		}
	}
	return nil
}

func (o *Orchestrator) recoverFailedQueuedJobs(ctx context.Context) error {
	jobs, err := o.store.ListRemoteFailedWithQueueTracking(ctx)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.DeleteRequested {
			continue
		}
		queued, queueErr := o.torbox.FindQueuedTask(ctx, string(job.SourceType), deref(job.QueuedID), deref(job.QueueAuthID), deref(job.RemoteHash))
		if queueErr == nil && queued != nil {
			job.ErrorMessage = nil
			job.Metadata.PollAttempts = 0
			job.NextRunAt = nil
			job.RemoteID = nil
			if queued.QueuedID != "" {
				job.QueuedID = ptr(queued.QueuedID)
			}
			if queued.Hash != "" {
				job.RemoteHash = ptr(queued.Hash)
			}
			if queued.QueueAuthID != "" {
				job.QueueAuthID = ptr(queued.QueueAuthID)
			}
			job.UpdatedAt = time.Now().UTC()
			ok, err := o.store.UpdateJobStateIfCurrent(ctx, job, store.StateRemoteFailed, store.StateRemoteQueued, "startup recovery found task in TorBox queue")
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			o.log.Info("recovered failed job in remote queue", "job_id", job.ID, "public_id", job.PublicID, "queued_id", deref(job.QueuedID))
			continue
		}
		if queueErr != nil {
			o.log.Warn("failed queued-job recovery lookup", "job_id", job.ID, "public_id", job.PublicID, "error", queueErr)
			continue
		}

		active, activeErr := o.findActive(ctx, job)
		if activeErr != nil {
			o.log.Warn("failed active recovery lookup", "job_id", job.ID, "public_id", job.PublicID, "error", activeErr)
			continue
		}
		if active == nil || active.Failed || active.Inactive {
			continue
		}
		job.ErrorMessage = nil
		job.Metadata.PollAttempts = 0
		job.NextRunAt = nil
		if active.RemoteID != "" {
			job.RemoteID = ptr(active.RemoteID)
		}
		if active.Hash != "" {
			job.RemoteHash = ptr(active.Hash)
		}
		if active.QueueAuthID != "" {
			job.QueueAuthID = ptr(active.QueueAuthID)
		}
		job.UpdatedAt = time.Now().UTC()
		ok, err := o.store.UpdateJobStateIfCurrent(ctx, job, store.StateRemoteFailed, store.StateRemoteActive, "startup recovery found active TorBox task")
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		o.log.Info("recovered failed job as active", "job_id", job.ID, "public_id", job.PublicID, "remote_id", deref(job.RemoteID))
	}
	return nil
}
