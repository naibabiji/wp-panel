//go:build linux

package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func wpInventoryPlatformSupported() error { return nil }

func wpInventoryEffectiveUID() int { return os.Geteuid() }

func wpInventoryFileOwner(info os.FileInfo) (int, int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(stat.Uid), int(stat.Gid), true
}

type wpInventoryFileLock struct{ file *os.File }

func (l *wpInventoryFileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}

func wpInventoryAcquireFileLock(ctx context.Context, path string, ownerUID, ownerGID int) (*wpInventoryFileLock, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("runner lock mode contract failed")
	}
	uid, gid, ok := wpInventoryFileOwner(info)
	if !ok || uid != ownerUID || gid != ownerGID {
		return nil, errors.New("runner lock owner contract failed")
	}
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			closeOnError = false
			return &wpInventoryFileLock{file: file}, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func wpInventoryConfigureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
}

func wpInventoryExitCode(state *os.ProcessState) int {
	if state == nil {
		return -1
	}
	return state.ExitCode()
}

func wpInventoryProcessMetrics(state *os.ProcessState) (time.Duration, time.Duration, int64) {
	if state == nil {
		return 0, 0, 0
	}
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok {
		return state.UserTime(), state.SystemTime(), 0
	}
	return state.UserTime(), state.SystemTime(), usage.Maxrss
}
