package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mrjoiny/torboxarr/internal/files"
	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
)

const progressCheckpointInterval = 15 * time.Second
const max5xxRetries = 3

func progressRateMBs(lastLogAt time.Time, lastLogDone, done int64, now time.Time) float64 {
	if lastLogAt.IsZero() {
		return 0
	}
	elapsed := now.Sub(lastLogAt).Seconds()
	deltaDone := done - lastLogDone
	if elapsed <= 0 || deltaDone <= 0 {
		return 0
	}
	return float64(deltaDone) / (1024 * 1024) / elapsed
}

func (o *Orchestrator) runDownloader(ctx context.Context) error {
	jobs, err := o.store.ClaimJobsDue(ctx, "downloader", []store.JobState{store.StateLocalDownloadPending, store.StateLocalDownloading}, time.Now().UTC(), o.cfg.Workers.BatchSize)
	if err != nil {
		return err
	}
	if len(jobs) > 0 {
		o.log.Debug("downloader fetched jobs", "count", len(jobs))
	}
	for _, job := range jobs {
		if err := o.processDownloadJob(ctx, job); err != nil {
			o.log.Error("download job failed", "job_id", job.ID, "error", err)
		}
		o.releaseJobClaim(ctx, "downloader", job.ID)
	}
	return nil
}

