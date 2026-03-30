package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
)

const progressCheckpointInterval = 15 * time.Second

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
		dlStart := time.Now()
		lastProgressLog := time.Time{}
		lastProgressPersist := time.Time{}
		downloadErr := o.downloader.Download(ctx, part, func(done, total int64) error {
			part.BytesDone = done
			if total > 0 {
				part.ContentLength = total
			}
			now := time.Now().UTC()
			part.UpdatedAt = now
			if now.Sub(lastProgressLog) >= 15*time.Second {
				lastProgressLog = now
				pct := 0.0
				if total > 0 {
					pct = float64(done) / float64(total) * 100
				}
				gbDone := float64(done) / (1024 * 1024 * 1024)
				gbTotal := float64(total) / (1024 * 1024 * 1024)
				elapsed := now.Sub(dlStart).Seconds()
				rateMBs := 0.0
				if elapsed > 0 {
					rateMBs = float64(done) / (1024 * 1024) / elapsed
				}
				o.log.Info("transfer progress",
					"job_id", job.ID,
					"part_key", part.PartKey,
					"progress", fmt.Sprintf("%.1f%%", pct),
					"done_gb", fmt.Sprintf("%.2f", gbDone),
					"total_gb", fmt.Sprintf("%.2f", gbTotal),
					"rate_mbs", fmt.Sprintf("%.2f", rateMBs),
				)
			}
			if shouldCheckpointProgress(lastProgressPersist, now, done, total) {
				lastProgressPersist = now
				return o.store.UpsertTransferPart(ctx, part)
			}
			return nil
		})
		if downloadErr != nil {
			msg := downloadErr.Error()
			if err := o.store.UpsertTransferPart(ctx, part); err != nil {
				o.log.Warn("failed to persist download progress after error",
					"job_id", job.ID,
					"public_id", job.PublicID,
					"part_key", part.PartKey,
					"error", err,
				)
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

func shouldCheckpointProgress(lastPersist, now time.Time, done, total int64) bool {
	if lastPersist.IsZero() {
		return true
	}
	if total > 0 && done >= total {
		return true
	}
	return now.Sub(lastPersist) >= progressCheckpointInterval
}
