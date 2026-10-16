package todoforai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs/config/configmap"
)

// Update must overwrite the same attachment (PUT by uri), not register a new one.
func TestUpdateOverwritesInPlace(t *testing.T) {
	var method, uri, body, ctype string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/resources":
			b, _ := io.ReadAll(r.Body)
			method, uri, body, ctype = r.Method, r.URL.Query().Get("uri"), string(b), r.Header.Get("Content-Type")
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/api/v1/resources/metadata":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "a1", "uri": "todoforai:attachment/a1", "fileSize": 3, "modifiedAt": 1000})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f, err := NewFs(context.Background(), "t", "", configmap.Simple{"url": srv.URL, "api_key": "k"})
	if err != nil {
		t.Fatal(err)
	}
	o := &Object{fs: f.(*Fs), remote: "doc.docx", uri: "todoforai:attachment/a1", id: "a1"}
	if err := o.Update(context.Background(), strings.NewReader("new"), nil); err != nil {
		t.Fatal(err)
	}
	if method != "PUT" || uri != "todoforai:attachment/a1" || body != "new" || ctype != "application/octet-stream" {
		t.Fatalf("got %s uri=%s body=%q ctype=%s", method, uri, body, ctype)
	}
	if o.id != "a1" || o.size != 3 {
		t.Fatalf("object changed identity or size: id=%s size=%d", o.id, o.size)
	}
}
