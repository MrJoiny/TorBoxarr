//go:build !windows

package files

import (
	"fmt"
	"os"
	"syscall"
)

func sameFilesystem(a, b string) (bool, error) {
	aInfo, err := os.Stat(a)
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", a, err)
	}
	bInfo, err := os.Stat(b)
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", b, err)
	}

	aStat, ok := aInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return true, nil
	}
	bStat, ok := bInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return true, nil
	}
	return aStat.Dev == bStat.Dev, nil
}
