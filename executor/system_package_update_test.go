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
	"time"

	"github.com/naibabiji/wp-panel/config"
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
	oldPreflight := systemPackageUpdatePreflight
	oldRebootRequired := systemPackageRebootRequired
	oldBootID := systemPackageBootID
	oldLockPath := systemPackageUpdateLockPath
	t.Cleanup(func() {
		systemPackageUpdateCommand = oldCommand
		systemPackageUpdatePreflight = oldPreflight
		systemPackageRebootRequired = oldRebootRequired
		systemPackageBootID = oldBootID
		systemPackageUpdateLockPath = oldLockPath
	})
	systemPackageUpdatePreflight = func(context.Context) (string, error) {
		return "0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n", nil
	}
	systemPackageRebootRequired = func() (bool, error) { return true, nil }
	systemPackageBootID = func() (string, error) { return "boot-before", nil }
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
	if status.Status != "success" || status.Stage != "complete" || !status.RebootRequired || status.BootID != "boot-before" || status.MessageKey != "settings.system_update_status_success_reboot" {
		t.Fatalf("unexpected status: %+v", status)
	}
	want := []string{
		"apt-get update",
		"env LC_ALL=C DEBIAN_FRONTEND=noninteractive apt-get -y --no-remove -o Dpkg::Options::=--force-confold full-upgrade",
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
	oldPreflight := systemPackageUpdatePreflight
	oldLockPath := systemPackageUpdateLockPath
	t.Cleanup(func() {
		systemPackageUpdateCommand = oldCommand
		systemPackageUpdatePreflight = oldPreflight
		systemPackageUpdateLockPath = oldLockPath
	})
	systemPackageUpdateLockPath = filepath.Join(tempDir, "update.lock")
	var calls []string
	systemPackageUpdateCommand = func(_ context.Context, name string, args ...string) error {
		call := name + " " + joinSystemPackageUpdateArgs(args)
		calls = append(calls, call)
		return nil
	}
	systemPackageUpdatePreflight = func(context.Context) (string, error) { return "", errors.New("dependency failure") }

	if err := RunSystemPackageUpdatePlan(planPath); err == nil {
		t.Fatal("expected failure")
	}
	status := readSystemPackageUpdateStatusFile(t, statusPath)
	if status.Status != "failed" || status.Stage != "preflight" || status.MessageKey != "settings.system_update_status_failed" {
		t.Fatalf("unexpected status: %+v", status)
	}
	if len(calls) != 1 {
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
	oldPreflight := systemPackageUpdatePreflight
	oldLockPath := systemPackageUpdateLockPath
	t.Cleanup(func() {
		systemPackageUpdateCommand = oldCommand
		systemPackageUpdatePreflight = oldPreflight
		systemPackageUpdateLockPath = oldLockPath
	})
	systemPackageUpdatePreflight = func(context.Context) (string, error) {
		return "0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n", nil
	}
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

func TestRunSystemPackageUpdatePlanBlocksBeforeRemoval(t *testing.T) {
	tempDir := t.TempDir()
	planPath := filepath.Join(tempDir, "plan.json")
	statusPath := filepath.Join(tempDir, "status.json")
	if err := writePanelDBRestoreJSON(planPath, systemPackageUpdatePlan{ID: "test", StatusPath: statusPath, PlanPath: planPath}); err != nil {
		t.Fatal(err)
	}

	oldCommand := systemPackageUpdateCommand
	oldPreflight := systemPackageUpdatePreflight
	oldLockPath := systemPackageUpdateLockPath
	t.Cleanup(func() {
		systemPackageUpdateCommand = oldCommand
		systemPackageUpdatePreflight = oldPreflight
		systemPackageUpdateLockPath = oldLockPath
	})
	systemPackageUpdateLockPath = filepath.Join(tempDir, "update.lock")
	var calls []string
	systemPackageUpdateCommand = func(_ context.Context, name string, args ...string) error {
		calls = append(calls, name+" "+joinSystemPackageUpdateArgs(args))
		return nil
	}
	systemPackageUpdatePreflight = func(context.Context) (string, error) {
		return "Remv old-kernel [1.0]\nPurg unused-library [2.0]\n0 upgraded, 0 newly installed, 2 to remove and 0 not upgraded.\n", nil
	}

	if err := RunSystemPackageUpdatePlan(planPath); err != nil {
		t.Fatal(err)
	}
	status := readSystemPackageUpdateStatusFile(t, statusPath)
	if status.Status != "blocked" || status.Stage != "preflight" || status.MessageKey != "settings.system_update_status_removal_blocked" {
		t.Fatalf("unexpected status: %+v", status)
	}
	wantPackages := []string{"old-kernel", "unused-library"}
	if !reflect.DeepEqual(status.RemovalPackages, wantPackages) {
		t.Fatalf("unexpected removal packages: got %#v want %#v", status.RemovalPackages, wantPackages)
	}
	if !reflect.DeepEqual(calls, []string{"apt-get update"}) {
		t.Fatalf("upgrade must not run when removals are planned: %#v", calls)
	}
}

func TestParseAPTPreflightRemovals(t *testing.T) {
	output := "Inst package-a [1.0] (2.0 Debian:13)\nRemv package-b [1.0]\nRemv package-b [1.0]\nPurg package-c:amd64 [3.0]\n1 upgraded, 0 newly installed, 2 to remove and 0 not upgraded.\n"
	want := []string{"package-b", "package-c:amd64"}
	got, planned, err := parseAPTPreflightRemovals(output)
	if err != nil || !planned || !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected removals: got %#v want %#v", got, want)
	}
}

func TestParseAPTPreflightRemovalsFailsClosedOnSummaryMismatch(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"missing package line", "0 upgraded, 0 newly installed, 1 to remove and 0 not upgraded.\n"},
		{"missing summary", "Remv package-a [1.0]\n"},
		{"unexpected package line", "Purg package-a [1.0]\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, planned, err := parseAPTPreflightRemovals(tt.output)
			if !planned || err == nil {
				t.Fatalf("planned=%v err=%v", planned, err)
			}
		})
	}
}

