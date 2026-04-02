package worker

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
)

func (o *Orchestrator) syncJobBytesFromParts(job *store.Job, parts []*store.TransferPart) {
	var total, done int64
	for _, part := range parts {
		total += part.ContentLength
		done += part.BytesDone
	}
	if total > 0 {
		job.BytesTotal = total
	}
	job.BytesDone = done
}

func allPartsCompleted(parts []*store.TransferPart) bool {
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !part.Completed {
			return false
		}
	}
	return true
}

func partKey(asset torbox.DownloadAsset, idx int) string {
	if asset.FileID != "" {
		return asset.FileID
	}
	return fmt.Sprintf("asset-%03d", idx)
}

func safeRelativePath(value string, idx int) string {
	fallback := fmt.Sprintf("asset-%03d.bin", idx)
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	if normalized == "." || strings.HasPrefix(normalized, "/") || strings.HasPrefix(normalized, "//") || hasWindowsDrivePrefix(normalized) {
		return fallback
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == "/" {
		return fallback
	}
	for part := range strings.SplitSeq(cleaned, "/") {
		if part == "" || part == "." || part == ".." {
			return fallback
		}
	}
	return filepath.FromSlash(cleaned)
}

func needsWakeup(state store.JobState) bool {
	switch state {
	case store.StateSubmitPending, store.StateSubmitRetry, store.StateRemoteQueued, store.StateRemoteActive, store.StateLocalDownloadPending, store.StateLocalDownloading, store.StateLocalVerify, store.StateRemovePending:
		return true
	default:
		return false
	}
}

func withJitter(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	extra := time.Duration(rand.Int64N(max(int64(base/5), 1)))
	return base + extra
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func ptr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func hasWindowsDrivePrefix(value string) bool {
	return len(value) >= 2 && value[1] == ':' && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z'))
}

func ensurePathWithinRoot(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve target: %w", err)
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return fmt.Errorf("compute relative path: %w", err)
	}
	rel = filepath.Clean(rel)
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("path %q escapes root %q", targetAbs, rootAbs)
	}
	return nil
}

func (o *Orchestrator) releaseJobClaim(ctx context.Context, workerName, jobID string) {
	if err := o.store.ReleaseJobClaim(ctx, jobID); err != nil {
		o.log.Error("failed to release job claim", "worker", workerName, "job_id", jobID, "error", err)
	}
}
