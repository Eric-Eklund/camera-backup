//go:build !windows

package ui

import "syscall"

// FreeSpace returns the number of bytes available to the user at path.
func FreeSpace(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	// Bavail = blocks available to unprivileged user (what df shows)
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
