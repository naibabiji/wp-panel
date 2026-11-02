package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/naibabiji/wp-panel/config"
)

func setupAIDevelopmentSSHTest(t *testing.T, content string) string {
	t.Helper()
	oldCommand := runAIDevelopmentSSHCommand
	oldRuntime := resolveAIDevelopmentSSHRuntime
	oldReload := aiDevelopmentSSHReload
	oldConfig := config.AppConfig
	path := filepath.Join(t.TempDir(), "sshd_config")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	config.AppConfig = &config.Config{Panel: config.PanelConfig{BackupDir: t.TempDir()}}
	resolveAIDevelopmentSSHRuntime = func(context.Context) (aiDevelopmentSSHRuntime, error) {
		return aiDevelopmentSSHRuntime{service: "ssh", configPath: path}, nil
	}
	t.Cleanup(func() {
		runAIDevelopmentSSHCommand = oldCommand
		resolveAIDevelopmentSSHRuntime = oldRuntime
		aiDevelopmentSSHReload = oldReload
		config.AppConfig = oldConfig
	})
	return path
}

func TestEnsureAIDevelopmentSSHRequiresExplicitRepair(t *testing.T) {
	setupAIDevelopmentSSHTest(t, "PubkeyAuthentication no\n")
	runAIDevelopmentSSHCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/usr/sbin/sshd" || args[0] != "-T" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		return []byte("pubkeyauthentication no\n"), nil
	}
	if err := EnsureAIDevelopmentSSH(context.Background(), "wp_example", "example.com", false); !errors.Is(err, ErrAIDevelopmentSSHPublicKeyAuthDisabled) {
		t.Fatalf("error=%v, want disabled error", err)
	}
}

func TestEnsureAIDevelopmentSSHUsesMatchContextAndLeavesEnabledConfigUntouched(t *testing.T) {
	path := setupAIDevelopmentSSHTest(t, "PubkeyAuthentication yes\n")
	original, _ := os.ReadFile(path)
	checks := 0
	runAIDevelopmentSSHCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/usr/sbin/sshd" || args[0] != "-T" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		checks++
		if !strings.Contains(strings.Join(args, " "), "-C user=wp_example,host=example.com,addr=127.0.0.1") {
			t.Fatalf("missing Match context: %v", args)
		}
		return []byte("pubkeyauthentication yes\n"), nil
	}
	if err := EnsureAIDevelopmentSSH(context.Background(), "wp_example", "example.com", false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) || checks != 1 {
		t.Fatalf("changed=%t checks=%d, want false/1", string(after) != string(original), checks)
	}
}

func TestResolveAIDevelopmentSSHRuntimeUsesValidExecStart(t *testing.T) {
	oldCommand := runAIDevelopmentSSHCommand
	t.Cleanup(func() { runAIDevelopmentSSHCommand = oldCommand })
	outputs := map[string][]byte{
		"ssh":  []byte("\n"),
		"sshd": []byte("argv[]=/usr/sbin/sshd -D -f /etc/ssh/custom.conf\n"),
	}
	runAIDevelopmentSSHCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/usr/bin/systemctl" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		return outputs[args[len(args)-1]], nil
	}
	runtime, err := resolveAIDevelopmentSSHRuntimeImpl(context.Background())
	if err != nil || runtime.service != "sshd" || runtime.configPath != "/etc/ssh/custom.conf" {
		t.Fatalf("runtime=%+v err=%v, want sshd/custom.conf", runtime, err)
	}
}

func TestResolveAIDevelopmentSSHRuntimeAcceptsDebianOptionalSSHDOpts(t *testing.T) {
	oldCommand := runAIDevelopmentSSHCommand
	t.Cleanup(func() { runAIDevelopmentSSHCommand = oldCommand })
	runAIDevelopmentSSHCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/usr/bin/systemctl" || args[len(args)-1] != "ssh" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		return []byte("argv[]=/usr/sbin/sshd -D $SSHD_OPTS\n"), nil
	}
	runtime, err := resolveAIDevelopmentSSHRuntimeImpl(context.Background())
	if err != nil || runtime.service != "ssh" || runtime.configPath != aiDevelopmentSSHDefaultConfig {
		t.Fatalf("runtime=%+v err=%v, want ssh/default config", runtime, err)
	}
}

