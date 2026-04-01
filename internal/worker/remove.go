package worker

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
)

func (o *Orchestrator) runRemover(ctx context.Context) error {
	jobs, err := o.store.ClaimJobsDue(ctx, "remover", []store.JobState{store.StateRemovePending}, time.Now().UTC(), o.cfg.Workers.BatchSize)
	if err != nil {
		return err
	}
	if len(jobs) > 0 {
		o.log.Debug("remover fetched jobs", "count", len(jobs))
	}
	for _, job := range jobs {
		if err := o.processRemoveJob(ctx, job); err != nil {
			o.log.Error("remove job failed", "job_id", job.ID, "error", err)
		}
	}
	return nil
}

func (o *Orchestrator) processRemoveJob(ctx context.Context, job *store.Job) error {
	o.log.Info("removing local job payloads", "job_id", job.ID, "public_id", job.PublicID, "remote_id", deref(job.RemoteID))
	if job.CompletedPath != nil {
		if err := ensurePathWithinRoot(o.layout.Completed, *job.CompletedPath); err != nil {
			return o.failUnsafeRemoval(ctx, job, "completed path", *job.CompletedPath, err)
		}
		if err := o.layout.RemovePath(*job.CompletedPath); err != nil {
			return fmt.Errorf("remove completed path: %w", err)
		}
		job.CompletedPath = nil
	}
	if job.StagingPath != nil {
		if err := ensurePathWithinRoot(o.layout.Staging, *job.StagingPath); err != nil {
			return o.failUnsafeRemoval(ctx, job, "staging path", *job.StagingPath, err)
		}
		if err := o.layout.RemovePath(*job.StagingPath); err != nil {
			return fmt.Errorf("remove staging path: %w", err)
		}
		job.StagingPath = nil
	}
	if job.PayloadRef != nil {
		payloadDir := filepath.Dir(*job.PayloadRef)
		if err := ensurePathWithinRoot(o.layout.Payloads, payloadDir); err != nil {
			return o.failUnsafeRemoval(ctx, job, "payload path", payloadDir, err)
		}
		if err := o.layout.RemovePath(payloadDir); err != nil {
			return fmt.Errorf("remove payload path: %w", err)
		}
		job.PayloadRef = nil
	}
	job.ErrorMessage = nil
	job.NextRunAt = nil
	job.UpdatedAt = time.Now().UTC()
	o.log.Info("job removed locally; torbox content retained", "job_id", job.ID, "public_id", job.PublicID, "remote_id", deref(job.RemoteID))
	return o.store.UpdateJobState(ctx, job, store.StateRemoved, "local payload removed; torbox content retained")
}

func (o *Orchestrator) runPruner(ctx context.Context) error {
	cutoff := time.Now().UTC().Add(-o.cfg.Workers.RemovedRetention)
	o.log.Debug("running pruner", "cutoff", cutoff.Format(time.RFC3339Nano))
	removedRows, err := o.store.DeleteRemovedOlderThan(ctx, cutoff)
	if err != nil {
		return err
	}
	if removedRows > 0 {
		o.log.Info("pruned removed jobs", "count", removedRows)
	}
	sessions, err := o.store.PruneExpiredQBitSessions(ctx)
	if err != nil {
		return err
	}
	if sessions > 0 {
		o.log.Info("pruned expired qbit sessions", "count", sessions)
	}
	return nil
}

func (o *Orchestrator) failUnsafeRemoval(ctx context.Context, job *store.Job, label, path string, err error) error {
	msg := fmt.Sprintf("refusing to remove %s outside configured root: %s", label, path)
	job.ErrorMessage = &msg
	job.NextRunAt = nil
	job.UpdatedAt = time.Now().UTC()
	o.log.Error("unsafe removal path rejected", "job_id", job.ID, "public_id", job.PublicID, "label", label, "path", path, "error", err)
	return o.store.UpdateJobState(ctx, job, store.StateFailed, msg)
}
