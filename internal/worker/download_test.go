package worker

import (
	"testing"
	"time"
)

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