func TestResolveAIDevelopmentSSHRuntimeRejectsUnknownExecStart(t *testing.T) {
	oldCommand := runAIDevelopmentSSHCommand
	t.Cleanup(func() { runAIDevelopmentSSHCommand = oldCommand })
	runAIDevelopmentSSHCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/usr/bin/systemctl" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		return []byte("argv[]=/usr/sbin/sshd -D $UNKNOWN_OPTS\n"), nil
	}
	if _, err := resolveAIDevelopmentSSHRuntimeImpl(context.Background()); err == nil {
		t.Fatal("resolve runtime succeeded with unexpanded ExecStart variables")
	}
}

func TestResolveAIDevelopmentSSHRuntimeRejectsEmptyExecStart(t *testing.T) {
	oldCommand := runAIDevelopmentSSHCommand
	t.Cleanup(func() { runAIDevelopmentSSHCommand = oldCommand })
	runAIDevelopmentSSHCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/usr/bin/systemctl" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		return nil, nil
	}
	if _, err := resolveAIDevelopmentSSHRuntimeImpl(context.Background()); err == nil || !strings.Contains(err.Error(), "ExecStart is empty") {
		t.Fatalf("error=%v, want empty ExecStart diagnostics", err)
	}
}

func TestEnsureManagedAIDevelopmentSSHPubkeyBlockPreservesFollowingDirectives(t *testing.T) {
	header := "# WP Panel managed: enable SSH public-key authentication for AI development access.\n"
	for name, input := range map[string]string{
		"legacy block at start":  header + "PubkeyAuthentication yes\nPort 2222\nPermitRootLogin no\nAllowUsers deploy\n",
		"legacy block in middle": "Port 2222\n" + header + "PubkeyAuthentication yes\nPermitRootLogin no\nAllowUsers deploy\n",
		"managed block":          header + "PubkeyAuthentication yes\n# WP Panel managed end: enable SSH public-key authentication for AI development access.\n\nPort 2222\nPermitRootLogin no\nAllowUsers deploy\n",
	} {
		t.Run(name, func(t *testing.T) {
			output := string(ensureManagedAIDevelopmentSSHPubkeyBlock([]byte(input), header))
			for _, directive := range []string{"Port 2222", "PermitRootLogin no", "AllowUsers deploy"} {
				if !strings.Contains(output, directive) {
					t.Fatalf("output lost %q: %q", directive, output)
				}
			}
			if strings.Count(output, "PubkeyAuthentication yes") != 1 {
				t.Fatalf("managed directive count=%d, output=%q", strings.Count(output, "PubkeyAuthentication yes"), output)
			}
			second := ensureManagedAIDevelopmentSSHPubkeyBlock([]byte(output), header)
			if string(second) != output {
				t.Fatalf("managed block is not idempotent:\nfirst=%q\nsecond=%q", output, second)
			}
		})
	}
}

