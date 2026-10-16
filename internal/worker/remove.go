package worker

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
)

func (o *Orchestrator) runRemover(ctx context.Context) error {
	if err := o.store.RecoverRemoverClaims(ctx, time.Now().UTC().Add(-o.removerClaimLease())); err != nil {
		return err
	}
	owner := "remover-" + o.claimToken
	jobs, err := o.store.ClaimJobsDue(ctx, owner, []store.JobState{store.StateRemovePending}, time.Now().UTC(), o.cfg.Workers.BatchSize)
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
		if err := o.store.ReleaseJobClaimOwned(ctx, job.ID, owner); err != nil {
			o.log.Error("failed to release job claim", "worker", "remover", "job_id", job.ID, "error", err)
		}
	}
	return nil
}

const maxUpstreamDeleteAttempts = 5
const maxUpstreamReconciliationFailures = 5

type removalView struct {
	name           string
	progress       *store.UpstreamRemovalProgress
	applicable     bool
	lookup         func(context.Context) (*torbox.TaskStatus, error)
	delete         func(context.Context, string) error
	controlID      func(*torbox.TaskStatus) string
	apply          func(*torbox.TaskStatus)
	status         *torbox.TaskStatus
	controlIDValue string
}

func (o *Orchestrator) processRemoveJob(ctx context.Context, job *store.Job) error {
	// A remove_pending state is itself the durable removal request. Older rows
	// may not have the flag set, but the remover must still be able to persist
	// progress and the final removed state.
	job.DeleteRequested = true
	if !o.cfg.UpstreamRemove {
		o.log.Info("upstream cleanup disabled; removing local payloads",
			"job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType)
		return o.removeLocalPayloads(ctx, job, "local payload removed; TorBox content retained by configuration", nil)
	}

	legacyRemovalProgress(job)
	if job.Metadata.ActiveRemoval.Outcome == store.UpstreamRemovalAbsent && !job.Metadata.QueuedRemoval.Terminal() {
		job.Metadata.ActiveRemoval.Outcome = ""
		job.Metadata.ActiveRemoval.CompletedAt = nil
	}
	active, queued := o.removalViews(job)
	o.log.Info("reconciling upstream cleanup",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"source_type", job.SourceType,
		"active_id", deref(job.RemoteID),
		"queued_id", deref(job.QueuedID),
		"has_hash", removalHash(job) != "",
	)

	activeLookupRetry := o.reconcileRemovalView(ctx, job, &active)
	// Active reconciliation can discover a queue ID that was not persisted on
	// the job. Rebuild only the queued view so that fresh identity participates
	// in the same pre-delete reconciliation phase.
	_, queued = o.removalViews(job)
	queuedLookupRetry := o.reconcileRemovalView(ctx, job, &queued)
	if queued.status != nil && strings.TrimSpace(queued.status.RemoteID) != "" && (activeLookupRetry || job.Metadata.ActiveRemoval.Outcome == store.UpstreamRemovalAbsent) {
		job.RemoteID = ptr(strings.TrimSpace(queued.status.RemoteID))
		if job.Metadata.ActiveRemoval.Outcome == store.UpstreamRemovalAbsent {
			job.Metadata.ActiveRemoval.Outcome = ""
			job.Metadata.ActiveRemoval.CompletedAt = nil
		}
		active, _ = o.removalViews(job)
		activeLookupRetry = o.reconcileRemovalView(ctx, job, &active)
	}
	if !active.applicable && !queued.applicable {
		setRemovalOutcome(&job.Metadata.ActiveRemoval, store.UpstreamRemovalUnidentifiable, "no safe upstream identity")
	}
	retryRemaining := activeLookupRetry || queuedLookupRetry
	if !activeLookupRetry && o.deleteRemovalView(ctx, job, &active) {
		retryRemaining = true
	}
	if err := o.store.UpdateJob(ctx, job); err != nil {
		return fmt.Errorf("persist active upstream removal progress: %w", err)
	}
	if !queuedLookupRetry && o.deleteRemovalView(ctx, job, &queued) {
		retryRemaining = true
	}
	// A queue entry can promote between either lookup and its delete request.
	// Recheck active absence after the queued representation has settled.
	if queued.applicable && job.Metadata.ActiveRemoval.Outcome == store.UpstreamRemovalAbsent &&
		(job.Metadata.QueuedRemoval.Outcome == store.UpstreamRemovalAbsent || job.Metadata.QueuedRemoval.Outcome == store.UpstreamRemovalDeleted) {
		job.Metadata.ActiveRemoval.Outcome = ""
		job.Metadata.ActiveRemoval.CompletedAt = nil
		active, _ = o.removalViews(job)
		if o.reconcileRemovalView(ctx, job, &active) || o.deleteRemovalView(ctx, job, &active) {
			retryRemaining = true
		}
	}
	if err := o.store.UpdateJob(ctx, job); err != nil {
		return fmt.Errorf("persist queued upstream removal progress: %w", err)
	}
	if retryRemaining {
		return o.scheduleRemovalRetry(ctx, job)
	}

	warning := removalWarning(job)
	message := "local payload and TorBox task removed"
	if warning != "" {
		message = "local payload removed; upstream cleanup incomplete: " + warning
	}
	return o.removeLocalPayloads(ctx, job, message, stringPtrOrNil(warning))
}

func (o *Orchestrator) removalViews(job *store.Job) (removalView, removalView) {
	hash := removalHash(job)
	activeApplicable := deref(job.RemoteID) != "" || deref(job.QueuedID) != "" || (isUsenet(job.SourceType) && deref(job.QueueAuthID) != "") || hash != ""
	queuedApplicable := deref(job.QueuedID) != "" || (isUsenet(job.SourceType) && deref(job.QueueAuthID) != "") || hash != ""

	active := removalView{
		name:       "active",
		progress:   &job.Metadata.ActiveRemoval,
		applicable: activeApplicable,
		lookup: func(ctx context.Context) (*torbox.TaskStatus, error) {
			return o.torbox.FindActiveTaskByIdentity(ctx, string(job.SourceType), deref(job.RemoteID), deref(job.QueuedID), deref(job.QueueAuthID), hash)
		},
		delete: func(ctx context.Context, id string) error {
			return o.torbox.DeleteTask(ctx, string(job.SourceType), id)
		},
		controlID: func(status *torbox.TaskStatus) string {
			if status != nil && strings.TrimSpace(status.RemoteID) != "" {
				return strings.TrimSpace(status.RemoteID)
			}
			return ""
		},
		apply: func(status *torbox.TaskStatus) {
			if status == nil {
				return
			}
			if status.RemoteID != "" {
				job.RemoteID = ptr(status.RemoteID)
			}
			if status.QueueAuthID != "" {
				job.QueueAuthID = ptr(status.QueueAuthID)
			}
			if status.QueuedID != "" {
				job.QueuedID = ptr(status.QueuedID)
			}
			if status.Hash != "" {
				job.RemoteHash = ptr(status.Hash)
			}
		},
	}

	queued := removalView{
		name:       "queued",
		progress:   &job.Metadata.QueuedRemoval,
		applicable: queuedApplicable,
		lookup: func(ctx context.Context) (*torbox.TaskStatus, error) {
			return o.torbox.FindQueuedTask(ctx, string(job.SourceType), deref(job.QueuedID), deref(job.QueueAuthID), hash)
		},
		delete: func(ctx context.Context, id string) error {
			return o.torbox.DeleteQueuedTask(ctx, string(job.SourceType), id)
		},
		controlID: func(status *torbox.TaskStatus) string {
			if status != nil && strings.TrimSpace(status.QueuedID) != "" {
				return strings.TrimSpace(status.QueuedID)
			}
			return ""
		},
		apply: func(status *torbox.TaskStatus) {
			if status == nil {
				return
			}
			if status.QueuedID != "" {
				job.QueuedID = ptr(status.QueuedID)
			}
			if status.QueueAuthID != "" {
				job.QueueAuthID = ptr(status.QueueAuthID)
			}
			if status.Hash != "" {
				job.RemoteHash = ptr(status.Hash)
			}
		},
	}
	return active, queued
}

func (o *Orchestrator) reconcileRemovalView(ctx context.Context, job *store.Job, view *removalView) bool {
	if !view.applicable || view.progress.Terminal() {
		return false
	}

	status, err := view.lookup(ctx)
	if err != nil {
		if torbox.IsTorboxAbsent(err) {
			setRemovalOutcome(view.progress, store.UpstreamRemovalAbsent, "")
			return false
		}
		if torbox.IsTorboxIdentityConflict(err) {
			setRemovalOutcome(view.progress, store.UpstreamRemovalUnidentifiable, "conflicting content hash")
			o.log.Warn("upstream candidate rejected because its hash conflicts",
				"job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType, "upstream_view", view.name)
			return false
		}
		view.progress.ReconciliationFailures++
		view.progress.LastError = "TorBox lookup failed"
		if view.progress.ReconciliationFailures >= maxUpstreamReconciliationFailures {
			setRemovalOutcome(view.progress, store.UpstreamRemovalExhausted, "TorBox reconciliation failure budget exhausted")
			o.log.Warn("upstream reconciliation exhausted its failure budget; proceeding with local cleanup",
				"job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType, "upstream_view", view.name, "failures", view.progress.ReconciliationFailures)
			return false
		}
		o.log.Warn("upstream cleanup reconciliation failed; will retry",
			"job_id", job.ID,
			"public_id", job.PublicID,
			"source_type", job.SourceType,
			"upstream_view", view.name,
			"control_id", removalControlID(job, view.name),
			"error", safeTorboxError(err),
		)
		return true
	}
	if status == nil {
		setRemovalOutcome(view.progress, store.UpstreamRemovalAbsent, "")
		o.log.Info("upstream representation already absent",
			"job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType, "upstream_view", view.name)
		return false
	}

	expectedHash := removalHash(job)
	if expectedHash != "" && status.Hash != "" && !strings.EqualFold(expectedHash, status.Hash) {
		setRemovalOutcome(view.progress, store.UpstreamRemovalUnidentifiable, "conflicting content hash")
		o.log.Warn("upstream candidate rejected because its hash conflicts",
			"job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType, "upstream_view", view.name)
		return false
	}

	view.apply(status)
	view.status = status
	controlID := view.controlID(status)
	if controlID == "" {
		setRemovalOutcome(view.progress, store.UpstreamRemovalUnidentifiable, "no safe control identity")
		o.log.Warn("upstream cleanup could not establish a safe control identity",
			"job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType, "upstream_view", view.name)
		return false
	}
	if _, err := strconv.ParseInt(controlID, 10, 64); err != nil || strings.HasPrefix(controlID, "-") {
		setRemovalOutcome(view.progress, store.UpstreamRemovalUnidentifiable, "control identity is not numeric")
		o.log.Warn("upstream cleanup control identity is not numeric",
			"job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType, "upstream_view", view.name)
		return false
	}
	view.controlIDValue = controlID
	o.log.Info("upstream representation matched for cleanup",
		"job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType, "upstream_view", view.name, "control_id", controlID)
	return false
}

func (o *Orchestrator) deleteRemovalView(ctx context.Context, job *store.Job, view *removalView) bool {
	if !view.applicable || view.progress.Terminal() || view.controlIDValue == "" {
		return false
	}
	attempt := view.progress.Attempts + 1
	controlID := view.controlIDValue
	o.log.Info("attempting upstream deletion",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"source_type", job.SourceType,
		"upstream_view", view.name,
		"control_id", controlID,
		"attempt", attempt,
	)
	err := view.delete(ctx, controlID)
	switch {
	case err == nil:
		setRemovalOutcome(view.progress, store.UpstreamRemovalDeleted, "")
		o.log.Info("upstream deletion accepted", "job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType, "upstream_view", view.name, "control_id", controlID)
		return false
	case torbox.IsTorboxAbsent(err):
		setRemovalOutcome(view.progress, store.UpstreamRemovalAbsent, "")
		o.log.Info("upstream deletion found representation already absent", "job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType, "upstream_view", view.name, "control_id", controlID)
		return false
	case torbox.IsRequestNotSent(err):
		view.progress.ReconciliationFailures++
		view.progress.LastError = "TorBox delete request was not sent"
		if view.progress.ReconciliationFailures >= maxUpstreamReconciliationFailures {
			setRemovalOutcome(view.progress, store.UpstreamRemovalExhausted, "TorBox pre-send failure budget exhausted")
			o.log.Warn("upstream deletion exhausted its pre-send failure budget; proceeding with local cleanup",
				"job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType, "upstream_view", view.name, "failures", view.progress.ReconciliationFailures)
			return false
		}
		o.log.Warn("upstream deletion was not sent; will retry without consuming an attempt",
			"job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType, "upstream_view", view.name, "control_id", controlID)
		return true
	case torbox.IsRetryable(err) || torbox.IsTorboxLogical(err):
		view.progress.Attempts = attempt
		view.progress.LastError = safeTorboxError(err)
		if view.progress.Attempts >= maxUpstreamDeleteAttempts {
			setRemovalOutcome(view.progress, store.UpstreamRemovalExhausted, view.progress.LastError)
			o.log.Warn("upstream deletion exhausted its attempt budget; proceeding with local cleanup",
				"job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType, "upstream_view", view.name, "control_id", controlID, "attempts", view.progress.Attempts)
			return false
		}
		o.log.Warn("upstream deletion uncertain; will reconcile before retrying",
			"job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType, "upstream_view", view.name, "control_id", controlID, "attempt", attempt, "error", view.progress.LastError)
		return true
	default:
		view.progress.Attempts = attempt
		setRemovalOutcome(view.progress, store.UpstreamRemovalRejected, safeTorboxError(err))
		o.log.Warn("upstream deletion definitively rejected; proceeding with local cleanup",
			"job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType, "upstream_view", view.name, "control_id", controlID, "error", view.progress.LastError)
		return false
	}
}

func (o *Orchestrator) scheduleRemovalRetry(ctx context.Context, job *store.Job) error {
	failures := max(job.Metadata.ActiveRemoval.Attempts+job.Metadata.ActiveRemoval.ReconciliationFailures,
		job.Metadata.QueuedRemoval.Attempts+job.Metadata.QueuedRemoval.ReconciliationFailures, 1)
	delay := max(o.cfg.Workers.RemoveInterval, time.Second) * time.Duration(1<<min(failures-1, 8))
	nextRun := time.Now().UTC().Add(delay)
	job.NextRunAt = &nextRun
	job.UpdatedAt = time.Now().UTC()
	if err := o.store.UpdateJob(ctx, job); err != nil {
		return fmt.Errorf("persist upstream removal retry: %w", err)
	}
	unresolved := make([]string, 0, 2)
	if !job.Metadata.ActiveRemoval.Terminal() {
		unresolved = append(unresolved, "active")
	}
	if !job.Metadata.QueuedRemoval.Terminal() {
		unresolved = append(unresolved, "queued")
	}
	o.log.Warn("upstream cleanup incomplete; local payloads retained for retry",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"source_type", job.SourceType,
		"upstream_views", strings.Join(unresolved, ","),
		"next_run_at", nextRun.Format(time.RFC3339Nano),
	)
	return nil
}

func (o *Orchestrator) removeLocalPayloads(ctx context.Context, job *store.Job, message string, warning *string) error {
	if warning != nil {
		job.ErrorMessage = warning
	} else {
		job.ErrorMessage = nil
	}
	job.NextRunAt = nil
	job.UpdatedAt = time.Now().UTC()
	nextRun := time.Now().UTC().Add(o.cfg.Workers.RemoveInterval)
	job.NextRunAt = &nextRun
	// Persist upstream terminal outcomes before any local filesystem operation.
	if err := o.store.UpdateJob(ctx, job); err != nil {
		return fmt.Errorf("persist upstream removal outcome: %w", err)
	}

	o.log.Info("removing local job payloads", "job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType)
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

	job.UpdatedAt = time.Now().UTC()
	job.NextRunAt = nil
	if warning != nil {
		o.log.Warn("job removed locally; upstream cleanup incomplete", "job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType, "warning", *warning)
	} else if !o.cfg.UpstreamRemove {
		o.log.Info("job removed locally; TorBox content retained by configuration", "job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType)
	} else {
		o.log.Info("job removed locally and upstream", "job_id", job.ID, "public_id", job.PublicID, "source_type", job.SourceType)
	}
	return o.store.UpdateJobState(ctx, job, store.StateRemoved, message)
}

func legacyRemovalProgress(job *store.Job) {
	legacy := job.Metadata.UpstreamDeleteAttempts
	if legacy == 0 {
		return
	}
	job.Metadata.UpstreamDeleteAttempts = 0
	if job.Metadata.ActiveRemoval.Outcome != "" || job.Metadata.ActiveRemoval.Attempts != 0 {
		return
	}
	job.Metadata.ActiveRemoval.Attempts = legacy
	if legacy >= maxUpstreamDeleteAttempts {
		setRemovalOutcome(&job.Metadata.ActiveRemoval, store.UpstreamRemovalExhausted, "legacy upstream delete attempt budget exhausted")
	}
}

func setRemovalOutcome(progress *store.UpstreamRemovalProgress, outcome, reason string) {
	if progress.Outcome != "" {
		return
	}
	now := time.Now().UTC()
	progress.CompletedAt = &now
	progress.Outcome = outcome
	progress.LastError = reason
}

func removalHash(job *store.Job) string {
	if value := strings.TrimSpace(deref(job.RemoteHash)); value != "" {
		return value
	}
	return strings.TrimSpace(deref(job.InfoHash))
}

func removalWarning(job *store.Job) string {
	warnings := make([]string, 0, 2)
	for _, item := range []struct {
		name string
		p    store.UpstreamRemovalProgress
	}{
		{name: "active", p: job.Metadata.ActiveRemoval},
		{name: "queued", p: job.Metadata.QueuedRemoval},
	} {
		if item.p.Outcome == store.UpstreamRemovalRejected || item.p.Outcome == store.UpstreamRemovalExhausted || item.p.Outcome == store.UpstreamRemovalUnidentifiable {
			reason := item.p.LastError
			if reason == "" {
				reason = item.p.Outcome
			}
			warnings = append(warnings, item.name+" ("+reason+")")
		}
	}
	return strings.Join(warnings, "; ")
}

func removalControlID(job *store.Job, view string) string {
	if view == "queued" {
		return deref(job.QueuedID)
	}
	return deref(job.RemoteID)
}

func safeTorboxError(err error) string {
	switch {
	case torbox.IsTorboxAbsent(err):
		return "TorBox reported the representation was absent"
	case torbox.IsTorboxLogical(err):
		return "TorBox returned an ambiguous structured failure"
	case torbox.IsRetryable(err):
		return "TorBox request failed uncertainly"
	case torbox.IsHTTPStatus(err, 400):
		return "TorBox rejected the request (HTTP 400)"
	case torbox.IsHTTPStatus(err, 401):
		return "TorBox rejected the request (HTTP 401)"
	case torbox.IsHTTPStatus(err, 403):
		return "TorBox rejected the request (HTTP 403)"
	case torbox.IsHTTPStatus(err, 404):
		return "TorBox rejected the request (HTTP 404)"
	case torbox.IsHTTPStatus(err, 422):
		return "TorBox rejected the request (HTTP 422)"
	default:
		return "TorBox rejected the request"
	}
}

func stringPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func isUsenet(sourceType store.SourceType) bool {
	return sourceType == store.SourceTypeNZB || strings.EqualFold(string(sourceType), "usenet")
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
