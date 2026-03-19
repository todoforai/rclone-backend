// Package todoforai provides an rclone backend for todofor.ai.
//
// Path mapping: todoforai:path → todoforai://path. That's it.
// "todos/" is reserved for TODO attachments.
package todoforai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/oauthutil"
	"golang.org/x/oauth2"
)

const defaultURL = "https://api.todofor.ai"

func makeOAuthConfig(apiURL string) *oauth2.Config {
	return &oauth2.Config{
		Endpoint: oauth2.Endpoint{
			AuthURL:  apiURL + "/oauth/authorize",
			TokenURL: apiURL + "/oauth/token",
		},
		RedirectURL: oauthutil.RedirectURL,
	}
}

func init() {
	opts := []fs.Option{
		{Name: "url", Help: "API server URL.", Default: defaultURL, Advanced: true},
		{Name: "api_key", Help: "API key (alternative to OAuth browser login).\n\nGet yours at https://todofor.ai/settings/api-keys\nLeave blank to use OAuth instead.", Sensitive: true},
	}
	opts = append(oauthutil.SharedOptions, opts...)

	fs.Register(&fs.RegInfo{
		Name:        "todoforai",
		Description: "todofor.ai Cloud Workspace",
		NewFs:       NewFs,
		Config: func(ctx context.Context, name string, m configmap.Mapper, cfgIn fs.ConfigIn) (*fs.ConfigOut, error) {
			if key, _ := m.Get("api_key"); key != "" {
				return nil, nil // API key provided, skip OAuth
			}
			url, _ := m.Get("url")
			if url == "" {
				url = defaultURL
			}
			return oauthutil.ConfigOut("", &oauthutil.Options{
				OAuth2Config: makeOAuthConfig(url),
				NoOffline:    true,
			})
		},
		Options: opts,
	})
}

// ---- types ----

type Options struct {
	URL    string `config:"url"`
	APIKey string `config:"api_key"`
}

type apiItem struct {
	ID, URI, OriginalName, MimeType string
	FileSize                        int64
	CreatedAt, ModifiedAt           *int64
}

type listResult struct {
	Items         []apiItem `json:"items"`
	NextPageToken string    `json:"nextPageToken,omitempty"`
}

type uploadResult struct {
	AttachmentID string `json:"attachmentId"`
	URI          string `json:"uri"`
	FileSize     int64  `json:"fileSize"`
	CreatedAt    int64  `json:"createdAt"`
}

// ---- Fs ----

type Fs struct {
	name, root string
	opt        Options
	http       *http.Client
	features   *fs.Features
	useOAuth   bool // true = http.Client handles auth via Bearer; false = X-API-Key header
}

type Object struct {
	fs                  *Fs
	remote              string
	size                int64
	modTime             time.Time
	mimeType, uri, id   string
}

func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	var opt Options
	if err := configstruct.Set(m, &opt); err != nil {
		return nil, err
	}
	if opt.URL == "" {
		opt.URL = defaultURL
	}

	f := &Fs{name: name, root: strings.Trim(root, "/"), opt: opt}

	if opt.APIKey != "" {
		f.http = &http.Client{Timeout: 5 * time.Minute}
	} else {
		oauthClient, _, err := oauthutil.NewClient(ctx, name, m, makeOAuthConfig(opt.URL))
		if err != nil {
			return nil, fmt.Errorf("todoforai: auth failed: %w\nHint: provide api_key or run 'rclone config reconnect %s:'", err, name)
		}
		f.http = oauthClient
		f.useOAuth = true
	}
	f.features = (&fs.Features{ReadMimeType: true, WriteMimeType: true, CanHaveEmptyDirectories: true}).Fill(ctx, f)

	if root != "" {
		if item, err := f.metadata(ctx, f.uri("")); err == nil && item.MimeType != "application/vnd.todoforai.folder" {
			f.root = path.Dir(f.root)
			return f, fs.ErrorIsFile
		}
	}
	return f, nil
}

func (f *Fs) Name() string             { return f.name }
func (f *Fs) Root() string             { return f.root }
func (f *Fs) String() string           { return "todoforai:" + f.root }
func (f *Fs) Precision() time.Duration { return time.Millisecond }
func (f *Fs) Hashes() hash.Set         { return hash.Set(hash.None) }
func (f *Fs) Features() *fs.Features   { return f.features }

func (f *Fs) uri(remote string) string {
	p := f.root
	if remote != "" {
		if p != "" {
			p += "/"
		}
		p += remote
	}
	return "todoforai://" + p
}

// ---- API helpers ----

