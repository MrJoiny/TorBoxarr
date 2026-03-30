package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
)

func (o *Orchestrator) runPoller(ctx context.Context) error {
	jobs, err := o.store.ClaimJobsDue(ctx, "poller", []store.JobState{store.StateRemoteQueued, store.StateRemoteActive}, time.Now().UTC(), o.cfg.Workers.BatchSize)
	if err != nil {
		return err
	}
	if len(jobs) > 0 {
		o.log.Debug("poller fetched jobs", "count", len(jobs))
	}
	for _, job := range jobs {
		if err := o.processPollJob(ctx, job); err != nil {
			o.log.Error("poll job failed", "job_id", job.ID, "error", err)
		}
	}
	return nil
}

func (o *Orchestrator) processPollJob(ctx context.Context, job *store.Job) error {
	if job.State == store.StateRemoteQueued || job.RemoteID == nil {
		return o.processQueuedPollJob(ctx, job)
	}

	o.log.Debug("polling remote job", "job_id", job.ID, "public_id", job.PublicID, "remote_id", deref(job.RemoteID))
	status, err := o.torbox.GetTaskStatus(ctx, string(job.SourceType), deref(job.RemoteID))
	if err != nil {
		if torbox.IsRetryable(err) {
			nextRun := time.Now().UTC().Add(withJitter(o.cfg.Workers.PollInterval))
			job.NextRunAt = &nextRun
			job.UpdatedAt = time.Now().UTC()
			o.log.Warn("poll failed, will retry",
				"job_id", job.ID,
				"public_id", job.PublicID,
				"next_run_at", nextRun.Format(time.RFC3339Nano),
				"error", err.Error(),
			)
			return o.store.UpdateJob(ctx, job)
		}
		msg := err.Error()
		job.ErrorMessage = &msg
		job.NextRunAt = nil
		job.UpdatedAt = time.Now().UTC()
		o.log.Error("poll failed permanently", "job_id", job.ID, "public_id", job.PublicID, "error", msg)
		return o.store.UpdateJobState(ctx, job, store.StateRemoteFailed, msg)
	}
	return o.applyActiveStatus(ctx, job, status)
}

func (o *Orchestrator) processQueuedPollJob(ctx context.Context, job *store.Job) error {
	o.log.Debug("polling queued remote job",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"queued_id", deref(job.QueuedID),
		"queue_auth_id", deref(job.QueueAuthID),
		"remote_hash", deref(job.RemoteHash),
	)
	queuedStatus, err := o.torbox.GetQueuedStatus(ctx, string(job.SourceType), deref(job.QueuedID))
	if err != nil {
		o.log.Warn("queued lookup failed, attempting active recovery",
			"job_id", job.ID,
			"public_id", job.PublicID,
			"queued_id", deref(job.QueuedID),
			"error", err.Error(),
		)
		return o.tryRecoverActiveStatus(ctx, job)
	}
	if queuedStatus == nil {
		o.log.Info("queued item not found in queue response, attempting active recovery",
			"job_id", job.ID,
			"public_id", job.PublicID,
			"queued_id", deref(job.QueuedID),
		)
		return o.tryRecoverActiveStatus(ctx, job)
	}

	if queuedStatus.QueueAuthID != "" {
		job.QueueAuthID = ptr(queuedStatus.QueueAuthID)
	}
	if queuedStatus.Hash != "" {
		job.RemoteHash = ptr(queuedStatus.Hash)
	}
	if queuedStatus.RemoteID != "" {
		job.RemoteID = ptr(queuedStatus.RemoteID)
		job.UpdatedAt = time.Now().UTC()
		o.log.Info("queued task promoted to active",
			"job_id", job.ID,
			"public_id", job.PublicID,
			"queued_id", deref(job.QueuedID),
			"remote_id", deref(job.RemoteID),
			"queue_auth_id", deref(job.QueueAuthID),
		)
		if err := o.store.UpdateJobState(ctx, job, store.StateRemoteActive, "queued task promoted to active"); err != nil {
			return err
		}
		activeStatus, err := o.torbox.FindActiveTask(ctx, string(job.SourceType), deref(job.RemoteID), deref(job.QueueAuthID), deref(job.RemoteHash))
		if err != nil {
			return err
		}
		if activeStatus == nil {
			nextRun := time.Now().UTC().Add(withJitter(o.cfg.Workers.PollInterval))
			job.NextRunAt = &nextRun
			job.UpdatedAt = time.Now().UTC()
			return o.store.UpdateJob(ctx, job)
		}
		return o.applyActiveStatus(ctx, job, activeStatus)
	}

	nextRun := time.Now().UTC().Add(withJitter(o.cfg.Workers.PollInterval))
	job.NextRunAt = &nextRun
	job.UpdatedAt = time.Now().UTC()
	o.log.Info("remote task still queued",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"queued_id", deref(job.QueuedID),
		"queue_state", queuedStatus.State,
		"queue_auth_id", deref(job.QueueAuthID),
		"next_run_at", nextRun.Format(time.RFC3339Nano),
	)
	return o.store.UpdateJob(ctx, job)
}

