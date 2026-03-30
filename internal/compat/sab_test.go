package compat_test

import (
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/compat"
	"github.com/mrjoiny/torboxarr/internal/store"
)

func TestProjectSABQueueSlot(t *testing.T) {
	job := &store.Job{
		PublicID:    "pub-sab-001",
		DisplayName: "Test NZB",
		Category:    "tv",
		State:       store.StateLocalDownloading,
		BytesTotal:  10 * 1024 * 1024, // 10 MB
		BytesDone:   5 * 1024 * 1024,  // 5 MB
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	slot := compat.ProjectSABQueueSlot(job)

	if slot.NzoID != "TBOX-pub-sab-001" {
		t.Errorf("NzoID = %q, want %q", slot.NzoID, "TBOX-pub-sab-001")
	}
	if slot.Filename != "Test NZB" {
		t.Errorf("Filename = %q, want %q", slot.Filename, "Test NZB")
	}
	if slot.Cat != "tv" {
		t.Errorf("Cat = %q, want %q", slot.Cat, "tv")
	}
	if slot.Status != "Downloading" {
		t.Errorf("Status = %q, want %q", slot.Status, "Downloading")
	}
	if slot.MB != "10.00" {
		t.Errorf("MB = %q, want %q", slot.MB, "10.00")
	}
	if slot.MBLeft != "5.00" {
		t.Errorf("MBLeft = %q, want %q", slot.MBLeft, "5.00")
	}
	if slot.Percentage != 50 {
		t.Errorf("Percentage = %d, want %d", slot.Percentage, 50)
	}
}

func TestProjectSABHistorySlot(t *testing.T) {
	staging := "/staging/job-001"
	completed := "/completed/tv/show"
	errMsg := "download failed"
	job := &store.Job{
		PublicID:      "pub-sab-002",
		DisplayName:   "Failed NZB",
		Category:      "tv",
		State:         store.StateRemoteFailed,
		BytesTotal:    1024,
		BytesDone:     512,
		StagingPath:   &staging,
		CompletedPath: &completed,
		ErrorMessage:  &errMsg,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}

	slot := compat.ProjectSABHistorySlot(job)

	if slot.NzoID != "TBOX-pub-sab-002" {
		t.Errorf("NzoID = %q, want %q", slot.NzoID, "TBOX-pub-sab-002")
	}
	if slot.Name != "Failed NZB" {
		t.Errorf("Name = %q, want %q", slot.Name, "Failed NZB")
	}
	if slot.Status != "Failed" {
		t.Errorf("Status = %q, want %q", slot.Status, "Failed")
	}
	if slot.FailMessage != "download failed" {
		t.Errorf("FailMessage = %q, want %q", slot.FailMessage, "download failed")
	}
	if slot.Path != staging {
		t.Errorf("Path = %q, want %q", slot.Path, staging)
	}
	if slot.Storage != completed {
		t.Errorf("Storage = %q, want %q", slot.Storage, completed)
	}
}

func TestProjectSABHistorySlot_Completed(t *testing.T) {
	job := &store.Job{
		PublicID:    "pub-comp",
		DisplayName: "Completed NZB",
		Category:    "movies",
		State:       store.StateCompleted,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	slot := compat.ProjectSABHistorySlot(job)
	if slot.Status != "Completed" {
		t.Errorf("Status = %q, want %q", slot.Status, "Completed")
	}
}

func TestIsSABQueueState(t *testing.T) {
	queueStates := []store.JobState{
		store.StateAccepted,
		store.StateSubmitPending,
		store.StateSubmitRetry,
		store.StateRemoteQueued,
		store.StateRemoteActive,
		store.StateLocalDownloadPending,
		store.StateLocalDownloading,
		store.StateLocalVerify,
	}
	nonQueueStates := []store.JobState{
		store.StateCompleted,
		store.StateRemovePending,
		store.StateRemoteFailed,
		store.StateFailed,
		store.StateRemoved,
	}

	// Queue states should appear in ProjectSABQueue
	for _, s := range queueStates {
		job := &store.Job{State: s, PublicID: "p", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		result := compat.ProjectSABQueue("1.0", []*store.Job{job})
		if len(result.Queue.Slots) != 1 {
			t.Errorf("state %q: expected in queue, got excluded", s)
		}
	}
	for _, s := range nonQueueStates {
		job := &store.Job{State: s, PublicID: "p", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		result := compat.ProjectSABQueue("1.0", []*store.Job{job})
		if len(result.Queue.Slots) != 0 {
			t.Errorf("state %q: expected excluded from queue, got included", s)
		}
	}
}

func TestIsSABHistoryState(t *testing.T) {
	historyStates := []store.JobState{
		store.StateCompleted,
		store.StateRemovePending,
		store.StateRemoteFailed,
		store.StateFailed,
	}
	nonHistoryStates := []store.JobState{
		store.StateAccepted,
		store.StateSubmitPending,
		store.StateRemoteActive,
		store.StateLocalDownloading,
		store.StateRemoved,
	}

	for _, s := range historyStates {
		job := &store.Job{State: s, PublicID: "p", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		result := compat.ProjectSABHistory("1.0", []*store.Job{job})
		if len(result.History.Slots) != 1 {
			t.Errorf("state %q: expected in history, got excluded", s)
		}
	}
	for _, s := range nonHistoryStates {
		job := &store.Job{State: s, PublicID: "p", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		result := compat.ProjectSABHistory("1.0", []*store.Job{job})
		if len(result.History.Slots) != 0 {
			t.Errorf("state %q: expected excluded from history, got included", s)
		}
	}
}

func TestNormalizeSABNZOID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"TBOX-abc123", "abc123"},
		{"TBOX-", ""},
		{"abc123", "abc123"},
		{"", ""},
	}

	for _, tt := range tests {
		got := compat.NormalizeSABNZOID(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeSABNZOID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSABNZOID(t *testing.T) {
	got := compat.SABNZOID("abc123")
	if got != "TBOX-abc123" {
		t.Errorf("SABNZOID(abc123) = %q, want %q", got, "TBOX-abc123")
	}
}

func TestProjectSABQueue_Status(t *testing.T) {
	// When at least one slot is Downloading, queue status should be "Downloading"
	jobs := []*store.Job{
		{State: store.StateRemoteQueued, PublicID: "p1", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()},
		{State: store.StateLocalDownloading, PublicID: "p2", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()},
	}
	result := compat.ProjectSABQueue("1.0", jobs)
	if result.Queue.Status != "Downloading" {
		t.Errorf("Queue.Status = %q, want %q", result.Queue.Status, "Downloading")
	}

	// When no downloading, status should be "Idle"
	idleJobs := []*store.Job{
		{State: store.StateRemoteQueued, PublicID: "p1", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()},
	}
	result2 := compat.ProjectSABQueue("1.0", idleJobs)
	if result2.Queue.Status != "Idle" {
		t.Errorf("Queue.Status = %q, want %q", result2.Queue.Status, "Idle")
	}
}
