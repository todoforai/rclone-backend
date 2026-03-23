// Package todoforai provides an rclone backend for todofor.ai.
//
// Path mapping: todoforai:path → todoforai:path (rclone-style, no authority).
// "todos/" is reserved for TODO attachments.
package todoforai

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/todoforai/rclone-backend/backend/todoforai/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/fserrors"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/rclone/rclone/lib/oauthutil"
	"github.com/rclone/rclone/lib/pacer"
	"github.com/rclone/rclone/lib/rest"
	"golang.org/x/oauth2"
)

const (
	defaultURL    = "https://api.todofor.ai"
	minSleep      = 10 * time.Millisecond
	maxSleep      = 2 * time.Second
	decayConstant = 2 // bigger = slower decay toward minSleep
)

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
		{
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			Default:  encoder.Standard | encoder.EncodeInvalidUtf8 | encoder.EncodeColon,
		},
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
			apiURL, _ := m.Get("url")
			if apiURL == "" {
				apiURL = defaultURL
			}
			return oauthutil.ConfigOut("", &oauthutil.Options{
				OAuth2Config: makeOAuthConfig(apiURL),
				NoOffline:    true,
			})
		},
		Options: opts,
	})
}

// Options defines the configuration for the todoforai backend.
type Options struct {
	URL    string               `config:"url"`
	APIKey string               `config:"api_key"`
	Enc    encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote todofor.ai workspace.
type Fs struct {
	name, root string
	opt        Options
	features   *fs.Features
	srv        *rest.Client // API client (handles auth, JSON, errors)
	pacer      *fs.Pacer    // rate limiter + retry
}

// Object represents a file in the workspace.
type Object struct {
	fs                *Fs
	remote            string
	size              int64
	modTime           time.Time
	mimeType, uri, id string
}

// NewFs creates a new todofor.ai backend.
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	var opt Options
	if err := configstruct.Set(m, &opt); err != nil {
		return nil, err
	}
	if opt.URL == "" {
		opt.URL = defaultURL
	}

	f := &Fs{
		name:  name,
		root:  strings.Trim(root, "/"),
		opt:   opt,
		pacer: fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(minSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
	}

	var httpClient *http.Client
	if opt.APIKey != "" {
		httpClient = fshttp.NewClient(ctx)
	} else {
		oauthClient, _, err := oauthutil.NewClient(ctx, name, m, makeOAuthConfig(opt.URL))
		if err != nil {
			return nil, fmt.Errorf("todoforai: auth failed: %w\nHint: provide api_key or run 'rclone config reconnect %s:'", err, name)
		}
		httpClient = oauthClient
	}

	f.srv = rest.NewClient(httpClient).SetRoot(strings.TrimRight(opt.URL, "/"))
	if opt.APIKey != "" {
		f.srv.SetHeader("X-API-Key", opt.APIKey)
	}

	f.features = (&fs.Features{ReadMimeType: true, WriteMimeType: true, CanHaveEmptyDirectories: true}).Fill(ctx, f)

	if root != "" {
		item, err := f.metadata(ctx, f.uri(""))
		if err == nil && item.MimeType != api.FolderMimeType {
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
	return "todoforai:" + f.encodePath(p)
}

// encodePath encodes each segment of a slash-separated path.
func (f *Fs) encodePath(p string) string {
	if p == "" {
		return ""
	}
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = f.opt.Enc.Encode(s)
	}
	return strings.Join(segments, "/")
}

// decodeName decodes a single filename from the API.
func (f *Fs) decodeName(name string) string {
	return f.opt.Enc.Decode(name)
}

// shouldRetry returns true if the request should be retried.
func shouldRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if fserrors.ContextError(ctx, &err) {
		return false, err
	}
	if resp != nil && (resp.StatusCode == 429 || resp.StatusCode >= 500) {
		return true, err
	}
	return fserrors.ShouldRetry(err), err
}

// ---- API helpers ----

// metadata fetches file/folder metadata by URI.
func (f *Fs) metadata(ctx context.Context, uri string) (*api.Item, error) {
	var item api.Item
	opts := rest.Opts{
		Method:     "GET",
		Path:       "/api/v1/resources/metadata",
		Parameters: url.Values{"uri": {uri}},
	}
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &item)
		return shouldRetry(ctx, resp, err)
	})
	return &item, err
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// rest.Client returns "HTTP error 404 ..." or "http error 404: ..."
	// The API may also return NOT_FOUND in the response body.
	return strings.Contains(s, " 404") || strings.Contains(s, "NOT_FOUND")
}

// ---- fs.Fs methods ----

