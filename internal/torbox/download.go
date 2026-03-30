package torbox

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

func (c *HTTPClient) GetDownloadLinks(ctx context.Context, sourceType string, remoteID string) ([]DownloadAsset, error) {
	c.debug("resolving torbox download links", "source_type", sourceType, "remote_id", remoteID)
	if err := RequireRemoteID(remoteID); err != nil {
		return nil, err
	}
	if err := c.wait(ctx, c.pollLimiter); err != nil {
		return nil, err
	}
	items, err := c.getRemoteItems(ctx, sourceType, remoteID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("empty item response")
	}
	item := items[0]
	files := extractRemoteFiles(item)
	if len(files) == 0 {
		if err := c.wait(ctx, c.dlLimiter); err != nil {
			return nil, err
		}
		link, err := c.requestDownloadLink(ctx, sourceType, remoteID, "", true)
		if err != nil {
			return nil, err
		}
		assets := []DownloadAsset{{
			URL:          link,
			RelativePath: "payload.zip",
		}}
		c.debug("resolved fallback zip download link", "source_type", sourceType, "remote_id", remoteID, "asset_count", len(assets))
		return assets, nil
	}

	assets := make([]DownloadAsset, 0, len(files))
	for _, file := range files {
		if err := c.wait(ctx, c.dlLimiter); err != nil {
			return nil, err
		}
		link, err := c.requestDownloadLink(ctx, sourceType, remoteID, file.FileID, false)
		if err != nil {
			return nil, err
		}
		relativePath := file.RelativePath
		if relativePath == "" {
			if file.ShortName != "" {
				relativePath = file.ShortName
			} else {
				relativePath = file.Name
			}
		}
		assets = append(assets, DownloadAsset{
			FileID:       file.FileID,
			URL:          link,
			RelativePath: filepath.Clean(relativePath),
			Size:         file.Size,
		})
	}
	c.debug("resolved torbox download links", "source_type", sourceType, "remote_id", remoteID, "asset_count", len(assets))
	return assets, nil
}

func (c *HTTPClient) requestDownloadLink(ctx context.Context, sourceType string, remoteID string, fileID string, zipLink bool) (string, error) {
	values := url.Values{}
	// The TorBox requestdl endpoint requires the API token as a query parameter
	// to generate permalink/CDN download URLs; the Authorization header (sent
	// separately by c.do) is not a substitute. The token is redacted in local
	// logs by sanitizeLoggedPath, but may appear in TorBox-side server or CDN
	// logs — accepted risk since the API mandates this.
	values.Set("token", c.apiToken)
	values.Set("redirect", "false")
	if zipLink {
		values.Set("zip_link", "true")
	}
	if fileID != "" {
		values.Set("file_id", fileID)
	}
	switch strings.ToLower(sourceType) {
	case "torrent":
		values.Set("torrent_id", remoteID)
		env, err := c.do(ctx, http.MethodGet, "/api/torrents/requestdl?"+values.Encode(), nil, "", true)
		if err != nil {
			return "", err
		}
		return parseLinkEnvelope(env)
	case "nzb", "usenet":
		values.Set("usenet_id", remoteID)
		env, err := c.do(ctx, http.MethodGet, "/api/usenet/requestdl?"+values.Encode(), nil, "", true)
		if err != nil {
			return "", err
		}
		return parseLinkEnvelope(env)
	default:
		return "", fmt.Errorf("unknown source type %q", sourceType)
	}
}
