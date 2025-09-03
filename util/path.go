package util

import (
	"runtime"
	"strings"
)

// IsRclonePath returns true if the path refers to an rclone remote.
// A colon (":") indicates an rclone path, except when it denotes a
// Windows drive letter (e.g., "C:").
func IsRclonePath(path string) bool {
	if runtime.GOOS == "windows" {
		if len(path) >= 2 && path[1] == ':' {
			return false
		}
	}
	return strings.Contains(path, ":")
}
