package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
	"github.com/mrjoiny/torboxarr/internal/worker"
)

func linkEnv(t *testing.T, wait time.Duration, createOnMount bool) (*workerEnv, string, *worker.Orchestrator) {
	t.Helper()
	env := newWorkerEnv(t)
	mount := filepath.Join(env.tmpDir, "mount")
	if createOnMount {
		if err := os.MkdirAll(filepath.Join(mount, "Show.S01E01.1080p-GRP"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(mount, "Show.S01E01.1080p-GRP", "ebfd37cc.mkv"), []byte("12345"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env.mock.GetTaskStatusFn = func(ctx context.Context, sourceType, remoteID string) (*torbox.TaskStatus, error) {
		return &torbox.TaskStatus{
			RemoteID: remoteID, Name: "Show.S01E01.1080p-GRP", DownloadFinished: true, DownloadPresent: true,
			Files: []torbox.RemoteFile{
				{FileID: "0", Name: "Show.S01E01.1080p-GRP/ebfd37cc.mkv", ShortName: "ebfd37cc.mkv", Size: 5},
				{FileID: "1", Name: "Show.S01E01.1080p-GRP/info.nfo", ShortName: "info.nfo", Size: 1},
			},
		}, nil
	}
	env.mock.GetDownloadLinksFn = func(ctx context.Context, sourceType, remoteID string) ([]torbox.DownloadAsset, error) {
		t.Error("link mode must never request download links")
		return nil, nil
	}
	cfg := workerConfig(env.tmpDir)
	cfg.Link.Root = mount
	cfg.Link.Wait = wait
	cfg.Workers.DownloadInterval = 50 * time.Millisecond
	cfg.Workers.FinalizeInterval = 50 * time.Millisecond
	orch := newOrchestratorWithConfig(t, env, cfg)

	job := makeWorkerJob("lnk-1", "pub-lnk-1", store.StateLocalDownloadPending, store.SourceTypeNZB)
	job.RemoteID = strPtr("2515089")
	job.Category = "tv"
	job.DisplayName = "Show.S01E01.1080p-GRP"
	past := time.Now().UTC().Add(-time.Minute)
	job.NextRunAt = &past
	if err := env.store.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	return env, mount, orch
}

func TestLinkModeCompletesWithSymlinksOnly(t *testing.T) {
	env, mount, orch := linkEnv(t, time.Hour, true)
	ctx, cancel := context.WithCancel(context.Background())
	if err := orch.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "link job completes", 10*time.Second, func() bool {
		got, _ := env.store.GetJobByID(context.Background(), "lnk-1")
		return got != nil && got.State == store.StateCompleted
	})
	cancel()
	orch.Wait()

	got, _ := env.store.GetJobByID(context.Background(), "lnk-1")
	entries, err := os.ReadDir(*got.CompletedPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "ebfd37cc.mkv" {
		t.Fatalf("completed dir = %v, want only the linked video", entries)
	}
	p := filepath.Join(*got.CompletedPath, "ebfd37cc.mkv")
	fi, err := os.Lstat(p)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink (mode %v, err %v)", p, fi.Mode(), err)
	}
	if tgt, _ := os.Readlink(p); tgt != filepath.Join(mount, "Show.S01E01.1080p-GRP", "ebfd37cc.mkv") {
		t.Fatalf("symlink target = %s", tgt)
	}
}

func TestLinkModeFailsInsteadOfDownloading(t *testing.T) {
	env, _, orch := linkEnv(t, 200*time.Millisecond, false)
	ctx, cancel := context.WithCancel(context.Background())
	if err := orch.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "link job fails", 10*time.Second, func() bool {
		got, _ := env.store.GetJobByID(context.Background(), "lnk-1")
		return got != nil && got.State == store.StateFailed
	})
	cancel()
	orch.Wait()
	got, _ := env.store.GetJobByID(context.Background(), "lnk-1")
	if got.ErrorMessage == nil || !strings.Contains(*got.ErrorMessage, "not visible") {
		t.Fatalf("error = %v", got.ErrorMessage)
	}
}
