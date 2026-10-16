package worker

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
)

func (o *Orchestrator) runSubmitter(ctx context.Context) error {
	jobs, err := o.store.ClaimJobsDue(ctx, "submitter", []store.JobState{store.StateSubmitPending, store.StateSubmitRetry}, time.Now().UTC(), o.cfg.Workers.BatchSize)
	if err != nil {
		return err
	}
	if len(jobs) > 0 {
		o.log.Debug("submitter fetched jobs", "count", len(jobs))
	}
	for _, job := range jobs {
		if err := o.processSubmitJob(ctx, job); err != nil {
			o.log.Error("submit job failed", "job_id", job.ID, "error", err)
		}
		o.releaseJobClaim(ctx, "submitter", job.ID)
	}
	return nil
}

func (o *Orchestrator) processSubmitJob(ctx context.Context, job *store.Job) error {
	expectedState := job.State
	if job.Metadata.Torbox521Attempts > 0 || job.Metadata.SubmissionUncertain {
		reconciled, err := o.reconcileSubmission(ctx, job, expectedState)
		if err != nil {
			return o.deferSubmissionReconciliation(ctx, job, expectedState, err)
		}
		if reconciled {
			return nil
		}
	}
	// Persist the possibility of acceptance before making the external request.
	job.Metadata.SubmissionUncertain = true
	ok, err := o.store.UpdateJobIfState(ctx, job, expectedState)
	if err != nil || !ok {
		return err
	}
	o.log.Info("submitting job to torbox",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"source_type", job.SourceType,
		"state", job.State,
		"display_name", job.DisplayName,
		"has_payload", job.PayloadRef != nil,
		"retry_count", job.RetryCount,
	)
	var (
		resp      *torbox.CreateTaskResponse
		createErr error
	)

	switch job.SourceType {
	case store.SourceTypeTorrent:
		resp, createErr = o.torbox.CreateTorrentTask(ctx, torbox.CreateTorrentTaskRequest{
			Magnet:          deref(job.SourceURI),
			PayloadPath:     deref(job.PayloadRef),
			Name:            job.DisplayName,
			AsQueued:        job.Metadata.AsQueued,
			AddOnlyIfCached: job.Metadata.AddOnlyIfCached,
		})
	case store.SourceTypeNZB:
		postProcessing := job.Metadata.PostProcessing
		resp, createErr = o.torbox.CreateUsenetTask(ctx, torbox.CreateUsenetTaskRequest{
			Link:            deref(job.SourceURI),
			PayloadPath:     deref(job.PayloadRef),
			Name:            job.DisplayName,
			Password:        job.Metadata.Password,
			PostProcessing:  &postProcessing,
			AsQueued:        job.Metadata.AsQueued,
			AddOnlyIfCached: job.Metadata.AddOnlyIfCached,
		})
	default:
		createErr = fmt.Errorf("unsupported source type %q", job.SourceType)
	}

	if createErr != nil {
		return o.handleSubmitFailure(ctx, job, createErr)
	}
	if resp == nil {
		return o.handleSubmitFailure(ctx, job, torbox.MarkRetryable(fmt.Errorf("torbox create returned empty response")))
	}

	remoteID := strings.TrimSpace(resp.RemoteID)
	queuedID := strings.TrimSpace(resp.QueuedID)
	// A duplicated generic ID is queue tracking, not active confirmation.
	if remoteID != "" && queuedID != "" && remoteID == queuedID && !resp.ActiveIDExplicit {
		remoteID = ""
	}
	job.RemoteID = ptr(remoteID)
	job.QueuedID = ptr(queuedID)
	job.QueueAuthID = ptr(strings.TrimSpace(resp.QueueAuthID))
	remoteHash := strings.TrimSpace(resp.RemoteHash)
	if remoteHash == "" && job.InfoHash != nil {
		remoteHash = strings.TrimSpace(*job.InfoHash)
	}
	job.RemoteHash = ptr(remoteHash)
	takeRemoteName(job, resp.DisplayName)
	if job.RemoteID == nil && job.QueuedID == nil && job.QueueAuthID == nil && job.RemoteHash == nil {
		return o.handleSubmitFailure(ctx, job, torbox.MarkRetryable(fmt.Errorf("torbox create returned no remote id or queue tracking identifiers")))
	}
	job.ErrorMessage = nil
	job.RetryCount = 0
	job.Metadata.Torbox521Attempts = 0
	job.Metadata.SubmissionUncertain = false
	nextRun := time.Now().UTC().Add(o.cfg.Workers.PollInterval)
	job.NextRunAt = &nextRun
	job.UpdatedAt = time.Now().UTC()
	if job.RemoteID != nil {
		o.log.Info("remote task created",
			"job_id", job.ID,
			"public_id", job.PublicID,
			"remote_id", deref(job.RemoteID),
			"queued_id", deref(job.QueuedID),
			"remote_hash", deref(job.RemoteHash),
			"display_name", job.DisplayName,
			"next_run_at", nextRun.Format(time.RFC3339Nano),
		)
		_, err := o.store.UpdateJobStateIfCurrent(ctx, job, expectedState, store.StateRemoteActive, "remote task created")
		return err
	}

	o.log.Info("remote task queued",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"queued_id", deref(job.QueuedID),
		"queue_auth_id", deref(job.QueueAuthID),
		"remote_hash", deref(job.RemoteHash),
		"display_name", job.DisplayName,
		"next_run_at", nextRun.Format(time.RFC3339Nano),
	)
	_, err = o.store.UpdateJobStateIfCurrent(ctx, job, expectedState, store.StateRemoteQueued, "remote task queued awaiting active id")
	return err
}

