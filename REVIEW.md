# rclone backend for todofor.ai — Design Review

## The core idea

The entire path mapping is **one line of Go**:

```go
func remoteToURI(remote string) string { return "todoforai://" + remote }
```

That's it. No switch statements, no path parsing, no category detection.
The user types `todoforai:documents/report.pdf`, rclone sends `todoforai://documents/report.pdf` to the API, the backend figures out the rest.

This is the same pattern as `gdrive:` — no artificial prefixes.

## How simple is it?

### What other rclone backends do

Most rclone backends (S3, GCS, Azure) have 50-200 lines of path translation:
- Bucket extraction from path
- Region-specific endpoint construction
- Path encoding/escaping rules
- Virtual-hosted vs path-style URL switching

### What we do

```go
func remoteToURI(remote string) string { return "todoforai://" + remote }
```

The complexity lives in the backend (where it belongs), not in the client.
The Go code is pure plumbing: take rclone calls → make HTTP requests → return results.

### Line counts

| File | Lines | What it does |
|------|-------|-------------|
| `todoforai.go` | 294 | HTTP client (6 methods), 3 types, 4 tiny helpers |
| `fs.go` | 329 | rclone interface impl (all methods are thin wrappers) |
| `todoforai_test.go` | 111 | 6 unit tests |
| `integration_test.go` | 60 | 2 live API tests |
| **Total** | **794** | |

For comparison, rclone's memory backend is ~670 lines, and it doesn't talk to any server.

## What changed on the backend

**One file: `LocalFileResolver.ts`** (~217 lines)

Added a simple routing rule: if the URI host is not one of `attachment`, `attachments`, `todos`, `root`, treat it as a folder path. So `todoforai://documents/report.pdf` → folder path `documents/report.pdf`.

Also added `todos` host handling: `todoforai://todos/<todoId>/` lists todo attachments.

All legacy URIs (`todoforai://attachment/<id>`, `todoforai://files/<path>`, etc.) keep working. Zero migration needed.

**One endpoint: `POST /resources/mkdir`** (10 lines in `resourceRoutes.ts`)

Was only available via tRPC, now also via REST for the rclone client.

## Architecture

```
User types:     todoforai:project/docs/spec.md
                        ↓
rclone calls:   Fs.NewObject("project/docs/spec.md")
                        ↓
Go code:        remoteToURI("project/docs/spec.md")
                = "todoforai://project/docs/spec.md"
                        ↓
HTTP:           GET /api/v1/resources/metadata?uri=todoforai://project/docs/spec.md
                        ↓
Backend:        ResourceService.parseUri() → { scheme: "todoforai", host: "project", path: "/docs/spec.md" }
                        ↓
                LocalFileResolver.isFilePath() → true (host "project" not in SPECIAL_HOSTS)
                        ↓
                resolveFolderPath() → "project/docs/spec.md"
                        ↓
                storage.getAttachmentByPath(userId, "project/docs", "spec.md")
```

Every step is a trivial transformation. No magic.

## The `todos/` reservation

`todos/` is a reserved folder name. If a user creates a folder called `todos`, it collides.

This is the same pattern as:
- `.git/` in git repos
- `node_modules/` in npm
- `__pycache__/` in Python

The backend enforces this — `isTodoPath()` in Go prevents `mkdir todos/`, and the backend routes `todoforai://todos/*` to todo attachment handling.

## What's NOT in scope (future work)

- **OAuth auth flow** — currently API key only. rclone's `oauthutil` makes OAuth trivial to add later.
- **Server-side range requests** — `Open()` currently discards bytes client-side for seeks. Adding HTTP Range header support would fix this.
- **`fs.Copier` / `fs.Mover`** — server-side copy/move. Not critical for v1.
- **Streaming uploads** — `fs.PutStreamer` for large files without buffering.
- **Upstream PR to rclone/rclone** — needs docs, integration test config, review cycles.
