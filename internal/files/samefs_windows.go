//go:build windows

package files

import (
	"path/filepath"
	"strings"
)

func sameFilesystem(a, b string) (bool, error) {
	return strings.EqualFold(filepath.VolumeName(a), filepath.VolumeName(b)), nil
}
