package compat_test

import (
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/compat"
	"github.com/mrjoiny/torboxarr/internal/store"
)

//go:fix inline
func strPtr(s string) *string { return new(s) }

func baseJob() *store.Job {
	now := time.Now().UTC()
	return &store.Job{
		ID:          "qb-001",
		PublicID:    "pubqb-001",
		SourceType:  store.SourceTypeTorrent,
		ClientKind:  store.ClientKindQBit,
		Category:    "movies",
		State:       store.StateAccepted,
		DisplayName: "Test Movie",
		BytesTotal:  1000,
		BytesDone:   0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func TestProjectQBitProgress_AllStates(t *testing.T) {
	tests := []struct {
		state   store.JobState
		total   int64
		done    int64
		wantMin float64
		wantMax float64
	}{
		{store.StateAccepted, 0, 0, 0, 0},
		{store.StateSubmitPending, 0, 0, 0, 0},
		{store.StateSubmitRetry, 0, 0, 0, 0},
		{store.StateRemoteQueued, 0, 0, 0.02, 0.02},
		{store.StateRemoteActive, 1000, 500, 0.49, 0.51},
		{store.StateRemoteActive, 0, 0, 0.05, 0.05},
		{store.StateLocalDownloadPending, 0, 0, 0.1, 0.1},
		{store.StateLocalDownloading, 1000, 500, 0.49, 0.51},
		{store.StateLocalDownloading, 0, 0, 0.5, 0.5},
		{store.StateLocalVerify, 0, 0, 0.99, 0.99},
		{store.StateCompleted, 0, 0, 1, 1},
		{store.StateRemovePending, 0, 0, 1, 1},
		{store.StateRemoteFailed, 0, 0, 0, 0},
		{store.StateRemoteFailed, 1000, 500, 0.49, 0.51},
		{store.StateFailed, 0, 0, 0, 0},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			job := baseJob()
			job.State = tt.state
			job.BytesTotal = tt.total
			job.BytesDone = tt.done

			info := compat.ProjectQBitTorrent(job)
			if info.Progress < tt.wantMin || info.Progress > tt.wantMax {
				t.Errorf("progress = %f, want [%f, %f]", info.Progress, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestProjectQBitState_AllStates(t *testing.T) {
	tests := []struct {
		state store.JobState
		want  string
	}{
		{store.StateAccepted, "queuedDL"},
		{store.StateSubmitPending, "queuedDL"},
		{store.StateSubmitRetry, "queuedDL"},
		{store.StateRemoteQueued, "queuedDL"},
		{store.StateRemoteActive, "downloading"},
		{store.StateLocalDownloadPending, "queuedDL"},
		{store.StateLocalDownloading, "downloading"},
		{store.StateLocalVerify, "checkingResumeData"},
		{store.StateCompleted, "pausedUP"},
		{store.StateRemovePending, "pausedUP"},
		{store.StateRemoteFailed, "error"},
		{store.StateFailed, "error"},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			job := baseJob()
			job.State = tt.state
			info := compat.ProjectQBitTorrent(job)
			if info.State != tt.want {
				t.Errorf("state = %q, want %q", info.State, tt.want)
			}
		})
	}
}

func TestProjectQBitTorrent(t *testing.T) {
	job := baseJob()
	job.State = store.StateCompleted
	job.BytesTotal = 2048
	job.BytesDone = 2048
	job.SourceURI = new("magnet:?xt=urn:btih:abc")
	completed := "/completed/movies/Test Movie"
	job.CompletedPath = &completed
	job.Metadata.Tags = []string{"radarr", "imported"}

	info := compat.ProjectQBitTorrent(job)

	if info.Hash != job.PublicID {
		t.Errorf("Hash = %q, want %q", info.Hash, job.PublicID)
	}
	if info.Name != "Test Movie" {
		t.Errorf("Name = %q, want %q", info.Name, "Test Movie")
	}
	if info.Category != "movies" {
		t.Errorf("Category = %q, want %q", info.Category, "movies")
	}
	if info.Progress != 1 {
		t.Errorf("Progress = %f, want 1.0", info.Progress)
	}
	if info.State != "pausedUP" {
		t.Errorf("State = %q, want %q", info.State, "pausedUP")
	}
	if info.MagnetURI != *job.SourceURI {
		t.Errorf("MagnetURI = %q, want %q", info.MagnetURI, *job.SourceURI)
	}
	if info.SavePath != completed {
		t.Errorf("SavePath = %q, want %q", info.SavePath, completed)
	}
	if info.Tags != "radarr,imported" {
		t.Errorf("Tags = %q, want %q", info.Tags, "radarr,imported")
	}
	if info.Size != 2048 {
		t.Errorf("Size = %d, want 2048", info.Size)
	}
}

func TestProjectQBitTransferInfo(t *testing.T) {
	jobs := []*store.Job{
		{State: store.StateLocalDownloading, BytesDone: 100},
		{State: store.StateCompleted, BytesDone: 500},
		{State: store.StateLocalDownloading, BytesDone: 200},
	}
	info := compat.ProjectQBitTransferInfo(jobs)

	if info.ConnectionStatus != "connected" {
		t.Errorf("ConnectionStatus = %q, want %q", info.ConnectionStatus, "connected")
	}
	if info.DLInfoData != 800 {
		t.Errorf("DLInfoData = %d, want 800", info.DLInfoData)
	}
	if info.DLInfoSpeed != 2 {
		t.Errorf("DLInfoSpeed = %d, want 2 (two downloading jobs)", info.DLInfoSpeed)
	}
}

func TestProjectQBitTransferInfo_Empty(t *testing.T) {
	info := compat.ProjectQBitTransferInfo(nil)
	if info.ConnectionStatus != "connected" {
		t.Errorf("ConnectionStatus = %q, want %q", info.ConnectionStatus, "connected")
	}
	if info.DLInfoData != 0 || info.DLInfoSpeed != 0 {
		t.Errorf("expected zero values for empty job list")
	}
}

func TestQBitDLSpeed(t *testing.T) {
	tests := []struct {
		name    string
		state   store.JobState
		done    int64
		total   int64
		elapsed time.Duration // UpdatedAt = CreatedAt + elapsed
		wantMin int64
		wantMax int64
	}{
		{
			name:    "non-downloading state returns 0",
			state:   store.StateCompleted,
			done:    1000,
			total:   1000,
			elapsed: 10 * time.Second,
			wantMin: 0,
			wantMax: 0,
		},
		{
			name:    "zero bytes done returns 0",
			state:   store.StateLocalDownloading,
			done:    0,
			total:   1000,
			elapsed: 10 * time.Second,
			wantMin: 0,
			wantMax: 0,
		},
		{
			name:    "same created and updated returns 0",
			state:   store.StateLocalDownloading,
			done:    1000,
			total:   2000,
			elapsed: 0,
			wantMin: 0,
			wantMax: 0,
		},
		{
			name:    "1000 bytes in 10s = 100 bytes/sec",
			state:   store.StateLocalDownloading,
			done:    1000,
			total:   2000,
			elapsed: 10 * time.Second,
			wantMin: 95,
			wantMax: 105,
		},
		{
			name:    "large download speed",
			state:   store.StateLocalDownloading,
			done:    100 * 1024 * 1024, // 100 MB
			total:   200 * 1024 * 1024,
			elapsed: 10 * time.Second,
			wantMin: 10 * 1024 * 1024 - 1024, // ~10 MB/s
			wantMax: 10*1024*1024 + 1024,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now().UTC()
			job := baseJob()
			job.State = tt.state
			job.BytesDone = tt.done
			job.BytesTotal = tt.total
			job.CreatedAt = now.Add(-tt.elapsed)
			job.UpdatedAt = now

			info := compat.ProjectQBitTorrent(job)
			if info.DLSpeed < tt.wantMin || info.DLSpeed > tt.wantMax {
				t.Errorf("DLSpeed = %d, want [%d, %d]", info.DLSpeed, tt.wantMin, tt.wantMax)
			}
		})
	}
}
