//go:build linux

package executor

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func serviceControlLockDirectory(dataDir string) (string, error) {
	dir := filepath.Join(dataDir, "locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	var st unix.Stat_t
	if err := unix.Lstat(dir, &st); err != nil {
		return "", err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != os.Geteuid() {
		return "", errors.New("unsafe service control lock directory")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}
