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
