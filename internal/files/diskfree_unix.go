//go:build !windows

package files

import (
	"fmt"
	"syscall"
)

// FreeSpace reports the bytes available to unprivileged users on the
// filesystem that holds path.
func FreeSpace(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
