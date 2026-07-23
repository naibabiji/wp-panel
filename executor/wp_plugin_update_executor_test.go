package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type fakeWPPluginUpdateOperations struct {
	active bool
	fail   map[string]error
	calls  []string
	hook   map[string]func()
}

func (f *fakeWPPluginUpdateOperations) call(name string) error {
	f.calls = append(f.calls, name)
	if hook := f.hook[name]; hook != nil {
		hook()
	}
	return f.fail[name]
}

func (f *fakeWPPluginUpdateOperations) Prepare(context.Context, wpPluginUpdateExecution) (bool, error) {
	return f.active, f.call("prepare")
}
func (f *fakeWPPluginUpdateOperations) Unlock(context.Context, wpPluginUpdateExecution) error {
	return f.call("unlock")
}
func (f *fakeWPPluginUpdateOperations) ApplyPluginUpdate(context.Context, wpPluginUpdateExecution) error {
	return f.call("update")
}
func (f *fakeWPPluginUpdateOperations) ReactivatePlugin(context.Context, wpPluginUpdateExecution) error {
	return f.call("reactivate")
}
func (f *fakeWPPluginUpdateOperations) CheckTargetHealth(_ context.Context, _ wpPluginUpdateExecution, active bool) error {
	if active != f.active {
		return errors.New("active expectation changed")
	}
	return f.call("target_health")
}
func (f *fakeWPPluginUpdateOperations) SetMaintenance(_ context.Context, _ wpPluginUpdateExecution, enabled bool) error {
	if enabled {
		return f.call("maintenance_on")
	}
	return f.call("maintenance_off")
}
func (f *fakeWPPluginUpdateOperations) RestoreDatabase(context.Context, wpPluginUpdateExecution) error {
	return f.call("restore_database")
}
func (f *fakeWPPluginUpdateOperations) RestorePluginFiles(context.Context, wpPluginUpdateExecution) error {
	return f.call("restore_plugin")
}
func (f *fakeWPPluginUpdateOperations) CheckRollbackHealth(context.Context, wpPluginUpdateExecution) error {
	return f.call("rollback_health")
}
func (f *fakeWPPluginUpdateOperations) RestoreFileLock(context.Context, wpPluginUpdateExecution) error {
	return f.call("restore_lock")
}

func TestWPPluginUpdateExecutorActiveSuccess(t *testing.T) {
	executor, store, task, ops := preparePluginExecutorTask(t, true)
	if err := executor.Execute(context.Background(), task.ID, "worker-plugin"); err != nil {
		t.Fatal(err)
	}
	want := []string{"prepare", "unlock", "update", "reactivate", "target_health", "restore_lock"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls=%v want=%v", ops.calls, want)
	}
	finished, err := store.getTask(context.Background(), task.ID)
	if err != nil || finished.Status != wpUpdateSuccess || finished.RollbackStatus != "not_required" {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
	var evidence int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_events
		WHERE task_id=? AND stage='prepare' AND error_code='plugin_observed_active'`, task.ID).Scan(&evidence); err != nil || evidence != 1 {
		t.Fatalf("active evidence=%d err=%v", evidence, err)
	}
}

func TestWPPluginUpdateExecutorInactiveNeverReactivates(t *testing.T) {
	executor, store, task, ops := preparePluginExecutorTask(t, false)
	if err := executor.Execute(context.Background(), task.ID, "worker-plugin"); err != nil {
		t.Fatal(err)
	}
	want := []string{"prepare", "unlock", "update", "target_health", "restore_lock"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls=%v want=%v", ops.calls, want)
	}
	var evidence int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_events
		WHERE task_id=? AND stage='prepare' AND error_code='plugin_observed_inactive'`, task.ID).Scan(&evidence); err != nil || evidence != 1 {
		t.Fatalf("inactive evidence=%d err=%v", evidence, err)
	}
}

