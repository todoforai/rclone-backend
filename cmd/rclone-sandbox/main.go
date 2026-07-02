// Slim rclone build for the todofor.ai sandbox image.
//
// The public release (built by the CI's generated main.go) imports
// backend/all + cmd/all — a general-purpose "rclone for todofor.ai" (~58MB).
// The sandbox only ever mounts the todoforai remote, so this entry point pulls
// in just that backend plus the handful of commands the entrypoint uses
// (config/mount/ls*/version), yielding a ~16MB binary that we bake into the
// OCI image. Kept in its own package dir so it never collides with the
// repo-root main.go the release workflow writes.
package main

import (
	_ "github.com/todoforai/rclone-backend/backend/todoforai"
	// The local backend backs `--vfs-cache-mode full` (the write-back cache is
	// stored on the local FS). Without it rclone silently disables the VFS
	// cache ("didn't find backend called local"), breaking buffered writes.
	_ "github.com/rclone/rclone/backend/local"

	"github.com/rclone/rclone/cmd"
	_ "github.com/rclone/rclone/cmd/config"
	_ "github.com/rclone/rclone/cmd/lsd"
	_ "github.com/rclone/rclone/cmd/lsf"
	_ "github.com/rclone/rclone/cmd/mount"
	_ "github.com/rclone/rclone/cmd/version"
	_ "github.com/rclone/rclone/lib/plugin"
)

func main() { cmd.Main() }
