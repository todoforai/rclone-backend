# rclone backend for todofor.ai

An [rclone](https://rclone.org) backend that exposes your todofor.ai cloud workspace as a virtual filesystem.

## What it does

```
todoforai:documents/report.pdf       → your files, just like gdrive
todoforai:images/logo.png            → your files
todoforai:todos/<todoId>/            → attachments for a TODO
todoforai:todos/<todoId>/att-id      → specific attachment
```

No `files/` prefix. `todos/` is a reserved folder name for TODO attachments.

Once configured:

```bash
# Browse your workspace
rclone ls todoforai:

# Mount as a local folder
rclone mount todoforai: ~/workspace

# Sync files
rclone sync ./local-dir todoforai:project/

# Copy TODO attachments locally
rclone copy todoforai:todos/abc123/ ./todo-files/
```

## Setup

### Option A: Browser login (OAuth — recommended)

```bash
rclone config create todoforai todoforai
# → Browser opens → sign in with Google → done
```

### Option B: API key (headless/CI)

```bash
# Get key from https://todofor.ai/settings/api-keys
rclone config create todoforai todoforai api_key=YOUR_API_KEY
```

### Verify

```bash
rclone ls todoforai:
```

## Configuration

| Option    | Default                  | Description                        |
|-----------|--------------------------|------------------------------------|
| `api_key` | *(empty)*                | API key (skip OAuth if set)        |
| `url`     | `https://api.todofor.ai` | API server URL (advanced)          |
| `token`   | *(auto)*                 | OAuth token JSON (managed by rclone) |

## Path mapping

The mapping is trivial: `todoforai:path` → `todoforai://path`.

That's it. The backend handles all URI routing.

## Supported operations

| Operation | files | todos/ |
|-----------|-------|--------|
| List      | ✅    | ✅     |
| Read      | ✅    | ✅     |
| Write     | ✅    | ✅     |
| Delete    | ✅    | ✅     |
| Mkdir     | ✅    | ❌     |

## Development

```bash
cd rclone-backend

# Unit tests
go test ./backend/todoforai/ -v

# Integration tests (needs API key)
TODOFORAI_API_KEY=xxx go test ./backend/todoforai/ -run Integration -v
```

## License

MIT