func TestDetectSystemPackageRebootRequired(t *testing.T) {
	tests := []struct {
		name      string
		running   string
		installed []string
		greater   map[string]bool
		listErr   error
		want      bool
		wantErr   bool
	}{
		{name: "same version", running: "6.12.111+deb13-cloud-amd64", installed: []string{"6.12.111+deb13-cloud-amd64"}},
		{name: "newer Debian suffix", running: "6.12.90+deb13.1-cloud-amd64", installed: []string{"6.12.90+deb13.1-cloud-amd64", "6.12.111+deb13-cloud-amd64"}, greater: map[string]bool{"6.12.111+deb13-cloud-amd64": true}, want: true},
		{name: "different flavour ignored", running: "6.12.90+deb13.1-cloud-amd64", installed: []string{"6.12.111+deb13-amd64", "6.12.90+deb13.1-cloud-amd64"}, greater: map[string]bool{"6.12.111+deb13-amd64": true}},
		{name: "empty boot", running: "6.12.90+deb13.1-cloud-amd64", wantErr: true},
		{name: "list command failure", running: "6.12.90+deb13.1-cloud-amd64", listErr: errors.New("command failed"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldMarker := systemPackageRebootMarker
			oldRunning := systemPackageRunningKernel
			oldInstalled := systemPackageInstalledKernels
			oldGreater := systemPackageKernelGreater
			t.Cleanup(func() {
				systemPackageRebootMarker = oldMarker
				systemPackageRunningKernel = oldRunning
				systemPackageInstalledKernels = oldInstalled
				systemPackageKernelGreater = oldGreater
			})
			systemPackageRebootMarker = func() (bool, error) { return false, nil }
			systemPackageRunningKernel = func() (string, error) { return tt.running, nil }
			systemPackageInstalledKernels = func() ([]string, error) { return tt.installed, tt.listErr }
			systemPackageKernelGreater = func(candidate, _ string) (bool, error) { return tt.greater[candidate], nil }

			got, err := detectSystemPackageRebootRequired()
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("got=%v err=%v want=%v wantErr=%v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestDetectSystemPackageRebootRequiredReturnsUnknownOnCompareFailure(t *testing.T) {
	oldMarker := systemPackageRebootMarker
	oldRunning := systemPackageRunningKernel
	oldInstalled := systemPackageInstalledKernels
	oldGreater := systemPackageKernelGreater
	t.Cleanup(func() {
		systemPackageRebootMarker = oldMarker
		systemPackageRunningKernel = oldRunning
		systemPackageInstalledKernels = oldInstalled
		systemPackageKernelGreater = oldGreater
	})
	systemPackageRebootMarker = func() (bool, error) { return false, nil }
	systemPackageRunningKernel = func() (string, error) { return "6.12.90+deb13.1-cloud-amd64", nil }
	systemPackageInstalledKernels = func() ([]string, error) { return []string{"6.12.111+deb13-cloud-amd64"}, nil }
	systemPackageKernelGreater = func(string, string) (bool, error) { return false, errors.New("compare failed") }
	if got, err := detectSystemPackageRebootRequired(); got || err == nil {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestRunSystemPackageUpdatePlanKeepsSuccessWhenRebootDetectionIsUnknown(t *testing.T) {
	tempDir := t.TempDir()
	planPath := filepath.Join(tempDir, "plan.json")
	statusPath := filepath.Join(tempDir, "status.json")
	if err := writePanelDBRestoreJSON(planPath, systemPackageUpdatePlan{ID: "test", StatusPath: statusPath, PlanPath: planPath}); err != nil {
		t.Fatal(err)
	}
	oldCommand := systemPackageUpdateCommand
	oldPreflight := systemPackageUpdatePreflight
	oldRebootRequired := systemPackageRebootRequired
	oldLockPath := systemPackageUpdateLockPath
	t.Cleanup(func() {
		systemPackageUpdateCommand = oldCommand
		systemPackageUpdatePreflight = oldPreflight
		systemPackageRebootRequired = oldRebootRequired
		systemPackageUpdateLockPath = oldLockPath
	})
	systemPackageUpdateCommand = func(context.Context, string, ...string) error { return nil }
	systemPackageUpdatePreflight = func(context.Context) (string, error) {
		return "0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n", nil
	}
	systemPackageRebootRequired = func() (bool, error) { return false, errors.New("unknown") }
	systemPackageUpdateLockPath = filepath.Join(tempDir, "update.lock")

	if err := RunSystemPackageUpdatePlan(planPath); err != nil {
		t.Fatal(err)
	}
	status := readSystemPackageUpdateStatusFile(t, statusPath)
	if status.Status != "success" || status.RebootRequired || status.MessageKey != "settings.system_update_status_success" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestReconcileSystemPackageUpdateStatusClearsRebootAfterBootChanges(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{Panel: config.PanelConfig{DataDir: dataDir}}
	statusPath := SystemPackageUpdateStatusPath(cfg)
	before := SystemPackageUpdateStatus{
		ID: "test", Status: "success", Stage: "complete",
		MessageKey: "settings.system_update_status_success_reboot",
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339), RebootRequired: true, BootID: "boot-before",
	}
	if err := writePanelDBRestoreJSON(statusPath, before); err != nil {
		t.Fatal(err)
	}
	oldBootID := systemPackageBootID
	t.Cleanup(func() { systemPackageBootID = oldBootID })
	systemPackageBootID = func() (string, error) { return "boot-after", nil }

	got := ReconcileSystemPackageUpdateStatus(cfg)
	if got.RebootRequired || got.BootID != "" || got.MessageKey != "settings.system_update_status_success" {
		t.Fatalf("unexpected reconciled status: %+v", got)
	}
	persisted := readSystemPackageUpdateStatusFile(t, statusPath)
	if persisted.RebootRequired || persisted.MessageKey != "settings.system_update_status_success" {
		t.Fatalf("unexpected persisted status: %+v", persisted)
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