func TestRepairAIDevelopmentSSHPublicKeyAuthBacksUpValidatesAndReloads(t *testing.T) {
	path := setupAIDevelopmentSSHTest(t, "Include /etc/ssh/sshd_config.d/*.conf\nPubkeyAuthentication no\n")
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	originalStat := originalInfo.Sys().(*syscall.Stat_t)
	backupDir := config.AppConfig.Panel.BackupDir
	checks := 0
	reloads := 0
	runAIDevelopmentSSHCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/usr/sbin/sshd" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		if args[0] == "-T" {
			checks++
			if checks == 1 {
				return []byte("pubkeyauthentication no\n"), nil
			}
			return []byte("pubkeyauthentication yes\n"), nil
		}
		if args[0] == "-t" {
			return nil, nil
		}
		t.Fatalf("unexpected sshd args %v", args)
		return nil, nil
	}
	aiDevelopmentSSHReload = func(context.Context, string) error { reloads++; return nil }
	if err := EnsureAIDevelopmentSSH(context.Background(), "wp_example", "example.com", true); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(content), "# WP Panel managed: enable SSH public-key authentication for AI development access.\nPubkeyAuthentication yes\n") {
		t.Fatalf("unexpected config: %s", content)
	}
	if checks != 2 || reloads != 1 {
		t.Fatalf("checks/reloads=%d/%d, want 2/1", checks, reloads)
	}
	finalInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	finalStat := finalInfo.Sys().(*syscall.Stat_t)
	if finalInfo.Mode().Perm() != originalInfo.Mode().Perm() || finalStat.Uid != originalStat.Uid || finalStat.Gid != originalStat.Gid {
		t.Fatalf("metadata changed: mode=%o/%o uid=%d/%d gid=%d/%d", finalInfo.Mode().Perm(), originalInfo.Mode().Perm(), finalStat.Uid, originalStat.Uid, finalStat.Gid, originalStat.Gid)
	}
	backups, _ := filepath.Glob(filepath.Join(backupDir, "ssh-config", "sshd_config.*.bak"))
	if len(backups) != 1 {
		t.Fatalf("backup files=%v, want one", backups)
	}
}

func TestRepairAIDevelopmentSSHPublicKeyAuthRestoresAfterSyntaxFailure(t *testing.T) {
	path := setupAIDevelopmentSSHTest(t, "PubkeyAuthentication no\n")
	original, _ := os.ReadFile(path)
	runAIDevelopmentSSHCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/usr/sbin/sshd" && args[0] == "-t" {
			return nil, errors.New("syntax error")
		}
		return []byte("pubkeyauthentication no\n"), nil
	}
	reloaded := false
	aiDevelopmentSSHReload = func(context.Context, string) error { reloaded = true; return nil }
	err := EnsureAIDevelopmentSSH(context.Background(), "wp_example", "example.com", true)
	after, _ := os.ReadFile(path)
	if err == nil || !strings.Contains(err.Error(), "restored") || string(after) != string(original) || reloaded {
		t.Fatalf("error=%v changed=%t reloaded=%t", err, string(after) != string(original), reloaded)
	}
}

func TestRepairAIDevelopmentSSHPublicKeyAuthRestoresAfterReloadFailure(t *testing.T) {
	path := setupAIDevelopmentSSHTest(t, "PubkeyAuthentication no\n")
	original, _ := os.ReadFile(path)
	runAIDevelopmentSSHCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/usr/sbin/sshd" && args[0] == "-t" {
			return nil, nil
		}
		if name == "/usr/sbin/sshd" && args[0] == "-T" {
			return []byte("pubkeyauthentication no\n"), nil
		}
		return nil, nil
	}
	reloads := 0
	aiDevelopmentSSHReload = func(context.Context, string) error {
		reloads++
		if reloads == 1 {
			return errors.New("reload failed")
		}
		return nil
	}
	err := EnsureAIDevelopmentSSH(context.Background(), "wp_example", "example.com", true)
	after, _ := os.ReadFile(path)
	if err == nil || !strings.Contains(err.Error(), "reload failed") || string(after) != string(original) || reloads != 2 {
		t.Fatalf("error=%v changed=%t reloads=%d", err, string(after) != string(original), reloads)
	}
}

