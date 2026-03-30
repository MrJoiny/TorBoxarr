package api

import (
	"context"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
)

func (s *Server) withLocalTransferProgress(ctx context.Context, jobs []*store.Job) ([]*store.Job, error) {
	out := make([]*store.Job, 0, len(jobs))
	for _, job := range jobs {
		clone := *job
		if !needsLocalTransferOverlay(job.State) {
			out = append(out, &clone)
			continue
		}

		parts, err := s.store.ListTransferParts(ctx, job.ID)
		if err != nil {
			return nil, err
		}
		if len(parts) == 0 {
			out = append(out, &clone)
			continue
		}

		var total, done int64
		var startedAt time.Time
		var updatedAt time.Time
		for _, part := range parts {
			total += part.ContentLength
			done += part.BytesDone
			if startedAt.IsZero() || part.CreatedAt.Before(startedAt) {
				startedAt = part.CreatedAt
			}
			if part.UpdatedAt.After(updatedAt) {
				updatedAt = part.UpdatedAt
			}
		}

		clone.BytesTotal = total
		clone.BytesDone = done
		if !startedAt.IsZero() {
			clone.LocalDownloadStartedAt = &startedAt
		}
		if updatedAt.After(clone.UpdatedAt) {
			clone.UpdatedAt = updatedAt
		}

		out = append(out, &clone)
	}
	return out, nil
}

func needsLocalTransferOverlay(state store.JobState) bool {
	switch state {
	case store.StateLocalDownloadPending, store.StateLocalDownloading, store.StateLocalVerify, store.StateCompleted, store.StateRemovePending, store.StateFailed:
		return true
	default:
		return false
	}
}
