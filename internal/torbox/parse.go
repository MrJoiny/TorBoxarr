package torbox

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func parseCreateTask(env *apiEnvelope) (*CreateTaskResponse, error) {
	if env == nil {
		return nil, fmt.Errorf("empty response envelope")
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return &CreateTaskResponse{}, nil
	}

	var object map[string]any
	if err := json.Unmarshal(env.Data, &object); err == nil {
		return &CreateTaskResponse{
			RemoteID:    extractActiveID("", object, true),
			QueuedID:    extractQueuedID(object),
			QueueAuthID: extractQueueAuthID("", object),
			RemoteHash:  firstString(object, "hash"),
			DisplayName: firstString(object, "name", "filename"),
		}, nil
	}

	var idOnly any
	if err := json.Unmarshal(env.Data, &idOnly); err == nil {
		if id := stringify(idOnly); id != "" {
			return &CreateTaskResponse{RemoteID: id}, nil
		}
	}
	return nil, fmt.Errorf("unable to parse create task response")
}

func parseItemsEnvelope(env *apiEnvelope) ([]map[string]any, error) {
	if env == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil, fmt.Errorf("empty item response")
	}

	var object map[string]any
	if err := json.Unmarshal(env.Data, &object); err == nil {
		return []map[string]any{object}, nil
	}

	var list []map[string]any
	if err := json.Unmarshal(env.Data, &list); err == nil {
		return list, nil
	}

	return nil, fmt.Errorf("unable to parse item response")
}

func parseLinkEnvelope(env *apiEnvelope) (string, error) {
	if env == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return "", fmt.Errorf("empty link response")
	}

	var plain string
	if err := json.Unmarshal(env.Data, &plain); err == nil && plain != "" {
		return plain, nil
	}

	var object map[string]any
	if err := json.Unmarshal(env.Data, &object); err == nil {
		link := firstString(object, "download_url", "link", "url", "cdn_url")
		if link != "" {
			return link, nil
		}
	}

	var list []map[string]any
	if err := json.Unmarshal(env.Data, &list); err == nil {
		for _, item := range list {
			if link := firstString(item, "download_url", "link", "url", "cdn_url"); link != "" {
				return link, nil
			}
		}
	}

	return "", fmt.Errorf("unable to parse download link response")
}

func parseTaskStatus(sourceType string, item map[string]any) *TaskStatus {
	files := extractRemoteFiles(item)
	progress := firstFloat(item, "progress", "download_progress")
	bytesTotal := firstInt(item, "size", "total_bytes", "download_size")
	bytesDone := firstInt(item, "downloaded", "downloaded_bytes")
	if bytesDone == 0 && bytesTotal > 0 && progress > 0 {
		bytesDone = int64(float64(bytesTotal) * progress)
	}

	state := strings.ToLower(firstString(item, "download_state", "status", "state"))
	label := strings.ToLower(firstString(item, "download_label", "label"))
	errorText := firstString(item, "error", "detail", "message")
	downloadPresent, hasDownloadPresent := boolField(item, "download_present")
	downloadFinished, _ := boolField(item, "download_finished")
	downloadReadyField, hasDownloadReady := boolField(item, "download_ready")
	downloadReady := false
	switch {
	case hasDownloadPresent:
		downloadReady = downloadPresent
	case hasDownloadReady:
		downloadReady = downloadReadyField
	default:
		downloadReady = label == "download ready" || label == "cached"
	}

	stateFailed := strings.Contains(state, "fail") ||
		strings.Contains(state, "error") ||
		strings.Contains(state, "abort") ||
		strings.Contains(state, "cancel") ||
		strings.Contains(state, "cannot be completed") ||
		strings.Contains(state, "repair failed") ||
		strings.Contains(state, "incomplete")
	labelFailed := strings.Contains(label, "fail") || strings.Contains(label, "incomplete")
	// If the download content is present/ready, do not treat it as failed
	// even if the state text contains failure markers (matches reference behaviour).
	failed := (stateFailed || labelFailed) && !downloadReady
	inactive := label == "inactive" || firstBool(item, "inactive")

	return &TaskStatus{
		RemoteID:         extractActiveID(sourceType, item, true),
		QueuedID:         extractQueuedID(item),
		QueueAuthID:      extractQueueAuthID(sourceType, item),
		Hash:             firstString(item, "hash"),
		Name:             firstString(item, "name", "filename", "title"),
		State:            state,
		Label:            label,
		Progress:         progress,
		BytesTotal:       bytesTotal,
		BytesDone:        bytesDone,
		DownloadPresent:  downloadPresent,
		DownloadFinished: downloadFinished,
		DownloadReady:    downloadReady,
		Failed:           failed,
		Inactive:         inactive,
		Error:            errorText,
		Files:            files,
	}
}

