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

func newSecurityTestOrchestrator(t *testing.T) (*Orchestrator, *store.Store, *files.Layout) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, ":memory:", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := store.RunMigrationsFS(db, store.EmbeddedMigrations); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
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

	var cfg config.Config
	cfg.Server.BaseURL = "http://localhost"
	cfg.Data.Root = tmpDir
	cfg.Data.Staging = layout.Staging
	cfg.Data.Completed = layout.Completed
	cfg.Data.Payloads = layout.Payloads
	cfg.Workers.DownloadInterval = 5 * time.Second
	cfg.Workers.RemoveInterval = 5 * time.Second
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	return NewOrchestrator(&cfg, logger, st, layout, files.NewRangeDownloader(logger, 30*time.Second), &torbox.MockClient{}), st, layout
}

func TestSafeRelativePathRejectsUnsafeInputs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "nested", input: "season/episode.mkv", want: filepath.Join("season", "episode.mkv")},
		{name: "dot dot", input: "../escape.mkv", want: "asset-001.bin"},
		{name: "absolute posix", input: "/etc/passwd", want: "asset-002.bin"},
		{name: "windows drive", input: "C:/Windows/system32.dll", want: "asset-003.bin"},
		{name: "unc path", input: "\\\\server\\share\\file.bin", want: "asset-004.bin"},
		{name: "backslashes", input: "season\\episode.mkv", want: filepath.Join("season", "episode.mkv")},
		{name: "empty", input: "", want: "asset-006.bin"},
		{name: "dot", input: ".", want: "asset-007.bin"},
	}

	for idx, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := safeRelativePath(tt.input, idx); got != tt.want {
				t.Fatalf("safeRelativePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEnsurePathWithinRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "nested", "file.bin")
	if err := ensurePathWithinRoot(root, inside); err != nil {
		t.Fatalf("ensurePathWithinRoot(inside) = %v, want nil", err)
	}
	outside := filepath.Join(root, "..", "outside.bin")
	if err := ensurePathWithinRoot(root, outside); err == nil {
		t.Fatal("expected out-of-root path to be rejected")
	}
}

func TestProcessRemoveJobRejectsOutOfRootCleanup(t *testing.T) {
	orch, st, layout := newSecurityTestOrchestrator(t)
	ctx := context.Background()

	outsideDir := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	job := &store.Job{
		ID:            "job-remove-unsafe",
		PublicID:      "pub-remove-unsafe",
		SourceType:    store.SourceTypeTorrent,
		ClientKind:    store.ClientKindQBit,
		Category:      "movies",
		State:         store.StateRemovePending,
		SubmissionKey: "remove-unsafe",
		DisplayName:   "Unsafe Remove",
		CompletedPath: &outsideDir,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	if err := orch.processRemoveJob(ctx, job); err != nil {
		t.Fatalf("processRemoveJob() = %v, want nil because failure is persisted", err)
	}

	got, err := st.GetJobByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateFailed {
		t.Fatalf("state = %q, want %q", got.State, store.StateFailed)
	}
	if got.ErrorMessage == nil || (*got.ErrorMessage == "") {
		t.Fatal("expected error message to be persisted for unsafe cleanup path")
	}
	if _, err := os.Stat(outsideDir); err != nil {
		t.Fatalf("outside path should remain untouched, stat error: %v", err)
	}
	if got.CompletedPath == nil || *got.CompletedPath != outsideDir {
		t.Fatalf("CompletedPath = %v, want %q retained for investigation", got.CompletedPath, outsideDir)
	}
	if layout.Completed == outsideDir {
		t.Fatal("test setup invalid: outside path matched completed root")
	}
}
