package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
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
	// Release all stale claims from a previous run (crash recovery)
	if err := o.store.ReleaseAllClaims(ctx); err != nil {
		return fmt.Errorf("release stale claims: %w", err)
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