func (o *Orchestrator) tryRecoverActiveStatus(ctx context.Context, job *store.Job) error {
	status, err := o.torbox.FindActiveTask(ctx, string(job.SourceType), deref(job.RemoteID), deref(job.QueueAuthID), deref(job.RemoteHash))
	if err != nil {
		if torbox.IsRetryable(err) {
			nextRun := time.Now().UTC().Add(withJitter(o.cfg.Workers.PollInterval))
			job.NextRunAt = &nextRun
			job.UpdatedAt = time.Now().UTC()
			o.log.Warn("active recovery failed, will retry",
				"job_id", job.ID,
				"public_id", job.PublicID,
				"next_run_at", nextRun.Format(time.RFC3339Nano),
				"error", err.Error(),
			)
			return o.store.UpdateJob(ctx, job)
		}
		msg := err.Error()
		job.ErrorMessage = &msg
		job.NextRunAt = nil
		job.UpdatedAt = time.Now().UTC()
		return o.store.UpdateJobState(ctx, job, store.StateRemoteFailed, msg)
	}
	if status == nil {
		nextRun := time.Now().UTC().Add(withJitter(o.cfg.Workers.PollInterval))
		job.NextRunAt = &nextRun
		job.UpdatedAt = time.Now().UTC()
		o.log.Info("queued task not yet present in active list",
			"job_id", job.ID,
			"public_id", job.PublicID,
			"queued_id", deref(job.QueuedID),
			"queue_auth_id", deref(job.QueueAuthID),
			"has_remote_hash", job.RemoteHash != nil,
			"next_run_at", nextRun.Format(time.RFC3339Nano),
		)
		return o.store.UpdateJob(ctx, job)
	}
	if status.RemoteID != "" {
		job.RemoteID = ptr(status.RemoteID)
	}
	job.UpdatedAt = time.Now().UTC()
	if job.State != store.StateRemoteActive {
		if err := o.store.UpdateJobState(ctx, job, store.StateRemoteActive, "active task discovered after queue lookup"); err != nil {
			return err
		}
	}
	return o.applyActiveStatus(ctx, job, status)
}

func (o *Orchestrator) applyActiveStatus(ctx context.Context, job *store.Job, status *torbox.TaskStatus) error {
	if status == nil {
		nextRun := time.Now().UTC().Add(withJitter(o.cfg.Workers.PollInterval))
		job.NextRunAt = &nextRun
		job.UpdatedAt = time.Now().UTC()
		return o.store.UpdateJob(ctx, job)
	}
	if status.Name != "" {
		job.DisplayName = status.Name
	}
	if status.RemoteID != "" {
		job.RemoteID = ptr(status.RemoteID)
	}
	if status.QueueAuthID != "" {
		job.QueueAuthID = ptr(status.QueueAuthID)
	}
	if status.Hash != "" {
		job.RemoteHash = ptr(status.Hash)
	}
	if status.State != "" {
		job.LastRemoteStatus = &status.State
	}
	if status.BytesTotal > 0 {
		job.BytesTotal = status.BytesTotal
	}
	if status.BytesDone > job.BytesDone {
		job.BytesDone = status.BytesDone
	} else if status.BytesDone > 0 && status.BytesDone < job.BytesDone {
		o.log.Warn("remote reported lower BytesDone than local",
			"job_id", job.ID,
			"public_id", job.PublicID,
			"local_bytes_done", job.BytesDone,
			"remote_bytes_done", status.BytesDone,
		)
	}
	job.UpdatedAt = time.Now().UTC()
	o.log.Debug("remote job status",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"remote_id", status.RemoteID,
		"state", status.State,
		"label", status.Label,
		"download_present", status.DownloadPresent,
		"download_finished", status.DownloadFinished,
		"download_ready", status.DownloadReady,
		"failed", status.Failed,
		"inactive", status.Inactive,
		"bytes_done", status.BytesDone,
		"bytes_total", status.BytesTotal,
		"files", len(status.Files),
	)

	switch {
	case status.DownloadReady:
		now := time.Now().UTC()
		job.NextRunAt = &now
		job.ErrorMessage = nil
		job.QueuedID = nil
		o.log.Info("remote content ready", "job_id", job.ID, "public_id", job.PublicID, "remote_id", deref(job.RemoteID))
		return o.store.UpdateJobState(ctx, job, store.StateLocalDownloadPending, "remote content ready for local download")
	case status.Failed || status.Inactive:
		msg := status.Error
		if msg == "" {
			msg = fmt.Sprintf("remote task ended in state=%s label=%s", status.State, status.Label)
		}
		job.ErrorMessage = &msg
		job.NextRunAt = nil
		o.log.Error("remote task entered terminal failure state", "job_id", job.ID, "public_id", job.PublicID, "error", msg)
		return o.store.UpdateJobState(ctx, job, store.StateRemoteFailed, msg)
	default:
		nextRun := time.Now().UTC().Add(withJitter(o.cfg.Workers.PollInterval))
		job.NextRunAt = &nextRun
		if job.State != store.StateRemoteActive {
			job.State = store.StateRemoteActive
		}
		o.log.Debug("remote job still active", "job_id", job.ID, "public_id", job.PublicID, "next_run_at", nextRun.Format(time.RFC3339Nano))
		return o.store.UpdateJob(ctx, job)
	}
}
