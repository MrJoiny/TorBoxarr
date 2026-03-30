package torbox

import (
	"context"
	"errors"
	"fmt"
)

type Client interface {
	CreateTorrentTask(ctx context.Context, req CreateTorrentTaskRequest) (*CreateTaskResponse, error)
	CreateUsenetTask(ctx context.Context, req CreateUsenetTaskRequest) (*CreateTaskResponse, error)
	GetQueuedStatus(ctx context.Context, sourceType string, queuedID string) (*TaskStatus, error)
	GetTaskStatus(ctx context.Context, sourceType string, remoteID string) (*TaskStatus, error)
	FindActiveTask(ctx context.Context, sourceType string, remoteID, queueAuthID, remoteHash string) (*TaskStatus, error)
	GetDownloadLinks(ctx context.Context, sourceType string, remoteID string) ([]DownloadAsset, error)
	DeleteTask(ctx context.Context, sourceType string, remoteID string) error
}

type CreateTorrentTaskRequest struct {
	Magnet          string
	PayloadPath     string
	Name            string
	Seed            int
	AllowZip        bool
	AsQueued        bool
	AddOnlyIfCached bool
}

type CreateUsenetTaskRequest struct {
	Link            string
	PayloadPath     string
	Name            string
	Password        string
	PostProcessing  int
	AsQueued        bool
	AddOnlyIfCached bool
}

type CreateTaskResponse struct {
	RemoteID    string
	QueuedID    string
	QueueAuthID string
	RemoteHash  string
	DisplayName string
}

type RemoteFile struct {
	FileID       string
	Name         string
	ShortName    string
	RelativePath string
	Size         int64
}

type TaskStatus struct {
	RemoteID         string
	QueuedID         string
	QueueAuthID      string
	Hash             string
	Name             string
	State            string
	Label            string
	Progress         float64
	BytesTotal       int64
	BytesDone        int64
	DownloadPresent  bool
	DownloadFinished bool
	DownloadReady    bool
	Failed           bool
	Inactive         bool
	Error            string
	Files            []RemoteFile
}

type DownloadAsset struct {
	FileID       string
	URL          string
	RelativePath string
	Size         int64
}

type RetryableError struct {
	Err error
}

func (e *RetryableError) Error() string {
	return e.Err.Error()
}

func (e *RetryableError) Unwrap() error {
	return e.Err
}

func MarkRetryable(err error) error {
	if err == nil {
		return nil
	}
	var retryable *RetryableError
	if errors.As(err, &retryable) {
		return err
	}
	return &RetryableError{Err: err}
}

func IsRetryable(err error) bool {
	var retryable *RetryableError
	return errors.As(err, &retryable)
}

func RequireRemoteID(remoteID string) error {
	if remoteID == "" {
		return fmt.Errorf("remote id is required")
	}
	return nil
}