func TestRepairAIDevelopmentSSHPublicKeyAuthRestoresAfterVerificationFailure(t *testing.T) {
	path := setupAIDevelopmentSSHTest(t, "PubkeyAuthentication no\n")
	original, _ := os.ReadFile(path)
	checks := 0
	runAIDevelopmentSSHCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/usr/sbin/sshd" && args[0] == "-t" {
			return nil, nil
		}
		if args[0] == "-T" {
			checks++
			return []byte("pubkeyauthentication no\n"), nil
		}
		return nil, nil
	}
	reloads := 0
	aiDevelopmentSSHReload = func(context.Context, string) error { reloads++; return nil }
	err := EnsureAIDevelopmentSSH(context.Background(), "wp_example", "example.com", true)
	after, _ := os.ReadFile(path)
	if err == nil || !strings.Contains(err.Error(), "remained disabled") || string(after) != string(original) || checks != 2 || reloads != 2 {
		t.Fatalf("error=%v changed=%t checks=%d reloads=%d", err, string(after) != string(original), checks, reloads)
	}
}

func TestRepairAIDevelopmentSSHPublicKeyAuthReportsRestoreFailure(t *testing.T) {
	setupAIDevelopmentSSHTest(t, "PubkeyAuthentication no\n")
	runAIDevelopmentSSHCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/usr/sbin/sshd" && args[0] == "-t" {
			return nil, errors.New("syntax error")
		}
		return []byte("pubkeyauthentication no\n"), nil
	}
	oldWrite := atomicWriteAIDevelopmentSSH
	t.Cleanup(func() { atomicWriteAIDevelopmentSSH = oldWrite })
	writes := 0
	atomicWriteAIDevelopmentSSH = func(path string, content []byte, mode os.FileMode) error {
		writes++
		if writes == 2 {
			return errors.New("restore write failed")
		}
		return oldWrite(path, content, mode)
	}
	err := EnsureAIDevelopmentSSH(context.Background(), "wp_example", "example.com", true)
	if err == nil || !strings.Contains(err.Error(), "restore write failed") {
		t.Fatalf("error=%v, want restore failure", err)
	}
}

func TestRepairAIDevelopmentSSHPublicKeyAuthReportsReloadRestoreFailure(t *testing.T) {
	setupAIDevelopmentSSHTest(t, "PubkeyAuthentication no\n")
	runAIDevelopmentSSHCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/usr/sbin/sshd" && args[0] == "-t" {
			return nil, nil
		}
		return []byte("pubkeyauthentication no\n"), nil
	}
	oldWrite := atomicWriteAIDevelopmentSSH
	t.Cleanup(func() { atomicWriteAIDevelopmentSSH = oldWrite })
	writes := 0
	atomicWriteAIDevelopmentSSH = func(path string, content []byte, mode os.FileMode) error {
		writes++
		if writes == 2 {
			return errors.New("restore write failed")
		}
		return oldWrite(path, content, mode)
	}
	aiDevelopmentSSHReload = func(context.Context, string) error { return errors.New("reload failed") }
	err := EnsureAIDevelopmentSSH(context.Background(), "wp_example", "example.com", true)
	if err == nil || !strings.Contains(err.Error(), "restore write failed") {
		t.Fatalf("error=%v, want reload restore failure", err)
	}
}

func TestRepairAIDevelopmentSSHPublicKeyAuthReportsVerificationRestoreFailure(t *testing.T) {
	setupAIDevelopmentSSHTest(t, "PubkeyAuthentication no\n")
	runAIDevelopmentSSHCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/usr/sbin/sshd" && args[0] == "-t" {
			return nil, nil
		}
		return []byte("pubkeyauthentication no\n"), nil
	}
	oldWrite := atomicWriteAIDevelopmentSSH
	t.Cleanup(func() { atomicWriteAIDevelopmentSSH = oldWrite })
	writes := 0
	atomicWriteAIDevelopmentSSH = func(path string, content []byte, mode os.FileMode) error {
		writes++
		if writes == 2 {
			return errors.New("restore write failed")
		}
		return oldWrite(path, content, mode)
	}
	err := EnsureAIDevelopmentSSH(context.Background(), "wp_example", "example.com", true)
	if err == nil || !strings.Contains(err.Error(), "restore write failed") {
		t.Fatalf("error=%v, want verification restore failure", err)
	}
}