func (f *Fs) api(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(f.opt.URL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if !f.useOAuth {
		req.Header.Set("X-API-Key", f.opt.APIKey)
	}
	// OAuth client sets Authorization: Bearer automatically via its Transport
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return f.http.Do(req)
}

func (f *Fs) apiJSON(ctx context.Context, method, path string, body io.Reader, out interface{}) error {
	resp, err := f.api(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, b)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (f *Fs) metadata(ctx context.Context, uri string) (*apiItem, error) {
	var item apiItem
	err := f.apiJSON(ctx, "GET", "/api/v1/resources/metadata?uri="+url.QueryEscape(uri), nil, &item)
	return &item, err
}

func isNotFound(err error) bool {
	s := err.Error()
	return strings.Contains(s, "404") || strings.Contains(s, "NOT_FOUND")
}

// ---- fs.Fs methods ----

func (f *Fs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	uri := f.uri(dir)
	var entries fs.DirEntries
	token := ""
	for {
		var res listResult
		q := "/api/v1/resources/list?uri=" + url.QueryEscape(uri) + "&pageSize=200"
		if token != "" {
			q += "&pageToken=" + url.QueryEscape(token)
		}
		if err := f.apiJSON(ctx, "GET", q, nil, &res); err != nil {
			if isNotFound(err) {
				return nil, fs.ErrorDirNotFound
			}
			return nil, err
		}
		for _, item := range res.Items {
			name := item.OriginalName
			if name == "" {
				parts := strings.Split(item.URI, "/")
				name = parts[len(parts)-1]
			}
			remote := name
			if dir != "" {
				remote = dir + "/" + name
			}
			mt := msToTime(item.ModifiedAt, item.CreatedAt)
			if item.MimeType == "application/vnd.todoforai.folder" {
				entries = append(entries, fs.NewDir(remote, mt))
			} else {
				entries = append(entries, &Object{fs: f, remote: remote, size: item.FileSize, modTime: mt, mimeType: item.MimeType, uri: item.URI, id: item.ID})
			}
		}
		if res.NextPageToken == "" {
			break
		}
		token = res.NextPageToken
	}
	return entries, nil
}

func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	item, err := f.metadata(ctx, f.uri(remote))
	if err != nil {
		if isNotFound(err) {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	if item.MimeType == "application/vnd.todoforai.folder" {
		return nil, fs.ErrorIsDir
	}
	return &Object{fs: f, remote: remote, size: item.FileSize, modTime: msToTime(item.ModifiedAt, item.CreatedAt), mimeType: item.MimeType, uri: item.URI, id: item.ID}, nil
}

func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	return f.upload(ctx, src.Remote(), in, src.Size())
}

func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	full := f.root
	if dir != "" {
		if full != "" {
			full += "/"
		}
		full += dir
	}
	if full == "" || isTodo(full) {
		return nil
	}
	parent, name := "todoforai://", full
	if i := strings.LastIndex(full, "/"); i >= 0 {
		parent = "todoforai://" + full[:i]
		name = full[i+1:]
	}
	b, _ := json.Marshal(map[string]string{"parentUri": parent, "name": name})
	return f.apiJSON(ctx, "POST", "/api/v1/resources/mkdir", bytes.NewReader(b), nil)
}

func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	uri := f.uri(dir)
	if uri == "todoforai://" {
		return fmt.Errorf("cannot remove root")
	}
	full := f.root
	if dir != "" {
		if full != "" {
			full += "/"
		}
		full += dir
	}
	if isTodo(full) {
		return fmt.Errorf("cannot remove todo directories")
	}
	return f.apiJSON(ctx, "DELETE", "/api/v1/resources?uri="+url.QueryEscape(uri), nil, nil)
}

// ---- upload (shared by Put and Update) ----

func (f *Fs) upload(ctx context.Context, remote string, in io.Reader, size int64) (*Object, error) {
	full := f.root
	if remote != "" {
		if full != "" {
			full += "/"
		}
		full += remote
	}
	name := path.Base(full)
	mt := guessMime(name)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if isTodo(full) {
		parts := strings.SplitN(full, "/", 3) // todos/<id>/...
		if len(parts) >= 2 {
			w.WriteField("todoId", parts[1])
		}
	} else if dir := path.Dir(full); dir != "." && dir != "" {
		w.WriteField("folderPath", dir)
	}
	part, _ := w.CreateFormFile("file", name)
	io.Copy(part, in)
	w.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(f.opt.URL, "/")+"/api/v1/resources/register", &buf)
	if err != nil {
		return nil, err
	}
	if !f.useOAuth {
		req.Header.Set("X-API-Key", f.opt.APIKey)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := f.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upload: HTTP %d: %s", resp.StatusCode, b)
	}

	var res uploadResult
	json.NewDecoder(resp.Body).Decode(&res)
	return &Object{fs: f, remote: remote, size: res.FileSize, modTime: time.UnixMilli(res.CreatedAt), mimeType: mt, uri: res.URI, id: res.AttachmentID}, nil
}

// ---- fs.Object ----

func (o *Object) Fs() fs.Info                           { return o.fs }
func (o *Object) Remote() string                        { return o.remote }
func (o *Object) Size() int64                           { return o.size }
func (o *Object) ModTime(ctx context.Context) time.Time { return o.modTime }
func (o *Object) Storable() bool                        { return true }
func (o *Object) String() string                        { return o.remote }
func (o *Object) MimeType(ctx context.Context) string   { return o.mimeType }
func (o *Object) SetModTime(context.Context, time.Time) error { return fs.ErrorCantSetModTime }
func (o *Object) Hash(context.Context, hash.Type) (string, error) { return "", hash.ErrUnsupported }

func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	resp, err := o.fs.api(ctx, "GET", "/api/v1/resources?uri="+url.QueryEscape(o.uri), nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("fetch: HTTP %d: %s", resp.StatusCode, b)
	}
	return resp.Body, nil
}

func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	obj, err := o.fs.upload(ctx, o.remote, in, src.Size())
	if err != nil {
		return err
	}
	o.size, o.modTime, o.uri, o.id = obj.size, obj.modTime, obj.uri, obj.id
	return nil
}

func (o *Object) Remove(ctx context.Context) error {
	return o.fs.apiJSON(ctx, "DELETE", "/api/v1/resources?uri="+url.QueryEscape(o.uri), nil, nil)
}

// ---- tiny helpers ----

func isTodo(p string) bool                 { return strings.HasPrefix(p, "todos/") || p == "todos" }
func guessMime(name string) string {
	if t := mime.TypeByExtension(path.Ext(name)); t != "" { return t }
	return "application/octet-stream"
}
func msToTime(ms ...*int64) time.Time {
	for _, p := range ms {
		if p != nil { return time.UnixMilli(*p) }
	}
	return time.Time{}
}

var (
	_ fs.Fs        = (*Fs)(nil)
	_ fs.Object    = (*Object)(nil)
	_ fs.MimeTyper = (*Object)(nil)
)
