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
	return parseCreateTask(env)
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
	return parseCreateTask(env)
}

func (c *HTTPClient) GetTaskStatus(ctx context.Context, sourceType string, remoteID string) (*TaskStatus, error) {
	c.debug("fetching torbox task status", "source_type", sourceType, "remote_id", remoteID)
	return c.FindActiveTask(ctx, sourceType, remoteID, "", "")
}

func (c *HTTPClient) GetQueuedStatus(ctx context.Context, sourceType string, queuedID string) (*TaskStatus, error) {
	c.debug("fetching torbox queued status", "source_type", sourceType, "queued_id", queuedID)
	if strings.TrimSpace(queuedID) == "" {
		return nil, fmt.Errorf("queued id is required")
	}
	if err := c.wait(ctx, c.pollLimiter); err != nil {
		return nil, err
	}
	items, err := c.getQueuedItems(ctx, sourceType, queuedID)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if extractQueuedID(item) != strings.TrimSpace(queuedID) {
			continue
		}
		status := parseTaskStatus(sourceType, item)
		status.QueuedID = extractQueuedID(item)
		status.QueueAuthID = extractQueueAuthID(sourceType, item)
		c.debug("received torbox queued status",
			"source_type", sourceType,
			"queued_id", queuedID,
			"state", status.State,
			"queue_auth_id", status.QueueAuthID,
			"remote_id", status.RemoteID,
		)
		return status, nil
	}
	return nil, nil
}

func (c *HTTPClient) FindActiveTask(ctx context.Context, sourceType string, remoteID, queueAuthID, remoteHash string) (*TaskStatus, error) {
	c.debug("finding active torbox task",
		"source_type", sourceType,
		"remote_id", remoteID,
		"queue_auth_id", queueAuthID,
		"has_hash", strings.TrimSpace(remoteHash) != "",
	)
	if err := c.wait(ctx, c.pollLimiter); err != nil {
		return nil, err
	}
	items, err := c.getRemoteItems(ctx, sourceType, remoteID)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		itemID := extractActiveID(sourceType, item, true)
		itemHash := firstString(item, "hash")
		itemAuthID := firstString(item, "auth_id")

		switch {
		case strings.TrimSpace(remoteID) != "" && itemID == strings.TrimSpace(remoteID):
		case strings.EqualFold(sourceType, "usenet") && strings.TrimSpace(queueAuthID) != "" && itemAuthID == strings.TrimSpace(queueAuthID):
		case strings.TrimSpace(remoteHash) != "" && itemHash == strings.TrimSpace(remoteHash):
		default:
			continue
		}

		status := parseTaskStatus(sourceType, item)
		c.debug("matched active torbox task",
			"source_type", sourceType,
			"remote_id", status.RemoteID,
			"queue_auth_id", status.QueueAuthID,
			"state", status.State,
			"label", status.Label,
			"download_ready", status.DownloadReady,
			"files", len(status.Files),
		)
		return status, nil
	}
	return nil, nil
}

func (c *HTTPClient) getRemoteItems(ctx context.Context, sourceType string, remoteID string) ([]map[string]any, error) {
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

	env, err := c.do(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil, "", true)
	if err != nil {
		return nil, err
	}
	return parseItemsEnvelope(env)
}

func (c *HTTPClient) getQueuedItems(ctx context.Context, sourceType string, queuedID string) ([]map[string]any, error) {
	query := url.Values{}
	query.Set("type", strings.ToLower(sourceType))
	query.Set("bypass_cache", "true")
	if !strings.EqualFold(sourceType, "usenet") && strings.TrimSpace(queuedID) != "" {
		query.Set("id", strings.TrimSpace(queuedID))
	}

	env, err := c.do(ctx, http.MethodGet, "/api/queued/getqueued?"+query.Encode(), nil, "", true)
	if err != nil {
		return nil, err
	}
	return parseItemsEnvelope(env)
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