func TestWPThemeUpdateExecutorNeverRunsPluginReactivation(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	seedThemeUpdateCandidate(t, store, siteID, "sample-theme", "1.0.0", "1.1.0", "collection-theme-executor")
	webRoot := filepath.Join(t.TempDir(), "wordpress")
	themeRoot := filepath.Join(webRoot, "wp-content", "themes", "sample-theme")
	if err := os.MkdirAll(themeRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeRoot, "style.css"), []byte("/*\nTheme Name: Sample\nVersion: 1.0.0\n*/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE websites SET web_root=?,db_name='wordpress_db' WHERE id=?`, webRoot, siteID); err != nil {
		t.Fatal(err)
	}
	task, err := store.createThemeManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, ComponentKey: "sample-theme", CurrentVersion: "1.0.0", TargetVersion: "1.1.0",
		PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/theme/sample-theme.1.1.0.zip",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	service, err := newWPUpdateArtifactService(store, filepath.Join(t.TempDir(), "artifacts"), fakeUpdateDump)
	if err != nil {
		t.Fatal(err)
	}
	source := writeThemePackageFixture(t, "sample-theme", "1.1.0", "")
	digest, _, _ := hashRegularFile(source)
	task, _, err = service.snapshotValidateAndSealThemePackage(context.Background(), task.ID, source, digest, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err = service.validateAndClaimThemeUpdate(context.Background(), task.ID, "worker-theme", "1.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.prepareThemeBackups(context.Background(), task.ID, "worker-theme"); err != nil {
		t.Fatal(err)
	}
	ops := &fakeWPPluginUpdateOperations{active: false, fail: map[string]error{}, hook: map[string]func(){}}
	executor, err := newWPThemeUpdateExecutor(store, service.root, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Execute(context.Background(), task.ID, "worker-theme"); err != nil {
		t.Fatal(err)
	}
	want := []string{"prepare", "unlock", "update", "target_health", "restore_lock"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls=%v want=%v", ops.calls, want)
	}
	finished, err := store.getTask(context.Background(), task.ID)
	if err != nil || finished.Status != wpUpdateSuccess || finished.ComponentType != "theme" {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
}

func TestWPPluginUpdateExecutorUpdateFailureRollsBack(t *testing.T) {
	executor, store, task, ops := preparePluginExecutorTask(t, true)
	ops.fail["update"] = errors.New("injected update failure")
	if err := executor.Execute(context.Background(), task.ID, "worker-plugin"); err == nil {
		t.Fatal("expected update failure")
	}
	want := []string{"prepare", "unlock", "update", "maintenance_on", "restore_database", "restore_plugin", "maintenance_off", "rollback_health", "restore_lock"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls=%v want=%v", ops.calls, want)
	}
	finished, err := store.getTask(context.Background(), task.ID)
	if err != nil || finished.Status != wpUpdateFailed || finished.RollbackStatus != "success" || finished.RequiresAttention {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
}

func TestWPPluginUpdateExecutorSupervisionUncertainStopsWithoutRollback(t *testing.T) {
	executor, store, task, ops := preparePluginExecutorTask(t, true)
	ops.fail["update"] = errWPPluginScopeSupervisionUncertain
	if err := executor.Execute(context.Background(), task.ID, "worker-plugin"); !errors.Is(err, errWPPluginScopeSupervisionUncertain) {
		t.Fatalf("execute error=%v", err)
	}
	if want := []string{"prepare", "unlock", "update"}; !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls=%v want=%v", ops.calls, want)
	}
	current, err := store.getTask(context.Background(), task.ID)
	if err != nil || current.Status != wpUpdateInterrupted || !current.RequiresAttention || current.LeaseOwner != "" || current.RollbackStatus != "not_required" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	var evidence int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_events
		WHERE task_id=? AND error_code='runner_supervision_uncertain'`, task.ID).Scan(&evidence); err != nil || evidence != 1 {
		t.Fatalf("evidence=%d err=%v", evidence, err)
	}
}

func TestWPUpdateStoreRejectsForgedAndDeduplicatesPluginJournal(t *testing.T) {
	_, store, task, _ := preparePluginExecutorTask(t, true)
	bad := wpPluginUpdateJournalReport{Checkpoints: []string{"upgrader_returned"}}
	if err := store.recordPluginRunnerJournal(context.Background(), task.ID, "worker-plugin", bad, time.Now()); err == nil {
		t.Fatal("forged checkpoint order was accepted")
	}
	report := wpPluginUpdateJournalReport{Checkpoints: []string{"before_upgrade", "upgrader_entered"}, Truncated: true}
	for i := 0; i < 2; i++ {
		if err := store.recordPluginRunnerJournal(context.Background(), task.ID, "worker-plugin", report, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_events
		WHERE task_id=? AND stage='runner_journal'`, task.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestWPPluginUpdateExecutorRollbackFailureRequiresAttention(t *testing.T) {
	executor, store, task, ops := preparePluginExecutorTask(t, true)
	ops.fail["target_health"] = errors.New("injected health failure")
	ops.fail["restore_plugin"] = errors.New("injected restore failure")
	if err := executor.Execute(context.Background(), task.ID, "worker-plugin"); err == nil {
		t.Fatal("expected rollback failure")
	}
	finished, err := store.getTask(context.Background(), task.ID)
	if err != nil || finished.Status != wpUpdateFailed || finished.RollbackStatus != "failed" || !finished.RequiresAttention {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
}

func TestWPPluginUpdateExecutorOwnershipLossDoesNotStartNextSubstage(t *testing.T) {
	executor, store, task, ops := preparePluginExecutorTask(t, true)
	ops.hook["update"] = func() {
		if _, err := store.db.Exec(`UPDATE wp_update_tasks SET lease_owner='replacement-owner' WHERE id=?`, task.ID); err != nil {
			t.Errorf("replace owner: %v", err)
		}
	}
	if err := executor.Execute(context.Background(), task.ID, "worker-plugin"); err == nil {
		t.Fatal("expected ownership loss")
	}
	want := []string{"prepare", "unlock", "update"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls=%v want=%v", ops.calls, want)
	}
	current, err := store.getTask(context.Background(), task.ID)
	if err != nil || current.Status != wpUpdateRunning || current.LeaseOwner != "replacement-owner" || current.RollbackStatus != "not_required" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func TestWPPluginUpdateExecutorRollbackOwnershipLossStopsAtBoundary(t *testing.T) {
	executor, store, task, ops := preparePluginExecutorTask(t, true)
	ops.fail["update"] = errors.New("injected update failure")
	ops.hook["maintenance_on"] = func() {
		if _, err := store.db.Exec(`UPDATE wp_update_tasks SET lease_owner='replacement-owner' WHERE id=?`, task.ID); err != nil {
			t.Errorf("replace owner: %v", err)
		}
	}
	if err := executor.Execute(context.Background(), task.ID, "worker-plugin"); err == nil {
		t.Fatal("expected rollback ownership loss")
	}
	want := []string{"prepare", "unlock", "update", "maintenance_on"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls=%v want=%v", ops.calls, want)
	}
	current, err := store.getTask(context.Background(), task.ID)
	if err != nil || current.Status != wpUpdateRunning || current.LeaseOwner != "replacement-owner" || current.RollbackStatus != "pending" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func TestWPPluginUpdateExecutorCancellationAfterWriteBecomesInterrupted(t *testing.T) {
	executor, store, task, ops := preparePluginExecutorTask(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	ops.hook["reactivate"] = cancel
	if err := executor.Execute(ctx, task.ID, "worker-plugin"); !errors.Is(err, context.Canceled) {
		t.Fatalf("execute error=%v", err)
	}
	want := []string{"prepare", "unlock", "update", "reactivate"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls=%v want=%v", ops.calls, want)
	}
	current, err := store.getTask(context.Background(), task.ID)
	if err != nil || current.Status != wpUpdateInterrupted || !current.RequiresAttention || current.LeaseOwner != "" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func TestWPPluginUpdateExecutorCancellationDuringRollbackStillFinishes(t *testing.T) {
	executor, store, task, ops := preparePluginExecutorTask(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	ops.fail["update"] = errors.New("injected update failure")
	ops.hook["restore_database"] = cancel
	if err := executor.Execute(ctx, task.ID, "worker-plugin"); err == nil {
		t.Fatal("expected rolled back update failure")
	}
	want := []string{"prepare", "unlock", "update", "maintenance_on", "restore_database", "restore_plugin", "maintenance_off", "rollback_health", "restore_lock"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls=%v want=%v", ops.calls, want)
	}
	current, err := store.getTask(context.Background(), task.ID)
	if err != nil || current.Status != wpUpdateFailed || current.RollbackStatus != "success" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func preparePluginExecutorTask(t *testing.T, active bool) (*wpPluginUpdateExecutor, *wpUpdateStore, WPUpdateTask, *fakeWPPluginUpdateOperations) {
	t.Helper()
	service, store, task, webRoot := prepareRunningPluginArtifactTask(t, fakeUpdateDump)
	pluginRoot := filepath.Join(webRoot, "wp-content", "plugins", "sample")
	if err := writePluginDirectoryFixture(pluginRoot); err != nil {
		t.Fatal(err)
	}
	if err := service.preparePluginBackups(context.Background(), task.ID, "worker-plugin"); err != nil {
		t.Fatal(err)
	}
	task, err := store.getTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	ops := &fakeWPPluginUpdateOperations{active: active, fail: map[string]error{}, hook: map[string]func(){}}
	executor, err := newWPPluginUpdateExecutor(store, service.root, ops)
	if err != nil {
		t.Fatal(err)
	}
	return executor, store, task, ops
}

func writePluginDirectoryFixture(root string) error {
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "sample.php"), []byte("<?php /* Plugin Name: Sample */"), 0644)
}
