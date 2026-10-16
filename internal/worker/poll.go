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
		o.releaseJobClaim(ctx, "poller", job.ID)
	}
	return nil
}

func (o *Orchestrator) processPollJob(ctx context.Context, job *store.Job) error {
	if job.State == store.StateRemoteQueued {
		return o.processQueuedPollJob(ctx, job)
	}

	o.log.Debug("polling active remote job",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"remote_id", deref(job.RemoteID),
	)
	status, err := o.findActive(ctx, job)
	if err != nil {
		return o.reconcileActiveMiss(ctx, job, err)
	}
	if status != nil {
		return o.applyActiveStatus(ctx, job, status)
	}
	return o.reconcileActiveMiss(ctx, job, nil)
}

func (o *Orchestrator) processQueuedPollJob(ctx context.Context, job *store.Job) error {
	o.log.Debug("polling queued remote job",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"queued_id", deref(job.QueuedID),
		"has_queue_auth_id", deref(job.QueueAuthID) != "",
		"has_hash", deref(job.RemoteHash) != "",
	)

	queuedStatus, queueErr := o.torbox.FindQueuedTask(ctx, string(job.SourceType), deref(job.QueuedID), deref(job.QueueAuthID), deref(job.RemoteHash))
	if torbox.IsTorboxAbsent(queueErr) {
		queueErr = nil
	}
	if queueErr == nil && queuedStatus != nil {
		return o.keepQueued(ctx, job, queuedStatus)
	}

	// A queue entry can disappear just before TorBox publishes the active
	// entry. Only an active-view match promotes the job.
	activeStatus, activeErr := o.findActive(ctx, job)
	if activeErr != nil {
		return o.schedulePoll(ctx, job, "remote status lookup failed", queueErr, activeErr)
	}
	if activeStatus != nil {
		return o.applyActiveStatus(ctx, job, activeStatus)
	}
	if queueErr != nil {
		return o.schedulePoll(ctx, job, "queued status lookup failed", queueErr)
	}
	return o.handleConfirmedAbsence(ctx, job)
}

func (o *Orchestrator) findActive(ctx context.Context, job *store.Job) (*torbox.TaskStatus, error) {
	remoteID := deref(job.RemoteID)
	if remoteID == "" && deref(job.QueuedID) == "" && deref(job.RemoteHash) == "" && deref(job.QueueAuthID) == "" {
		return nil, fmt.Errorf("no remote identity available for active lookup")
	}
	status, err := o.torbox.FindActiveTaskByIdentity(ctx,
		string(job.SourceType),
		remoteID,
		deref(job.QueuedID),
		deref(job.QueueAuthID),
		deref(job.RemoteHash),
	)
	if torbox.IsTorboxAbsent(err) {
		return nil, nil
	}
	return status, err
}

func (o *Orchestrator) reconcileActiveMiss(ctx context.Context, job *store.Job, activeErr error) error {
	// A queue-aware active lookup can fail for a queue-only ID. Check the
	// queue before treating a missing active item as confirmed absence.
	if hasQueueTracking(job) {
		queuedStatus, queueErr := o.torbox.FindQueuedTask(ctx, string(job.SourceType), deref(job.QueuedID), deref(job.QueueAuthID), deref(job.RemoteHash))
		if torbox.IsTorboxAbsent(queueErr) {
			queueErr = nil
		}
		if queueErr == nil && queuedStatus != nil {
			return o.restoreQueued(ctx, job, queuedStatus)
		}
		if activeErr != nil || queueErr != nil {
			return o.schedulePoll(ctx, job, "remote status lookup failed", activeErr, queueErr)
		}
		return o.handleConfirmedAbsence(ctx, job)
	}
	if activeErr != nil {
		return o.schedulePoll(ctx, job, "remote status lookup failed", activeErr)
	}
	// Jobs created directly as active have no applicable queue view. Preserve
	// their existing active polling behavior rather than applying queue-aware
	// absence accounting to them.
	return o.schedulePoll(ctx, job, "active task not present in active view")
}

func hasQueueTracking(job *store.Job) bool {
	return deref(job.QueuedID) != "" || deref(job.QueueAuthID) != "" || deref(job.RemoteHash) != ""
}

func (o *Orchestrator) keepQueued(ctx context.Context, job *store.Job, status *torbox.TaskStatus) error {
	job.Metadata.PollAttempts = 0
	job.ErrorMessage = nil
	job.RemoteID = nil
	if status.QueueAuthID != "" {
		job.QueueAuthID = ptr(status.QueueAuthID)
	}
	if status.Hash != "" {
		job.RemoteHash = ptr(status.Hash)
	}
	if status.QueuedID != "" {
		job.QueuedID = ptr(status.QueuedID)
	}
	takeRemoteName(job, status.Name)
	nextRun := time.Now().UTC().Add(withJitter(o.cfg.Workers.PollInterval))
	job.NextRunAt = &nextRun
	job.UpdatedAt = time.Now().UTC()
	o.log.Info("remote task still queued",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"queued_id", deref(job.QueuedID),
		"queue_state", status.State,
		"next_run_at", nextRun.Format(time.RFC3339Nano),
	)
	_, err := o.store.UpdateJobIfState(ctx, job, store.StateRemoteQueued)
	return err
}

