package executor

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestAIDevelopmentPHPKillArgsTargetsOnlyPHPFPMWorkers(t *testing.T) {
	want := []string{"-KILL", "-u", "wp_example", "-f", `^php-fpm: pool `}
	if got := aiDevelopmentPHPKillArgs("KILL", "wp_example"); !reflect.DeepEqual(got, want) {
		t.Fatalf("args=%q want=%q", got, want)
	}
}

type aiDevelopmentExitError int

func (e aiDevelopmentExitError) Error() string { return "usermod failed" }
func (e aiDevelopmentExitError) ExitCode() int { return int(e) }

func TestRetryAIDevelopmentUsermodRetriesBusyExit(t *testing.T) {
	calls := 0
	err := retryAIDevelopmentUsermod(context.Background(), 0, 3, func() error {
		calls++
		if calls < 3 {
			return aiDevelopmentExitError(8)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryAIDevelopmentUsermod() error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("retryAIDevelopmentUsermod() calls = %d, want 3", calls)
	}
}

func TestRetryAIDevelopmentUsermodDoesNotRetryOtherExit(t *testing.T) {
	calls := 0
	err := retryAIDevelopmentUsermod(context.Background(), 0, 3, func() error {
		calls++
		return aiDevelopmentExitError(6)
	})
	if !errors.Is(err, aiDevelopmentExitError(6)) {
		t.Fatalf("retryAIDevelopmentUsermod() error = %v, want exit 6", err)
	}
	if calls != 1 {
		t.Fatalf("retryAIDevelopmentUsermod() calls = %d, want 1", calls)
	}
}

func TestRetryAIDevelopmentUsermodStopsAfterAttemptLimit(t *testing.T) {
	calls := 0
	err := retryAIDevelopmentUsermod(context.Background(), 0, 3, func() error {
		calls++
		return aiDevelopmentExitError(8)
	})
	if !errors.Is(err, aiDevelopmentExitError(8)) {
		t.Fatalf("retryAIDevelopmentUsermod() error = %v, want exit 8", err)
	}
	if calls != 3 {
		t.Fatalf("retryAIDevelopmentUsermod() calls = %d, want 3", calls)
	}
}

func TestRetryAIDevelopmentUsermodHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := retryAIDevelopmentUsermod(ctx, time.Hour, 3, func() error {
		calls++
		cancel()
		return aiDevelopmentExitError(8)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retryAIDevelopmentUsermod() error = %v, want context canceled", err)
	}
	if calls != 1 {
		t.Fatalf("retryAIDevelopmentUsermod() calls = %d, want 1", calls)
	}
}
