package torbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type HTTPClient struct {
	log           *slog.Logger
	baseURL       string
	apiToken      string
	userAgent     string
	httpClient    *http.Client
	createLimiter Waiter
	pollLimiter   Waiter
	dlLimiter     Waiter
}

type apiEnvelope struct {
	Success bool            `json:"success"`
	Error   any             `json:"error"`
	Detail  string          `json:"detail"`
	Data    json.RawMessage `json:"data"`
}

func NewHTTPClient(log *slog.Logger, baseURL, apiToken, userAgent string, timeout time.Duration, createLimiter, pollLimiter, dlLimiter Waiter) *HTTPClient {
	return &HTTPClient{
		log:       log,
		baseURL:   strings.TrimRight(baseURL, "/"),
		apiToken:  apiToken,
		userAgent: userAgent,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		createLimiter: createLimiter,
		pollLimiter:   pollLimiter,
		dlLimiter:     dlLimiter,
	}
}

func (c *HTTPClient) do(ctx context.Context, method, path string, body io.Reader, contentType string, useBearer bool) (*apiEnvelope, error) {
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if useBearer {
		req.Header.Set("Authorization", "Bearer "+c.apiToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.warn("torbox http request failed", "method", method, "path", sanitizeLoggedPath(path), "duration", time.Since(started).String(), "error", err.Error())
		if isNetRetryable(err) {
			return nil, MarkRetryable(fmt.Errorf("torbox request failed: %w", err))
		}
		return nil, fmt.Errorf("torbox request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	c.debug("torbox http response",
		"method", method,
		"path", sanitizeLoggedPath(path),
		"status", resp.StatusCode,
		"duration", time.Since(started).String(),
		"response_bytes", len(raw),
	)

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, MarkRetryable(fmt.Errorf("torbox status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))))
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("torbox status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var env apiEnvelope
	if len(bytes.TrimSpace(raw)) == 0 {
		return &env, nil
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode response envelope: %w", err)
	}
	if !env.Success && env.Error != nil {
		return nil, fmt.Errorf("torbox api error: %v (%s)", env.Error, env.Detail)
	}
	return &env, nil
}

func (c *HTTPClient) wait(ctx context.Context, limiter Waiter) error {
	if limiter == nil {
		return nil
	}
	if err := limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter wait: %w", err)
	}
	return nil
}

func (c *HTTPClient) debug(msg string, args ...any) {
	if c.log != nil {
		c.log.Debug(msg, args...)
	}
}

func (c *HTTPClient) warn(msg string, args ...any) {
	if c.log != nil {
		c.log.Warn(msg, args...)
	}
}

func sanitizeLoggedPath(rawPath string) string {
	parsed, err := url.Parse(rawPath)
	if err != nil {
		return rawPath
	}
	query := parsed.Query()
	if _, ok := query["token"]; ok {
		query.Set("token", "[redacted]")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func isNetRetryable(err error) bool {
	if err == nil {
		return false
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "timeout")
}
