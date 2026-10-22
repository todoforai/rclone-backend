package todoforai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/object"
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

// Put onto an existing path overwrites it in place; onto a free path it registers a new file.
func TestPutUpsertsByPath(t *testing.T) {
	var calls []string
	exists := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/resources/metadata":
			if !exists {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "a1", "uri": "todoforai:attachment/a1", "fileSize": 3, "modifiedAt": 1000})
		case "/api/v1/resources":
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/api/v1/resources/register":
			_ = json.NewEncoder(w).Encode(map[string]any{"attachmentId": "a2", "uri": "todoforai:attachment/a2", "fileSize": 3})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	f, err := NewFs(context.Background(), "t", "", configmap.Simple{"url": srv.URL, "api_key": "k"})
	if err != nil {
		t.Fatal(err)
	}
	put := func() fs.Object {
		calls = nil
		o, err := f.Put(context.Background(), strings.NewReader("new"), object.NewStaticObjectInfo("todos/t1/a.md", time.Now(), 3, true, nil, nil))
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	if o := put(); o.(*Object).id != "a1" || !slices.Contains(calls, "PUT /api/v1/resources") || slices.Contains(calls, "POST /api/v1/resources/register") {
		t.Fatalf("existing path: id=%s calls=%v", o.(*Object).id, calls)
	}
	exists = false
	if o := put(); o.(*Object).id != "a2" || !slices.Contains(calls, "POST /api/v1/resources/register") {
		t.Fatalf("free path: id=%s calls=%v", o.(*Object).id, calls)
	}
}
