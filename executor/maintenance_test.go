package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

const testMaintenancePassword = "correct-maintenance-password"

func TestMaintenanceRestartProcessHelper(t *testing.T) {
	dbPath := os.Getenv("WPP_MAINTENANCE_RESTART_TEST_DB")
	if dbPath == "" {
		return
	}
	if err := database.Open(dbPath); err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	m := NewMaintenanceManager(database.GetDB())
	m.alert = func(int, string) {}
	m.lock = func(site *models.Website, _ string) error {
		return os.Chmod(filepath.Join(site.WebRoot, "probe"), 0444)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	s, err := m.Status(1)
	if err != nil || s.State != "locked" || s.WindowID != "" || s.Notice != "restart" {
		t.Fatalf("startup did not relock: %+v %v", s, err)
	}
}

func TestMaintenanceRestartAcrossProcess(t *testing.T) {
	m, id, _, _, _ := maintenanceFixture(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "probe"), []byte("safe fixture"), 0444); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.Exec(`UPDATE websites SET web_root=? WHERE id=?`, root, id); err != nil {
		t.Fatal(err)
	}
	m.unlock = func(site *models.Website) error { return os.Chmod(filepath.Join(site.WebRoot, "probe"), 0644) }
	maintenanceUnlock(t, m, id)
	var seq int
	var name, dbPath string
	if err := m.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &dbPath); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestMaintenanceRestartProcessHelper$", "-test.count=1")
	command.Env = append(os.Environ(), "WPP_MAINTENANCE_RESTART_TEST_DB="+dbPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restart process: %v %s", err, output)
	}
	info, err := os.Stat(filepath.Join(root, "probe"))
	if err != nil || info.Mode().Perm() != 0444 {
		t.Fatalf("permissions after restart: %v %v", info, err)
	}
}

