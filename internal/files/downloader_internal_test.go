package files

import (
	"log/slog"
	"testing"
	"time"
)

func TestNewRangeDownloaderUsesLargeWriteBuffer(t *testing.T) {
	dl := NewRangeDownloader(slog.Default(), 30*time.Second)
	if dl.writeBufSize != downloadWriteBufSize {
		t.Fatalf("writeBufSize = %d, want %d", dl.writeBufSize, downloadWriteBufSize)
	}
	if dl.notifyEvery != downloadNotifyEvery {
		t.Fatalf("notifyEvery = %d, want %d", dl.notifyEvery, downloadNotifyEvery)
	}
}
