# OAuth for rclone backend — upgrade plan

**Status:** API key auth works today. OAuth is a future PR.

## Current: Token (API key)

```
rclone config create todoforai todoforai api_key=YOUR_KEY
```

- User gets key from https://todofor.ai/settings/api-keys
- Sent as `X-API-Key` header on every request
- Simple, works everywhere (CLI, scripts, CI)

## Future: OAuth via `oauthutil`

rclone's `oauthutil` package handles the entire OAuth browser flow.
Adding it is mechanical — no architectural changes needed.

### Go side (rclone backend)

```go
import (
    "github.com/rclone/rclone/backend/todoforai"
    "github.com/rclone/rclone/lib/oauthutil"
    "golang.org/x/oauth2"
)

var oauthConfig = &oauth2.Config{
    ClientID:     rclone.MustReveal("..."),  // or user-supplied
    ClientSecret: rclone.MustReveal("..."),
    Endpoint: oauth2.Endpoint{
        AuthURL:  "https://api.todofor.ai/api/auth/authorize",
        TokenURL: "https://api.todofor.ai/api/auth/token",
    },
    Scopes: []string{"files:read", "files:write", "todos:read"},
}

func init() {
    fsi := &fs.RegInfo{
        Name:    "todoforai",
        NewFs:   NewFs,
        Config:  oauthutil.ConfigOut("", &oauthutil.Options{OAuth2Config: oauthConfig}),
        Options: append(oauthutil.SharedOptions, /* existing api_key option as fallback */),
    }
    fs.Register(fsi)
}
```

In `NewFs`, try OAuth token first, fall back to `api_key`:

```go
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
    // oauthutil.NewClient returns an http.Client with auto-refresh
    oAuthClient, _, err := oauthutil.NewClient(ctx, name, m, oauthConfig)
    if err == nil {
        // Use OAuth — token refresh is automatic
        f.client = NewClientWithHTTP(opt.URL, oAuthClient)
    } else if opt.APIKey != "" {
        // Fall back to API key
        f.client = NewClient(opt.URL, opt.APIKey)
    } else {
        return nil, fmt.Errorf("todoforai: need api_key or oauth token")
    }
}
```

### Backend side (BetterAuth)

BetterAuth already has session management. To issue OAuth tokens:

1. Add `/api/auth/authorize` — redirect-based consent screen
2. Add `/api/auth/token` — exchange code for access+refresh tokens
3. Validate Bearer tokens in `context.ts` alongside existing `X-API-Key`

BetterAuth's `bearer()` plugin is already enabled (`backend/src/auth/auth.ts`).
The token endpoint just wraps existing session creation.

### What changes

| Component | Change |
|-----------|--------|
| `fs.go` init() | Add `Config: oauthutil.ConfigOut(...)` |
| `fs.go` NewFs() | Try OAuth client first, fall back to api_key |
| `todoforai.go` Client | Add `NewClientWithHTTP(url, *http.Client)` constructor |
| `backend/src/auth/` | Add authorize + token endpoints |
| `backend/src/trpc/context.ts` | Accept Bearer tokens (already partially done) |

### What doesn't change

- All API endpoints stay the same
- Path ↔ URI translation unchanged
- API key auth continues to work (not deprecated)
- No new Go dependencies beyond what rclone already has

## Why not now

API key is simpler for the current use case (CLI, edge devices, scripts).
OAuth adds value when:
- Third-party apps integrate with todofor.ai
- Users want granular scopes (read-only, specific TODOs)
- We need token revocation without rotating API keys
