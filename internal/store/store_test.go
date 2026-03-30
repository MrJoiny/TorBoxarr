package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
)

func TestOpenCreatesParentDir(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "db", "nested", "torboxarr.db")

	db, err := store.Open(ctx, dbPath, 5*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := os.Stat(filepath.Dir(dbPath)); err != nil {
		t.Fatalf("Stat(parent) = %v, want existing parent dir", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("Stat(dbPath) = %v, want created sqlite file", err)
	}
}

func TestOpenMemoryPath(t *testing.T) {
	ctx := context.Background()

	db, err := store.Open(ctx, ":memory:", 5*time.Second)
	if err != nil {
		t.Fatalf("Open(:memory:) = %v", err)
	}
	t.Cleanup(func() { db.Close() })
}

// ─── CreateJob ───────────────────────────────────────────────────────────────

func TestCreateJob(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	job := makeJob("test-001", "pub-001", store.StateAccepted)
	job.SourceURI = new("magnet:?xt=urn:btih:abc123")

	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	got, err := st.GetJobByID(ctx, "test-001")
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if got.DisplayName != job.DisplayName {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, job.DisplayName)
	}
	if got.State != store.StateAccepted {
		t.Errorf("State = %q, want %q", got.State, store.StateAccepted)
	}
	if got.SourceURI == nil || *got.SourceURI != "magnet:?xt=urn:btih:abc123" {
		t.Errorf("SourceURI = %v, want %q", got.SourceURI, "magnet:?xt=urn:btih:abc123")
	}
	if got.Category != "movies" {
		t.Errorf("Category = %q, want %q", got.Category, "movies")
	}
}

func TestCreateJob_DuplicateID(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	job1 := makeJob("dup-001", "pub-dup-001", store.StateAccepted)
	if err := st.CreateJob(ctx, job1); err != nil {
		t.Fatalf("first CreateJob: %v", err)
	}

	job2 := makeJob("dup-001", "pub-dup-002", store.StateAccepted)
	job2.SubmissionKey = "different-key"
	if err := st.CreateJob(ctx, job2); err == nil {
		t.Fatal("expected error on duplicate ID, got nil")
	}
}

// ─── UpdateJob ───────────────────────────────────────────────────────────────

func TestUpdateJob(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	job := makeJob("upd-001", "pub-upd-001", store.StateAccepted)
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	job.DisplayName = "Updated Name"
	job.BytesTotal = 1024
	job.BytesDone = 512
	job.UpdatedAt = time.Now().UTC()
	if err := st.UpdateJob(ctx, job); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}

	got, err := st.GetJobByID(ctx, "upd-001")
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if got.DisplayName != "Updated Name" {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, "Updated Name")
	}
	if got.BytesTotal != 1024 {
		t.Errorf("BytesTotal = %d, want 1024", got.BytesTotal)
	}
	if got.BytesDone != 512 {
		t.Errorf("BytesDone = %d, want 512", got.BytesDone)
	}
}

// ─── UpdateJobState ──────────────────────────────────────────────────────────

func TestUpdateJobState(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	job := makeJob("st-001", "pub-st-001", store.StateAccepted)
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	if err := st.UpdateJobState(ctx, job, store.StateSubmitPending, "queued"); err != nil {
		t.Fatalf("UpdateJobState: %v", err)
	}

	got, err := st.GetJobByID(ctx, "st-001")
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if got.State != store.StateSubmitPending {
		t.Errorf("State = %q, want %q", got.State, store.StateSubmitPending)
	}
}

// ─── FindActiveBySubmissionKey ───────────────────────────────────────────────

func TestFindActiveBySubmissionKey(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	job := makeJob("find-001", "pub-find-001", store.StateRemoteActive)
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	got, err := st.FindActiveBySubmissionKey(ctx, "key-find-001")
	if err != nil {
		t.Fatalf("FindActiveBySubmissionKey: %v", err)
	}
	if got == nil {
		t.Fatal("expected job, got nil")
	}
	if got.ID != "find-001" {
		t.Errorf("ID = %q, want %q", got.ID, "find-001")
	}
}

func TestFindActiveBySubmissionKey_NoMatch(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Create a removed job — should NOT be found
	job := makeJob("rem-001", "pub-rem-001", store.StateRemoved)
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	got, err := st.FindActiveBySubmissionKey(ctx, "key-rem-001")
	if err != nil {
		t.Fatalf("FindActiveBySubmissionKey: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for removed job, got %+v", got)
	}
}

// ─── GetJobByPublicID ────────────────────────────────────────────────────────

