package worker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
)

// resolveTorrentSource resolves an http(s) download URL the way a real
// torrent client does when handed a link (e.g. a Prowlarr /download URL):
// it follows redirects, extracts a magnet: target when the indexer
// redirects to one, or downloads the .torrent payload to disk.
// Returns either a magnet URI or a local payload path.
func (o *Orchestrator) resolveTorrentSource(ctx context.Context, job *store.Job, raw string) (string, string, error) {
	client := &http.Client{
		Timeout: 90 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	cur := raw
	for range 8 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cur, nil)
		if err != nil {
			return "", "", fmt.Errorf("build request %q: %w", cur, err)
		}
		req.Header.Set("User-Agent", "qBittorrent/5.0.0")
		resp, err := client.Do(req)
		if err != nil {
			return "", "", torbox.MarkRetryable(fmt.Errorf("fetch %q: %w", cur, err))
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := strings.TrimSpace(resp.Header.Get("Location"))
			resp.Body.Close()
			if loc == "" {
				return "", "", fmt.Errorf("redirect from %q without Location", cur)
			}
			if strings.HasPrefix(strings.ToLower(loc), "magnet:") {
				return loc, "", nil
			}
			ref, err := url.Parse(loc)
			if err != nil {
				return "", "", fmt.Errorf("bad redirect location %q: %w", loc, err)
			}
			base, err := url.Parse(cur)
			if err != nil {
				return "", "", fmt.Errorf("bad url %q: %w", cur, err)
			}
			cur = base.ResolveReference(ref).String()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			sniff, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			resp.Body.Close()
			err = fmt.Errorf("fetch %q: http %d %s", cur, resp.StatusCode, strings.TrimSpace(string(sniff)))
			if resp.StatusCode >= 500 {
				return "", "", torbox.MarkRetryable(err)
			}
			return "", "", err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			return "", "", torbox.MarkRetryable(fmt.Errorf("read %q: %w", cur, err))
		}
		if !looksLikeTorrent(body) {
			return "", "", fmt.Errorf("response from %q is not a torrent file (%d bytes)", cur, len(body))
		}
		path, _, err := o.layout.SavePayload(job.ID, "source.torrent", bytes.NewReader(body))
		if err != nil {
			return "", "", fmt.Errorf("save torrent payload: %w", err)
		}
		return "", path, nil
	}
	return "", "", fmt.Errorf("too many redirects resolving %q", raw)
}

func looksLikeTorrent(body []byte) bool {
	return len(body) > 8 && body[0] == 'd' && bytes.Contains(body, []byte("4:info"))
}

func isHTTPURL(raw string) bool {
	return strings.HasPrefix(strings.ToLower(raw), "http://") || strings.HasPrefix(strings.ToLower(raw), "https://")
}
