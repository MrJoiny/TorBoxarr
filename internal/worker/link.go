package worker

// Link mode.
//
// By default TorBoxarr pulls finished files down to local disk. With
// TORBOXARR_LINK_ROOT set, it instead waits for the finished TorBox item to
// appear on a filesystem mount that already serves TorBox content (for example
// a decypharr, rclone or TorBox WebDAV mount), symlinks each video file into
// the job's staging directory, and hands the job to the normal verify/finalize
// steps. Only symlinks are written locally.
//
// It never falls back to downloading: if the files do not appear on the mount
// within TORBOXARR_LINK_WAIT (default 2h) the job fails, so a broken mount can
// never silently turn back into full local downloads.

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
)

var linkVideoExt = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true, ".ts": true,
	".m2ts": true, ".webm": true, ".mov": true, ".wmv": true, ".mpg": true, ".mpeg": true,
}

type linkPlan struct {
	Key        string   // stable part key (TorBox file id, or index)
	Candidates []string // absolute paths on the mount to try, in order
	Rel        string   // file name inside the staging dir
	Size       int64
}

// planLinks decides, from TorBox's file list, which files to link and where
// each should be found on the mount. Only video files are linked: decypharr
// filters other files out of the mount, so waiting on them would never end.
func planLinks(root, itemName string, files []torbox.RemoteFile) []linkPlan {
	plans := make([]linkPlan, 0, len(files))
	for idx, f := range files {
		full := strings.ReplaceAll(strings.TrimSpace(f.Name), "\\", "/")
		base := path.Base(full)
		if base == "." || base == "/" || base == "" {
			base = path.Base(strings.TrimSpace(f.ShortName))
		}
		if base == "." || base == "/" || base == "" || strings.Contains(base, "..") {
			continue
		}
		if !linkVideoExt[strings.ToLower(path.Ext(base))] {
			continue
		}
		folders := make([]string, 0, 2)
		if i := strings.Index(path.Clean(full), "/"); i > 0 {
			folders = append(folders, path.Clean(full)[:i])
		}
		if itemName != "" && (len(folders) == 0 || folders[0] != itemName) {
			folders = append(folders, itemName)
		}
		cands := make([]string, 0, len(folders))
		for _, folder := range folders {
			if strings.Contains(folder, "..") {
				continue
			}
			cands = append(cands, filepath.Join(root, folder, base))
		}
		if len(cands) == 0 {
			continue
		}
		key := f.FileID
		if key == "" {
			key = fmt.Sprintf("link-%03d", idx)
		}
		plans = append(plans, linkPlan{Key: key, Candidates: cands, Rel: base, Size: f.Size})
	}
	return plans
}

// resolveLink returns the first candidate that exists on the mount.
func resolveLink(p linkPlan) (string, bool) {
	for _, c := range p.Candidates {
		if _, err := os.Stat(c); err == nil {
			return c, true
		}
	}
	return "", false
}

// placeSymlink (re)creates stagingDir/rel -> target.
func placeSymlink(stagingDir, rel, target string) (string, error) {
	dst := filepath.Join(stagingDir, rel)
	if err := ensurePathWithinRoot(stagingDir, dst); err != nil {
		return "", err
	}
	if cur, err := os.Readlink(dst); err == nil && cur == target {
		return dst, nil
	}
	_ = os.Remove(dst)
	if err := os.Symlink(target, dst); err != nil {
		return "", fmt.Errorf("symlink %s -> %s: %w", dst, target, err)
	}
	return dst, nil
}

func (o *Orchestrator) processLinkJob(ctx context.Context, job *store.Job) error {
	root := o.cfg.Link.Root
	retry := func(msg string) error {
		next := time.Now().UTC().Add(o.cfg.Workers.DownloadInterval)
		job.NextRunAt = &next
		job.ErrorMessage = &msg
		job.UpdatedAt = time.Now().UTC()
		return o.store.UpdateJobState(ctx, job, store.StateLocalDownloading, msg)
	}
	fail := func(msg string) error {
		job.NextRunAt = nil
		job.ErrorMessage = &msg
		job.UpdatedAt = time.Now().UTC()
		o.log.Error("link mode failed", "job_id", job.ID, "public_id", job.PublicID, "error", msg)
		return o.store.UpdateJobState(ctx, job, store.StateFailed, msg)
	}

	// The wait clock starts the first time this job enters link mode and is
	// kept in the transfer parts' CreatedAt, which survives restarts.
	parts, err := o.store.ListTransferParts(ctx, job.ID)
	if err != nil {
		return err
	}
	started := time.Now().UTC()
	for _, p := range parts {
		// Only link-mode parts count: jobs that were mid-download when link
		// mode was switched on must not inherit their old clock and fail at once.
		if !strings.HasPrefix(p.SourceURL, "link://") {
			continue
		}
		if !p.CreatedAt.IsZero() && p.CreatedAt.Before(started) {
			started = p.CreatedAt
		}
	}

	status, err := o.torbox.GetTaskStatus(ctx, string(job.SourceType), deref(job.RemoteID))
	if err != nil {
		return retry(fmt.Sprintf("link mode: fetching TorBox item: %v", err))
	}
	plans := planLinks(root, status.Name, status.Files)
	if len(plans) == 0 {
		return fail(fmt.Sprintf("link mode: TorBox item %q has no video files to link", status.Name))
	}

	now := time.Now().UTC()
	missing := 0
	for _, p := range plans {
		part := &store.TransferPart{
			JobID:         job.ID,
			PartKey:       p.Key,
			SourceURL:     "link://" + p.Candidates[0],
			TempPath:      filepath.Join(*job.StagingPath, p.Rel),
			RelativePath:  p.Rel,
			ContentLength: p.Size,
			CreatedAt:     started,
			UpdatedAt:     now,
		}
		target, ok := resolveLink(p)
		if ok {
			dst, err := placeSymlink(*job.StagingPath, p.Rel, target)
			if err != nil {
				return fail(err.Error())
			}
			part.TempPath = dst
			part.SourceURL = "link://" + target
			part.BytesDone = p.Size
			part.Completed = true
		} else {
			missing++
		}
		if err := o.store.UpsertTransferPart(ctx, part); err != nil {
			return err
		}
	}

	if missing > 0 {
		waited := time.Since(started)
		if waited > o.cfg.Link.Wait {
			return fail(fmt.Sprintf("link mode: %d of %d files not visible under %s after %s (mount down, or TorBox item not listed)", missing, len(plans), root, waited.Round(time.Minute)))
		}
		o.log.Info("link mode: waiting for files to appear on mount", "job_id", job.ID, "public_id", job.PublicID, "missing", missing, "total", len(plans), "waited", waited.Round(time.Second))
		return retry(fmt.Sprintf("link mode: waiting for %d of %d files on mount", missing, len(plans)))
	}

	parts, err = o.store.ListTransferParts(ctx, job.ID)
	if err != nil {
		return err
	}
	o.syncJobBytesFromParts(job, parts)
	job.ErrorMessage = nil
	job.NextRunAt = &now
	job.UpdatedAt = now
	o.log.Info("link mode: all files linked", "job_id", job.ID, "public_id", job.PublicID, "files", len(plans))
	return o.store.UpdateJobState(ctx, job, store.StateLocalVerify, "linked from mount")
}