func TestGetJobByPublicID(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	job := makeJob("pub-001", "publicid-001", store.StateAccepted)
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	got, err := st.GetJobByPublicID(ctx, "publicid-001")
	if err != nil {
		t.Fatalf("GetJobByPublicID: %v", err)
	}
	if got.ID != "pub-001" {
		t.Errorf("ID = %q, want %q", got.ID, "pub-001")
	}
}

func TestGetJobByPublicID_NotFound(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	got, err := st.GetJobByPublicID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil job, got %+v", got)
	}
}

// ─── ListVisibleClientJobs ──────────────────────────────────────────────────

func TestListVisibleClientJobs(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for i, state := range []store.JobState{store.StateAccepted, store.StateCompleted, store.StateRemoved} {
		job := makeJob("vis-"+string(rune('a'+i)), "pub-vis-"+string(rune('a'+i)), state)
		if err := st.CreateJob(ctx, job); err != nil {
			t.Fatalf("CreateJob[%d]: %v", i, err)
		}
	}

	jobs, err := st.ListVisibleClientJobs(ctx, store.ClientKindQBit, "", 100)
	if err != nil {
		t.Fatalf("ListVisibleClientJobs: %v", err)
	}
	// "removed" should be excluded
	if len(jobs) != 2 {
		t.Errorf("got %d visible jobs, want 2", len(jobs))
	}
}

