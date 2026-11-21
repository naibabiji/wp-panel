//go:build linux

package executor

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMaintenancePOSIXLockHelper(t *testing.T) {
	path := os.Getenv("WP_PANEL_MAINTENANCE_LOCK_FIXTURE")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Len: 0}
	if err := unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lock); err != nil {
		t.Fatal(err)
	}
	fmt.Println("locked")
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
}

func TestMaintenancePOSIXQueriesBothLocksWithoutTakingThem(t *testing.T) {
	for _, locked := range []string{"lock-frontend", "lock"} {
		t.Run(locked, func(t *testing.T) {
			dir := t.TempDir()
			paths := []string{filepath.Join(dir, "lock-frontend"), filepath.Join(dir, "lock")}
			for _, path := range paths {
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if busy, err := maintenancePOSIXLocksBusy(paths); err != nil || busy {
				t.Fatalf("unlocked: %v %v", busy, err)
			}
			// POSIX locks belong to a process: an actual child is required to
			// demonstrate F_GETLK, not a second fd in this test process.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMaintenancePOSIXLockHelper$")
			cmd.Env = append(os.Environ(), "WP_PANEL_MAINTENANCE_LOCK_FIXTURE="+filepath.Join(dir, locked))
			ready, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			scanner := bufio.NewScanner(ready)
			if !scanner.Scan() || scanner.Text() != "locked" {
				t.Fatal("child did not acquire POSIX lock")
			}
			if busy, err := maintenancePOSIXLocksBusy(paths); err != nil || !busy {
				t.Fatalf("held %s: %v %v", locked, busy, err)
			}
			// A flock on the same inode succeeds, proving it would be an
			// incorrect test for the external dpkg lock.
			f, err := os.OpenFile(filepath.Join(dir, locked), os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			if busy, err := maintenancePOSIXLocksBusy(paths); err != nil || !busy {
				t.Fatalf("query changed POSIX lock: %v %v", busy, err)
			}
		})
	}
	missing := filepath.Join(t.TempDir(), "absent")
	if busy, err := maintenancePOSIXLocksBusy([]string{missing}); err != nil || busy {
		t.Fatalf("absent lock: %v %v", busy, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("query created a lock file")
	}
}
