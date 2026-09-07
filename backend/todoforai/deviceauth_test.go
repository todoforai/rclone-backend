package todoforai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fake API: token endpoint mints t1,t2,…; other paths 401 unless the key is
// the latest minted token. tokenDown makes the token endpoint fail.
type fakeAPI struct {
	mints, tokenDown int32
	mu               sync.Mutex
	seen             []string
}

func (f *fakeAPI) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/cli/device/token" {
		if atomic.LoadInt32(&f.tokenDown) != 0 {
			w.WriteHeader(503)
			return
		}
		n := atomic.AddInt32(&f.mints, 1)
		json.NewEncoder(w).Encode(map[string]any{"token": fmt.Sprintf("t%d", n), "expiresIn": 100})
		return
	}
	key := r.Header.Get("X-API-Key")
	f.mu.Lock()
	f.seen = append(f.seen, key)
	f.mu.Unlock()
	if key != fmt.Sprintf("t%d", atomic.LoadInt32(&f.mints)) {
		w.WriteHeader(401)
		return
	}
	w.Write([]byte("ok"))
}

func newDeviceAuthTest(t *testing.T) (*fakeAPI, *http.Client, *deviceAuth) {
	api := &fakeAPI{}
	srv := httptest.NewServer(http.HandlerFunc(api.handler))
	t.Cleanup(srv.Close)
	d := &deviceAuth{base: http.DefaultTransport, apiURL: srv.URL, id: "d", secret: "s"}
	return api, &http.Client{Transport: d}, d
}

func mustOK(t *testing.T, c *http.Client, url string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("want 200, got %v %v", resp, err)
	}
	resp.Body.Close()
}

func TestDeviceAuthMintsAndCaches(t *testing.T) {
	api, c, d := newDeviceAuthTest(t)
	mustOK(t, c, d.apiURL+"/x")
	mustOK(t, c, d.apiURL+"/x")
	if api.mints != 1 {
		t.Fatalf("mints=%d", api.mints)
	}
	d.renewAt = time.Now().Add(-time.Second)
	mustOK(t, c, d.apiURL+"/x")
	if api.mints != 2 {
		t.Fatalf("expired token not renewed: %d", api.mints)
	}
}

func TestDeviceAuthRemintsOn401(t *testing.T) {
	api, c, d := newDeviceAuthTest(t)
	mustOK(t, c, d.apiURL+"/x")
	atomic.AddInt32(&api.mints, 1) // server-side rotation: t1 now invalid, t2 expected
	mustOK(t, c, d.apiURL+"/x")
	if api.mints != 3 || api.seen[1] != "t1" || api.seen[2] != "t3" {
		t.Fatalf("mints=%d seen=%v", api.mints, api.seen)
	}
}

func TestDeviceAuthConcurrent401sCoalesce(t *testing.T) {
	api, c, d := newDeviceAuthTest(t)
	mustOK(t, c, d.apiURL+"/x")
	atomic.AddInt32(&api.mints, 1)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); mustOK(t, c, d.apiURL+"/x") }()
	}
	wg.Wait()
	if api.mints != 3 {
		t.Fatalf("expected a single re-mint for concurrent 401s, mints=%d", api.mints)
	}
}

func TestDeviceAuthKeepsValidTokenWhenRenewalFails(t *testing.T) {
	api, c, d := newDeviceAuthTest(t)
	mustOK(t, c, d.apiURL+"/x")
	atomic.StoreInt32(&api.tokenDown, 1)
	d.renewAt = time.Now().Add(-time.Second) // due for renewal, still valid
	mustOK(t, c, d.apiURL+"/x")
	d.expireAt = time.Now().Add(-time.Second) // now truly expired
	if _, err := d.current(context.Background(), ""); err == nil {
		t.Fatal("expected error once token expired and renewal fails")
	}
}
