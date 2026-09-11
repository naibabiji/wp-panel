//go:build linux

package executor

import (
	"fmt"
	"os"
	"syscall"
)

func fileOwnerIDs(info os.FileInfo) (int, int, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("file ownership is unavailable")
	}
	return int(stat.Uid), int(stat.Gid), nil
}
