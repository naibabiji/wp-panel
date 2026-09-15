package executor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRunPanelDBRestorePlanStopsBeforeReplaceAndReportsSuccess(t *testing.T) {
	root := t.TempDir()
	plan := panelDBRestoreTestPlan(t, root, "current", "selected", "safe")
	writeTestRestorePlan(t, plan)
	writeTestFile(t, plan.DBPath+"-wal", "wal")
	writeTestFile(t, plan.DBPath+"-shm", "shm")

	var commands []string
	restorePanelDBRestoreHooks(t, func(name string, args ...string) error {
		commands = append(commands, name+" "+args[0]+" "+args[1])
		return nil
	}, func(string, time.Duration) error { return nil })
	if err := RunPanelDBRestorePlan(plan.PlanPath); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, plan.DBPath); got != "selected" {
		t.Fatalf("restored database = %q", got)
	}
	if _, err := os.Stat(plan.DBPath + "-wal"); !os.IsNotExist(err) {
		t.Fatal("WAL sidecar was not removed")
	}
	if want := []string{"systemctl stop wp-panel", "systemctl start wp-panel"}; !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	if status := readTestRestoreStatus(t, plan.StatusPath); status.Status != "success" {
		t.Fatalf("status = %#v", status)
	}
}

func TestRunPanelDBRestorePlanRollsBackAfterHealthFailure(t *testing.T) {
	root := t.TempDir()
	plan := panelDBRestoreTestPlan(t, root, "current", "selected", "safe")
	writeTestRestorePlan(t, plan)
	checks := 0
	restorePanelDBRestoreHooks(t, func(string, ...string) error { return nil }, func(string, time.Duration) error {
		checks++
		if checks == 1 {
			return errors.New("unhealthy restored database")
		}
		return nil
	})
	if err := RunPanelDBRestorePlan(plan.PlanPath); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, plan.DBPath); got != "safe" {
		t.Fatalf("rolled back database = %q", got)
	}
	if status := readTestRestoreStatus(t, plan.StatusPath); status.Status != "rolled_back" {
		t.Fatalf("status = %#v", status)
	}
}

func TestRunPanelDBRestorePlanReportsRollbackFailure(t *testing.T) {
	root := t.TempDir()
	plan := panelDBRestoreTestPlan(t, root, "current", "selected", "safe")
	writeTestRestorePlan(t, plan)
	if err := os.Remove(plan.SafeBackup); err != nil {
		t.Fatal(err)
	}
	restorePanelDBRestoreHooks(t, func(string, ...string) error { return nil }, func(string, time.Duration) error {
		return errors.New("unhealthy")
	})
	if err := RunPanelDBRestorePlan(plan.PlanPath); err == nil {
		t.Fatal("RunPanelDBRestorePlan error = nil, want rollback failure")
	}
	if status := readTestRestoreStatus(t, plan.StatusPath); status.Status != "failed" {
		t.Fatalf("status = %#v", status)
	}
}

func panelDBRestoreTestPlan(t *testing.T, root, current, selected, safe string) panelDBRestorePlan {
	t.Helper()
	plan := panelDBRestorePlan{
		ID: "test", DBPath: filepath.Join(root, "panel.db"), BackupPath: filepath.Join(root, "selected.db"),
		SafeBackup: filepath.Join(root, "safe.db"), StatusPath: filepath.Join(root, "status.json"),
		PlanPath: filepath.Join(root, "plan.json"), HealthURL: "http://127.0.0.1/healthz",
	}
	writeTestFile(t, plan.DBPath, current)
	writeTestFile(t, plan.BackupPath, selected)
	writeTestFile(t, plan.SafeBackup, safe)
	return plan
}

func restorePanelDBRestoreHooks(t *testing.T, command func(string, ...string) error, wait func(string, time.Duration) error) {
	t.Helper()
	oldCommand, oldWait := panelDBRestoreCommand, panelDBRestoreWaitHealth
	panelDBRestoreCommand, panelDBRestoreWaitHealth = command, wait
	t.Cleanup(func() { panelDBRestoreCommand, panelDBRestoreWaitHealth = oldCommand, oldWait })
}

func writeTestRestorePlan(t *testing.T, plan panelDBRestorePlan) {
	t.Helper()
	if err := writePanelDBRestoreJSON(plan.PlanPath, plan); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func readTestRestoreStatus(t *testing.T, path string) PanelDBRestoreStatus {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var status PanelDBRestoreStatus
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatal(err)
	}
	return status
}
