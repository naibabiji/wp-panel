package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

type fakeCoreUpdateWorkerLifecycle struct {
	startErr error
	stop     func(context.Context) error
}

func (f *fakeCoreUpdateWorkerLifecycle) Start() error { return f.startErr }
func (f *fakeCoreUpdateWorkerLifecycle) Stop(ctx context.Context) error {
	if f.stop != nil {
		return f.stop(ctx)
	}
	return nil
}

func TestStartWPCoreUpdateWorkerFailsClosedWithoutHalfStartedWorker(t *testing.T) {
	cfg := &config.Config{}
	wantErr := errors.New("injected")
	if worker, err := startWPCoreUpdateWorker(cfg, func(*config.Config) (wpCoreUpdateWorkerLifecycle, error) {
		return nil, wantErr
	}); !errors.Is(err, wantErr) || worker != nil {
		t.Fatalf("constructor failure worker=%v err=%v", worker, err)
	}
	if worker, err := startWPCoreUpdateWorker(cfg, func(*config.Config) (wpCoreUpdateWorkerLifecycle, error) {
		return &fakeCoreUpdateWorkerLifecycle{startErr: wantErr}, nil
	}); !errors.Is(err, wantErr) || worker != nil {
		t.Fatalf("start failure worker=%v err=%v", worker, err)
	}
	want := &fakeCoreUpdateWorkerLifecycle{}
	worker, err := startWPCoreUpdateWorker(cfg, func(*config.Config) (wpCoreUpdateWorkerLifecycle, error) { return want, nil })
	if err != nil || worker != want {
		t.Fatalf("worker=%v err=%v", worker, err)
	}
}

func TestStopWPCoreUpdateWorkerUsesBoundedContext(t *testing.T) {
	timeout := 20 * time.Millisecond
	worker := &fakeCoreUpdateWorkerLifecycle{stop: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	started := time.Now()
	err := stopWPCoreUpdateWorker(worker, timeout)
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	if elapsed < timeout || elapsed > 500*time.Millisecond {
		t.Fatalf("shutdown elapsed=%s", elapsed)
	}
}