func (o *Orchestrator) processDownloadJob(ctx context.Context, job *store.Job) error {
	o.log.Info("processing local download", "job_id", job.ID, "public_id", job.PublicID, "state", job.State, "staging_path", deref(job.StagingPath))
	if job.StagingPath == nil {
		staging := o.layout.StagingPathForJob(job.ID)
		job.StagingPath = &staging
	}
	if err := os.MkdirAll(*job.StagingPath, 0o755); err != nil {
		return fmt.Errorf("ensure staging path: %w", err)
	}

	parts, err := o.store.ListTransferParts(ctx, job.ID)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		assets, err := o.torbox.GetDownloadLinks(ctx, string(job.SourceType), deref(job.RemoteID))
		if err != nil {
			nextRun := time.Now().UTC().Add(o.cfg.Workers.DownloadInterval)
			job.NextRunAt = &nextRun
			msg := err.Error()
			job.ErrorMessage = &msg
			job.UpdatedAt = time.Now().UTC()
			o.log.Warn("failed to fetch download links", "job_id", job.ID, "public_id", job.PublicID, "error", msg)
			_ = o.store.UpdateJobState(ctx, job, store.StateLocalDownloading, msg)
			return nil
		}
		if len(assets) == 0 {
			msg := "remote task returned no downloadable assets"
			job.ErrorMessage = &msg
			job.UpdatedAt = time.Now().UTC()
			o.log.Error("no downloadable assets returned", "job_id", job.ID, "public_id", job.PublicID, "remote_id", deref(job.RemoteID))
			return o.store.UpdateJobState(ctx, job, store.StateFailed, msg)
		}
		o.log.Info("resolved download assets", "job_id", job.ID, "public_id", job.PublicID, "asset_count", len(assets))
		now := time.Now().UTC()
		for idx, asset := range assets {
			rel := safeRelativePath(asset.RelativePath, idx)
			tempPath := filepath.Join(*job.StagingPath, rel)
			if err := ensurePathWithinRoot(*job.StagingPath, tempPath); err != nil {
				msg := fmt.Sprintf("refusing to use download path outside staging root: %q", asset.RelativePath)
				job.ErrorMessage = &msg
				job.NextRunAt = nil
				job.UpdatedAt = time.Now().UTC()
				o.log.Error("unsafe download path rejected",
					"job_id", job.ID,
					"public_id", job.PublicID,
					"remote_path", asset.RelativePath,
					"resolved_path", tempPath,
					"error", err,
				)
				return o.store.UpdateJobState(ctx, job, store.StateFailed, msg)
			}
			o.log.Debug("registering transfer part",
				"job_id", job.ID,
				"part_key", partKey(asset, idx),
				"relative_path", rel,
				"size", asset.Size,
				"has_file_id", asset.FileID != "",
			)
			part := &store.TransferPart{
				JobID:         job.ID,
				PartKey:       partKey(asset, idx),
				SourceURL:     asset.URL,
				TempPath:      tempPath,
				RelativePath:  rel,
				ContentLength: asset.Size,
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			if asset.FileID != "" {
				part.FileID = &asset.FileID
			}
			if err := o.store.UpsertTransferPart(ctx, part); err != nil {
				return err
			}
		}
		parts, err = o.store.ListTransferParts(ctx, job.ID)
		if err != nil {
			return err
		}
	}

	visibleLocalDownload := job.State == store.StateLocalDownloading && job.BytesTotal > 0

	for _, part := range parts {
		if part.Completed {
			o.log.Debug("skipping completed transfer part", "job_id", job.ID, "part_key", part.PartKey, "path", part.TempPath)
			continue
		}
		now := time.Now().UTC()
		part.UpdatedAt = now
		if part.CreatedAt.IsZero() {
			part.CreatedAt = now
		}
		lastProgressLogAt := time.Time{}
		lastProgressLogDone := int64(0)
		lastProgressPersist := time.Time{}
		downloadErr := o.downloader.Download(ctx, part, func(done, total int64) error {
			reset5xxCount(job, part.BytesDone, done)
			part.BytesDone = done
			if total > 0 {
				part.ContentLength = total
			}
			now := time.Now().UTC()
			part.UpdatedAt = now
			if !visibleLocalDownload && total > 0 {
				job.BytesTotal = total
				if done > job.BytesDone {
					job.BytesDone = done
				}
				job.ErrorMessage = nil
				job.UpdatedAt = now
				nextRun := now.Add(o.cfg.Workers.DownloadInterval)
				job.NextRunAt = &nextRun
				if err := o.store.UpdateJobState(ctx, job, store.StateLocalDownloading, "local download started"); err != nil {
					return err
				}
				visibleLocalDownload = true
			}
			if now.Sub(lastProgressLogAt) >= 15*time.Second {
				pct := 0.0
				if total > 0 {
					pct = float64(done) / float64(total) * 100
				}
				gbDone := float64(done) / (1024 * 1024 * 1024)
				gbTotal := float64(total) / (1024 * 1024 * 1024)
				rateMBs := progressRateMBs(lastProgressLogAt, lastProgressLogDone, done, now)
				o.log.Info("transfer progress",
					"job_id", job.ID,
					"part_key", part.PartKey,
					"progress", fmt.Sprintf("%.1f%%", pct),
					"done_gb", fmt.Sprintf("%.2f", gbDone),
					"total_gb", fmt.Sprintf("%.2f", gbTotal),
					"rate_mbs", fmt.Sprintf("%.2f", rateMBs),
				)
				lastProgressLogAt = now
				lastProgressLogDone = done
			}
			if shouldCheckpointProgress(lastProgressPersist, now, done, total) {
				lastProgressPersist = now
				return o.store.UpsertTransferPart(ctx, part)
			}
			return nil
		})
		if downloadErr != nil {
			var statusErr *files.HTTPStatusError
			statusCode := 0
			if errors.As(downloadErr, &statusErr) {
				statusCode = statusErr.StatusCode
			}
			isNotFound := statusCode == http.StatusNotFound
			is5xx := isRefresh5xx(statusCode)
			if !is5xx {
				job.RetryCount = 0
			}
			refreshed := false
			if refreshedPart, refreshErr := o.refreshDownloadPartSourceURL(ctx, job, part, downloadErr); refreshErr != nil {
				o.log.Warn("failed to refresh download link after status error",
					"job_id", job.ID,
					"public_id", job.PublicID,
					"part_key", part.PartKey,
					"error", refreshErr,
				)
			} else if refreshedPart {
				refreshed = true
				downloadErr = fmt.Errorf("download link refreshed after HTTP %d", statusCode)
			}
			if is5xx {
				job.RetryCount++
				o.log.Warn("5xx download response recorded",
					"job_id", job.ID,
					"public_id", job.PublicID,
					"part_key", part.PartKey,
					"status_code", statusCode,
					"retry_count", job.RetryCount,
					"refreshed", refreshed,
				)
			}
			msg := downloadErr.Error()
			if err := o.store.UpsertTransferPart(ctx, part); err != nil {
				o.log.Warn("failed to persist download progress after error",
					"job_id", job.ID,
					"public_id", job.PublicID,
					"part_key", part.PartKey,
					"error", err,
				)
			}
			if isNotFound && !refreshed {
				msg = "download part returned HTTP 404 and no fresh link was available"
				job.ErrorMessage = &msg
				job.NextRunAt = nil
				job.UpdatedAt = time.Now().UTC()
				o.log.Error("local download failed permanently",
					"job_id", job.ID,
					"public_id", job.PublicID,
					"part_key", part.PartKey,
					"error", msg,
				)
				_ = o.store.UpdateJobState(ctx, job, store.StateFailed, msg)
				return nil
			}
			if is5xx && job.RetryCount >= max5xxRetries {
				msg = fmt.Sprintf("download part %s returned HTTP %d %d consecutive times",
					part.PartKey, statusCode, job.RetryCount)
				job.ErrorMessage = &msg
				job.NextRunAt = nil
				job.UpdatedAt = time.Now().UTC()
				o.log.Error("local download failed permanently after repeated 5xx",
					"job_id", job.ID,
					"public_id", job.PublicID,
					"part_key", part.PartKey,
					"status_code", statusCode,
					"retry_count", job.RetryCount,
					"error", msg,
				)
				_ = o.store.UpdateJobState(ctx, job, store.StateFailed, msg)
				return nil
			}
			job.ErrorMessage = &msg
			job.UpdatedAt = time.Now().UTC()
			nextRun := time.Now().UTC().Add(o.cfg.Workers.DownloadInterval)
			job.NextRunAt = &nextRun
			o.log.Warn("local download failed, will retry",
				"job_id", job.ID,
				"public_id", job.PublicID,
				"part_key", part.PartKey,
				"next_run_at", nextRun.Format(time.RFC3339Nano),
				"error", msg,
			)
			_ = o.store.UpdateJobState(ctx, job, store.StateLocalDownloading, msg)
			return nil
		}
		job.RetryCount = 0
		part.Completed = part.ContentLength == 0 || part.BytesDone >= part.ContentLength
		part.UpdatedAt = time.Now().UTC()
		o.log.Info("transfer part complete",
			"job_id", job.ID,
			"public_id", job.PublicID,
			"part_key", part.PartKey,
			"bytes_done", part.BytesDone,
			"bytes_total", part.ContentLength,
			"path", part.TempPath,
		)
		if err := o.store.UpsertTransferPart(ctx, part); err != nil {
			return err
		}
	}

	parts, err = o.store.ListTransferParts(ctx, job.ID)
	if err != nil {
		return err
	}
	o.syncJobBytesFromParts(job, parts)
	job.ErrorMessage = nil
	job.UpdatedAt = time.Now().UTC()
	if allPartsCompleted(parts) {
		now := time.Now().UTC()
		job.NextRunAt = &now
		o.log.Info("all transfer parts completed", "job_id", job.ID, "public_id", job.PublicID, "bytes_done", job.BytesDone, "bytes_total", job.BytesTotal)
		return o.store.UpdateJobState(ctx, job, store.StateLocalVerify, "local download finished")
	}
	nextRun := time.Now().UTC().Add(o.cfg.Workers.DownloadInterval)
	job.NextRunAt = &nextRun
	o.log.Debug("download still in progress", "job_id", job.ID, "public_id", job.PublicID, "next_run_at", nextRun.Format(time.RFC3339Nano))
	return o.store.UpdateJobState(ctx, job, store.StateLocalDownloading, "local download in progress")
}