// List the objects and directories in dir into entries.
func (f *Fs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	uri := f.uri(dir)
	var entries fs.DirEntries
	token := ""
	for {
		var res api.ListResult
		params := url.Values{"uri": {uri}, "pageSize": {"200"}}
		if token != "" {
			params.Set("pageToken", token)
		}
		opts := rest.Opts{
			Method:     "GET",
			Path:       "/api/v1/resources/list",
			Parameters: params,
		}
		err := f.pacer.Call(func() (bool, error) {
			resp, err := f.srv.CallJSON(ctx, &opts, nil, &res)
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
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
			name = f.decodeName(name)
			remote := name
			if dir != "" {
				remote = dir + "/" + name
			}
			mt := msToTime(item.ModifiedAt, item.CreatedAt)
			if item.MimeType == api.FolderMimeType {
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

// NewObject finds the Object at remote. Returns ErrorObjectNotFound if not found.
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	item, err := f.metadata(ctx, f.uri(remote))
	if err != nil {
		if isNotFound(err) {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	if item.MimeType == api.FolderMimeType {
		return nil, fs.ErrorIsDir
	}
	return &Object{fs: f, remote: remote, size: item.FileSize, modTime: msToTime(item.ModifiedAt, item.CreatedAt), mimeType: item.MimeType, uri: item.URI, id: item.ID}, nil
}

// Put uploads a new file.
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	return f.upload(ctx, src.Remote(), in, src.Size())
}

// Mkdir creates a directory.
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
	parent, name := "todoforai:", full
	if i := strings.LastIndex(full, "/"); i >= 0 {
		parent = "todoforai:" + f.encodePath(full[:i])
		name = full[i+1:]
	}
	opts := rest.Opts{
		Method: "POST",
		Path:   "/api/v1/resources/mkdir",
	}
	req := api.MkdirRequest{ParentURI: parent, Name: f.opt.Enc.Encode(name)}
	return f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &req, nil)
		return shouldRetry(ctx, resp, err)
	})
}

// Rmdir removes a directory.
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	uri := f.uri(dir)
	if uri == "todoforai:" {
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
	opts := rest.Opts{
		Method:     "DELETE",
		Path:       "/api/v1/resources",
		Parameters: url.Values{"uri": {uri}},
		NoResponse: true,
	}
	return f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.Call(ctx, &opts)
		return shouldRetry(ctx, resp, err)
	})
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
	name := f.opt.Enc.Encode(path.Base(full))
	mt := guessMime(name)

	// Build multipart form fields.
	params := url.Values{}
	if isTodo(full) {
		parts := strings.SplitN(full, "/", 3) // todos/<id>/...
		if len(parts) >= 2 {
			params.Set("todoId", parts[1])
		}
	} else if dir := path.Dir(full); dir != "." && dir != "" {
		params.Set("folderPath", f.encodePath(dir))
	}

	opts := rest.Opts{
		Method:                "POST",
		Path:                  "/api/v1/resources/register",
		Body:                  in,
		MultipartParams:       params,
		MultipartContentName:  "file",
		MultipartFileName:     name,
	}
	var res api.UploadResult
	// CallNoRetry: the input stream is consumed on first attempt and
	// cannot be replayed. Rclone retries uploads at a higher level
	// (fs/operations) with a fresh reader.
	err := f.pacer.CallNoRetry(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &res)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	return &Object{fs: f, remote: remote, size: res.FileSize, modTime: time.UnixMilli(res.CreatedAt), mimeType: mt, uri: res.URI, id: res.AttachmentID}, nil
}

// ---- fs.Object ----

func (o *Object) Fs() fs.Info                                        { return o.fs }
func (o *Object) Remote() string                                     { return o.remote }
func (o *Object) Size() int64                                        { return o.size }
func (o *Object) ModTime(ctx context.Context) time.Time              { return o.modTime }
func (o *Object) Storable() bool                                     { return true }
func (o *Object) String() string                                     { return o.remote }
func (o *Object) MimeType(ctx context.Context) string                { return o.mimeType }
func (o *Object) SetModTime(context.Context, time.Time) error        { return fs.ErrorCantSetModTime }
func (o *Object) Hash(context.Context, hash.Type) (string, error)    { return "", hash.ErrUnsupported }

// Open downloads the object.
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	opts := rest.Opts{
		Method:     "GET",
		Path:       "/api/v1/resources",
		Parameters: url.Values{"uri": {o.uri}},
		Options:    options,
	}
	var resp *http.Response
	err := o.fs.pacer.Call(func() (bool, error) {
		var err error
		resp, err = o.fs.srv.Call(ctx, &opts)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Update replaces the object contents.
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	obj, err := o.fs.upload(ctx, o.remote, in, src.Size())
	if err != nil {
		return err
	}
	o.size, o.modTime, o.uri, o.id = obj.size, obj.modTime, obj.uri, obj.id
	return nil
}

// Remove deletes the object.
func (o *Object) Remove(ctx context.Context) error {
	opts := rest.Opts{
		Method:     "DELETE",
		Path:       "/api/v1/resources",
		Parameters: url.Values{"uri": {o.uri}},
		NoResponse: true,
	}
	return o.fs.pacer.Call(func() (bool, error) {
		resp, err := o.fs.srv.Call(ctx, &opts)
		return shouldRetry(ctx, resp, err)
	})
}

// ---- tiny helpers ----

func isTodo(p string) bool { return strings.HasPrefix(p, "todos/") || p == "todos" }
func guessMime(name string) string {
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}
func msToTime(ms ...*int64) time.Time {
	for _, p := range ms {
		if p != nil {
			return time.UnixMilli(*p)
		}
	}
	return time.Time{}
}

var (
	_ fs.Fs        = (*Fs)(nil)
	_ fs.Object    = (*Object)(nil)
	_ fs.MimeTyper = (*Object)(nil)
)
