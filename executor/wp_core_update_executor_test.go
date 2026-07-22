package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type fakeWPCoreUpdateOperations struct {
	calls []string
	fail  map[string]error
}

func (f *fakeWPCoreUpdateOperations) call(name string) error {
	f.calls = append(f.calls, name)
	return f.fail[name]
}
func (f *fakeWPCoreUpdateOperations) Prepare(context.Context, wpCoreUpdateExecution) error {
	return f.call("prepare")
}
func (f *fakeWPCoreUpdateOperations) Unlock(context.Context, wpCoreUpdateExecution) error {
	return f.call("unlock")
}
func (f *fakeWPCoreUpdateOperations) ApplyCoreUpdate(context.Context, wpCoreUpdateExecution) error {
	return f.call("update")
}
func (f *fakeWPCoreUpdateOperations) CheckTargetHealth(context.Context, wpCoreUpdateExecution) error {
	return f.call("target_health")
}
func (f *fakeWPCoreUpdateOperations) SetMaintenance(_ context.Context, _ wpCoreUpdateExecution, enabled bool) error {
	if enabled {
		return f.call("maintenance_on")
	}
	return f.call("maintenance_off")
}
func (f *fakeWPCoreUpdateOperations) RestoreDatabase(context.Context, wpCoreUpdateExecution) error {
	return f.call("restore_database")
}
func (f *fakeWPCoreUpdateOperations) RestoreCoreFiles(context.Context, wpCoreUpdateExecution) error {
	return f.call("restore_core")
}
func (f *fakeWPCoreUpdateOperations) CheckRollbackHealth(context.Context, wpCoreUpdateExecution) error {
	return f.call("rollback_health")
}
func (f *fakeWPCoreUpdateOperations) RestoreFileLock(context.Context, wpCoreUpdateExecution) error {
	return f.call("restore_lock")
}

func TestWPCoreUpdateExecutorSuccess(t *testing.T) {
	exec, store, task, ops := newWPCoreUpdateExecutorTest(t)
	if err := exec.Execute(context.Background(), task.ID, "worker-a"); err != nil {
		t.Fatal(err)
	}
	want := []string{"prepare", "unlock", "update", "target_health", "restore_lock"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls=%v want=%v", ops.calls, want)
	}
	finished, _ := store.getTask(context.Background(), task.ID)
	if finished.Status != wpUpdateSuccess || finished.RollbackStatus != "not_required" || finished.RequiresAttention {
		t.Fatalf("task=%+v", finished)
	}
}

func TestWPCoreUpdateExecutorUpdateFailureRollsBack(t *testing.T) {
	exec, store, task, ops := newWPCoreUpdateExecutorTest(t)
	ops.fail["update"] = errors.New("injected")
	if err := exec.Execute(context.Background(), task.ID, "worker-a"); err == nil {
		t.Fatal("expected update failure")
	}
	want := []string{"prepare", "unlock", "update", "maintenance_on", "restore_database", "restore_core", "maintenance_off", "rollback_health", "restore_lock"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls=%v want=%v", ops.calls, want)
	}
	finished, _ := store.getTask(context.Background(), task.ID)
	if finished.Status != wpUpdateFailed || finished.FailureStage != "core_update" || finished.RollbackStatus != "success" || finished.RequiresAttention {
		t.Fatalf("task=%+v", finished)
	}
}

func TestWPCoreUpdateExecutorHealthFailureRollsBack(t *testing.T) {
	exec, store, task, ops := newWPCoreUpdateExecutorTest(t)
	ops.fail["target_health"] = errors.New("injected")
	if err := exec.Execute(context.Background(), task.ID, "worker-a"); err == nil {
		t.Fatal("expected health failure")
	}
	want := []string{"prepare", "unlock", "update", "target_health", "maintenance_on", "restore_database", "restore_core", "maintenance_off", "rollback_health", "restore_lock"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls=%v want=%v", ops.calls, want)
	}
	finished, _ := store.getTask(context.Background(), task.ID)
	if finished.FailureStage != "health_check" || finished.RollbackStatus != "success" {
		t.Fatalf("task=%+v", finished)
	}
}

