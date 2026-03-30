package compat

import (
	"fmt"
	"math"
	"strings"

	"github.com/mrjoiny/torboxarr/internal/store"
)

type SABAddResponse struct {
	Status bool     `json:"status"`
	NzoIDs []string `json:"nzo_ids"`
}

type SABQueueResponse struct {
	Queue SABQueue `json:"queue"`
}

type SABQueue struct {
	Version   string         `json:"version"`
	Paused    bool           `json:"paused"`
	NoOfSlots string         `json:"noofslots"`
	Start     int            `json:"start"`
	Limit     int            `json:"limit"`
	Finish    int            `json:"finish"`
	Slots     []SABQueueSlot `json:"slots"`
	Status    string         `json:"status"`
}

type SABQueueSlot struct {
	NzoID      string `json:"nzo_id"`
	Filename   string `json:"filename"`
	Cat        string `json:"cat"`
	MB         string `json:"mb"`
	MBLeft     string `json:"mbleft"`
	Percentage int    `json:"percentage"`
	Status     string `json:"status"`
	TimeLeft   string `json:"timeleft"`
	Priority   string `json:"priority"`
	PP         string `json:"pp"`
	Script     string `json:"script"`
}

type SABHistoryResponse struct {
	History SABHistory `json:"history"`
}

type SABHistory struct {
	Version   string           `json:"version"`
	NoOfSlots string           `json:"noofslots"`
	Slots     []SABHistorySlot `json:"slots"`
}

type SABHistorySlot struct {
	NzoID       string `json:"nzo_id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Status      string `json:"status"`
	FailMessage string `json:"fail_message"`
	Path        string `json:"path"`
	Storage     string `json:"storage"`
	Completed   int64  `json:"completed"`
	Downloaded  int64  `json:"downloaded"`
	Size        string `json:"size"`
}

func SABNZOID(publicID string) string {
	return "TBOX-" + publicID
}

func ProjectSABQueue(version string, jobs []*store.Job) SABQueueResponse {
	slots := make([]SABQueueSlot, 0)
	status := "Idle"
	for _, job := range jobs {
		if !isSABQueueState(job.State) {
			continue
		}
		slot := ProjectSABQueueSlot(job)
		slots = append(slots, slot)
		if slot.Status == "Downloading" {
			status = "Downloading"
		}
	}
	return SABQueueResponse{Queue: SABQueue{
		Version:   version,
		Paused:    false,
		NoOfSlots: fmt.Sprintf("%d", len(slots)),
		Start:     0,
		Limit:     len(slots),
		Finish:    len(slots),
		Slots:     slots,
		Status:    status,
	}}
}

func ProjectSABHistory(version string, jobs []*store.Job) SABHistoryResponse {
	slots := make([]SABHistorySlot, 0)
	for _, job := range jobs {
		if !isSABHistoryState(job.State) {
			continue
		}
		slots = append(slots, ProjectSABHistorySlot(job))
	}
	return SABHistoryResponse{History: SABHistory{
		Version:   version,
		NoOfSlots: fmt.Sprintf("%d", len(slots)),
		Slots:     slots,
	}}
}

func ProjectSABQueueSlot(job *store.Job) SABQueueSlot {
	done, total := projectLocalTransferBytes(job)
	totalMB := bytesToMB(total)
	leftMB := bytesToMB(max(total-done, 0))
	return SABQueueSlot{
		NzoID:      SABNZOID(job.PublicID),
		Filename:   job.DisplayName,
		Cat:        job.Category,
		MB:         fmt.Sprintf("%.2f", totalMB),
		MBLeft:     fmt.Sprintf("%.2f", leftMB),
		Percentage: percent(done, total),
		Status:     projectSABQueueStatus(job.State),
		TimeLeft:   "0:00:00",
		Priority:   "Normal",
		PP:         projectSABPP(job.Metadata.PostProcessing),
		Script:     "None",
	}
}

func ProjectSABHistorySlot(job *store.Job) SABHistorySlot {
	path := ""
	if job.StagingPath != nil {
		path = *job.StagingPath
	}
	storage := ""
	if job.CompletedPath != nil {
		storage = *job.CompletedPath
	}
	failMessage := ""
	if job.ErrorMessage != nil {
		failMessage = *job.ErrorMessage
	}
	return SABHistorySlot{
		NzoID:       SABNZOID(job.PublicID),
		Name:        job.DisplayName,
		Category:    job.Category,
		Status:      projectSABHistoryStatus(job.State),
		FailMessage: failMessage,
		Path:        path,
		Storage:     storage,
		Completed:   job.UpdatedAt.Unix(),
		Downloaded:  job.BytesDone,
		Size:        fmt.Sprintf("%.2f MB", bytesToMB(max(job.BytesTotal, job.BytesDone))),
	}
}

func isSABQueueState(state store.JobState) bool {
	switch state {
	case store.StateAccepted, store.StateSubmitPending, store.StateSubmitRetry, store.StateRemoteQueued, store.StateRemoteActive, store.StateLocalDownloadPending, store.StateLocalDownloading, store.StateLocalVerify:
		return true
	default:
		return false
	}
}

func isSABHistoryState(state store.JobState) bool {
	switch state {
	case store.StateCompleted, store.StateRemovePending, store.StateRemoteFailed, store.StateFailed:
		return true
	default:
		return false
	}
}

func projectSABQueueStatus(state store.JobState) string {
	switch state {
	case store.StateRemoteQueued:
		return "Queued"
	case store.StateLocalDownloading:
		return "Downloading"
	case store.StateLocalVerify:
		return "Moving"
	case store.StateRemoteActive:
		return "Downloading"
	default:
		return "Queued"
	}
}

func projectSABHistoryStatus(state store.JobState) string {
	switch state {
	case store.StateCompleted, store.StateRemovePending:
		return "Completed"
	default:
		return "Failed"
	}
}

func projectSABPP(v int) string {
	switch v {
	case 0:
		return "0"
	case 1:
		return "R"
	case 2:
		return "U"
	case 3:
		return "D"
	default:
		return "D"
	}
}

func bytesToMB(v int64) float64 {
	return math.Round((float64(v)/(1024*1024))*100) / 100
}

func percent(done, total int64) int {
	if total <= 0 {
		return 0
	}
	p := (float64(done) / float64(total)) * 100
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return int(math.Round(p))
}

func NormalizeSABNZOID(v string) string {
	return strings.TrimPrefix(v, "TBOX-")
}
