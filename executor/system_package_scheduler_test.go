package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

func TestCountAPTUpgradablePackages(t *testing.T) {
	got := countAPTUpgradablePackages("Listing... Done\nphp8.3-fpm/stable 8.3.36 amd64 [upgradable from: 8.3.35]\nnginx/stable 1.26 amd64 [upgradable from: 1.25]\n")
	if got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
}

func TestSystemPackageUpdateCommandOutputKeepsStderrOutOfCount(t *testing.T) {
	out, err := systemPackageUpdateCommandOutput("sh", "-c",
		"echo php8.3-fpm/stable 8.3.36 amd64; echo 'W: target http://deb.debian.org/debian configured multiple times' 1>&2")
	if err != nil {
		t.Fatal(err)
	}
	if got := countAPTUpgradablePackages(out); got != 1 {
		t.Fatalf("upgradable count = %d, want 1; stderr warnings must not be counted", got)
	}
}

func TestSystemPackageAutoCheckDailyRefreshAndOptIn(t *testing.T) {
	setupWPPackageSchedulerTestDB(t)
	cfg := &config.Config{}
	cfg.Panel.DataDir = t.TempDir()
	oldCommand, oldOutput, oldStart := systemPackageUpdateCommand, systemPackageUpdateCommandOutput, startSystemPackageUpdate
	t.Cleanup(func() {
		systemPackageUpdateCommand, systemPackageUpdateCommandOutput, startSystemPackageUpdate = oldCommand, oldOutput, oldStart
	})
	refreshes, starts := 0, 0
	systemPackageUpdateCommand = func(_ context.Context, name string, args ...string) error {
		if name != "apt-get" || len(args) != 1 || args[0] != "update" {
			t.Fatalf("unexpected refresh command: %s %v", name, args)
		}
		refreshes++
		return nil
	}
	systemPackageUpdateCommandOutput = func(string, ...string) (string, error) {
		return "Listing... Done\nphp8.3-fpm/stable 8.3.36 amd64 [upgradable from: 8.3.35]\n", nil
	}
	startSystemPackageUpdate = func(*config.Config) (SystemPackageUpdateStatus, error) {
		starts++
		return SystemPackageUpdateStatus{Status: "running"}, nil
	}
	setSecuritySetting("system_package_auto_update_enabled", "false")
	setSecuritySetting("system_package_auto_check_at", "")
	runSystemPackageAutoCheck(cfg)
	if refreshes != 1 || starts != 0 || readSecuritySetting("system_package_auto_check_status") != "updates_available" {
		t.Fatalf("refreshes=%d starts=%d status=%q", refreshes, starts, readSecuritySetting("system_package_auto_check_status"))
	}
	setSecuritySetting("system_package_auto_update_enabled", "true")
	setSecuritySetting("system_package_auto_check_at", "")
	runSystemPackageAutoCheck(cfg)
	if refreshes != 2 || starts != 1 || readSecuritySetting("system_package_auto_check_status") != "update_started" {
		t.Fatalf("refreshes=%d starts=%d status=%q", refreshes, starts, readSecuritySetting("system_package_auto_check_status"))
	}
}

func TestSystemPackageAutoCheckFailureIsNotUpToDate(t *testing.T) {
	setupWPPackageSchedulerTestDB(t)
	cfg := &config.Config{}
	cfg.Panel.DataDir = t.TempDir()
	oldCommand := systemPackageUpdateCommand
	t.Cleanup(func() { systemPackageUpdateCommand = oldCommand })
	systemPackageUpdateCommand = func(context.Context, string, ...string) error { return errors.New("mirror unavailable") }
	setSecuritySetting("system_package_auto_check_at", "")
	runSystemPackageAutoCheck(cfg)
	if got := readSecuritySetting("system_package_auto_check_status"); got != "failed" {
		t.Fatalf("status=%q, want failed", got)
	}
	if got := readSecuritySetting("system_package_auto_check_error"); got == "" {
		t.Fatal("expected failure reason")
	}
}

func TestSystemPackageAutoCheckDueUsesPersistedTime(t *testing.T) {
	setupWPPackageSchedulerTestDB(t)
	now := time.Now().UTC()
	setSecuritySetting("system_package_auto_check_at", now.Add(-23*time.Hour).Format(time.RFC3339))
	if systemPackageAutoCheckDue(now) {
		t.Fatal("check should not be due before 24 hours")
	}
	setSecuritySetting("system_package_auto_check_at", now.Add(-25*time.Hour).Format(time.RFC3339))
	if !systemPackageAutoCheckDue(now) {
		t.Fatal("check should be due after 24 hours")
	}
}
