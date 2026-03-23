package todoforai

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/lib/encoder"
)

// defaultEnc matches the encoder configured in init().
var defaultEnc = encoder.Standard | encoder.EncodeInvalidUtf8

func TestUri(t *testing.T) {
	f := &Fs{root: "", opt: Options{Enc: defaultEnc}}
	for _, tt := range []struct{ remote, want string }{
		{"", "todoforai:"},
		{"docs/report.pdf", "todoforai:docs/report.pdf"},
		{"todos/abc/att", "todoforai:todos/abc/att"},
	} {
		if got := f.uri(tt.remote); got != tt.want {
			t.Errorf("uri(%q) = %q, want %q", tt.remote, got, tt.want)
		}
	}

	f2 := &Fs{root: "project", opt: Options{Enc: defaultEnc}}
	for _, tt := range []struct{ remote, want string }{
		{"", "todoforai:project"},
		{"file.txt", "todoforai:project/file.txt"},
	} {
		if got := f2.uri(tt.remote); got != tt.want {
			t.Errorf("uri(%q) with root = %q, want %q", tt.remote, got, tt.want)
		}
	}
}

func TestIsTodo(t *testing.T) {
	for _, tt := range []struct {
		p    string
		want bool
	}{
		{"todos", true}, {"todos/abc", true}, {"todos/a/b", true},
		{"docs", false}, {"docs/todos", false}, {"todosfoo", false},
	} {
		if got := isTodo(tt.p); got != tt.want {
			t.Errorf("isTodo(%q) = %v, want %v", tt.p, got, tt.want)
		}
	}
}

func TestMsToTime(t *testing.T) {
	ms := int64(1700000000000)
	if got := msToTime(&ms); got != time.UnixMilli(ms) {
		t.Errorf("got %v", got)
	}
	if got := msToTime(nil, &ms); got != time.UnixMilli(ms) {
		t.Errorf("fallback got %v", got)
	}
	if got := msToTime(nil); !got.IsZero() {
		t.Errorf("nil got %v", got)
	}
}

func TestGuessMime(t *testing.T) {
	for _, tt := range []struct{ name, want string }{
		{"a.pdf", "application/pdf"}, {"a.png", "image/png"}, {"x", "application/octet-stream"},
	} {
		if got := guessMime(tt.name); got != tt.want {
			t.Errorf("guessMime(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// ---- integration (needs TODOFORAI_API_KEY) ----

func newTestFs(t *testing.T) *Fs {
	key := os.Getenv("TODOFORAI_API_KEY")
	if key == "" {
		t.Skip("TODOFORAI_API_KEY not set")
	}
	apiURL := os.Getenv("TODOFORAI_URL")
	if apiURL == "" {
		apiURL = defaultURL
	}
	m := configmap.Simple{
		"url":     apiURL,
		"api_key": key,
	}
	fsInterface, err := NewFs(context.Background(), "TestTodoforai", "", m)
	if err != nil {
		t.Fatal(err)
	}
	return fsInterface.(*Fs)
}

func TestIntegrationUploadAndDelete(t *testing.T) {
	f := newTestFs(t)
	ctx := context.Background()

	obj, err := f.upload(ctx, "rclone-test.txt", strings.NewReader("hello"), 5)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("uploaded: %s", obj.uri)

	rc, err := obj.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()

	if err := obj.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	t.Log("deleted")
}

// NOTE: When submitting upstream to rclone/rclone, add the standard
// integration test suite reference:
//   var _ = fstests.TestFs
// This requires building inside the rclone monorepo.
