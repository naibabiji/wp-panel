//go:build linux

package executor

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// F_GETLK queries POSIX locks used by APT/dpkg. Trying F_SETLK would briefly
// compete with external APT, and flock would query a different locking domain.
// This observation cannot reserve an external package-manager transaction:
// callers still need panel locks and a fresh check before a destructive action.
func maintenanceDPKGLocksBusy() (bool, error) {
	return maintenancePOSIXLocksBusy([]string{"/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock"})
}

func maintenancePOSIXLocksBusy(paths []string) (bool, error) {
	for _, path := range paths {
		// Do not create missing lock files or write into an APT-managed file.
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("cannot query package lock %s: %w", path, err)
		}
		lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
		err = unix.FcntlFlock(f.Fd(), unix.F_GETLK, &lock)
		f.Close()
		if err != nil {
			return false, fmt.Errorf("cannot query package lock %s: %w", path, err)
		}
		if lock.Type != unix.F_UNLCK {
			return true, nil
		}
	}
	return false, nil
}
