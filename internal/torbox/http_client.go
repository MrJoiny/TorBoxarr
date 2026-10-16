package torbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
		return nil, &RequestNotSentError{Err: fmt.Errorf("new request: %w", err)}
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
		// Once Do is called, delivery is uncertain even when the returned error
		// is not a net.Error (for example context cancellation after headers).
		return nil, MarkRetryable(fmt.Errorf("torbox request failed: %w", err))
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, MarkRetryable(fmt.Errorf("read response: %w", err))
	}
	c.debug("torbox http response",
		"method", method,
		"path", sanitizeLoggedPath(path),
		"status", resp.StatusCode,
		"duration", time.Since(started).String(),
		"response_bytes", len(raw),
	)

	if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		// TorBox frequently returns 500 with a structured error body
		// (e.g. {"success":false,"error":"DATABASE_ERROR",...}) for a task that
		// does not exist or is otherwise unrecoverable. Surface that as a
		// logical error so callers can distinguish it from a transport-level
		// failure, but it is still subject to retry/escalation caps.
		var env apiEnvelope
		if (resp.StatusCode == http.StatusNotFound || resp.StatusCode >= 500) && len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, &env) == nil && !env.Success {
			err := classifyEnvelopeError(&env, resp.StatusCode)
			if IsTorboxLogical(err) {
				return nil, MarkRetryable(err)
			}
			return nil, err
		}
		return nil, MarkRetryable(&HTTPStatusError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("torbox status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))),
		})
	}
	if resp.StatusCode >= 400 {
		return nil, &HTTPStatusError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("torbox status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))),
		}
	}

	var env apiEnvelope
	if len(bytes.TrimSpace(raw)) == 0 {
		return &env, nil
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, MarkRetryable(fmt.Errorf("decode response envelope: %w", err))
	}
	if !env.Success {
		return nil, classifyEnvelopeError(&env, resp.StatusCode)
	}
	return &env, nil
}

func classifyEnvelopeError(env *apiEnvelope, status int) error {
	code := strings.ToUpper(strings.TrimSpace(stringify(env.Error)))
	message := "torbox api returned success=false"
	if code != "" || env.Detail != "" {
		message = fmt.Sprintf("torbox api error: %s (%s)", code, env.Detail)
	}
	if status == StatusTorboxOriginDown {
		return MarkRetryable(&HTTPStatusError{StatusCode: status, Message: fmt.Sprintf("torbox status %d: %s", status, message)})
	}
	switch code {
	case "ITEM_NOT_FOUND":
		return &ErrTorboxAbsent{Err: errors.New(message)}
	case "BAD_TOKEN":
		return &HTTPStatusError{StatusCode: http.StatusUnauthorized, Message: message}
	case "INVALID_OPTION", "MISSING_REQUIRED_OPTION":
		return &HTTPStatusError{StatusCode: http.StatusBadRequest, Message: message}
	default:
		return &ErrTorboxLogical{Err: fmt.Errorf("torbox status %d: %s", status, message)}
	}
}

func (c *HTTPClient) wait(ctx context.Context, limiter Waiter) error {
	if limiter == nil {
		return nil
	}
	if err := limiter.Wait(ctx); err != nil {
		return &RequestNotSentError{Err: fmt.Errorf("rate limiter wait: %w", err)}
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
