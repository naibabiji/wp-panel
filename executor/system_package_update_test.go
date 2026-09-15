package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunSystemPackageUpdatePlanCompletesAllChecks(t *testing.T) {
	tempDir := t.TempDir()
	planPath := filepath.Join(tempDir, "plan.json")
	statusPath := filepath.Join(tempDir, "status.json")
	plan := systemPackageUpdatePlan{ID: "test", StatusPath: statusPath, PlanPath: planPath}
	if err := writePanelDBRestoreJSON(planPath, plan); err != nil {
		t.Fatal(err)
	}

	oldCommand := systemPackageUpdateCommand
	oldLockPath := systemPackageUpdateLockPath
	t.Cleanup(func() {
		systemPackageUpdateCommand = oldCommand
		systemPackageUpdateLockPath = oldLockPath
	})
	systemPackageUpdateLockPath = filepath.Join(tempDir, "update.lock")
	var calls []string
	systemPackageUpdateCommand = func(_ context.Context, name string, args ...string) error {
		calls = append(calls, name+" "+joinSystemPackageUpdateArgs(args))
		return nil
	}

	if err := RunSystemPackageUpdatePlan(planPath); err != nil {
		t.Fatal(err)
	}
	status := readSystemPackageUpdateStatusFile(t, statusPath)
	if status.Status != "success" || status.Stage != "complete" {
		t.Fatalf("unexpected status: %+v", status)
	}
	want := []string{
		"apt-get update",
		"apt-get -s upgrade",
		"env DEBIAN_FRONTEND=noninteractive apt-get -y -o Dpkg::Options::=--force-confold upgrade",
		"apt-get check",
		"dpkg --audit",
		"nginx -t",
		"systemctl is-active --quiet nginx",
		"systemctl is-active --quiet php8.3-fpm",
		"systemctl is-active --quiet mariadb",
		"systemctl is-active --quiet redis-server",
		"systemctl is-active --quiet wp-panel",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("unexpected commands:\n got: %#v\nwant: %#v", calls, want)
	}
}

func TestRunSystemPackageUpdatePlanStopsBeforeUpgradeWhenPreflightFails(t *testing.T) {
	tempDir := t.TempDir()
	planPath := filepath.Join(tempDir, "plan.json")
	statusPath := filepath.Join(tempDir, "status.json")
	if err := writePanelDBRestoreJSON(planPath, systemPackageUpdatePlan{ID: "test", StatusPath: statusPath, PlanPath: planPath}); err != nil {
		t.Fatal(err)
	}

	oldCommand := systemPackageUpdateCommand
	oldLockPath := systemPackageUpdateLockPath
	t.Cleanup(func() {
		systemPackageUpdateCommand = oldCommand
		systemPackageUpdateLockPath = oldLockPath
	})
	systemPackageUpdateLockPath = filepath.Join(tempDir, "update.lock")
	var calls []string
	systemPackageUpdateCommand = func(_ context.Context, name string, args ...string) error {
		call := name + " " + joinSystemPackageUpdateArgs(args)
		calls = append(calls, call)
		if call == "apt-get -s upgrade" {
			return errors.New("dependency failure")
		}
		return nil
	}

	if err := RunSystemPackageUpdatePlan(planPath); err == nil {
		t.Fatal("expected failure")
	}
	status := readSystemPackageUpdateStatusFile(t, statusPath)
	if status.Status != "failed" || status.Stage != "preflight" || status.MessageKey != "settings.system_update_status_failed" {
		t.Fatalf("unexpected status: %+v", status)
	}
	if len(calls) != 2 {
		t.Fatalf("upgrade should not run after failed preflight: %#v", calls)
	}
}

func TestRunSystemPackageUpdatePlanReportsPostUpdateHealthFailure(t *testing.T) {
	tempDir := t.TempDir()
	planPath := filepath.Join(tempDir, "plan.json")
	statusPath := filepath.Join(tempDir, "status.json")
	if err := writePanelDBRestoreJSON(planPath, systemPackageUpdatePlan{ID: "test", StatusPath: statusPath, PlanPath: planPath}); err != nil {
		t.Fatal(err)
	}

	oldCommand := systemPackageUpdateCommand
	oldLockPath := systemPackageUpdateLockPath
	t.Cleanup(func() {
		systemPackageUpdateCommand = oldCommand
		systemPackageUpdateLockPath = oldLockPath
	})
	systemPackageUpdateLockPath = filepath.Join(tempDir, "update.lock")
	systemPackageUpdateCommand = func(_ context.Context, name string, args ...string) error {
		if name == "systemctl" && len(args) == 3 && args[2] == "mariadb" {
			return errors.New("inactive")
		}
		return nil
	}

	if err := RunSystemPackageUpdatePlan(planPath); err == nil {
		t.Fatal("expected failure")
	}
	status := readSystemPackageUpdateStatusFile(t, statusPath)
	if status.Status != "failed" || status.Stage != "services" || status.MessageKey != "settings.system_update_status_health_failed" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func readSystemPackageUpdateStatusFile(t *testing.T, path string) SystemPackageUpdateStatus {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var status SystemPackageUpdateStatus
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func joinSystemPackageUpdateArgs(args []string) string {
	return strings.Join(args, " ")
}
