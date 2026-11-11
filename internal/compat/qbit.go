package compat

import (
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
)

type QBitCategory struct {
	Name     string `json:"name"`
	SavePath string `json:"savePath"`
}

type QBitTransferInfo struct {
	ConnectionStatus string `json:"connection_status"`
	DHTNodes         int    `json:"dht_nodes"`
	DLInfoData       int64  `json:"dl_info_data"`
	DLInfoSpeed      int64  `json:"dl_info_speed"`
	DLRateLimit      int64  `json:"dl_rate_limit"`
	UPInfoData       int64  `json:"up_info_data"`
	UPInfoSpeed      int64  `json:"up_info_speed"`
	UPRateLimit      int64  `json:"up_rate_limit"`
}

// QBitMainData is a /api/v2/sync/maindata answer. TorBoxarr keeps no
// per-client delta history, so every answer is a full update.
type QBitMainData struct {
	Rid         int64                      `json:"rid"`
	FullUpdate  bool                       `json:"full_update"`
	Torrents    map[string]QBitTorrentInfo `json:"torrents"`
	Categories  map[string]QBitCategory    `json:"categories"`
	ServerState QBitServerState            `json:"server_state"`
}

type QBitServerState struct {
	ConnectionStatus  string `json:"connection_status"`
	DLInfoData        int64  `json:"dl_info_data"`
	DLInfoSpeed       int64  `json:"dl_info_speed"`
	DLRateLimit       int64  `json:"dl_rate_limit"`
	UPInfoData        int64  `json:"up_info_data"`
	UPInfoSpeed       int64  `json:"up_info_speed"`
	UPRateLimit       int64  `json:"up_rate_limit"`
	AllTimeDL         int64  `json:"alltime_dl"`
	AllTimeUL         int64  `json:"alltime_ul"`
	FreeSpaceOnDisk   int64  `json:"free_space_on_disk"`
	UseAltSpeedLimits bool   `json:"use_alt_speed_limits"`
}

type QBitTorrentInfo struct {
	AddedOn      int64   `json:"added_on"`
	AmountLeft   int64   `json:"amount_left"`
	AutoTMM      bool    `json:"auto_tmm"`
	Availability float64 `json:"availability"`
	Category     string  `json:"category"`
	Completed    int64   `json:"completed"`
	CompletionOn int64   `json:"completion_on"`
	ContentPath  string  `json:"content_path"`
	DLSpeed      int64   `json:"dlspeed"`
	Downloaded   int64   `json:"downloaded"`
	Eta          int64   `json:"eta"`
	Hash         string  `json:"hash"`
	InfohashV1   string  `json:"infohash_v1"`
	MagnetURI    string  `json:"magnet_uri"`
	Name         string  `json:"name"`
	Priority     int     `json:"priority"`
	Progress     float64 `json:"progress"`
	Ratio        float64 `json:"ratio"`
	RatioLimit   float64 `json:"ratio_limit"`
	SeedingTime  int64   `json:"seeding_time"`
	SavePath     string  `json:"save_path"`
	Size         int64   `json:"size"`
	State        string  `json:"state"`
	Tags         string  `json:"tags"`
	TotalSize    int64   `json:"total_size"`
	Upspeed      int64   `json:"upspeed"`
}

func ProjectQBitCategory(category, savePath string) QBitCategory {
	return QBitCategory{
		Name:     category,
		SavePath: strings.TrimSpace(savePath),
	}
}

func ProjectQBitTransferInfo(jobs []*store.Job) QBitTransferInfo {
	var dlInfoData, dlInfoSpeed int64
	for _, job := range jobs {
		done, _ := projectLocalTransferBytes(job)
		dlInfoData += done
		dlInfoSpeed += qbitDLSpeed(job)
	}
	return QBitTransferInfo{
		ConnectionStatus: "connected",
		DHTNodes:         0,
		DLInfoData:       dlInfoData,
		DLInfoSpeed:      dlInfoSpeed,
		DLRateLimit:      0,
		UPInfoData:       0,
		UPInfoSpeed:      0,
		UPRateLimit:      0,
	}
}

func ProjectQBitMainData(rid int64, jobs []*store.Job, categories map[string]QBitCategory, freeSpace int64) QBitMainData {
	torrents := make(map[string]QBitTorrentInfo, len(jobs))
	for _, job := range jobs {
		torrents[QBitHash(job)] = ProjectQBitTorrent(job)
	}
	if categories == nil {
		categories = map[string]QBitCategory{}
	}
	transfer := ProjectQBitTransferInfo(jobs)
	return QBitMainData{
		Rid:        rid,
		FullUpdate: true,
		Torrents:   torrents,
		Categories: categories,
		ServerState: QBitServerState{
			ConnectionStatus: transfer.ConnectionStatus,
			DLInfoData:       transfer.DLInfoData,
			DLInfoSpeed:      transfer.DLInfoSpeed,
			DLRateLimit:      transfer.DLRateLimit,
			UPInfoData:       transfer.UPInfoData,
			UPInfoSpeed:      transfer.UPInfoSpeed,
			UPRateLimit:      transfer.UPRateLimit,
			AllTimeDL:        transfer.DLInfoData,
			FreeSpaceOnDisk:  freeSpace,
		},
	}
}

// QBitHash is the hash qBit clients see for a job: the torrent's info hash
// when TorBoxarr knows it, as real qBittorrent reports, else the public ID.
// Clients that follow a torrent by the info hash of what they added (magnet
// or .torrent) need the former to find it again.
func QBitHash(job *store.Job) string {
	if job.InfoHash != nil {
		if h := strings.ToLower(strings.TrimSpace(*job.InfoHash)); h != "" {
			return h
		}
	}
	return strings.ToLower(job.PublicID)
}

