//go:build !linux

package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

func wpInventoryPlatformSupported() error {
	return errors.New("wordpress inventory runner requires Linux")
}

func wpInventoryEffectiveUID() int { return -1 }

func wpInventoryFileOwner(os.FileInfo) (int, int, bool) { return 0, 0, false }

type wpInventoryFileLock struct{}

func (l *wpInventoryFileLock) Close() error { return nil }

func wpInventoryAcquireFileLock(context.Context, string, int, int) (*wpInventoryFileLock, error) {
	return nil, errors.New("wordpress inventory runner requires Linux")
}

func wpInventoryConfigureCommand(*exec.Cmd) {}

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
	return state.UserTime(), state.SystemTime(), 0
}