func TestListVisibleClientJobs_CategoryFilter(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	j1 := makeJob("cat-001", "pub-cat-001", store.StateAccepted)
	j1.Category = "movies"
	j2 := makeJob("cat-002", "pub-cat-002", store.StateAccepted)
	j2.Category = "tv"
	for _, j := range []*store.Job{j1, j2} {
		if err := st.CreateJob(ctx, j); err != nil {
			t.Fatalf("CreateJob %s: %v", j.ID, err)
		}
	}

	jobs, err := st.ListVisibleClientJobs(ctx, store.ClientKindQBit, "tv", 100)
	if err != nil {
		t.Fatalf("ListVisibleClientJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	if jobs[0].Category != "tv" {
		t.Errorf("Category = %q, want %q", jobs[0].Category, "tv")
	}
}

// ─── ListJobsDue ────────────────────────────────────────────────────────────

func TestListJobsDue(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	j1 := makeJob("due-001", "pub-due-001", store.StateSubmitPending)
	past := now.Add(-1 * time.Minute)
	j1.NextRunAt = &past
	if err := st.CreateJob(ctx, j1); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	jobs, err := st.ListJobsDue(ctx, []store.JobState{store.StateSubmitPending}, now, 10)
	if err != nil {
		t.Fatalf("ListJobsDue: %v", err)
	}
	if len(jobs) != 1 {
		t.Errorf("got %d due jobs, want 1", len(jobs))
	}
}

func TestListJobsDue_FutureRunAt(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	j := makeJob("future-001", "pub-future-001", store.StateSubmitPending)
	future := now.Add(1 * time.Hour)
	j.NextRunAt = &future
	if err := st.CreateJob(ctx, j); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	jobs, err := st.ListJobsDue(ctx, []store.JobState{store.StateSubmitPending}, now, 10)
	if err != nil {
		t.Fatalf("ListJobsDue: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("got %d due jobs for future NextRunAt, want 0", len(jobs))
	}
}

// ─── DeleteRemovedOlderThan ────────────────────────────────────────────────

func TestDeleteRemovedOlderThan(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	j := makeJob("prune-001", "pub-prune-001", store.StateRemoved)
	j.UpdatedAt = now.Add(-48 * time.Hour)
	if err := st.CreateJob(ctx, j); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	affected, err := st.DeleteRemovedOlderThan(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("DeleteRemovedOlderThan: %v", err)
	}
	if affected != 1 {
		t.Errorf("affected = %d, want 1", affected)
	}

	got, err := st.GetJobByID(ctx, "prune-001")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil job after prune, got %+v", got)
	}
}

// ─── TransferParts ──────────────────────────────────────────────────────────

func TestUpsertTransferPart(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	job := makeJob("tp-001", "pub-tp-001", store.StateLocalDownloading)
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	part := &store.TransferPart{
		JobID:         "tp-001",
		PartKey:       "file-0",
		SourceURL:     "https://example.com/file.bin",
		TempPath:      "/tmp/file.bin",
		RelativePath:  "file.bin",
		ContentLength: 1024,
		BytesDone:     0,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := st.UpsertTransferPart(ctx, part); err != nil {
		t.Fatalf("UpsertTransferPart (insert): %v", err)
	}

	// Update the same part
	part.BytesDone = 512
	part.UpdatedAt = now.Add(1 * time.Second)
	if err := st.UpsertTransferPart(ctx, part); err != nil {
		t.Fatalf("UpsertTransferPart (update): %v", err)
	}

	parts, err := st.ListTransferParts(ctx, "tp-001")
	if err != nil {
		t.Fatalf("ListTransferParts: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1 (upsert should not duplicate)", len(parts))
	}
	if parts[0].BytesDone != 512 {
		t.Errorf("BytesDone = %d, want 512", parts[0].BytesDone)
	}
}

func TestListTransferParts(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	job := makeJob("ltp-001", "pub-ltp-001", store.StateLocalDownloading)
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	for i := range 3 {
		part := &store.TransferPart{
			JobID:         "ltp-001",
			PartKey:       "file-" + string(rune('0'+i)),
			SourceURL:     "https://example.com/file.bin",
			TempPath:      "/tmp/file.bin",
			RelativePath:  "file.bin",
			ContentLength: 100,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if err := st.UpsertTransferPart(ctx, part); err != nil {
			t.Fatalf("UpsertTransferPart[%d]: %v", i, err)
		}
	}

	parts, err := st.ListTransferParts(ctx, "ltp-001")
	if err != nil {
		t.Fatalf("ListTransferParts: %v", err)
	}
	if len(parts) != 3 {
		t.Errorf("got %d parts, want 3", len(parts))
	}
}

func TestDeleteTransferParts(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	job := makeJob("dtp-001", "pub-dtp-001", store.StateLocalDownloading)
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	part := &store.TransferPart{
		JobID: "dtp-001", PartKey: "file-0",
		SourceURL: "https://example.com/f", TempPath: "/tmp/f", RelativePath: "f",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.UpsertTransferPart(ctx, part); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteTransferParts(ctx, "dtp-001"); err != nil {
		t.Fatalf("DeleteTransferParts: %v", err)
	}
	parts, err := st.ListTransferParts(ctx, "dtp-001")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 0 {
		t.Errorf("got %d parts after delete, want 0", len(parts))
	}
}

// ─── QBit Sessions ──────────────────────────────────────────────────────────

func TestCreateQBitSession(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	err := st.CreateQBitSession(ctx, "sid-001", "admin", time.Now().Add(1*time.Hour))
	if err != nil {
		t.Fatalf("CreateQBitSession: %v", err)
	}
}

func TestValidateQBitSession_Valid(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.CreateQBitSession(ctx, "sid-valid", "admin", time.Now().Add(1*time.Hour)); err != nil {
		t.Fatal(err)
	}

	valid, err := st.ValidateQBitSession(ctx, "sid-valid")
	if err != nil {
		t.Fatalf("ValidateQBitSession: %v", err)
	}
	if !valid {
		t.Error("expected valid session")
	}
}

func TestValidateQBitSession_Expired(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.CreateQBitSession(ctx, "sid-expired", "admin", time.Now().Add(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}

	valid, err := st.ValidateQBitSession(ctx, "sid-expired")
	if err != nil {
		t.Fatalf("ValidateQBitSession: %v", err)
	}
	if valid {
		t.Error("expected expired session to be invalid")
	}
}

func TestValidateQBitSession_Missing(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	valid, err := st.ValidateQBitSession(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("ValidateQBitSession: %v", err)
	}
	if valid {
		t.Error("expected missing session to be invalid")
	}
}

func TestPruneExpiredQBitSessions(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// One expired, one valid
	if err := st.CreateQBitSession(ctx, "sid-exp", "admin", time.Now().Add(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateQBitSession(ctx, "sid-ok", "admin", time.Now().Add(1*time.Hour)); err != nil {
		t.Fatal(err)
	}

	pruned, err := st.PruneExpiredQBitSessions(ctx)
	if err != nil {
		t.Fatalf("PruneExpiredQBitSessions: %v", err)
	}
	if pruned != 1 {
		t.Errorf("pruned = %d, want 1", pruned)
	}

	// Verify the valid session is still there
	valid, err := st.ValidateQBitSession(ctx, "sid-ok")
	if err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Error("valid session was pruned")
	}
}

// ─── ClaimJobsDue ───────────────────────────────────────────────────────────

func TestClaimJobsDue(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	j1 := makeJob("claim-001", "pub-claim-001", store.StateSubmitPending)
	past := now.Add(-1 * time.Minute)
	j1.NextRunAt = &past
	if err := st.CreateJob(ctx, j1); err != nil {
		t.Fatal(err)
	}

	j2 := makeJob("claim-002", "pub-claim-002", store.StateSubmitPending)
	j2.NextRunAt = &past
	if err := st.CreateJob(ctx, j2); err != nil {
		t.Fatal(err)
	}

	claimed, err := st.ClaimJobsDue(ctx, "worker-1", []store.JobState{store.StateSubmitPending}, now, 10)
	if err != nil {
		t.Fatalf("ClaimJobsDue: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed %d jobs, want 2", len(claimed))
	}

	// Second claim should find nothing (already claimed)
	claimed2, err := st.ClaimJobsDue(ctx, "worker-2", []store.JobState{store.StateSubmitPending}, now, 10)
	if err != nil {
		t.Fatalf("ClaimJobsDue (second): %v", err)
	}
	if len(claimed2) != 0 {
		t.Errorf("second claim got %d jobs, want 0", len(claimed2))
	}
}

func TestClaimJobsDue_EmptyStates(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	claimed, err := st.ClaimJobsDue(ctx, "worker-1", nil, time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Errorf("expected nil for empty states, got %v", claimed)
	}
}

// ─── ReleaseJobClaim ────────────────────────────────────────────────────────

func TestReleaseJobClaim(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	j := makeJob("rel-001", "pub-rel-001", store.StateSubmitPending)
	past := now.Add(-1 * time.Minute)
	j.NextRunAt = &past
	if err := st.CreateJob(ctx, j); err != nil {
		t.Fatal(err)
	}

	// Claim
	claimed, err := st.ClaimJobsDue(ctx, "w1", []store.JobState{store.StateSubmitPending}, now, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimJobsDue: err=%v len=%d", err, len(claimed))
	}

	// Release
	if err := st.ReleaseJobClaim(ctx, "rel-001"); err != nil {
		t.Fatalf("ReleaseJobClaim: %v", err)
	}

	// Can now be claimed again
	claimed2, err := st.ClaimJobsDue(ctx, "w2", []store.JobState{store.StateSubmitPending}, now, 10)
	if err != nil {
		t.Fatalf("ClaimJobsDue after release: %v", err)
	}
	if len(claimed2) != 1 {
		t.Errorf("expected 1 claimable job after release, got %d", len(claimed2))
	}
}

// ─── ReleaseAllClaims ───────────────────────────────────────────────────────

func TestReleaseAllClaims(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, id := range []string{"rac-001", "rac-002"} {
		j := makeJob(id, "pub-"+id, store.StateSubmitPending)
		past := now.Add(-1 * time.Minute)
		j.NextRunAt = &past
		if err := st.CreateJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}

	// Claim all
	_, err := st.ClaimJobsDue(ctx, "w1", []store.JobState{store.StateSubmitPending}, now, 10)
	if err != nil {
		t.Fatal(err)
	}

	// Release all
	if err := st.ReleaseAllClaims(ctx); err != nil {
		t.Fatalf("ReleaseAllClaims: %v", err)
	}

	// All should be claimable again
	claimed, err := st.ClaimJobsDue(ctx, "w2", []store.JobState{store.StateSubmitPending}, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 {
		t.Errorf("expected 2 claimable jobs, got %d", len(claimed))
	}
}

// ─── GetJobsByIDs ───────────────────────────────────────────────────────────

func TestGetJobsByIDs(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"ids-001", "ids-002", "ids-003"} {
		if err := st.CreateJob(ctx, makeJob(id, "pub-"+id, store.StateAccepted)); err != nil {
			t.Fatal(err)
		}
	}

	jobs, err := st.GetJobsByIDs(ctx, []string{"ids-001", "ids-003"})
	if err != nil {
		t.Fatalf("GetJobsByIDs: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("got %d jobs, want 2", len(jobs))
	}
}

func TestGetJobsByIDs_Empty(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	jobs, err := st.GetJobsByIDs(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if jobs != nil {
		t.Errorf("expected nil for empty ids, got %v", jobs)
	}
}

// ─── ListOpenJobs ───────────────────────────────────────────────────────────

func TestListOpenJobs(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	states := []store.JobState{
		store.StateAccepted,
		store.StateRemoteActive,
		store.StateRemoved,
		store.StateRemoteFailed,
	}
	for i, s := range states {
		id := "open-" + string(rune('a'+i))
		if err := st.CreateJob(ctx, makeJob(id, "pub-"+id, s)); err != nil {
			t.Fatal(err)
		}
	}

	jobs, err := st.ListOpenJobs(ctx)
	if err != nil {
		t.Fatalf("ListOpenJobs: %v", err)
	}
	// removed + remote_failed + failed are excluded → 2
	if len(jobs) != 2 {
		t.Errorf("got %d open jobs, want 2", len(jobs))
	}
}
