//go:build linux

package executor

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestServiceControlNamesAndPartialRelease(t *testing.T) {
	dir := t.TempDir()
	release, err := tryServiceControlLocks(dir, "mariadb.service", "mariadb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tryServiceControlLocks(dir, "mariadb"); !errors.Is(err, errServiceControlBusy) {
		t.Fatalf("alias did not conflict: %v", err)
	}
	if _, err := tryServiceControlLocks(dir, "nginx", "mariadb"); !errors.Is(err, errServiceControlBusy) {
		t.Fatalf("multi-lock: %v", err)
	}
	// A failed multi-lock cannot retain even an earlier sorted descriptor.
	if _, err := tryServiceControlLocks(dir, "fail2ban", "mariadb"); !errors.Is(err, errServiceControlBusy) {
		t.Fatal(err)
	}
	other, err := tryServiceControlLocks(dir, "fail2ban")
	if err != nil {
		t.Fatal(err)
	}
	other()
	release()
	again, err := tryServiceControlLocks(dir, guardServiceNames...)
	if err != nil {
		t.Fatal(err)
	}
	again()
	for _, unit := range []string{"../mariadb", "cron", "rsyslog.service", "mariadb.service.service"} {
		if _, err := tryServiceControlLocks(dir, unit); err == nil {
			t.Fatalf("accepted %q", unit)
		}
	}
}

func TestServiceControlRejectsUnsafePaths(t *testing.T) {
	for _, kind := range []string{"directory_symlink", "file_symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			locks := filepath.Join(dir, "locks")
			if kind == "directory_symlink" {
				if err := os.Symlink(t.TempDir(), locks); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(locks, 0700); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(dir, "target")
				if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(locks, "service-control-mariadb.lock")
				var err error
				if kind == "hardlink" {
					err = os.Link(target, link)
				} else {
					err = os.Symlink(target, link)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if release, err := tryServiceControlLocks(dir, "mariadb"); err == nil {
				release()
				t.Fatal("unsafe lock accepted")
			} else if errors.Is(err, errServiceControlBusy) {
				t.Fatal("unsafe path reported as planned operation")
			}
		})
	}
}

// A real child process is necessary: POSIX F_GETLK does not report locks held
// by the querying process, and goroutines alone do not prove worker isolation.
func TestServiceControlChild(t *testing.T) {
	kind := os.Getenv("WPP_SERVICE_LOCK_TEST_KIND")
	if kind == "" {
		return
	}
	dir := os.Getenv("WPP_SERVICE_LOCK_TEST_DIR")
	if kind == "flock" {
		release, err := tryServiceControlLocks(dir, "mariadb")
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	} else {
		f, err := os.OpenFile(filepath.Join(dir, "dpkg.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
		if err := unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lock); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("locked")
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
}

func startServiceControlChild(t *testing.T, dir, kind string) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestServiceControlChild$")
	cmd.Env = append(os.Environ(), "WPP_SERVICE_LOCK_TEST_KIND="+kind, "WPP_SERVICE_LOCK_TEST_DIR="+dir)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		in.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("child: %q %v", line, err)
	}
	return cmd, in
}

func TestServiceControlCrossProcessExitAndPOSIX(t *testing.T) {
	for _, kill := range []bool{false, true} {
		t.Run(fmt.Sprintf("flock_kill_%v", kill), func(t *testing.T) {
			dir := t.TempDir()
			cmd, in := startServiceControlChild(t, dir, "flock")
			if _, err := tryServiceControlLocks(dir, "mariadb.service"); !errors.Is(err, errServiceControlBusy) {
				t.Fatal(err)
			}
			if kill {
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			} else {
				in.Close()
			}
			if err := cmd.Wait(); err != nil && !kill {
				t.Fatal(err)
			}
			release, err := tryServiceControlLocks(dir, "mariadb")
			if err != nil {
				t.Fatal(err)
			}
			release()
		})
	}
	dir := t.TempDir()
	cmd, in := startServiceControlChild(t, dir, "posix")
	paths := []string{filepath.Join(dir, "dpkg.lock")}
	if busy, err := maintenancePOSIXLocksBusy(paths); err != nil || !busy {
		t.Fatalf("POSIX busy=%v err=%v", busy, err)
	}
	in.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if busy, err := maintenancePOSIXLocksBusy(paths); err != nil || busy {
		t.Fatalf("POSIX released=%v err=%v", busy, err)
	}
}

func TestProcessGuardSurvivesRunnerExitWithDPKGChild(t *testing.T) {
	dir, pg, s, _ := setupPlannedGuard(t)
	// A new guard may first observe an already running detached worker.
	pg.firstRun = true
	s.Running = false
	s.Restarts = 0
	owner, ownerIn := startServiceControlChild(t, dir, "flock")
	dpkg, dpkgIn := startServiceControlChild(t, dir, "posix")
	mockPlannedGuardCommand(t, func(string, ...string) ([]byte, error) {
		t.Fatal("sampled while runner owned service")
		return nil, nil
	})
	pg.check(s)
	ownerIn.Close()
	if err := owner.Wait(); err != nil {
		t.Fatal(err)
	}
	maintenanceDPKGQuery = func() (bool, error) { return maintenancePOSIXLocksBusy([]string{filepath.Join(dir, "dpkg.lock")}) }
	starts := 0
	mockPlannedGuardCommand(t, func(name string, args ...string) ([]byte, error) {
		if name == "journalctl" {
			return nil, nil
		}
		if strings.Join(args, " ") == "show --property=SystemState --value" {
			return []byte("running"), nil
		}
		if args[0] == "start" {
			starts++
			return nil, nil
		}
		state := "inactive"
		if starts > 0 {
			state = "active"
		}
		return []byte("ActiveState=" + state + "\nNRestarts=0\n"), nil
	})
	pg.check(s)
	if starts != 0 || !s.rebaselineAfterPlannedOperation || s.Restarts != 0 {
		t.Fatalf("dpkg interference: starts=%d %+v", starts, s)
	}
	dpkgIn.Close()
	if err := dpkg.Wait(); err != nil {
		t.Fatal(err)
	}
	pg.check(s)
	if starts != 1 || !s.Running || s.Restarts != 1 || s.rebaselineAfterPlannedOperation {
		t.Fatalf("failed to resume: starts=%d %+v", starts, s)
	}
}
