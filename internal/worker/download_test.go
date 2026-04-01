package worker

import (
	"testing"
	"time"
)

func TestProgressRateMBs(t *testing.T) {
	t0 := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	mb := int64(1024 * 1024)

	tests := []struct {
		name        string
		lastLogAt   time.Time
		lastLogDone int64
		done        int64
		now         time.Time
		want        float64
	}{
		{
			name: "first sample returns zero",
			done: 42 * mb,
			now:  t0,
			want: 0,
		},
		{
			name:        "subsequent sample uses byte delta over time delta",
			lastLogAt:   t0,
			lastLogDone: 100 * mb,
			done:        160 * mb,
			now:         t0.Add(15 * time.Second),
			want:        4,
		},
		{
			name:        "no new bytes returns zero",
			lastLogAt:   t0,
			lastLogDone: 160 * mb,
			done:        160 * mb,
			now:         t0.Add(15 * time.Second),
			want:        0,
		},
		{
			name:        "non positive elapsed returns zero",
			lastLogAt:   t0,
			lastLogDone: 160 * mb,
			done:        220 * mb,
			now:         t0,
			want:        0,
		},
		{
			name:        "resume baseline only counts bytes since first emitted log",
			lastLogAt:   t0,
			lastLogDone: 512 * mb,
			done:        572 * mb,
			now:         t0.Add(15 * time.Second),
			want:        4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := progressRateMBs(tt.lastLogAt, tt.lastLogDone, tt.done, tt.now)
			if got != tt.want {
				t.Fatalf("progressRateMBs(...) = %.2f, want %.2f", got, tt.want)
			}
		})
	}
}

func TestShouldCheckpointProgress(t *testing.T) {
	now := time.Now().UTC()

	if !shouldCheckpointProgress(time.Time{}, now, 1, 10) {
		t.Fatal("expected first progress update to persist")
	}
	if shouldCheckpointProgress(now, now.Add(5*time.Second), 5, 10) {
		t.Fatal("expected progress within interval to be skipped")
	}
	if !shouldCheckpointProgress(now, now.Add(progressCheckpointInterval), 5, 10) {
		t.Fatal("expected progress at interval boundary to persist")
	}
	if !shouldCheckpointProgress(now, now.Add(5*time.Second), 10, 10) {
		t.Fatal("expected completed progress to persist immediately")
	}
}