func (o *Orchestrator) handleSubmitFailure(ctx context.Context, job *store.Job, err error) error {
	expectedState := job.State
	message := err.Error()
	job.ErrorMessage = &message
	job.UpdatedAt = time.Now().UTC()
	job.Metadata.SubmissionUncertain = torbox.IsRetryable(err) && !torbox.IsRequestNotSent(err)
	if torbox.IsHTTPStatus(err, torbox.StatusTorboxOriginDown) {
		return o.handle521Failure(ctx, job, expectedState, message)
	}
	job.Metadata.Torbox521Attempts = 0
	if torbox.IsRetryable(err) {
		job.RetryCount++
		nextRun := time.Now().UTC().Add(o.submitBackoff(job.RetryCount))
		job.NextRunAt = &nextRun
		o.log.Warn("submit failed, scheduling retry",
			"job_id", job.ID,
			"public_id", job.PublicID,
			"retry_count", job.RetryCount,
			"next_run_at", nextRun.Format(time.RFC3339Nano),
			"error", message,
		)
		_, updateErr := o.store.UpdateJobStateIfCurrent(ctx, job, expectedState, store.StateSubmitRetry, message)
		return updateErr
	}
	job.NextRunAt = nil
	o.log.Error("submit failed permanently", "job_id", job.ID, "public_id", job.PublicID, "error", message)
	_, updateErr := o.store.UpdateJobStateIfCurrent(ctx, job, expectedState, store.StateRemoteFailed, message)
	return updateErr
}

const maxTorbox521Attempts = 5

var torbox521RetryDelays = [...]time.Duration{
	15 * time.Minute,
	30 * time.Minute,
	1 * time.Hour,
	3 * time.Hour,
}

func (o *Orchestrator) handle521Failure(ctx context.Context, job *store.Job, expectedState store.JobState, message string) error {
	job.Metadata.Torbox521Attempts++
	attempt := job.Metadata.Torbox521Attempts
	job.RetryCount++
	if attempt < maxTorbox521Attempts {
		delay := torbox521RetryDelays[attempt-1]
		nextRun := time.Now().UTC().Add(delay)
		job.NextRunAt = &nextRun
		job.UpdatedAt = time.Now().UTC()
		o.log.Warn("TorBox origin unavailable with HTTP 521; scheduling bounded retry",
			"job_id", job.ID,
			"public_id", job.PublicID,
			"attempt", attempt,
			"max_attempts", maxTorbox521Attempts,
			"retry_after", delay.String(),
			"next_run_at", nextRun.Format(time.RFC3339Nano),
		)
		_, err := o.store.UpdateJobStateIfCurrent(ctx, job, expectedState, store.StateSubmitRetry, message)
		return err
	}

	job.NextRunAt = nil
	terminal := fmt.Sprintf("TorBox remained unavailable after %d submission attempts (HTTP 521)", attempt)
	job.ErrorMessage = &terminal
	job.UpdatedAt = time.Now().UTC()
	ok, updateErr := o.store.UpdateJobStateIfCurrent(ctx, job, expectedState, store.StateRemoteFailed, terminal)
	if updateErr != nil {
		return updateErr
	}
	if !ok {
		return nil
	}
	if err := o.cleanupSubmitArtifacts(job); err != nil {
		o.log.Error("failed to clean up submission artifacts after HTTP 521 exhaustion",
			"job_id", job.ID,
			"public_id", job.PublicID,
			"error", err,
		)
	}
	job.UpdatedAt = time.Now().UTC()
	o.log.Error("TorBox origin remained unavailable after HTTP 521 retry schedule",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"attempts", attempt,
		"status", torbox.StatusTorboxOriginDown,
	)
	_, updateErr = o.store.UpdateJobIfState(ctx, job, store.StateRemoteFailed)
	return updateErr
}

