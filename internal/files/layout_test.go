package files_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrjoiny/torboxarr/internal/files"
)

func TestStagingPathForJob(t *testing.T) {
	tmpDir := t.TempDir()
	layout := files.NewLayout(tmpDir, filepath.Join(tmpDir, "staging"), filepath.Join(tmpDir, "completed"), filepath.Join(tmpDir, "payloads"))
	got := layout.StagingPathForJob("job-123")
	want := filepath.Join(tmpDir, "staging", "job-123")
	if got != want {
		t.Errorf("StagingPathForJob = %q, want %q", got, want)
	}
}

func TestCompletedPathForJob(t *testing.T) {
	tmpDir := t.TempDir()
	completedDir := filepath.Join(tmpDir, "completed")
	layout := files.NewLayout(tmpDir, filepath.Join(tmpDir, "staging"), completedDir, filepath.Join(tmpDir, "payloads"))

	tests := []struct {
		category    string
		displayName string
		jobID       string
		wantSuffix  string
	}{
		{"movies", "My Movie", "job-001", filepath.Join("movies", "My Movie-job-001")},
		{"", "My Movie", "job-002", filepath.Join("unnamed", "My Movie-job-002")},
		{"tv", "", "job-003", filepath.Join("tv", "unnamed-job-003")},
		{"movies", "bad_name_here", "job-004", filepath.Join("movies", "bad_name_here-job-004")},
		{"movies", "file/with<illegal>chars", "job-005", filepath.Join("movies", "file_with_illegal_chars-job-005")},
		{"cat:egory", "clean", "job-006", filepath.Join("cat_egory", "clean-job-006")},
	}

	for _, tt := range tests {
		got := layout.CompletedPathForJob(tt.category, tt.displayName, tt.jobID)
		wantFull := filepath.Join(completedDir, tt.wantSuffix)
		if got != wantFull {
			t.Errorf("CompletedPathForJob(%q, %q, %q) = %q, want %q",
				tt.category, tt.displayName, tt.jobID, got, wantFull)
		}
	}
}

func TestSavePayload(t *testing.T) {
	tmpDir := t.TempDir()
	layout := files.NewLayout(tmpDir,
		filepath.Join(tmpDir, "staging"),
		filepath.Join(tmpDir, "completed"),
		filepath.Join(tmpDir, "payloads"),
	)

	content := "hello world payload"
	path, digest, err := layout.SavePayload("job-001", "test.torrent", strings.NewReader(content))
	if err != nil {
		t.Fatalf("SavePayload: %v", err)
	}

	if path == "" {
		t.Error("expected non-empty path")
	}
	if digest == "" {
		t.Error("expected non-empty digest")
	}
	if len(digest) != 64 {
		t.Errorf("digest length = %d, want 64 (SHA-256 hex)", len(digest))
	}

	// Read back
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != content {
		t.Errorf("file content = %q, want %q", string(data), content)
	}
}

func TestPromote(t *testing.T) {
	tmpDir := t.TempDir()
	layout := files.NewLayout(tmpDir,
		filepath.Join(tmpDir, "staging"),
		filepath.Join(tmpDir, "completed"),
		filepath.Join(tmpDir, "payloads"),
	)

	stagingDir := filepath.Join(tmpDir, "staging", "job-001")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(stagingDir, "movie.mkv")
	if err := os.WriteFile(testFile, []byte("video data"), 0o644); err != nil {
		t.Fatal(err)
	}

	completedDir := filepath.Join(tmpDir, "completed", "movies", "My Movie-job-001")
	if err := layout.Promote(stagingDir, completedDir); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	// Staging should be gone
	if _, err := os.Stat(stagingDir); !os.IsNotExist(err) {
		t.Error("staging dir should not exist after promote")
	}

	// Completed should exist with file
	promotedFile := filepath.Join(completedDir, "movie.mkv")
	data, err := os.ReadFile(promotedFile)
	if err != nil {
		t.Fatalf("ReadFile promoted: %v", err)
	}
	if string(data) != "video data" {
		t.Errorf("promoted file content = %q, want %q", string(data), "video data")
	}
}

func TestPromote_EmptyPaths(t *testing.T) {
	layout := files.NewLayout("/data", "/data/staging", "/data/completed", "/data/payloads")

	if err := layout.Promote("", "/completed/foo"); err == nil {
		t.Error("expected error for empty stagingPath")
	}
	if err := layout.Promote("/staging/foo", ""); err == nil {
		t.Error("expected error for empty completedPath")
	}
}

func TestRemoveOrphanStagingDirs(t *testing.T) {
	tmpDir := t.TempDir()
	stagingDir := filepath.Join(tmpDir, "staging")
	layout := files.NewLayout(tmpDir, stagingDir,
		filepath.Join(tmpDir, "completed"),
		filepath.Join(tmpDir, "payloads"),
	)

	// Create staging dirs: one valid, two orphans
	for _, name := range []string{"valid-001", "orphan-001", "orphan-002"} {
		if err := os.MkdirAll(filepath.Join(stagingDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	validIDs := map[string]struct{}{"valid-001": {}}
	removed, err := layout.RemoveOrphanStagingDirs(validIDs)
	if err != nil {
		t.Fatalf("RemoveOrphanStagingDirs: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("removed %d dirs, want 2", len(removed))
	}

	// Valid dir should still exist
	if _, err := os.Stat(filepath.Join(stagingDir, "valid-001")); os.IsNotExist(err) {
		t.Error("valid staging dir was incorrectly removed")
	}

	// Orphan dirs should be gone
	for _, name := range []string{"orphan-001", "orphan-002"} {
		if _, err := os.Stat(filepath.Join(stagingDir, name)); !os.IsNotExist(err) {
			t.Errorf("orphan dir %q should have been removed", name)
		}
	}
}

func TestRemovePath(t *testing.T) {
	tmpDir := t.TempDir()
	layout := files.NewLayout(tmpDir, tmpDir, tmpDir, tmpDir)

	dir := filepath.Join(tmpDir, "to-remove")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := layout.RemovePath(dir); err != nil {
		t.Fatalf("RemovePath: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("dir should have been removed")
	}

	// Empty path should be no-op
	if err := layout.RemovePath(""); err != nil {
		t.Errorf("RemovePath(\"\") should be no-op, got %v", err)
	}
}
