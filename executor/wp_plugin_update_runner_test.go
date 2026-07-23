package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type fakeWPPluginScope struct {
	run  func(context.Context, string, ...string) error
	args [][]string
}

func (s *fakeWPPluginScope) Run(ctx context.Context, taskID string, args ...string) error {
	s.args = append(s.args, append([]string{taskID}, args...))
	return s.run(ctx, taskID, args...)
}

func TestWPPluginPHPRunnerSessionProtocol(t *testing.T) {
	execution, opts, scope := prepareWPPluginRunnerTest(t)
	runner, err := newWPPluginPHPRunner(opts)
	if err != nil {
		t.Fatal(err)
	}
	session, err := runner.Prepare(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	active, err := session.Observe(context.Background())
	if err != nil || !active {
		t.Fatalf("active=%v err=%v", active, err)
	}
	if err := session.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.Reactivate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.Check(context.Background(), execution.Task.TargetVersion, true); err != nil {
		t.Fatal(err)
	}
	report, err := session.Journal()
	want := []string{"before_upgrade", "upgrader_entered", "upgrader_returned", "reactivate_started", "reactivate_completed"}
	if err != nil || report.Truncated || !reflect.DeepEqual(report.Checkpoints, want) {
		t.Fatalf("journal=%+v err=%v", report, err)
	}
	if len(scope.args) != 5 {
		t.Fatalf("scope calls=%d", len(scope.args))
	}
	for _, call := range scope.args {
		joined := strings.Join(call, "\x00")
		for _, required := range []string{"-u\x00wp_test\x00--", opts.envPath, "-i", "display_errors=0", "WP_HTTP_BLOCK_EXTERNAL"} {
			if !strings.Contains(joined, required) {
				t.Fatalf("runner args missing %q", required)
			}
		}
	}
}

func TestWPPluginPHPRunnerPreservesSupervisionUncertain(t *testing.T) {
	execution, opts, scope := prepareWPPluginRunnerTest(t)
	scope.run = func(context.Context, string, ...string) error { return errWPPluginScopeSupervisionUncertain }
	runner, err := newWPPluginPHPRunner(opts)
	if err != nil {
		t.Fatal(err)
	}
	session, err := runner.Prepare(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Update(context.Background()); !errors.Is(err, errWPPluginScopeSupervisionUncertain) {
		t.Fatalf("error=%v", err)
	}
}

func TestWPPluginPHPRunnerUpdateRequiresIndependentCheck(t *testing.T) {
	execution, opts, scope := prepareWPPluginRunnerTest(t)
	baseRun := scope.run
	scope.run = func(ctx context.Context, taskID string, args ...string) error {
		if err := baseRun(ctx, taskID, args...); err != nil {
			return err
		}
		if args[len(args)-10] != "check" {
			return nil
		}
		token := ""
		for _, arg := range args {
			if strings.HasPrefix(arg, "WP_PANEL_RUNNER_TOKEN=") {
				token = strings.TrimPrefix(arg, "WP_PANEL_RUNNER_TOKEN=")
			}
		}
		raw, _ := json.Marshal(wpPluginRunnerEnvelope{Token: token, OK: false, Version: execution.Task.CurrentVersion, ErrorCode: "health_mismatch"})
		return os.WriteFile(args[len(args)-1], raw, 0600)
	}
	runner, err := newWPPluginPHPRunner(opts)
	if err != nil {
		t.Fatal(err)
	}
	session, err := runner.Prepare(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Update(context.Background()); err == nil {
		t.Fatal("update trusted the upgrader process without an independent check")
	}
	if len(scope.args) != 2 || scope.args[0][len(scope.args[0])-10] != "update" || scope.args[1][len(scope.args[1])-10] != "check" {
		t.Fatalf("scope calls=%v", scope.args)
	}
}

func TestWPPluginPHPRunnerRejectsPackageDriftBeforeRuntime(t *testing.T) {
	execution, opts, _ := prepareWPPluginRunnerTest(t)
	execution.Task.TargetVersion = "2.0.1"
	runner, err := newWPPluginPHPRunner(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Prepare(context.Background(), execution); err == nil {
		t.Fatal("version-drifted package accepted")
	}
	entries, err := os.ReadDir(opts.runtimeRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("runtime entries=%v err=%v", entries, err)
	}
}

func TestWPPluginUpdatePHPSourceParses(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is unavailable")
	}
	name := filepath.Join(t.TempDir(), "runner.php")
	if err := os.WriteFile(name, []byte("<?php\n"+wpPluginUpdatePHPSource), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(php, "-d", "display_errors=0", "-l", name).CombinedOutput(); err != nil {
		t.Fatalf("PHP source syntax error: %s", out)
	}
}

func TestRunWPPluginScopeCommandCapsOutput(t *testing.T) {
	head, err := exec.LookPath("head")
	if err != nil {
		t.Skip("head is unavailable")
	}
	if _, err := runWPPluginScopeCommand(context.Background(), head, "-c", strconv.Itoa(wpPluginScopeOutput+1), "/dev/zero"); err == nil || !strings.Contains(err.Error(), "output exceeded") {
		t.Fatalf("error=%v", err)
	}
}

func prepareWPPluginRunnerTest(t *testing.T) (wpPluginUpdateExecution, wpPluginPHPRunnerOptions, *fakeWPPluginScope) {
	t.Helper()
	wwwRoot := t.TempDir()
	webRoot := filepath.Join(wwwRoot, "example.com")
	if err := os.Mkdir(webRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "wp-load.php"), []byte("<?php\n"), 0644); err != nil {
		t.Fatal(err)
	}
	packagePath := writeComponentZIP(t, []componentZIPEntry{{name: "sample/sample.php", body: pluginHeader("Sample", "2.0.0")}})
	sha, _, err := hashRegularFile(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	execution := wpPluginUpdateExecution{Task: WPUpdateTask{
		ID: "wpu_0123456789abcdef0123456789abcdef", ComponentType: "plugin", ComponentKey: "sample/sample.php",
		CurrentVersion: "1.0.0", TargetVersion: "2.0.0", DownloadedSHA256: sha, VerificationLevel: "structure_only",
	}, WebRoot: webRoot, SystemUser: "wp_test", PackagePath: packagePath}
	scope := &fakeWPPluginScope{}
	scope.run = func(_ context.Context, _ string, args ...string) error {
		if len(args) < 10 {
			return errors.New("short runner arguments")
		}
		action := args[len(args)-10]
		current := args[len(args)-5]
		target := args[len(args)-4]
		journal := args[len(args)-2]
		result := args[len(args)-1]
		token := ""
		for _, arg := range args {
			if strings.HasPrefix(arg, "WP_PANEL_RUNNER_TOKEN=") {
				token = strings.TrimPrefix(arg, "WP_PANEL_RUNNER_TOKEN=")
			}
		}
		version, active := target, false
		switch action {
		case "observe":
			version, active = current, true
		case "update":
			appendJournalForTest(t, journal, "before_upgrade", "upgrader_entered", "upgrader_returned")
		case "reactivate":
			active = true
			appendJournalForTest(t, journal, "reactivate_started", "reactivate_completed")
		case "check":
			active = args[len(args)-3] == "active"
		default:
			return errors.New("unknown action")
		}
		raw, _ := json.Marshal(wpPluginRunnerEnvelope{Token: token, OK: true, Version: version, Active: active})
		return os.WriteFile(result, raw, 0600)
	}
	uid, gid := os.Getuid(), os.Getgid()
	binDir := t.TempDir()
	phpPath := filepath.Join(binDir, "php")
	envPath := filepath.Join(binDir, "env")
	runuserPath := filepath.Join(binDir, "runuser")
	for _, name := range []string{phpPath, envPath, runuserPath} {
		if err := os.WriteFile(name, []byte("test binary"), 0555); err != nil {
			t.Fatal(err)
		}
	}
	opts := wpPluginPHPRunnerOptions{
		wwwRoot: wwwRoot, runtimeRoot: t.TempDir(), phpPath: phpPath, envPath: envPath, runuserPath: runuserPath, phpDir: binDir, envDir: binDir, runuserDir: binDir,
		ownerUID: uid, ownerGID: gid, lookupUser: func(string) (*user.User, error) {
			return &user.User{Username: "wp_test", Uid: strconv.Itoa(uid), Gid: strconv.Itoa(gid), HomeDir: t.TempDir()}, nil
		}, chown: func(string, int, int) error { return nil }, scope: scope,
	}
	return execution, opts, scope
}

func appendJournalForTest(t *testing.T, name string, checkpoints ...string) {
	t.Helper()
	f, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, checkpoint := range checkpoints {
		if _, err := f.WriteString(checkpoint + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}
