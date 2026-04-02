package worker

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
)

func (o *Orchestrator) runFinalizer(ctx context.Context) error {
	jobs, err := o.store.ClaimJobsDue(ctx, "finalizer", []store.JobState{store.StateLocalVerify}, time.Now().UTC(), o.cfg.Workers.BatchSize)
	if err != nil {
		return err
	}
	if len(jobs) > 0 {
		o.log.Debug("finalizer fetched jobs", "count", len(jobs))
	}
	for _, job := range jobs {
		if err := o.processFinalizeJob(ctx, job); err != nil {
			o.log.Error("finalize job failed", "job_id", job.ID, "error", err)
		}
		o.releaseJobClaim(ctx, "finalizer", job.ID)
	}
	return nil
}

func (o *Orchestrator) processFinalizeJob(ctx context.Context, job *store.Job) error {
	o.log.Info("verifying completed download", "job_id", job.ID, "public_id", job.PublicID, "staging_path", deref(job.StagingPath))
	parts, err := o.store.ListTransferParts(ctx, job.ID)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		msg := "no transfer parts available for verification"
		job.ErrorMessage = &msg
		job.UpdatedAt = time.Now().UTC()
		return o.store.UpdateJobState(ctx, job, store.StateFailed, msg)
	}
	for _, part := range parts {
		info, err := os.Stat(part.TempPath)
		if err != nil {
			msg := fmt.Sprintf("missing downloaded part %s", part.TempPath)
			job.ErrorMessage = &msg
			job.UpdatedAt = time.Now().UTC()
			nextRun := time.Now().UTC().Add(o.cfg.Workers.DownloadInterval)
			job.NextRunAt = &nextRun
			return o.store.UpdateJobState(ctx, job, store.StateLocalDownloading, msg)
		}
		if part.ContentLength > 0 && info.Size() < part.ContentLength {
			msg := fmt.Sprintf("part %s is incomplete", part.TempPath)
			job.ErrorMessage = &msg
			job.UpdatedAt = time.Now().UTC()
			nextRun := time.Now().UTC().Add(o.cfg.Workers.DownloadInterval)
			job.NextRunAt = &nextRun
			return o.store.UpdateJobState(ctx, job, store.StateLocalDownloading, msg)
		}
	}

	completedPath := o.layout.CompletedPathForJob(job.Category, job.DisplayName, job.ID)
	if err := o.layout.Promote(deref(job.StagingPath), completedPath); err != nil {
		msg := err.Error()
		job.ErrorMessage = &msg
		job.UpdatedAt = time.Now().UTC()
		return o.store.UpdateJobState(ctx, job, store.StateFailed, msg)
	}

	o.syncJobBytesFromParts(job, parts)
	job.CompletedPath = &completedPath
	job.ErrorMessage = nil
	job.NextRunAt = nil
	job.UpdatedAt = time.Now().UTC()
	o.log.Info("job finalized", "job_id", job.ID, "public_id", job.PublicID, "completed_path", completedPath, "bytes_done", job.BytesDone, "bytes_total", job.BytesTotal)
	return o.store.UpdateJobState(ctx, job, store.StateCompleted, "moved to completed path")
}
