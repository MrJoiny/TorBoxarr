package torbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (c *HTTPClient) CreateTorrentTask(ctx context.Context, req CreateTorrentTaskRequest) (*CreateTaskResponse, error) {
	c.debug("creating torrent task", "has_magnet", req.Magnet != "", "has_payload", req.PayloadPath != "", "name", req.Name)
	if err := c.wait(ctx, c.createLimiter); err != nil {
		return nil, err
	}
	values := map[string]string{}
	if req.Name != "" {
		values["name"] = req.Name
	}
	if req.Magnet != "" {
		values["magnet"] = req.Magnet
	}
	if req.Seed != 0 {
		values["seed"] = strconv.Itoa(req.Seed)
	}
	if req.AllowZip {
		values["allow_zip"] = "true"
	}
	if req.AsQueued {
		values["as_queued"] = "true"
	}
	if req.AddOnlyIfCached {
		values["add_only_if_cached"] = "true"
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if req.PayloadPath != "" {
		if err := attachFile(writer, "file", req.PayloadPath); err != nil {
			return nil, err
		}
	}
	for key, value := range values {
		if value == "" {
			continue
		}
		if err := writer.WriteField(key, value); err != nil {
			return nil, fmt.Errorf("write field %s: %w", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close writer: %w", err)
	}

	env, err := c.do(ctx, http.MethodPost, "/api/torrents/createtorrent", &body, writer.FormDataContentType(), true)
	if err != nil {
		return nil, err
	}
	return parseCreateTask(env, req.AsQueued)
}

func (c *HTTPClient) CreateUsenetTask(ctx context.Context, req CreateUsenetTaskRequest) (*CreateTaskResponse, error) {
	postProcessingValue := any(nil)
	if req.PostProcessing != nil {
		postProcessingValue = *req.PostProcessing
	}
	c.debug("creating usenet task", "has_link", req.Link != "", "has_payload", req.PayloadPath != "", "name", req.Name, "has_post_processing", req.PostProcessing != nil, "post_processing", postProcessingValue)
	if err := c.wait(ctx, c.createLimiter); err != nil {
		return nil, err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if req.PayloadPath != "" {
		if err := attachFile(writer, "file", req.PayloadPath); err != nil {
			return nil, err
		}
	}
	fields := map[string]string{}
	if req.Link != "" {
		fields["link"] = req.Link
	}
	if req.Name != "" {
		fields["name"] = req.Name
	}
	if req.Password != "" {
		fields["password"] = req.Password
	}
	if req.PostProcessing != nil {
		fields["post_processing"] = strconv.Itoa(*req.PostProcessing)
	}
	if req.AsQueued {
		fields["as_queued"] = "true"
	}
	if req.AddOnlyIfCached {
		fields["add_only_if_cached"] = "true"
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return nil, fmt.Errorf("write field %s: %w", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close writer: %w", err)
	}

	env, err := c.do(ctx, http.MethodPost, "/api/usenet/createusenetdownload", &body, writer.FormDataContentType(), true)
	if err != nil {
		return nil, err
	}
	return parseCreateTask(env, req.AsQueued)
}

func (c *HTTPClient) GetTaskStatus(ctx context.Context, sourceType string, remoteID string) (*TaskStatus, error) {
	c.debug("fetching torbox task status", "source_type", sourceType, "remote_id", remoteID)
	return c.FindActiveTask(ctx, sourceType, remoteID, "", "")
}

func (c *HTTPClient) GetQueuedStatus(ctx context.Context, sourceType string, queuedID string) (*TaskStatus, error) {
	if strings.TrimSpace(queuedID) == "" {
		return nil, fmt.Errorf("queued id is required")
	}
	c.debug("fetching torbox queued status", "source_type", sourceType, "queued_id", queuedID)
	return c.FindQueuedTask(ctx, sourceType, queuedID, "", "")
}

func (c *HTTPClient) FindQueuedTask(ctx context.Context, sourceType string, queuedID, queueAuthID, remoteHash string) (*TaskStatus, error) {
	sourceType = normalizeSourceType(sourceType)
	if sourceType == "" {
		return nil, fmt.Errorf("source type is required")
	}
	if strings.TrimSpace(queuedID) == "" && strings.TrimSpace(queueAuthID) == "" && strings.TrimSpace(remoteHash) == "" {
		return nil, fmt.Errorf("queued identity is required")
	}
	if err := c.wait(ctx, c.pollLimiter); err != nil {
		return nil, err
	}
	items, err := c.getQueuedItems(ctx, sourceType, queuedID)
	initialErr := err
	usedFullList := false
	if err != nil {
		if strings.TrimSpace(queuedID) == "" {
			return nil, err
		}
		if err := c.wait(ctx, c.pollLimiter); err != nil {
			return nil, err
		}
		items, err = c.getQueuedItems(ctx, sourceType, "")
		usedFullList = true
		if err != nil {
			return nil, err
		}
	}
	status, err := findTaskStatus(sourceType, items, "", queuedID, queueAuthID, remoteHash, true)
	if err != nil {
		if usedFullList || !IsTorboxIdentityConflict(err) || strings.TrimSpace(queuedID) == "" {
			return nil, err
		}
		initialErr = err
	}
	if status != nil {
		c.debug("matched queued torbox task",
			"source_type", sourceType,
			"queued_id", status.QueuedID,
			"has_hash", status.Hash != "",
		)
		return status, nil
	}
	if !usedFullList && strings.TrimSpace(queuedID) != "" && !strings.EqualFold(sourceType, "usenet") {
		if err := c.wait(ctx, c.pollLimiter); err != nil {
			return nil, err
		}
		items, err = c.getQueuedItems(ctx, sourceType, "")
		usedFullList = true
		if err != nil {
			return nil, err
		}
		status, err = findTaskStatus(sourceType, items, "", queuedID, queueAuthID, remoteHash, true)
		if err != nil {
			return nil, err
		}
		if status != nil {
			return status, nil
		}
	}
	if initialErr != nil {
		return nil, initialErr
	}
	return nil, nil
}

func (c *HTTPClient) FindActiveTask(ctx context.Context, sourceType string, remoteID, queueAuthID, remoteHash string) (*TaskStatus, error) {
	return c.FindActiveTaskByIdentity(ctx, sourceType, remoteID, "", queueAuthID, remoteHash)
}

func (c *HTTPClient) FindActiveTaskByIdentity(ctx context.Context, sourceType string, remoteID, queuedID, queueAuthID, remoteHash string) (*TaskStatus, error) {
	sourceType = normalizeSourceType(sourceType)
	c.debug("finding active torbox task",
		"source_type", sourceType,
		"remote_id", remoteID,
		"has_hash", strings.TrimSpace(remoteHash) != "",
	)
	if err := c.wait(ctx, c.pollLimiter); err != nil {
		return nil, err
	}
	items, err := c.getRemoteItems(ctx, sourceType, remoteID)
	initialErr := err
	usedFullList := false
	if err != nil {
		if strings.TrimSpace(remoteID) == "" || (strings.TrimSpace(queuedID) == "" && strings.TrimSpace(queueAuthID) == "" && strings.TrimSpace(remoteHash) == "") {
			return nil, err
		}
		if err := c.wait(ctx, c.pollLimiter); err != nil {
			return nil, err
		}
		items, err = c.getRemoteItems(ctx, sourceType, "")
		usedFullList = true
		if err != nil {
			return nil, err
		}
	}
	status, err := findTaskStatus(sourceType, items, remoteID, queuedID, queueAuthID, remoteHash, false)
	if err != nil {
		if usedFullList || !IsTorboxIdentityConflict(err) || strings.TrimSpace(remoteID) == "" {
			return nil, err
		}
		initialErr = err
	}
	if status != nil {
		c.debug("matched active torbox task",
			"source_type", sourceType,
			"remote_id", status.RemoteID,
			"state", status.State,
			"label", status.Label,
			"download_ready", status.DownloadReady,
			"files", len(status.Files),
		)
		return status, nil
	}
	// An old active ID can disappear while the same task remains discoverable
	// by its queue-auth ID, queue ID, or exact hash. Retry the lookup without the
	// stale ID before declaring the active representation absent.
	if !usedFullList && strings.TrimSpace(remoteID) != "" && (strings.TrimSpace(queuedID) != "" || strings.TrimSpace(queueAuthID) != "" || strings.TrimSpace(remoteHash) != "") {
		if err := c.wait(ctx, c.pollLimiter); err != nil {
			return nil, err
		}
		items, err = c.getRemoteItems(ctx, sourceType, "")
		usedFullList = true
		if err != nil {
			return nil, err
		}
		status, err = findTaskStatus(sourceType, items, remoteID, queuedID, queueAuthID, remoteHash, false)
		if err != nil {
			return nil, err
		}
		if status != nil {
			return status, nil
		}
	}
	if initialErr != nil {
		return nil, initialErr
	}
	return nil, nil
}

func findTaskStatus(sourceType string, items []map[string]any, remoteID, queuedID, queueAuthID, remoteHash string, queued bool) (*TaskStatus, error) {
	var conflict bool
	for _, item := range items {
		if taskIdentityConflict(sourceType, item, remoteID, queuedID, queueAuthID, remoteHash, queued) {
			conflict = true
			continue
		}
		if !matchesTaskIdentity(sourceType, item, remoteID, queuedID, queueAuthID, remoteHash, queued) {
			continue
		}
		status := parseTaskStatus(sourceType, item, !queued)
		if queued && status.QueuedID == "" {
			status.QueuedID = extractQueuedID(item)
		}
		return status, nil
	}
	if conflict {
		return nil, &ErrTorboxIdentityConflict{Err: fmt.Errorf("upstream %s task identity conflicts with stored content hash", map[bool]string{true: "queued", false: "active"}[queued])}
	}
	return nil, nil
}

func (c *HTTPClient) getRemoteItems(ctx context.Context, sourceType string, remoteID string) ([]map[string]any, error) {
	sourceType = normalizeSourceType(sourceType)
	endpoint := ""
	query := url.Values{}
	query.Set("bypass_cache", "true")
	switch strings.ToLower(sourceType) {
	case "torrent":
		endpoint = "/api/torrents/mylist"
	case "nzb", "usenet":
		endpoint = "/api/usenet/mylist"
	default:
		return nil, fmt.Errorf("unknown source type %q", sourceType)
	}
	if strings.TrimSpace(remoteID) != "" {
		query.Set("id", strings.TrimSpace(remoteID))
	}

	return c.listItems(ctx, endpoint, query)
}

func (c *HTTPClient) getQueuedItems(ctx context.Context, sourceType string, queuedID string) ([]map[string]any, error) {
	sourceType = normalizeSourceType(sourceType)
	query := url.Values{}
	query.Set("type", strings.ToLower(sourceType))
	query.Set("bypass_cache", "true")
	if !strings.EqualFold(sourceType, "usenet") && strings.TrimSpace(queuedID) != "" {
		query.Set("id", strings.TrimSpace(queuedID))
	}

	return c.listItems(ctx, "/api/queued/getqueued", query)
}

func (c *HTTPClient) listItems(ctx context.Context, endpoint string, query url.Values) ([]map[string]any, error) {
	const pageSize = 1000
	var result []map[string]any
	seen := make(map[string]bool)
	for page := 0; page < 100; page++ {
		if query.Get("id") == "" {
			query.Set("offset", strconv.Itoa(page*pageSize))
			query.Set("limit", strconv.Itoa(pageSize))
		}
		if page > 0 {
			if err := c.wait(ctx, c.pollLimiter); err != nil {
				return nil, err
			}
		}
		env, err := c.do(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil, "", true)
		if err != nil {
			return nil, err
		}
		items, err := parseItemsEnvelope(env)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
		if query.Get("id") != "" || len(items) < pageSize {
			return result, nil
		}
		if seen[string(env.Data)] {
			return nil, MarkRetryable(fmt.Errorf("TorBox listing repeated a full page"))
		}
		seen[string(env.Data)] = true
	}
	return nil, MarkRetryable(fmt.Errorf("TorBox listing exceeded pagination limit"))
}

func (c *HTTPClient) DeleteTask(ctx context.Context, sourceType string, remoteID string) error {
	id, err := strconv.ParseInt(strings.TrimSpace(remoteID), 10, 64)
	if err != nil || id < 0 {
		if err == nil {
			err = fmt.Errorf("id must not be negative")
		}
		return fmt.Errorf("upstream delete: remote id %q is not a numeric TorBox task id: %w", remoteID, err)
	}
	switch strings.ToLower(sourceType) {
	case "torrent":
		if err := c.wait(ctx, c.pollLimiter); err != nil {
			return err
		}
		body := fmt.Sprintf(`{"operation":"delete","torrent_id":%d}`, id)
		_, err := c.do(ctx, http.MethodPost, "/api/torrents/controltorrent", strings.NewReader(body), "application/json", true)
		if err != nil {
			return fmt.Errorf("delete torrent: %w", err)
		}
		return nil
	case "nzb", "usenet":
		if err := c.wait(ctx, c.pollLimiter); err != nil {
			return err
		}
		body := fmt.Sprintf(`{"operation":"delete","usenet_id":%d}`, id)
		_, err := c.do(ctx, http.MethodPost, "/api/usenet/controlusenetdownload", strings.NewReader(body), "application/json", true)
		if err != nil {
			return fmt.Errorf("delete usenet: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown source type for upstream delete: %q", sourceType)
	}
}

func (c *HTTPClient) DeleteQueuedTask(ctx context.Context, sourceType string, queuedID string) error {
	id, err := strconv.ParseInt(strings.TrimSpace(queuedID), 10, 64)
	if err != nil || id < 0 {
		if err == nil {
			err = fmt.Errorf("id must not be negative")
		}
		return fmt.Errorf("upstream queued delete: queued id %q is not a numeric TorBox queue id: %w", queuedID, err)
	}
	body := fmt.Sprintf(`{"operation":"delete","queued_id":%d}`, id)
	if err := c.wait(ctx, c.pollLimiter); err != nil {
		return err
	}
	if _, err := c.do(ctx, http.MethodPost, "/api/queued/controlqueued", strings.NewReader(body), "application/json", true); err != nil {
		return fmt.Errorf("delete queued task: %w", err)
	}
	return nil
}

func normalizeSourceType(sourceType string) string {
	switch strings.ToLower(strings.TrimSpace(sourceType)) {
	case "nzb", "usenet":
		return "usenet"
	case "torrent":
		return "torrent"
	default:
		return ""
	}
}

func matchesTaskIdentity(sourceType string, item map[string]any, remoteID, queuedID, queueAuthID, remoteHash string, queued bool) bool {
	remoteID = strings.TrimSpace(remoteID)
	queuedID = strings.TrimSpace(queuedID)
	queueAuthID = strings.TrimSpace(queueAuthID)
	remoteHash = strings.TrimSpace(remoteHash)
	itemHash := strings.TrimSpace(firstString(item, "hash"))
	if remoteHash != "" && itemHash != "" && !strings.EqualFold(remoteHash, itemHash) {
		return false
	}
	itemID := extractActiveID(sourceType, item, true)
	if queued {
		itemID = extractQueuedID(item)
		if queuedID != "" && itemID == queuedID {
			return true
		}
	} else if remoteID != "" && itemID == remoteID {
		return true
	}
	if !queued && queuedID != "" && extractQueueReferenceID(item) == queuedID {
		return true
	}
	if strings.EqualFold(sourceType, "usenet") && queueAuthID != "" && extractQueueAuthID(sourceType, item) == queueAuthID {
		return true
	}
	return remoteHash != "" && itemHash != "" && strings.EqualFold(remoteHash, itemHash)
}

func taskIdentityConflict(sourceType string, item map[string]any, remoteID, queuedID, queueAuthID, remoteHash string, queued bool) bool {
	remoteHash = strings.TrimSpace(remoteHash)
	itemHash := strings.TrimSpace(firstString(item, "hash"))
	if remoteHash == "" || itemHash == "" || strings.EqualFold(remoteHash, itemHash) {
		return false
	}
	remoteID = strings.TrimSpace(remoteID)
	queuedID = strings.TrimSpace(queuedID)
	queueAuthID = strings.TrimSpace(queueAuthID)
	if queued {
		if queuedID != "" && extractQueuedID(item) == queuedID {
			return true
		}
	} else {
		if remoteID != "" && extractActiveID(sourceType, item, true) == remoteID {
			return true
		}
		if queuedID != "" && extractQueueReferenceID(item) == queuedID {
			return true
		}
	}
	return strings.EqualFold(sourceType, "usenet") && queueAuthID != "" && extractQueueAuthID(sourceType, item) == queueAuthID
}

func attachFile(writer *multipart.Writer, field, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	part, err := writer.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return fmt.Errorf("copy form file: %w", err)
	}
	return nil
}