func ProjectQBitTorrent(job *store.Job) QBitTorrentInfo {
	done, total := projectLocalTransferBytes(job)
	progress := projectQBitProgress(job)
	state := projectQBitState(job)
	savePath, contentPath := qbitPathsForJob(job)

	completedOn := int64(0)
	if job.State == store.StateCompleted {
		completedOn = job.UpdatedAt.Unix()
	}

	magnetURI := ""
	if job.SourceURI != nil && strings.HasPrefix(strings.ToLower(*job.SourceURI), "magnet:") {
		magnetURI = *job.SourceURI
	}

	tags := strings.Join(job.Metadata.Tags, ",")
	return QBitTorrentInfo{
		AddedOn:      job.CreatedAt.Unix(),
		AmountLeft:   max(total-done, 0),
		AutoTMM:      false,
		Availability: 1,
		Category:     job.Category,
		Completed:    done,
		CompletionOn: completedOn,
		ContentPath:  contentPath,
		DLSpeed:      qbitDLSpeed(job),
		Downloaded:   done,
		Eta:          qbitETA(job),
		Hash:         QBitHash(job),
		InfohashV1:   QBitHash(job),
		MagnetURI:    magnetURI,
		Name:         job.DisplayName,
		Priority:     0,
		Progress:     progress,
		Ratio:        0,
		RatioLimit:   0,
		SeedingTime:  0,
		SavePath:     savePath,
		Size:         max(total, done),
		State:        state,
		Tags:         tags,
		TotalSize:    max(total, done),
		Upspeed:      0,
	}
}

func qbitPathsForJob(job *store.Job) (savePath, contentPath string) {
	switch {
	case job.CompletedPath != nil:
		contentPath = strings.TrimSpace(*job.CompletedPath)
	case job.StagingPath != nil:
		contentPath = strings.TrimSpace(*job.StagingPath)
	default:
		return "", ""
	}

	savePath = filepath.Dir(contentPath)
	if savePath == "." {
		savePath = ""
	}
	return savePath, contentPath
}

func projectQBitProgress(job *store.Job) float64 {
	done, total := projectLocalTransferBytes(job)
	switch job.State {
	case store.StateAccepted, store.StateSubmitPending, store.StateSubmitRetry:
		return 0
	case store.StateRemoteQueued:
		return 0
	case store.StateRemoteActive:
		return 0
	case store.StateLocalDownloadPending:
		return 0
	case store.StateLocalDownloading:
		if total == 0 {
			return 0
		}
		return clamp(float64(done) / float64(total))
	case store.StateLocalVerify:
		return 1
	case store.StateCompleted, store.StateRemovePending:
		return 1
	case store.StateRemoteFailed, store.StateFailed:
		if total > 0 && done > 0 {
			return clamp(float64(done) / float64(total))
		}
		return 0
	default:
		return 0
	}
}

func projectLocalTransferBytes(job *store.Job) (done, total int64) {
	switch job.State {
	case store.StateLocalDownloading, store.StateLocalVerify, store.StateCompleted, store.StateRemovePending, store.StateFailed:
		return max(job.BytesDone, 0), max(job.BytesTotal, 0)
	default:
		return 0, 0
	}
}

func projectQBitState(job *store.Job) string {
	_, total := projectLocalTransferBytes(job)
	switch job.State {
	case store.StateAccepted, store.StateSubmitPending, store.StateSubmitRetry:
		return "queuedDL"
	case store.StateRemoteQueued:
		return "queuedDL"
	case store.StateRemoteActive:
		return "queuedDL"
	case store.StateLocalDownloadPending:
		return "queuedDL"
	case store.StateLocalDownloading:
		if total <= 0 {
			return "queuedDL"
		}
		return "downloading"
	case store.StateLocalVerify:
		return "checkingResumeData"
	case store.StateCompleted, store.StateRemovePending:
		return "pausedUP"
	case store.StateRemoteFailed, store.StateFailed:
		return "error"
	default:
		return "stoppedDL"
	}
}

func qbitDLSpeed(job *store.Job) int64 {
	if job.State != store.StateLocalDownloading {
		return 0
	}
	return int64(localTransferRate(job))
}

func qbitETA(job *store.Job) int64 {
	seconds := localTransferETA(job)
	if seconds <= 0 {
		return 0
	}
	return seconds
}

func clamp(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

func localTransferRate(job *store.Job) float64 {
	if job.State != store.StateLocalDownloading {
		return 0
	}
	done, _ := projectLocalTransferBytes(job)
	if done <= 0 {
		return 0
	}
	startedAt := localTransferStartedAt(job)
	if startedAt == nil {
		return 0
	}
	elapsed := job.UpdatedAt.Sub(*startedAt)
	if elapsed <= 0 {
		return 0
	}
	return float64(done) / elapsed.Seconds()
}

func localTransferETA(job *store.Job) int64 {
	if job.State != store.StateLocalDownloading {
		return 0
	}
	done, total := projectLocalTransferBytes(job)
	if done <= 0 || total <= done {
		return 0
	}
	rate := localTransferRate(job)
	if rate <= 0 {
		return 0
	}
	return int64(math.Ceil(float64(total-done) / rate))
}

func localTransferStartedAt(job *store.Job) *time.Time {
	if job.LocalDownloadStartedAt != nil {
		return job.LocalDownloadStartedAt
	}
	if job.State != store.StateLocalDownloading {
		return nil
	}
	return &job.CreatedAt
}
