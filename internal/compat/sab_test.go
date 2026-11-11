package compat_test

import (
	"encoding/json"
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
	if slot.Percentage != "50" {
		t.Errorf("Percentage = %q, want %q", slot.Percentage, "50")
	}
}

func TestProjectSABQueueSlot_LocalDownloadingWithoutKnownSizeStaysQueued(t *testing.T) {
	job := &store.Job{
		PublicID:    "pub-sab-unknown",
		DisplayName: "Unknown Size NZB",
		Category:    "tv",
		State:       store.StateLocalDownloading,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	slot := compat.ProjectSABQueueSlot(job)

	if slot.Status != "Queued" {
		t.Errorf("Status = %q, want %q", slot.Status, "Queued")
	}
}

func TestProjectSABQueueSlot_IgnoresRemoteProgress(t *testing.T) {
	job := &store.Job{
		PublicID:    "pub-sab-remote",
		DisplayName: "Remote NZB",
		Category:    "tv",
		State:       store.StateRemoteActive,
		BytesTotal:  10 * 1024 * 1024,
		BytesDone:   5 * 1024 * 1024,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	slot := compat.ProjectSABQueueSlot(job)

	if slot.Status != "Queued" {
		t.Errorf("Status = %q, want %q", slot.Status, "Queued")
	}
	if slot.MB != "0.00" {
		t.Errorf("MB = %q, want %q", slot.MB, "0.00")
	}
	if slot.MBLeft != "0.00" {
		t.Errorf("MBLeft = %q, want %q", slot.MBLeft, "0.00")
	}
	if slot.Percentage != "0" {
		t.Errorf("Percentage = %q, want %q", slot.Percentage, "0")
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

// Strict JSON clients decode SAB responses into typed structs, so the field
// types must match SABnzbd's API exactly.
func TestSABResponses_DecodeWithSABnzbdTypes(t *testing.T) {
	now := time.Now().UTC()
	jobs := []*store.Job{
		{PublicID: "q1", State: store.StateLocalDownloading, BytesTotal: 1000, BytesDone: 500, CreatedAt: now, UpdatedAt: now},
		{PublicID: "q2", State: store.StateRemoteQueued, CreatedAt: now, UpdatedAt: now},
		{PublicID: "h1", State: store.StateCompleted, BytesTotal: 2048, BytesDone: 2048, CreatedAt: now, UpdatedAt: now},
	}

	queueJSON, err := json.Marshal(compat.ProjectSABQueue("1.0", jobs))
	if err != nil {
		t.Fatalf("marshal queue: %v", err)
	}
	var queue struct {
		Queue struct {
			NoOfSlots      int `json:"noofslots"`
			NoOfSlotsTotal int `json:"noofslots_total"`
			Slots          []struct {
				Index      int    `json:"index"`
				Percentage string `json:"percentage"`
			} `json:"slots"`
		} `json:"queue"`
	}
	if err := json.Unmarshal(queueJSON, &queue); err != nil {
		t.Fatalf("strict decode of queue failed: %v\n%s", err, queueJSON)
	}
	if queue.Queue.NoOfSlots != 2 || queue.Queue.NoOfSlotsTotal != 2 {
		t.Errorf("noofslots = %d, noofslots_total = %d, want 2 and 2", queue.Queue.NoOfSlots, queue.Queue.NoOfSlotsTotal)
	}
	if got := []int{queue.Queue.Slots[0].Index, queue.Queue.Slots[1].Index}; got[0] != 0 || got[1] != 1 {
		t.Errorf("slot indexes = %v, want [0 1]", got)
	}

	historyJSON, err := json.Marshal(compat.ProjectSABHistory("1.0", jobs))
	if err != nil {
		t.Fatalf("marshal history: %v", err)
	}
	var history struct {
		History struct {
			NoOfSlots int `json:"noofslots"`
			Slots     []struct {
				Bytes     int64 `json:"bytes"`
				Completed int64 `json:"completed"`
			} `json:"slots"`
		} `json:"history"`
	}
	if err := json.Unmarshal(historyJSON, &history); err != nil {
		t.Fatalf("strict decode of history failed: %v\n%s", err, historyJSON)
	}
	if history.History.NoOfSlots != 1 {
		t.Errorf("history noofslots = %d, want 1", history.History.NoOfSlots)
	}
	if history.History.Slots[0].Bytes != 2048 {
		t.Errorf("history bytes = %d, want 2048", history.History.Slots[0].Bytes)
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
		{State: store.StateLocalDownloading, PublicID: "p2", BytesTotal: 1000, BytesDone: 100, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()},
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
