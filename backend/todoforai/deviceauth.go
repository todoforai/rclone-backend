package todoforai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rclone/rclone/fs"
)

// deviceAuth is an http.RoundTripper that authenticates with a short-lived
// device-session token (dst_…) minted from a durable deviceId+deviceSecret.
// Long-running mounts outlive any single token, so it re-mints at half the
// TTL (keeping the old one while it is still valid if the mint fails) and,
// as a safety net, once more on a 401.
type deviceAuth struct {
	base               http.RoundTripper
	apiURL, id, secret string

	mu       sync.Mutex
	token    string
	renewAt  time.Time
	expireAt time.Time
}

func (d *deviceAuth) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := d.current(req.Context(), "")
	if err != nil {
		return nil, err
	}
	resp, err := d.do(req, tok)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	// Token rejected: re-mint (unless another request already did) and replay
	// once. Streaming bodies can't be replayed — hand the 401 back, the next
	// request will use the fresh token.
	fresh, err := d.current(req.Context(), tok)
	if err != nil || fresh == tok || !(req.Body == nil || req.Body == http.NoBody || req.GetBody != nil) {
		return resp, err
	}
	resp.Body.Close()
	retry := req.Clone(req.Context())
	if req.GetBody != nil {
		if retry.Body, err = req.GetBody(); err != nil {
			return nil, err
		}
	}
	return d.do(retry, fresh)
}

func (d *deviceAuth) do(req *http.Request, tok string) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("X-API-Key", tok)
	return d.base.RoundTrip(r)
}

// current returns a usable token, minting a new one when due. rejected, if
// non-empty, is a token a request just got a 401 with: it forces a re-mint
// only if that token is still the current one (coalesces concurrent 401s).
func (d *deviceAuth) current(ctx context.Context, rejected string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if d.token != "" && d.token != rejected && now.Before(d.renewAt) {
		return d.token, nil
	}
	tok, ttl, err := d.mint(ctx)
	if err != nil {
		if d.token != "" && d.token != rejected && now.Before(d.expireAt) {
			fs.Debugf("todoforai", "device token renewal failed, keeping current: %v", err)
			return d.token, nil
		}
		return "", err
	}
	d.token, d.renewAt, d.expireAt = tok, now.Add(ttl/2), now.Add(ttl)
	fs.Debugf("todoforai", "device token minted, renew in %v", ttl/2)
	return d.token, nil
}

func (d *deviceAuth) mint(ctx context.Context) (string, time.Duration, error) {
	body, _ := json.Marshal(map[string]string{"deviceId": d.id, "secret": d.secret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.apiURL+"/api/v1/cli/device/token", bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.base.RoundTrip(req)
	if err != nil {
		return "", 0, fmt.Errorf("todoforai: device token exchange: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expiresIn"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || out.Token == "" {
		return "", 0, fmt.Errorf("todoforai: device token exchange failed: HTTP %d", resp.StatusCode)
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return out.Token, ttl, nil
}