func (o *Orchestrator) reconcileSubmission(ctx context.Context, job *store.Job, expectedState store.JobState) (bool, error) {
	hash := strings.TrimSpace(deref(job.RemoteHash))
	if hash == "" {
		hash = strings.TrimSpace(deref(job.InfoHash))
	}
	if deref(job.RemoteID) == "" && deref(job.QueuedID) == "" && deref(job.QueueAuthID) == "" && hash == "" {
		return false, fmt.Errorf("submission outcome unknown and no tracking identity is available")
	}

	var queued *torbox.TaskStatus
	if deref(job.QueuedID) != "" || deref(job.QueueAuthID) != "" || hash != "" {
		var err error
		queued, err = o.torbox.FindQueuedTask(ctx, string(job.SourceType), deref(job.QueuedID), deref(job.QueueAuthID), hash)
		if err != nil && !torbox.IsTorboxAbsent(err) {
			return false, err
		}
	}
	if queued != nil {
		job.QueuedID = ptr(queued.QueuedID)
		job.RemoteID = nil
		job.QueueAuthID = ptr(queued.QueueAuthID)
		job.RemoteHash = ptr(queued.Hash)
		if queued.Hash == "" {
			job.RemoteHash = ptr(hash)
		}
		job.ErrorMessage = nil
		job.RetryCount = 0
		job.Metadata.Torbox521Attempts = 0
		job.Metadata.SubmissionUncertain = false
		now := time.Now().UTC()
		job.NextRunAt = &now
		job.UpdatedAt = now
		ok, updateErr := o.store.UpdateJobStateIfCurrent(ctx, job, expectedState, store.StateRemoteQueued, "recovered existing queued task after uncertain submission")
		if updateErr != nil || !ok {
			return true, updateErr
		}
		o.log.Info("recovered existing queued task after uncertain submission", "job_id", job.ID, "public_id", job.PublicID, "queued_id", deref(job.QueuedID))
		return true, nil
	}

	remoteID := deref(job.RemoteID)
	active, err := o.torbox.FindActiveTaskByIdentity(ctx, string(job.SourceType), remoteID, deref(job.QueuedID), deref(job.QueueAuthID), hash)
	if err != nil && !torbox.IsTorboxAbsent(err) {
		return false, err
	}
	if active == nil {
		return false, nil
	}
	job.RemoteID = ptr(active.RemoteID)
	job.QueuedID = ptr(active.QueuedID)
	job.QueueAuthID = ptr(active.QueueAuthID)
	job.RemoteHash = ptr(active.Hash)
	if active.Hash == "" {
		job.RemoteHash = ptr(hash)
	}
	takeRemoteName(job, active.Name)
	job.ErrorMessage = nil
	job.RetryCount = 0
	job.Metadata.Torbox521Attempts = 0
	job.Metadata.SubmissionUncertain = false
	now := time.Now().UTC()
	job.NextRunAt = &now
	job.UpdatedAt = now
	ok, updateErr := o.store.UpdateJobStateIfCurrent(ctx, job, expectedState, store.StateRemoteActive, "recovered existing active task after uncertain submission")
	if updateErr != nil || !ok {
		return true, updateErr
	}
	o.log.Info("recovered existing active task after uncertain submission", "job_id", job.ID, "public_id", job.PublicID, "remote_id", deref(job.RemoteID))
	return true, nil
}

func (o *Orchestrator) deferSubmissionReconciliation(ctx context.Context, job *store.Job, expectedState store.JobState, lookupErr error) error {
	nextRun := time.Now().UTC().Add(o.cfg.Workers.PollInterval)
	job.NextRunAt = &nextRun
	job.UpdatedAt = time.Now().UTC()
	message := "submission outcome uncertain; waiting for TorBox identity reconciliation"
	job.ErrorMessage = &message
	o.log.Warn("postponing submission retry because TorBox identity reconciliation was inconclusive",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"next_run_at", nextRun.Format(time.RFC3339Nano),
		"error", lookupErr,
	)
	_, err := o.store.UpdateJobIfState(ctx, job, expectedState)
	return err
}

func (o *Orchestrator) cleanupSubmitArtifacts(job *store.Job) error {
	var firstErr error
	if job.StagingPath != nil {
		path := *job.StagingPath
		if err := ensurePathWithinRoot(o.layout.Staging, path); err != nil {
			firstErr = fmt.Errorf("validate staging path: %w", err)
		} else if err := o.layout.RemovePath(path); err != nil {
			firstErr = fmt.Errorf("remove staging path: %w", err)
		} else {
			job.StagingPath = nil
		}
	}
	if job.PayloadRef != nil {
		payloadDir := filepath.Dir(*job.PayloadRef)
		if err := ensurePathWithinRoot(o.layout.Payloads, payloadDir); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("validate payload path: %w", err)
			}
		} else if err := o.layout.RemovePath(payloadDir); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("remove payload path: %w", err)
			}
		} else {
			job.PayloadRef = nil
		}
	}
	return firstErr
}

func (o *Orchestrator) submitBackoff(retryCount int) time.Duration {
	base := float64(o.cfg.Workers.SubmitRetryMin)
	maxDelay := float64(o.cfg.Workers.SubmitRetryMax)
	delay := base * math.Pow(2, float64(max(retryCount-1, 0)))
	if delay > maxDelay {
		delay = maxDelay
	}
	return withJitter(time.Duration(delay))
}
