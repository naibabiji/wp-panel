package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoveSiteLogDirRemovesEmptyTargetCreatedByPoolApply(t *testing.T) {
	root := t.TempDir()
	oldLogDir := filepath.Join(root, "old.example.com")
	newLogDir := filepath.Join(root, "new.example.com")

	if err := os.MkdirAll(oldLogDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldLogDir, "access.log"), []byte("old log"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newLogDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := moveSiteLogDir(oldLogDir, newLogDir); err != nil {
		t.Fatalf("moveSiteLogDir failed: %v", err)
	}

	if _, err := os.Stat(oldLogDir); !os.IsNotExist(err) {
		t.Fatalf("old log dir still exists or stat failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(newLogDir, "access.log"))
	if err != nil {
		t.Fatalf("new log file missing: %v", err)
	}
	if string(got) != "old log" {
		t.Fatalf("new log content = %q, want old log", string(got))
	}
}

func TestMoveSiteLogDirRejectsNonEmptyTarget(t *testing.T) {
	root := t.TempDir()
	oldLogDir := filepath.Join(root, "old.example.com")
	newLogDir := filepath.Join(root, "new.example.com")

	if err := os.MkdirAll(oldLogDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldLogDir, "access.log"), []byte("old log"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newLogDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newLogDir, "access.log"), []byte("new log"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := moveSiteLogDir(oldLogDir, newLogDir); err == nil {
		t.Fatal("expected non-empty target log dir to be rejected")
	}
	if _, err := os.Stat(filepath.Join(oldLogDir, "access.log")); err != nil {
		t.Fatalf("old log file should remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newLogDir, "access.log")); err != nil {
		t.Fatalf("target log file should remain: %v", err)
	}
}

func TestCreateSiteLogDirCreatesMissingLogs(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "88.vps17.top")

	if err := createSiteLogDir(logDir); err != nil {
		t.Fatalf("createSiteLogDir failed: %v", err)
	}
	for _, name := range []string{"access.log", "error.log", "wp-security.log", "php-error.log", "php-slow.log"} {
		if _, err := os.Stat(filepath.Join(logDir, name)); err != nil {
			t.Fatalf("%s should exist: %v", name, err)
		}
	}
}

func TestCreateSiteLogDirRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "88.vps17.top")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable on this platform: %v", err)
	}

	if err := createSiteLogDir(link); err == nil {
		t.Fatal("expected symlink log dir to be rejected")
	}
}

func TestManagedSubpathAllowsOnlyChildren(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sites")
	target := filepath.Join(root, "example.com")

	got, err := managedSubpath(root, target, "网站目录")
	if err != nil {
		t.Fatalf("managedSubpath rejected child path: %v", err)
	}
	if got != filepath.Clean(target) {
		t.Fatalf("managedSubpath() = %q, want %q", got, filepath.Clean(target))
	}
}

func TestManagedSubpathRejectsRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sites")

	if _, err := managedSubpath(root, root, "网站目录"); err == nil {
		t.Fatal("expected root path to be rejected")
	}
}

func TestManagedSubpathRejectsEscape(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sites")
	target := filepath.Join(root, "..", "outside")

	if _, err := managedSubpath(root, target, "网站目录"); err == nil {
		t.Fatal("expected escaped path to be rejected")
	}
}