func (o *Orchestrator) refreshDownloadPartSourceURL(ctx context.Context, job *store.Job, part *store.TransferPart, downloadErr error) (bool, error) {
	var statusErr *files.HTTPStatusError
	if !errors.As(downloadErr, &statusErr) || !shouldRefreshLink(statusErr.StatusCode) {
		return false, nil
	}
	if strings.TrimSpace(deref(job.RemoteID)) == "" {
		return false, nil
	}

	assets, err := o.torbox.GetDownloadLinks(ctx, string(job.SourceType), deref(job.RemoteID))
	if err != nil {
		return false, fmt.Errorf("refresh download links: %w", err)
	}
	asset, ok := findMatchingDownloadAsset(part, assets)
	if !ok {
		return false, nil
	}
	if asset.URL == "" || asset.URL == part.SourceURL {
		return false, nil
	}

	part.SourceURL = asset.URL
	if asset.FileID != "" {
		part.FileID = &asset.FileID
	}
	if asset.Size > 0 && part.ContentLength == 0 {
		part.ContentLength = asset.Size
	}
	part.UpdatedAt = time.Now().UTC()
	if err := o.store.UpsertTransferPart(ctx, part); err != nil {
		return false, fmt.Errorf("persist refreshed transfer part: %w", err)
	}

	o.log.Info("refreshed transfer part download link",
		"job_id", job.ID,
		"public_id", job.PublicID,
		"part_key", part.PartKey,
		"status_code", statusErr.StatusCode,
	)
	return true, nil
}

func shouldRefreshLink(statusCode int) bool {
	return statusCode == http.StatusNotFound || isRefresh5xx(statusCode)
}

func isRefresh5xx(statusCode int) bool {
	return statusCode >= http.StatusInternalServerError && statusCode < 600
}

func reset5xxCount(job *store.Job, prevDone, done int64) {
	if done > prevDone && job.RetryCount > 0 {
		job.RetryCount = 0
	}
}

func findMatchingDownloadAsset(part *store.TransferPart, assets []torbox.DownloadAsset) (torbox.DownloadAsset, bool) {
	for idx, asset := range assets {
		if partMatchesAsset(part, asset, idx) {
			return asset, true
		}
	}
	return torbox.DownloadAsset{}, false
}

func partMatchesAsset(part *store.TransferPart, asset torbox.DownloadAsset, idx int) bool {
	if part.PartKey == partKey(asset, idx) {
		return true
	}
	if part.FileID != nil && *part.FileID != "" && asset.FileID == *part.FileID {
		return true
	}
	return part.RelativePath == safeRelativePath(asset.RelativePath, idx)
}

func shouldCheckpointProgress(lastPersist, now time.Time, done, total int64) bool {
	if lastPersist.IsZero() {
		return true
	}
	if total > 0 && done >= total {
		return true
	}
	return now.Sub(lastPersist) >= progressCheckpointInterval
}
