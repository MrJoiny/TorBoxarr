package compat

import (
	"path/filepath"
	"strings"

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

type QBitMainData struct {
	FullUpdate bool  `json:"full_update"`
	Torrents   any   `json:"torrents"`
	Rid        int64 `json:"rid"`
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
		if job.State == store.StateLocalDownloading {
			dlInfoSpeed += 1
		}
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
		Hash:         job.PublicID,
		InfohashV1:   job.PublicID,
		MagnetURI:    magnetURI,
		Name:         job.DisplayName,
		Priority:     0,
		Progress:     progress,
		Ratio:        0,
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
	switch job.State {
	case store.StateAccepted, store.StateSubmitPending, store.StateSubmitRetry:
		return "queuedDL"
	case store.StateRemoteQueued:
		return "queuedDL"
	case store.StateRemoteActive:
		return "downloading"
	case store.StateLocalDownloadPending:
		return "queuedDL"
	case store.StateLocalDownloading:
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
	if job.BytesDone <= 0 {
		return 0
	}
	// Use elapsed time since job creation as an approximation. This slightly
	// underestimates actual download speed (includes queue/remote wait time)
	// but avoids a schema migration for a dedicated download_started_at field.
	elapsed := job.UpdatedAt.Sub(job.CreatedAt)
	if elapsed <= 0 {
		return 0
	}
	return job.BytesDone / int64(elapsed.Seconds())
}

func qbitETA(job *store.Job) int64 {
	if job.State != store.StateLocalDownloading || job.BytesDone <= 0 || job.BytesTotal <= job.BytesDone {
		return 0
	}
	return -1
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
