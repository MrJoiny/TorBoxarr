package files

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
)

type RangeDownloader struct {
	log          *slog.Logger
	client       *http.Client
	notifyEvery  int64
	writeBufSize int
}

type HTTPStatusError struct {
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("download part status %d", e.StatusCode)
}

const (
	downloadNotifyEvery  = 1 << 20
	downloadWriteBufSize = 8 << 20
)

func NewRangeDownloader(log *slog.Logger, timeout time.Duration) *RangeDownloader {
	return &RangeDownloader{
		log:          log,
		client:       &http.Client{Timeout: timeout},
		notifyEvery:  downloadNotifyEvery,
		writeBufSize: downloadWriteBufSize,
	}
}

func (d *RangeDownloader) Download(ctx context.Context, part *store.TransferPart, progress func(done, total int64) error) error {
	d.debug("starting range download", "part_key", part.PartKey, "temp_path", part.TempPath, "source_url", sanitizeDownloadURL(part.SourceURL))
	if err := os.MkdirAll(filepath.Dir(part.TempPath), 0o755); err != nil {
		return fmt.Errorf("ensure part directory: %w", err)
	}

	existingSize := int64(0)
	if info, err := os.Stat(part.TempPath); err == nil {
		existingSize = info.Size()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat temp part: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, part.SourceURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if existingSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
	}
	d.debug("issuing download request", "part_key", part.PartKey, "existing_size", existingSize)

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("download part: %w", err)
	}
	defer resp.Body.Close()
	d.debug("download response received", "part_key", part.PartKey, "status", resp.StatusCode, "content_length", resp.ContentLength)

	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
	case http.StatusRequestedRangeNotSatisfiable:
		part.BytesDone = existingSize
		if part.ContentLength == 0 {
			part.ContentLength = existingSize
		}
		return progress(part.BytesDone, part.ContentLength)
	default:
		return &HTTPStatusError{StatusCode: resp.StatusCode}
	}

	var file *os.File
	if existingSize > 0 && resp.StatusCode == http.StatusPartialContent {
		file, err = os.OpenFile(part.TempPath, os.O_WRONLY|os.O_APPEND, 0o644)
	} else {
		existingSize = 0
		file, err = os.OpenFile(part.TempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	}
	if err != nil {
		return fmt.Errorf("open temp file: %w", err)
	}
	defer file.Close()

	if etag := strings.TrimSpace(resp.Header.Get("ETag")); etag != "" {
		part.ETag = &etag
	}

	total := contentLength(resp, existingSize)
	if total > 0 {
		part.ContentLength = total
	}
	d.debug("download target size resolved", "part_key", part.PartKey, "total", total)

	if err := progress(existingSize, total); err != nil {
		return err
	}

	buf := make([]byte, d.writeBufSize)
	writtenSinceNotify := int64(0)
	current := existingSize
	sentFirstWriteProgress := existingSize > 0

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := file.Write(buf[:n]); err != nil {
				return fmt.Errorf("write temp file: %w", err)
			}
			current += int64(n)
			writtenSinceNotify += int64(n)
			part.BytesDone = current
			if !sentFirstWriteProgress || writtenSinceNotify >= d.notifyEvery {
				if err := progress(current, total); err != nil {
					return err
				}
				sentFirstWriteProgress = true
				writtenSinceNotify = 0
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fmt.Errorf("read response body: %w", readErr)
		}
	}

	part.BytesDone = current
	if part.ContentLength == 0 {
		part.ContentLength = current
	}
	d.debug("download finished", "part_key", part.PartKey, "bytes_done", current, "bytes_total", part.ContentLength)
	return progress(current, part.ContentLength)
}

func contentLength(resp *http.Response, existing int64) int64 {
	if resp.StatusCode == http.StatusPartialContent {
		if contentRange := resp.Header.Get("Content-Range"); contentRange != "" {
			if slash := strings.LastIndex(contentRange, "/"); slash >= 0 && slash+1 < len(contentRange) {
				if parsed, err := strconv.ParseInt(contentRange[slash+1:], 10, 64); err == nil {
					return parsed
				}
			}
		}
	}
	if resp.ContentLength > 0 {
		return existing + resp.ContentLength
	}
	return 0
}

func (d *RangeDownloader) debug(msg string, args ...any) {
	if d.log != nil {
		d.log.Debug(msg, args...)
	}
}

func sanitizeDownloadURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parsed.RawQuery = ""
	return parsed.String()
}
