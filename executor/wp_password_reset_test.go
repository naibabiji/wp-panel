package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePasswordResetMode(t *testing.T) {
	for _, m := range []string{PasswordResetModeAllow, PasswordResetModeAll, PasswordResetModeAdmin} {
		if got, err := ValidatePasswordResetMode(m); err != nil || got != m {
			t.Fatalf("ValidatePasswordResetMode(%q) = %q, %v; want %q, nil", m, got, err, m)
		}
	}
	if _, err := ValidatePasswordResetMode("bogus"); err == nil {
		t.Fatalf("ValidatePasswordResetMode(bogus) expected error")
	}
}

func TestApplyWPPasswordResetModeAll(t *testing.T) {
	root := t.TempDir()
	if err := ApplyWPPasswordResetMode(root, "", PasswordResetModeAll); err != nil {
		t.Fatalf("apply all: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "wp-content", "mu-plugins", wpPasswordResetPluginFile))
	if err != nil {
		t.Fatalf("read mu-plugin: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, wpPanelPasswordResetMarker) {
		t.Fatalf("marker missing")
	}
	if !strings.Contains(content, "allow_password_reset") || !strings.Contains(content, "__return_false") {
		t.Fatalf("allow_password_reset filter missing")
	}
	if !strings.Contains(content, "login_head") || !strings.Contains(content, "p#nav") {
		t.Fatalf("login link hiding missing")
	}
}

func TestApplyWPPasswordResetModeAdmin(t *testing.T) {
	root := t.TempDir()
	if err := ApplyWPPasswordResetMode(root, "", PasswordResetModeAdmin); err != nil {
		t.Fatalf("apply admin: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "wp-content", "mu-plugins", wpPasswordResetPluginFile))
	if err != nil {
		t.Fatalf("read mu-plugin: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "has_cap('administrator')") {
		t.Fatalf("administrator capability check missing")
	}
	if strings.Contains(content, "login_head") {
		t.Fatalf("admin mode must not hide login link")
	}
}

func TestApplyWPPasswordResetModeAllFilterSignature(t *testing.T) {
	root := t.TempDir()
	if err := ApplyWPPasswordResetMode(root, "", PasswordResetModeAll); err != nil {
		t.Fatalf("apply all: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "wp-content", "mu-plugins", wpPasswordResetPluginFile))
	if err != nil {
		t.Fatalf("read mu-plugin: %v", err)
	}
	content := string(data)
	// 全站禁用必须注册 allow_password_reset 过滤器，且返回 __return_false（拒绝所有找回）。
	if !strings.Contains(content, "add_filter('allow_password_reset', '__return_false');") {
		t.Fatalf("all mode filter signature missing")
	}
}

func TestApplyWPPasswordResetModeAdminFilterSignature(t *testing.T) {
	root := t.TempDir()
	if err := ApplyWPPasswordResetMode(root, "", PasswordResetModeAdmin); err != nil {
		t.Fatalf("apply admin: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "wp-content", "mu-plugins", wpPasswordResetPluginFile))
	if err != nil {
		t.Fatalf("read mu-plugin: %v", err)
	}
	content := string(data)
	// 仅禁用管理员：过滤器必须接收 2 个参数（$allow, $user_id）并以优先级 10 注册，
	// 否则 WordPress 不会把 $user_id 传入回调，无法判断是否为管理员。
	if !strings.Contains(content, "add_filter('allow_password_reset', function ($allow, $user_id) {") {
		t.Fatalf("admin filter opening signature missing")
	}
	if !strings.Contains(content, ", 10, 2);") {
		t.Fatalf("admin filter priority/accepted_args (10, 2) missing")
	}
}

func TestApplyWPPasswordResetModeMkdirAllFails(t *testing.T) {
	root := t.TempDir()
	// 在 mu-plugins 路径上放一个同名普通文件，使 MkdirAll 失败。
	if err := os.MkdirAll(filepath.Join(root, "wp-content"), 0755); err != nil {
		t.Fatalf("mkdir wp-content: %v", err)
	}
	blocker := filepath.Join(root, "wp-content", "mu-plugins")
	if err := os.WriteFile(blocker, []byte("block"), 0644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	if err := ApplyWPPasswordResetMode(root, "", PasswordResetModeAll); err == nil {
		t.Fatalf("expected error when mu-plugins path is a regular file")
	}
}

func TestApplyWPPasswordResetModeWriteFails(t *testing.T) {
	root := t.TempDir()
	muDir := filepath.Join(root, "wp-content", "mu-plugins")
	if err := os.MkdirAll(muDir, 0755); err != nil {
		t.Fatalf("mkdir mu-plugins: %v", err)
	}
	// 让目标文件路径本身是一个目录，WriteFile 会失败（is a directory）。
	if err := os.MkdirAll(filepath.Join(muDir, wpPasswordResetPluginFile), 0755); err != nil {
		t.Fatalf("mkdir plugin path as dir: %v", err)
	}
	if err := ApplyWPPasswordResetMode(root, "", PasswordResetModeAll); err == nil {
		t.Fatalf("expected error when plugin path is a directory")
	}
}

func TestApplyWPPasswordResetModeChownFails(t *testing.T) {
	root := t.TempDir()
	// 传入不存在的系统用户：ChownSitePath 的 user.Lookup 失败应返回错误，
	// 而不是静默吞掉（审核修复点：mu-plugin 文件 chown 失败必须报错）。
	if err := ApplyWPPasswordResetMode(root, "no-such-system-user-xyz-12345", PasswordResetModeAll); err == nil {
		t.Fatalf("expected error when system user does not exist")
	}
}

func TestApplyWPPasswordResetModeAllowRemovesFile(t *testing.T) {
	root := t.TempDir()
	if err := ApplyWPPasswordResetMode(root, "", PasswordResetModeAll); err != nil {
		t.Fatalf("seed all: %v", err)
	}
	if err := ApplyWPPasswordResetMode(root, "", PasswordResetModeAllow); err != nil {
		t.Fatalf("apply allow: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "wp-content", "mu-plugins", wpPasswordResetPluginFile)); !os.IsNotExist(err) {
		t.Fatalf("mu-plugin should be removed in allow mode")
	}
}

func TestApplyWPPasswordResetModeAllowNoFileIsNoop(t *testing.T) {
	root := t.TempDir()
	if err := ApplyWPPasswordResetMode(root, "", PasswordResetModeAllow); err != nil {
		t.Fatalf("apply allow on empty site: %v", err)
	}
}
