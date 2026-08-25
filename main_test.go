package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/router"
)

type fakeCoreUpdateWorkerLifecycle struct {
	startErr error
	stop     func(context.Context) error
}

func TestSiteMigrationMachineRoutePassesRealScanDefenseStack(t *testing.T) {
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Panel.RandomSuffix = "test-panel-prefix"
	cfg.Panel.TLSCertPath = filepath.Join(t.TempDir(), "unused.crt")
	engine := router.SetupRouter(cfg, TemplatesFS, StaticFS, "test-version", "")

	request := httptest.NewRequest(http.MethodPost, "/api/site-migration/v1/pair/redeem", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Go-http-client/1.1")
	request.RemoteAddr = "203.0.113.10:12345"
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want downstream pairing rejection %d", recorder.Code, http.StatusUnauthorized)
	}
	var bans int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM firewall_bans WHERE source_jail='panel_scan'`).Scan(&bans); err != nil {
		t.Fatal(err)
	}
	if bans != 0 {
		t.Fatalf("panel scan bans=%d, want 0", bans)
	}
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
