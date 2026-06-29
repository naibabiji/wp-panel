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

func TestWordPressTemplatesBlockRuntimePHPExecution(t *testing.T) {
	rule := "wp-content/(?!plugins/|themes/|mu-plugins/).*\\.(php|phtml|phar|php[0-9])"
	rootConfigRule := "wp-config\\.php|wordfence-waf\\.php|php\\.ini"
	for name, tmpl := range map[string]string{
		"http":  nginxHTTPTemplate,
		"https": nginxHTTPSTemplate,
	} {
		if !strings.Contains(tmpl, rule) {
			t.Fatalf("%s template missing wp-content runtime PHP deny rule", name)
		}
		if !strings.Contains(tmpl, rootConfigRule) {
			t.Fatalf("%s template missing root config deny rule", name)
		}
		phpLocationIndex := strings.Index(tmpl, "location ~ \\.php$")
		if phpLocationIndex < 0 {
			t.Fatalf("%s template missing generic PHP location", name)
		}
		if strings.Index(tmpl, rule) > phpLocationIndex {
			t.Fatalf("%s template deny rule must appear before generic PHP location", name)
		}
		if strings.Index(tmpl, rootConfigRule) > phpLocationIndex {
			t.Fatalf("%s template root config deny rule must appear before generic PHP location", name)
		}
	}
	for name, tmpl := range map[string]string{
		"php-http":  phpHTTPTemplate,
		"php-https": phpHTTPSTemplate,
	} {
		if strings.Contains(tmpl, rule) {
			t.Fatalf("%s template should not include WordPress runtime PHP rule", name)
		}
	}
}

func TestWPSecurityLogMapRecordsRuntimePHPBeforeContentExclusion(t *testing.T) {
	rule := "~*^/wp-content/(?!plugins/|themes/|mu-plugins/).*\\.(php|phtml|phar|php[0-9])$ 1;"
	exclusion := "~^/wp-content/ 0;"
	ruleIndex := strings.Index(nginxGlobalLogMapConfig(), rule)
	exclusionIndex := strings.Index(nginxGlobalLogMapConfig(), exclusion)
	if ruleIndex < 0 {
		t.Fatalf("security log map missing runtime PHP rule")
	}
	if exclusionIndex < 0 {
		t.Fatalf("security log map missing wp-content exclusion")
	}
	if ruleIndex > exclusionIndex {
		t.Fatalf("runtime PHP rule must appear before wp-content exclusion")
	}
}
