// Package all imports all rclone backends including todoforai.
//
// When submitting upstream to rclone/rclone, add this import to
// backend/all/all.go in the rclone repo:
//
//	_ "github.com/rclone/rclone/backend/todoforai"
package all

import (
	_ "github.com/todoforai/rclone-backend/backend/todoforai"
)