func maintenanceFixture(t *testing.T) (*MaintenanceManager, int, *time.Time, *int, *int) {
	t.Helper()
	store, id := newWPUpdateStoreTest(t)
	if _, err := store.db.Exec(`UPDATE websites SET file_lock_enabled=1,file_lock_mode='strict',file_lock_apply_status='ready' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	m := NewMaintenanceManager(store.db)
	now := time.Unix(1800000000, 0)
	m.now = func() time.Time { return now }
	unlocks, locks := 0, 0
	m.unlock = func(*models.Website) error { unlocks++; return nil }
	m.lock = func(*models.Website, string) error { locks++; return nil }
	m.alert = func(int, string) {}
	if err := m.Configure(id, true, 5, testMaintenancePassword); err != nil {
		t.Fatal(err)
	}
	return m, id, &now, &unlocks, &locks
}

func maintenanceUnlock(t *testing.T, m *MaintenanceManager, id int) MaintenanceStatus {
	t.Helper()
	if err := m.Unlock(id, MaintenanceRequest{RequestID: uuid.NewString(), Password: testMaintenancePassword, Actor: "1"}); err != nil {
		t.Fatal(err)
	}
	s, err := m.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMaintenanceExtensionBoundariesAndIdempotency(t *testing.T) {
	m, id, _, unlocks, _ := maintenanceFixture(t)
	s := maintenanceUnlock(t, m, id)
	start := s.ServerTime
	for i := 0; i < 5; i++ {
		r := MaintenanceRequest{WindowID: s.WindowID, RequestID: uuid.NewString(), Revision: s.Revision, Minutes: 5}
		if err := m.Extend(id, r); err != nil {
			t.Fatal(err)
		}
		if err := m.Extend(id, r); err != nil {
			t.Fatal("retry", err)
		}
		s, _ = m.Status(id)
	}
	if s.ExpiresAt != start+1800 || *unlocks != 1 {
		t.Fatalf("boundary: %+v", s)
	}
	r := MaintenanceRequest{WindowID: s.WindowID, RequestID: uuid.NewString(), Revision: s.Revision, Minutes: 1}
	if err := m.Extend(id, r); !errors.Is(err, ErrMaintenancePasswordRequired) {
		t.Fatal(err)
	}
	r.Password = testMaintenancePassword
	if err := m.Extend(id, r); err != nil {
		t.Fatal(err)
	}
	s, _ = m.Status(id)
	if s.ExpiresAt != start+1860 || s.VerifiedUntil != start+3600 {
		t.Fatalf("cross: %+v", s)
	}
	if err := m.Extend(id, MaintenanceRequest{WindowID: s.WindowID, RequestID: uuid.NewString(), Revision: 0, Minutes: 5}); err == nil {
		t.Fatal("stale revision accepted")
	}
}

func TestMaintenanceSharedFailureFreezePersists(t *testing.T) {
	m, id, now, _, _ := maintenanceFixture(t)
	alerts := 0
	m.alert = func(int, string) { alerts++ }
	for i := 0; i < 3; i++ {
		_ = m.Unlock(id, MaintenanceRequest{RequestID: uuid.NewString(), Password: "wrong"})
	}
	s := maintenanceUnlock(t, m, id)
	for i := 0; i < 5; i++ {
		if err := m.Extend(id, MaintenanceRequest{WindowID: s.WindowID, RequestID: uuid.NewString(), Revision: s.Revision, Minutes: 5}); err != nil {
			t.Fatal(err)
		}
		s, _ = m.Status(id)
	}
	for i := 0; i < 2; i++ {
		_ = m.Extend(id, MaintenanceRequest{WindowID: s.WindowID, RequestID: uuid.NewString(), Revision: s.Revision, Minutes: 1, Password: "wrong"})
	}
	_, state, _, _ := m.load(id)
	if state.FrozenUntil != now.Unix()+600 || alerts != 1 {
		t.Fatalf("freeze=%+v alerts=%d", state, alerts)
	}
	err := m.Extend(id, MaintenanceRequest{WindowID: s.WindowID, RequestID: uuid.NewString(), Revision: s.Revision, Minutes: 1, Password: testMaintenancePassword})
	if !errors.Is(err, ErrMaintenanceValidation) {
		t.Fatal(err)
	}
	if err := m.Relock(id, s.WindowID); err != nil {
		t.Fatal(err)
	}
	next := NewMaintenanceManager(m.db)
	next.now = m.now
	next.lock = m.lock
	next.alert = m.alert
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	next.Start(ctx)
	_, state, _, _ = next.load(id)
	if state.FrozenUntil != now.Unix()+600 || alerts != 1 {
		t.Fatal("freeze lost")
	}
	*now = now.Add(601 * time.Second)
	if err := m.Unlock(id, MaintenanceRequest{RequestID: uuid.NewString(), Password: testMaintenancePassword}); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceRetrySixtySecondsAndStartupRelocks(t *testing.T) {
	m, id, now, _, locks := maintenanceFixture(t)
	s := maintenanceUnlock(t, m, id)
	m.lock = func(*models.Website, string) error { *locks++; return errors.New("permission denied") }
	if err := m.Relock(id, s.WindowID); err == nil {
		t.Fatal("expected failure")
	}
	for i := 0; i < 5; i++ {
		*now = now.Add(10 * time.Second)
		m.Tick()
	}
	if *locks != 1 {
		t.Fatalf("busy retry %d", *locks)
	}
	*now = now.Add(10 * time.Second)
	m.Tick()
	if *locks != 2 {
		t.Fatal(*locks)
	}
	m.lock = func(_ *models.Website, mode string) error {
		*locks++
		if mode != "strict" {
			t.Fatal(mode)
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.mu.Lock()
	m.retry = map[int]int64{}
	m.mu.Unlock()
	m.Start(ctx)
	s, _ = m.Status(id)
	if s.State != "locked" || s.WindowID != "" {
		t.Fatalf("restart: %+v", s)
	}
	// An unexpired, freshly extended window is also revoked after restart.
	s = maintenanceUnlock(t, m, id)
	if err := m.Extend(id, MaintenanceRequest{WindowID: s.WindowID, RequestID: uuid.NewString(), Minutes: 5}); err != nil {
		t.Fatal(err)
	}
	next := NewMaintenanceManager(m.db)
	next.lock = m.lock
	next.now = m.now
	next.alert = m.alert
	next.Start(ctx)
	s, _ = next.Status(id)
	if s.State != "locked" {
		t.Fatal(s)
	}
}

func TestMaintenanceDatabaseFailureCompensatesAndKeepsWindow(t *testing.T) {
	m, id, _, unlocks, locks := maintenanceFixture(t)
	_, err := m.db.Exec(`CREATE TRIGGER fail_maintenance_open BEFORE UPDATE OF maintenance_security ON websites
	WHEN json_extract(NEW.maintenance_security,'$.window.state')='unlocked' BEGIN SELECT RAISE(ABORT,'injected'); END`)
	if err != nil {
		t.Fatal(err)
	}
	err = m.Unlock(id, MaintenanceRequest{RequestID: uuid.NewString(), Password: testMaintenancePassword})
	if err == nil || *unlocks != 1 || *locks != 1 {
		t.Fatalf("compensation %v %d %d", err, *unlocks, *locks)
	}
	s, _ := m.Status(id)
	if s.State != "unknown" {
		t.Fatal(s)
	}
	_, state, _, _ := m.load(id)
	if state.Window == nil || state.Window.Mode != "strict" {
		t.Fatal("lost recovery")
	}
	if _, err := m.db.Exec(`DROP TRIGGER fail_maintenance_open`); err != nil {
		t.Fatal(err)
	}
	m.Tick()
	s, _ = m.Status(id)
	if s.State != "locked" {
		t.Fatal(s)
	}
}

func TestMaintenancePartialUnlockCompensatesDespiteDatabaseFailure(t *testing.T) {
	m, id, _, _, locks := maintenanceFixture(t)
	m.unlock = func(*models.Website) error {
		_, err := m.db.Exec(`CREATE TRIGGER fail_after_partial_unlock BEFORE UPDATE OF maintenance_security ON websites BEGIN SELECT RAISE(ABORT,'injected'); END`)
		if err != nil {
			t.Fatal(err)
		}
		return errors.New("partial permission change")
	}
	if err := m.Unlock(id, MaintenanceRequest{RequestID: uuid.NewString(), Password: testMaintenancePassword}); err == nil || *locks != 1 {
		t.Fatalf("database failure prevented immediate compensation: %v locks=%d", err, *locks)
	}
	s, _ := m.Status(id)
	if s.State != "unknown" || s.WindowID == "" {
		t.Fatal(s)
	}
}

func TestMaintenanceSlowRelockFailureRetryAndAlertDedup(t *testing.T) {
	m, id, now, _, locks := maintenanceFixture(t)
	s := maintenanceUnlock(t, m, id)
	alerts := 0
	m.alert = func(int, string) { alerts++ }
	m.lock = func(*models.Website, string) error {
		*locks++
		*now = now.Add(90 * time.Second)
		_, err := m.db.Exec(`CREATE TRIGGER IF NOT EXISTS fail_failed_record BEFORE UPDATE OF maintenance_security ON websites WHEN json_extract(NEW.maintenance_security,'$.window.state')='relock_failed' BEGIN SELECT RAISE(ABORT,'injected'); END`)
		if err != nil {
			t.Fatal(err)
		}
		return errors.New("permission denied")
	}
	_ = m.Relock(id, s.WindowID)
	*now = now.Add(59 * time.Second)
	m.Tick()
	if *locks != 1 {
		t.Fatal("retried less than 60 seconds after completion")
	}
	*now = now.Add(time.Second)
	m.Tick()
	if *locks != 2 || alerts != 1 {
		t.Fatalf("locks=%d alerts=%d", *locks, alerts)
	}
}

func TestMaintenanceNoFilesystemWriteBeforeIntentAndNoEarlyClear(t *testing.T) {
	m, id, _, unlocks, locks := maintenanceFixture(t)
	_, err := m.db.Exec(`CREATE TRIGGER fail_maintenance_intent BEFORE UPDATE OF maintenance_security ON websites BEGIN SELECT RAISE(ABORT,'injected'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Unlock(id, MaintenanceRequest{RequestID: uuid.NewString(), Password: testMaintenancePassword}); err == nil || *unlocks != 0 {
		t.Fatal("unlock without intent")
	}
	_, _ = m.db.Exec(`DROP TRIGGER fail_maintenance_intent`)
	m.Tick()
	s := maintenanceUnlock(t, m, id)
	_, err = m.db.Exec(`CREATE TRIGGER fail_maintenance_close BEFORE UPDATE OF maintenance_security ON websites
	WHEN json_extract(NEW.maintenance_security,'$.window.id') IS NULL BEGIN SELECT RAISE(ABORT,'injected'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Relock(id, s.WindowID); err == nil || *locks != 1 {
		t.Fatal("expected close failure")
	}
	_, state, _, _ := m.load(id)
	if state.Window == nil {
		t.Fatal("premature clear")
	}
}

func TestMaintenanceConcurrentExtensionAndConflictingEntry(t *testing.T) {
	m, id, _, _, _ := maintenanceFixture(t)
	s := maintenanceUnlock(t, m, id)
	if TryAcquireSiteOpLock(id, "update") {
		ReleaseSiteOpLock(id)
		t.Fatal("update entered active window")
	}
	r := MaintenanceRequest{WindowID: s.WindowID, RequestID: uuid.NewString(), Minutes: 1}
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if m.Extend(id, r) == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	updated, _ := m.Status(id)
	if successes.Load() == 0 || updated.ExpiresAt != s.ExpiresAt+60 {
		t.Fatal(updated)
	}
	if err := m.Relock(id, s.WindowID); err != nil {
		t.Fatal(err)
	}
	s2 := maintenanceUnlock(t, m, id)
	if err := m.Relock(id, s.WindowID); err == nil {
		t.Fatal("old window closed new window")
	}
	_, private, _, _ := m.load(id)
	if private.Window.ID != s2.WindowID || strings.Contains(private.Hash, testMaintenancePassword) {
		t.Fatal("identity or secret failure")
	}
}