func TestWPCoreUpdateExecutorRollbackFailureRequiresAttention(t *testing.T) {
	exec, store, task, ops := newWPCoreUpdateExecutorTest(t)
	ops.fail["update"] = errors.New("injected")
	ops.fail["restore_core"] = errors.New("injected rollback")
	if err := exec.Execute(context.Background(), task.ID, "worker-a"); err == nil {
		t.Fatal("expected rollback failure")
	}
	want := []string{"prepare", "unlock", "update", "maintenance_on", "restore_database", "restore_core", "restore_lock"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls=%v want=%v", ops.calls, want)
	}
	finished, _ := store.getTask(context.Background(), task.ID)
	if finished.RollbackStatus != "failed" || !finished.RequiresAttention || finished.Status != wpUpdateFailed {
		t.Fatalf("task=%+v", finished)
	}
}

func TestWPCoreUpdateExecutorFileLockRestoreFailureRollsBackAndRequiresAttention(t *testing.T) {
	exec, store, task, ops := newWPCoreUpdateExecutorTest(t)
	ops.fail["restore_lock"] = errors.New("injected lock restore failure")
	if err := exec.Execute(context.Background(), task.ID, "worker-a"); err == nil {
		t.Fatal("expected lock restoration failure")
	}
	want := []string{"prepare", "unlock", "update", "target_health", "restore_lock", "maintenance_on", "restore_database", "restore_core", "maintenance_off", "rollback_health", "restore_lock"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls=%v want=%v", ops.calls, want)
	}
	finished, _ := store.getTask(context.Background(), task.ID)
	if finished.FailureStage != "file_lock_restore" || finished.RollbackStatus != "failed" || !finished.RequiresAttention {
		t.Fatalf("task=%+v", finished)
	}
}

func TestWPCoreUpdateExecutorUnlockFailureDoesNotRestoreContent(t *testing.T) {
	exec, store, task, ops := newWPCoreUpdateExecutorTest(t)
	ops.fail["unlock"] = errors.New("injected")
	if err := exec.Execute(context.Background(), task.ID, "worker-a"); err == nil {
		t.Fatal("expected unlock failure")
	}
	want := []string{"prepare", "unlock", "restore_lock"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls=%v want=%v", ops.calls, want)
	}
	finished, _ := store.getTask(context.Background(), task.ID)
	if finished.FailureStage != "unlock" || finished.RollbackStatus != "not_required" {
		t.Fatalf("task=%+v", finished)
	}
}

func TestWPCoreUpdateExecutorRejectsTamperedArtifactBeforeOperations(t *testing.T) {
	exec, _, task, ops := newWPCoreUpdateExecutorTest(t)
	if err := os.WriteFile(task.PackageSnapshotPath, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := exec.Execute(context.Background(), task.ID, "worker-a"); err == nil {
		t.Fatal("expected artifact rejection")
	}
	if len(ops.calls) != 0 {
		t.Fatalf("operations called: %v", ops.calls)
	}
}

func TestWPCoreUpdateExecutorRejectsTamperedBackupBeforeOperations(t *testing.T) {
	exec, _, task, ops := newWPCoreUpdateExecutorTest(t)
	if err := os.WriteFile(filepath.Join(exec.root, task.ID, "core-files.tar.gz"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := exec.Execute(context.Background(), task.ID, "worker-a"); err == nil {
		t.Fatal("expected backup rejection")
	}
	if len(ops.calls) != 0 {
		t.Fatalf("operations called: %v", ops.calls)
	}
}

func TestWPCoreUpdateExecutorRejectsDuplicateInvocationBeforeOperations(t *testing.T) {
	exec, store, task, ops := newWPCoreUpdateExecutorTest(t)
	if err := store.advanceOwnedStage(context.Background(), task.ID, "worker-a", "backups_ready", "unlocking", exec.now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := exec.Execute(context.Background(), task.ID, "worker-a"); err == nil {
		t.Fatal("expected stale-stage rejection")
	}
	if !reflect.DeepEqual(ops.calls, []string{"prepare"}) {
		t.Fatalf("operations called: %v", ops.calls)
	}
}

func newWPCoreUpdateExecutorTest(t *testing.T) (*wpCoreUpdateExecutor, *wpUpdateStore, WPUpdateTask, *fakeWPCoreUpdateOperations) {
	t.Helper()
	service, store, task, webRoot := prepareClaimedArtifactTask(t, fakeUpdateDump)
	writeWordPressCoreFixture(t, webRoot)
	if err := service.prepareCoreBackups(context.Background(), task.ID, "worker-a"); err != nil {
		t.Fatal(err)
	}
	task, err := store.getTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	ops := &fakeWPCoreUpdateOperations{fail: map[string]error{}}
	exec, err := newWPCoreUpdateExecutor(store, service.root, ops)
	if err != nil {
		t.Fatal(err)
	}
	return exec, store, task, ops
}
