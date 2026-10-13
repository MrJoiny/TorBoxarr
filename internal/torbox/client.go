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
	FindQueuedTask(ctx context.Context, sourceType string, queuedID, queueAuthID, remoteHash string) (*TaskStatus, error)
	GetTaskStatus(ctx context.Context, sourceType string, remoteID string) (*TaskStatus, error)
	FindActiveTask(ctx context.Context, sourceType string, remoteID, queueAuthID, remoteHash string) (*TaskStatus, error)
	FindActiveTaskByIdentity(ctx context.Context, sourceType string, remoteID, queuedID, queueAuthID, remoteHash string) (*TaskStatus, error)
	GetDownloadLinks(ctx context.Context, sourceType string, remoteID string) ([]DownloadAsset, error)
	DeleteTask(ctx context.Context, sourceType string, remoteID string) error
	DeleteQueuedTask(ctx context.Context, sourceType string, queuedID string) error
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
	PostProcessing  *int
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

// RequestNotSentError reports a failure before an HTTP request was issued.
// Callers may retry it, but it must not consume an uncertain-request budget.
type RequestNotSentError struct {
	Err error
}

func (e *RequestNotSentError) Error() string {
	return e.Err.Error()
}

func (e *RequestNotSentError) Unwrap() error {
	return e.Err
}

func IsRequestNotSent(err error) bool {
	var notSent *RequestNotSentError
	return errors.As(err, &notSent)
}

type HTTPStatusError struct {
	StatusCode int
	Message    string
}

func (e *HTTPStatusError) Error() string {
	return e.Message
}

func IsHTTPStatus(err error, status int) bool {
	var statusErr *HTTPStatusError
	return errors.As(err, &statusErr) && statusErr.StatusCode == status
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
	return errors.As(err, &retryable) || IsRequestNotSent(err)
}

// ErrTorboxLogical indicates the TorBox API returned a structured error in its
// response body (typically `{"success":false,"error":"DATABASE_ERROR",...}` on a
// 500). This most often means the requested task does not exist or is otherwise
// unrecoverable upstream — but TorBox returns the same shape during a transient
// backend outage, so callers must NOT treat it as immediately fatal. It is used
// to distinguish a logical failure from a transport-level retryable error.
type ErrTorboxLogical struct {
	Err error
}

func (e *ErrTorboxLogical) Error() string {
	return e.Err.Error()
}

func (e *ErrTorboxLogical) Unwrap() error {
	return e.Err
}

func IsTorboxLogical(err error) bool {
	var logical *ErrTorboxLogical
	return errors.As(err, &logical)
}

// ErrTorboxAbsent means the requested upstream representation is already gone.
// It is intentionally distinct from a generic non-retryable API error so
// deletion callers can make the operation idempotent.
type ErrTorboxAbsent struct {
	Err error
}

func (e *ErrTorboxAbsent) Error() string {
	return e.Err.Error()
}

func (e *ErrTorboxAbsent) Unwrap() error {
	return e.Err
}

func IsTorboxAbsent(err error) bool {
	var absent *ErrTorboxAbsent
	return errors.As(err, &absent)
}

type ErrTorboxIdentityConflict struct {
	Err error
}

func (e *ErrTorboxIdentityConflict) Error() string {
	return e.Err.Error()
}

func (e *ErrTorboxIdentityConflict) Unwrap() error {
	return e.Err
}

func IsTorboxIdentityConflict(err error) bool {
	var conflict *ErrTorboxIdentityConflict
	return errors.As(err, &conflict)
}

func RequireRemoteID(remoteID string) error {
	if remoteID == "" {
		return fmt.Errorf("remote id is required")
	}
	return nil
}
