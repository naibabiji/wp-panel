package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanupNginxConfigBackupsKeepsNewestForTargetOnly(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		"example.com.conf.bak.100",
		"example.com.conf.bak.200",
		"example.com.conf.bak.300",
		"example.com.conf.bak.bad",
		"other.com.conf.bak.100",
		"notes.txt",
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	removed := cleanupNginxConfigBackups(dir, "/etc/nginx/sites-available/example.com.conf", 2)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	if _, err := os.Stat(filepath.Join(dir, "example.com.conf.bak.100")); !os.IsNotExist(err) {
		t.Fatalf("oldest target backup still exists or stat failed: %v", err)
	}
	for _, name := range []string{
		"example.com.conf.bak.200",
		"example.com.conf.bak.300",
		"example.com.conf.bak.bad",
		"other.com.conf.bak.100",
		"notes.txt",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s should remain: %v", name, err)
		}
	}
}

func TestCleanupNginxConfigBackupsDefaultsKeepCount(t *testing.T) {
	dir := t.TempDir()
	for i := int64(1); i <= nginxConfigBackupKeepCount+1; i++ {
		name := fmt.Sprintf("example.com.conf.bak.%d", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	removed := cleanupNginxConfigBackups(dir, "/etc/nginx/sites-available/example.com.conf", 0)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
}

func TestCleanupNginxConfigBackupsNoopCases(t *testing.T) {
	if removed := cleanupNginxConfigBackups(filepath.Join(t.TempDir(), "missing"), "/etc/nginx/sites-available/example.com.conf", 2); removed != 0 {
		t.Fatalf("missing dir removed = %d, want 0", removed)
	}

	dir := t.TempDir()
	for _, name := range []string{"other.conf.bak.1", "example.com.conf.tmp", "example.com.conf.bak.bad"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if removed := cleanupNginxConfigBackups(dir, "/etc/nginx/sites-available/example.com.conf", 2); removed != 0 {
		t.Fatalf("unmatched files removed = %d, want 0", removed)
	}

	for _, name := range []string{"example.com.conf.bak.1", "example.com.conf.bak.2"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if removed := cleanupNginxConfigBackups(dir, "/etc/nginx/sites-available/example.com.conf", 2); removed != 0 {
		t.Fatalf("exact keep count removed = %d, want 0", removed)
	}
}

func TestWordPressTemplatesBlockUploadPHPExecution(t *testing.T) {
	rule := "wp-content/uploads/.*\\.(php|phtml|phar|php[0-9])"
	for name, tmpl := range map[string]string{
		"http":  nginxHTTPTemplate,
		"https": nginxHTTPSTemplate,
	} {
		if !strings.Contains(tmpl, rule) {
			t.Fatalf("%s template missing uploads PHP deny rule", name)
		}
		if strings.Index(tmpl, rule) > strings.Index(tmpl, "location ~ \\.php$") {
			t.Fatalf("%s template deny rule must appear before generic PHP location", name)
		}
	}
	for name, tmpl := range map[string]string{
		"php-http":  phpHTTPTemplate,
		"php-https": phpHTTPSTemplate,
	} {
		if strings.Contains(tmpl, rule) {
			t.Fatalf("%s template should not include WordPress uploads rule", name)
		}
	}
}
