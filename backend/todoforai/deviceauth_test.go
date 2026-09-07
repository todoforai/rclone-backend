package todoforai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeviceAuthRemintsOn401(t *testing.T) {
	mints, seen := 0, []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/cli/device/token" {
			mints++
			json.NewEncoder(w).Encode(map[string]any{"token": "t" + string(rune('0'+mints)), "expiresIn": 100})
			return
		}
		seen = append(seen, r.Header.Get("X-API-Key"))
		if r.Header.Get("X-API-Key") == "t1" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := &http.Client{Transport: &deviceAuth{base: http.DefaultTransport, apiURL: srv.URL, id: "d", secret: "s"}}
	resp, err := c.Get(srv.URL + "/x")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("resp %v %v", resp, err)
	}
	if mints != 2 || len(seen) != 2 || seen[0] != "t1" || seen[1] != "t2" {
		t.Fatalf("mints=%d seen=%v", mints, seen)
	}
	// cached token reused, renewAt honoured
	c.Get(srv.URL + "/x")
	if mints != 2 {
		t.Fatalf("unexpected re-mint")
	}
	d := c.Transport.(*deviceAuth)
	d.renewAt = time.Now().Add(-time.Second)
	if _, err := d.current(context.Background(), false); err != nil || mints != 3 {
		t.Fatalf("expired token not renewed: %d %v", mints, err)
	}
}
