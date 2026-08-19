package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeLegacyPHPMaxInputVarsUpgradesUntouchedDefault(t *testing.T) {
	oldPath := phpRuntimeConfigPath
	phpRuntimeConfigPath = filepath.Join(t.TempDir(), "99-wppanel.ini")
	t.Cleanup(func() { phpRuntimeConfigPath = oldPath })

	original := "memory_limit = 256M\nmax_input_vars = 2000\n"
	if err := os.WriteFile(phpRuntimeConfigPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := UpgradeLegacyPHPMaxInputVars(); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	data, err := os.ReadFile(phpRuntimeConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "max_input_vars = 10000") {
		t.Fatalf("expected max_input_vars upgraded to 10000, got:\n%s", content)
	}
	if !strings.Contains(content, "memory_limit = 256M") {
		t.Fatalf("expected unrelated keys preserved, got:\n%s", content)
	}
}

func TestUpgradeLegacyPHPMaxInputVarsPreservesUserValue(t *testing.T) {
	oldPath := phpRuntimeConfigPath
	phpRuntimeConfigPath = filepath.Join(t.TempDir(), "99-wppanel.ini")
	t.Cleanup(func() { phpRuntimeConfigPath = oldPath })

	original := "max_input_vars = 5000\n"
	if err := os.WriteFile(phpRuntimeConfigPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := UpgradeLegacyPHPMaxInputVars(); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	data, err := os.ReadFile(phpRuntimeConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "max_input_vars = 5000") {
		t.Fatalf("expected user-modified value to be preserved untouched, got:\n%s", string(data))
	}
}

func TestUpgradeLegacyPHPMaxInputVarsNoFileIsNoop(t *testing.T) {
	oldPath := phpRuntimeConfigPath
	phpRuntimeConfigPath = filepath.Join(t.TempDir(), "does-not-exist.ini")
	t.Cleanup(func() { phpRuntimeConfigPath = oldPath })

	if err := UpgradeLegacyPHPMaxInputVars(); err != nil {
		t.Fatalf("expected no error when config file does not yet exist, got: %v", err)
	}
}
