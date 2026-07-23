package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestWPPluginUpdateScopeUsesDeterministicServiceAndBudgets(t *testing.T) {
	var name string
	var args []string
	run := func(_ context.Context, command string, commandArgs ...string) ([]byte, error) {
		name, args = command, append([]string(nil), commandArgs...)
		return nil, nil
	}
	scope, err := newWPPluginUpdateScope("/usr/bin/systemd-run", "/usr/bin/systemctl", "/usr/sbin/runuser", run)
	if err != nil {
		t.Fatal(err)
	}
	taskID := "wpu_0123456789abcdef0123456789abcdef"
	if err := scope.Run(context.Background(), taskID, "-u", "wp_site", "--", "/usr/bin/php", "-v"); err != nil {
		t.Fatal(err)
	}
	want := []string{"--quiet", "--wait", "--collect", "--unit=wp-panel-plugin-0123456789abcdef0123456789abcdef.service", "--property=RuntimeMaxSec=540s", "--property=TimeoutStopSec=10s", "--property=KillMode=control-group", "--", "/usr/sbin/runuser", "-u", "wp_site", "--", "/usr/bin/php", "-v"}
	if name != "/usr/bin/systemd-run" || !reflect.DeepEqual(args, want) {
		t.Fatalf("command=%q args=%v", name, args)
	}
}

func TestWPPluginUpdateScopeRejectsInvalidTaskAndHidesCommandFailure(t *testing.T) {
	run := func(_ context.Context, command string, _ ...string) ([]byte, error) {
		if strings.HasSuffix(command, "systemd-run") {
			return []byte("secret runner output"), errors.New("exit 1")
		}
		return []byte("LoadState=not-found\nActiveState=inactive\nSubState=dead\nResult=failed\nMainPID=0\n"), nil
	}
	scope, _ := newWPPluginUpdateScope("/usr/bin/systemd-run", "/usr/bin/systemctl", "/usr/sbin/runuser", run)
	if err := scope.Run(context.Background(), "bad", "x"); err == nil {
		t.Fatal("invalid task accepted")
	}
	err := scope.Run(context.Background(), "wpu_0123456789abcdef0123456789abcdef", "x")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("scope error=%v", err)
	}
}

func TestWPPluginUpdateScopeWaitsForUnitAfterClientFailure(t *testing.T) {
	inspections := 0
	run := func(_ context.Context, command string, _ ...string) ([]byte, error) {
		if strings.HasSuffix(command, "systemd-run") {
			return nil, errors.New("client disappeared")
		}
		inspections++
		active := "active"
		sub := "running"
		pid := "123"
		if inspections >= 2 {
			active, sub, pid = "inactive", "dead", "0"
		}
		return []byte("LoadState=loaded\nActiveState=" + active + "\nSubState=" + sub + "\nResult=failed\nMainPID=" + pid + "\n"), nil
	}
	scope, _ := newWPPluginUpdateScope("/usr/bin/systemd-run", "/usr/bin/systemctl", "/usr/sbin/runuser", run)
	err := scope.Run(context.Background(), "wpu_0123456789abcdef0123456789abcdef", "x")
	if err == nil || errors.Is(err, errWPPluginScopeSupervisionUncertain) || inspections != 2 {
		t.Fatalf("error=%v inspections=%d", err, inspections)
	}
}

func TestWPPluginUpdateScopeReportsUncertainWhenUnitCannotBeObserved(t *testing.T) {
	run := func(_ context.Context, command string, _ ...string) ([]byte, error) {
		if strings.HasSuffix(command, "systemd-run") {
			return nil, errors.New("client disappeared")
		}
		return nil, errors.New("systemctl unavailable")
	}
	scope, _ := newWPPluginUpdateScope("/usr/bin/systemd-run", "/usr/bin/systemctl", "/usr/sbin/runuser", run)
	// The wrapper intentionally ignores caller cancellation while proving the
	// unit stopped; use an already elapsed internal budget by calling wait
	// directly to lock the fail-closed result without a long test.
	waitCtx, waitCancel := context.WithCancel(context.Background())
	waitCancel()
	if err := scope.waitInactive(waitCtx, "wpu_0123456789abcdef0123456789abcdef"); !errors.Is(err, errWPPluginScopeSupervisionUncertain) {
		t.Fatalf("wait error=%v", err)
	}
}

func TestWPPluginUpdateScopeInspectsUnitState(t *testing.T) {
	run := func(_ context.Context, command string, _ ...string) ([]byte, error) {
		if command != "/usr/bin/systemctl" {
			t.Fatalf("command=%q", command)
		}
		return []byte("LoadState=loaded\nActiveState=active\nSubState=running\nResult=success\nMainPID=123\n"), nil
	}
	scope, _ := newWPPluginUpdateScope("/usr/bin/systemd-run", "/usr/bin/systemctl", "/usr/sbin/runuser", run)
	state, err := scope.Inspect(context.Background(), "wpu_0123456789abcdef0123456789abcdef")
	if err != nil || state.LoadState != "loaded" || state.ActiveState != "active" || state.SubState != "running" || state.MainPID != 123 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestWPPluginUpdateJournalAcceptsOnlyFixedCompleteCheckpoints(t *testing.T) {
	taskDir := t.TempDir()
	name, err := createWPPluginUpdateJournal(taskDir, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat type=%T", info.Sys())
	}
	if info.Mode().Perm() != 0640 || int(stat.Uid) != os.Getuid() || int(stat.Gid) != os.Getgid() {
		t.Fatalf("mode=%v uid=%d gid=%d", info.Mode().Perm(), stat.Uid, stat.Gid)
	}
	if err := os.WriteFile(name, []byte("before_upgrade\nupgrader_entered\nupgrader_returned\n"), 0640); err != nil {
		t.Fatal(err)
	}
	report, err := readWPPluginUpdateJournal(taskDir)
	want := []string{"before_upgrade", "upgrader_entered", "upgrader_returned"}
	if err != nil || report.Truncated || !reflect.DeepEqual(report.Checkpoints, want) {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if err := os.WriteFile(name, []byte("before_upgrade\nsecret=/var/www/site\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := readWPPluginUpdateJournal(taskDir); err == nil {
		t.Fatal("unknown journal entry accepted")
	}
	if err := os.WriteFile(name, []byte("before_upgrade\nupgrader_ent"), 0640); err != nil {
		t.Fatal(err)
	}
	report, err = readWPPluginUpdateJournal(taskDir)
	if err != nil || !report.Truncated || !reflect.DeepEqual(report.Checkpoints, []string{"before_upgrade"}) {
		t.Fatalf("truncated report=%+v err=%v", report, err)
	}
}

func TestWPPluginUpdateJournalRejectsSymlinkAndOversize(t *testing.T) {
	taskDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("before_upgrade\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(taskDir, wpPluginJournalName)); err != nil {
		t.Fatal(err)
	}
	if _, err := readWPPluginUpdateJournal(taskDir); err == nil {
		t.Fatal("journal symlink accepted")
	}
	if err := os.Remove(filepath.Join(taskDir, wpPluginJournalName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, wpPluginJournalName), make([]byte, wpPluginJournalMax+1), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := readWPPluginUpdateJournal(taskDir); err == nil {
		t.Fatal("oversize journal accepted")
	}
}