func extractQueuedID(item map[string]any) string {
	return firstString(item, "queued_id", "queue_id", "id")
}

func extractActiveID(sourceType string, item map[string]any, allowGenericID bool) string {
	keys := []string{}
	switch strings.ToLower(sourceType) {
	case "torrent":
		keys = append(keys, "torrent_id", "download_id")
	case "nzb", "usenet":
		keys = append(keys, "usenetdownload_id", "usenet_id")
	default:
		keys = append(keys, "torrent_id", "usenetdownload_id", "usenet_id", "download_id")
	}
	if allowGenericID {
		keys = append(keys, "id")
	}
	return firstString(item, keys...)
}

func extractQueueAuthID(sourceType string, item map[string]any) string {
	if authID := firstString(item, "auth_id"); authID != "" {
		return authID
	}
	if !strings.EqualFold(sourceType, "usenet") {
		return ""
	}
	torrentFile := firstString(item, "torrent_file")
	if torrentFile == "" {
		return ""
	}
	if slash := strings.Index(torrentFile, "/"); slash > 0 {
		return torrentFile[:slash]
	}
	return torrentFile
}

func extractRemoteFiles(item map[string]any) []RemoteFile {
	raw, ok := item["files"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	files := make([]RemoteFile, 0, len(list))
	for _, entry := range list {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		fileID := firstString(object, "id", "file_id")
		name := firstString(object, "name", "filename")
		shortName := firstString(object, "short_name", "shortName")
		relativePath := firstString(object, "path", "relative_path")
		if relativePath == "" {
			if shortName != "" {
				relativePath = shortName
			} else {
				relativePath = name
			}
		}
		files = append(files, RemoteFile{
			FileID:       fileID,
			Name:         name,
			ShortName:    shortName,
			RelativePath: relativePath,
			Size:         firstInt(object, "size", "bytes"),
		})
	}
	return files
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := m[key]
		if !ok {
			continue
		}
		if out := stringify(value); out != "" {
			return out
		}
	}
	return ""
}

func firstInt(m map[string]any, keys ...string) int64 {
	for _, key := range keys {
		value, ok := m[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case float64:
			return int64(typed)
		case float32:
			return int64(typed)
		case int:
			return int64(typed)
		case int64:
			return typed
		case json.Number:
			if out, err := typed.Int64(); err == nil {
				return out
			}
		case string:
			if parsed, err := strconv.ParseInt(typed, 10, 64); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func firstFloat(m map[string]any, keys ...string) float64 {
	for _, key := range keys {
		value, ok := m[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case float64:
			return typed
		case float32:
			return float64(typed)
		case int:
			return float64(typed)
		case int64:
			return float64(typed)
		case json.Number:
			if out, err := typed.Float64(); err == nil {
				return out
			}
		case string:
			if parsed, err := strconv.ParseFloat(typed, 64); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func firstBool(m map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, ok := m[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed
		case string:
			parsed, err := strconv.ParseBool(typed)
			if err == nil {
				return parsed
			}
		case float64:
			return typed != 0
		case int:
			return typed != 0
		}
	}
	return false
}

func boolField(m map[string]any, key string) (bool, bool) {
	value, ok := m[key]
	if !ok {
		return false, false
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, err := strconv.ParseBool(typed)
		if err == nil {
			return parsed, true
		}
	case float64:
		return typed != 0, true
	case int:
		return typed != 0, true
	}
	return false, false
}

func stringify(v any) string {
	switch typed := v.(type) {
	case nil:
		return ""
	case string:
		return typed
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case float32:
		return strconv.FormatInt(int64(typed), 10)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprintf("%v", typed)
	}
}
