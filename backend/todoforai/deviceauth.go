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
// TTL and, as a safety net, once more on a 401.
type deviceAuth struct {
	base               http.RoundTripper
	apiURL, id, secret string

	mu      sync.Mutex
	token   string
	renewAt time.Time
}

func (d *deviceAuth) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := d.current(req.Context(), false)
	if err != nil {
		return nil, err
	}
	resp, err := d.do(req, tok)
	replayable := req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
	if err != nil || resp.StatusCode != http.StatusUnauthorized || !replayable {
		return resp, err
	}
	resp.Body.Close()
	if tok, err = d.current(req.Context(), true); err != nil {
		return nil, err
	}
	if req.GetBody != nil {
		if req.Body, err = req.GetBody(); err != nil {
			return nil, err
		}
	}
	return d.do(req, tok)
}

func (d *deviceAuth) do(req *http.Request, tok string) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("X-API-Key", tok)
	return d.base.RoundTrip(r)
}

func (d *deviceAuth) current(ctx context.Context, force bool) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !force && d.token != "" && time.Now().Before(d.renewAt) {
		return d.token, nil
	}
	body, _ := json.Marshal(map[string]string{"deviceId": d.id, "secret": d.secret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.apiURL+"/api/v1/cli/device/token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.base.RoundTrip(req)
	if err != nil {
		return "", fmt.Errorf("todoforai: device token exchange: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expiresIn"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || out.Token == "" {
		return "", fmt.Errorf("todoforai: device token exchange failed: HTTP %d", resp.StatusCode)
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	d.token, d.renewAt = out.Token, time.Now().Add(ttl/2)
	fs.Debugf("todoforai", "device token minted, renew in %v", ttl/2)
	return d.token, nil
}
