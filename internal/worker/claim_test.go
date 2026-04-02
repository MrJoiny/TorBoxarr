package worker

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/config"
	"github.com/mrjoiny/torboxarr/internal/files"
	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
)

func TestRunDownloader_ReleasesClaimAfterProcessingError(t *testing.T) {
	ctx := context.Background()

	db, err := store.Open(ctx, ":memory:", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if err := store.RunMigrationsFS(db, store.EmbeddedMigrations); err != nil {
		t.Fatal(err)
	}

	st := store.New(db)
	tmpDir := t.TempDir()
	layout := files.NewLayout(
		tmpDir,
		filepath.Join(tmpDir, "staging"),
		filepath.Join(tmpDir, "completed"),
		filepath.Join(tmpDir, "payloads"),
	)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.Workers.DownloadInterval = 5 * time.Second
	cfg.Workers.BatchSize = 25

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	downloader := files.NewRangeDownloader(log, 30*time.Second)
	orch := NewOrchestrator(cfg, log, st, layout, downloader, &torbox.MockClient{})

	past := time.Now().UTC().Add(-time.Minute)
	blockingPath := filepath.Join(tmpDir, "blocked-staging")
	if err := os.WriteFile(blockingPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	job := &store.Job{
		ID:            "claim-error-001",
		PublicID:      "pub-claim-error-001",
		SourceType:    store.SourceTypeTorrent,
		ClientKind:    store.ClientKindQBit,
		Category:      "tv",
		State:         store.StateLocalDownloadPending,
		SubmissionKey: "claim-error-key",
		DisplayName:   "claim-error",
		StagingPath:   &blockingPath,
		NextRunAt:     &past,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	if err := orch.runDownloader(ctx); err != nil {
		t.Fatalf("runDownloader: %v", err)
	}

	got, err := st.GetJobByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClaimedBy != nil || got.ClaimedAt != nil {
		t.Fatalf("claim leaked after processing error: claimed_by=%v claimed_at=%v", got.ClaimedBy, got.ClaimedAt)
	}
	if got.State != store.StateLocalDownloadPending {
		t.Fatalf("State = %s, want %s", got.State, store.StateLocalDownloadPending)
	}
}