func (o *Orchestrator) restoreQueued(ctx context.Context, job *store.Job, status *torbox.TaskStatus) error {
	job.Metadata.PollAttempts = 0
	job.ErrorMessage = nil
	job.RemoteID = nil
	if status.QueueAuthID != "" {
		job.QueueAuthID = ptr(status.QueueAuthID)
	}
	if status.Hash != "" {
		job.RemoteHash = ptr(status.Hash)
	}
	if status.QueuedID != "" {
		job.QueuedID = ptr(status.QueuedID)
	}
	takeRemoteName(job, status.Name)
	nextRun := time.Now().UTC().Add(withJitter(o.cfg.Workers.PollInterval))
	job.NextRunAt = &nextRun
	job.UpdatedAt = time.Now().UTC()
	if job.State == store.StateRemoteQueued {
		_, err := o.store.UpdateJobIfState(ctx, job, store.StateRemoteQueued)
		return err
	}
	ok, err := o.store.UpdateJobStateIfCurrent(ctx, job, store.StateRemoteActive, store.StateRemoteQueued, "active lookup recovered queued task")
	if err != nil || !ok {
		return err
	}
	o.log.Info("active task recovered in remote queue", "job_id", job.ID, "public_id", job.PublicID, "queued_id", deref(job.QueuedID))
	return nil
}

func (o *Orchestrator) applyActiveStatus(ctx context.Context, job *store.Job, status *torbox.TaskStatus) error {
	if status == nil {
		return o.schedulePoll(ctx, job, "active status was empty")
	}
	job.Metadata.PollAttempts = 0
	job.ErrorMessage = nil
	takeRemoteName(job, status.Name)
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
	if job.State == store.StateRemoteQueued {
		ok, err := o.store.UpdateJobStateIfCurrent(ctx, job, store.StateRemoteQueued, store.StateRemoteActive, "active task discovered after queue lookup")
		if err != nil || !ok {
			return err
		}
	}
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
		job.QueuedID = nil
		ok, err := o.store.UpdateJobStateIfCurrent(ctx, job, store.StateRemoteActive, store.StateLocalDownloadPending, "remote content ready for local download")
		if err != nil || !ok {
			return err
		}
		o.log.Info("remote content ready", "job_id", job.ID, "public_id", job.PublicID, "remote_id", deref(job.RemoteID))
		return nil
	case status.Failed || status.Inactive:
		msg := status.Error
		if msg == "" {
			msg = fmt.Sprintf("remote task entered terminal state=%s label=%s", status.State, status.Label)
		}
		job.ErrorMessage = &msg
		job.NextRunAt = nil
		ok, err := o.store.UpdateJobStateIfCurrent(ctx, job, store.StateRemoteActive, store.StateRemoteFailed, msg)
		if err != nil || !ok {
			return err
		}
		o.log.Error("remote task entered terminal failure state", "job_id", job.ID, "public_id", job.PublicID, "error", msg)
		return nil
	default:
		nextRun := time.Now().UTC().Add(withJitter(o.cfg.Workers.PollInterval))
		job.NextRunAt = &nextRun
		_, err := o.store.UpdateJobIfState(ctx, job, store.StateRemoteActive)
		return err
	}
}

func (o *Orchestrator) schedulePoll(ctx context.Context, job *store.Job, reason string, errs ...error) error {
	nextRun := time.Now().UTC().Add(withJitter(o.cfg.Workers.PollInterval))
	job.NextRunAt = &nextRun
	job.UpdatedAt = time.Now().UTC()
	for _, err := range errs {
		if err != nil {
			o.log.Warn(reason, "job_id", job.ID, "public_id", job.PublicID, "next_run_at", nextRun.Format(time.RFC3339Nano), "error", err.Error())
			break
		}
	}
	_, err := o.store.UpdateJobIfState(ctx, job, job.State)
	return err
}

func (o *Orchestrator) handleConfirmedAbsence(ctx context.Context, job *store.Job) error {
	job.Metadata.PollAttempts++
	maxAttempts := o.cfg.Workers.RemoteAbsenceAttempts
	if maxAttempts < 1 {
		maxAttempts = 5
	}
	if job.Metadata.PollAttempts < maxAttempts {
		return o.schedulePoll(ctx, job, "remote task absent from queue and active views")
	}
	msg := "remote task not found in TorBox queue or active list after max attempts"
	job.ErrorMessage = &msg
	job.NextRunAt = nil
	ok, err := o.store.UpdateJobStateIfCurrent(ctx, job, job.State, store.StateRemoteFailed, msg)
	if err == nil && ok {
		o.log.Error("remote task confirmed absent", "job_id", job.ID, "public_id", job.PublicID, "attempts", job.Metadata.PollAttempts)
	}
	return err
}
