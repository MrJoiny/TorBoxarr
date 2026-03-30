package store

import "time"

type SourceType string

const (
	SourceTypeTorrent SourceType = "torrent"
	SourceTypeNZB     SourceType = "nzb"
)

type ClientKind string

const (
	ClientKindQBit ClientKind = "qbit"
	ClientKindSAB  ClientKind = "sab"
)

type JobState string

const (
	StateAccepted             JobState = "accepted"
	StateSubmitPending        JobState = "submit_pending"
	StateSubmitRetry          JobState = "submit_retry"
	StateRemoteQueued         JobState = "remote_queued"
	StateRemoteActive         JobState = "remote_active"
	StateRemoteFailed         JobState = "remote_failed"
	StateLocalDownloadPending JobState = "local_download_pending"
	StateLocalDownloading     JobState = "local_downloading"
	StateLocalVerify          JobState = "local_verify"
	StateCompleted            JobState = "completed"
	StateRemovePending        JobState = "remove_pending"
	StateRemoved              JobState = "removed"
	StateFailed               JobState = "failed"
)

func (s JobState) Closed() bool {
	switch s {
	case StateRemoteFailed, StateRemoved, StateFailed:
		return true
	default:
		return false
	}
}

type SubmissionMetadata struct {
	SavePath         string   `json:"save_path,omitempty"`
	Rename           string   `json:"rename,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	SkipChecking     bool     `json:"skip_checking,omitempty"`
	Paused           bool     `json:"paused,omitempty"`
	RootFolder       bool     `json:"root_folder,omitempty"`
	Password         string   `json:"password,omitempty"`
	PostProcessing   int      `json:"post_processing,omitempty"`
	AsQueued         bool     `json:"as_queued,omitempty"`
	AddOnlyIfCached  bool     `json:"add_only_if_cached,omitempty"`
	UploadedFilename string   `json:"uploaded_filename,omitempty"`
	OriginalFilename string   `json:"original_filename,omitempty"`
}

type Job struct {
	ID                     string
	PublicID               string
	SourceType             SourceType
	ClientKind             ClientKind
	Category               string
	State                  JobState
	SubmissionKey          string
	RemoteID               *string
	QueuedID               *string
	QueueAuthID            *string
	RemoteHash             *string
	DisplayName            string
	InfoHash               *string
	SourceURI              *string
	PayloadRef             *string
	StagingPath            *string
	CompletedPath          *string
	BytesTotal             int64
	BytesDone              int64
	LocalDownloadStartedAt *time.Time
	ErrorMessage           *string
	RetryCount             int
	NextRunAt              *time.Time
	LastRemoteStatus       *string
	Metadata               SubmissionMetadata
	DeleteRequested        bool
	ClaimedBy              *string
	ClaimedAt              *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type TransferPart struct {
	ID            int64
	JobID         string
	PartKey       string
	FileID        *string
	SourceURL     string
	TempPath      string
	RelativePath  string
	ContentLength int64
	BytesDone     int64
	ETag          *string
	Completed     bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type JobEvent struct {
	ID        int64
	JobID     string
	FromState *JobState
	ToState   *JobState
	Message   string
	CreatedAt time.Time
}
