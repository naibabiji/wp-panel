package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

func TestRuntimePHPRequestPath(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"/wp-content/uploads/2026/shell.php?x=1", "/wp-content/uploads/2026/shell.php"},
		{"/wp-content/cache/page/shell.phtml", "/wp-content/cache/page/shell.phtml"},
		{"/wp-content/languages/drop.phar", "/wp-content/languages/drop.phar"},
		{"/wp-content/wflogs/drop.php9", "/wp-content/wflogs/drop.php9"},
		{"/wp-content/plugins/plugin/ajax.php", ""},
		{"/wp-content/themes/theme/index.php", ""},
		{"/wp-content/uploads/style.css", ""},
		{"/index.php", ""},
	}
	for _, tt := range tests {
		if got := runtimePHPRequestPath(tt.raw); got != tt.want {
			t.Fatalf("runtimePHPRequestPath(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestImportSiteRuntimePHPAccessEventsAggregatesLogEntries(t *testing.T) {
	openTestDB(t)

	logDir := t.TempDir()
	logContent := strings.Join([]string{
		`203.0.113.10 - - [30/Jun/2026:10:00:00 +0800] "GET /wp-content/uploads/2026/shell.php HTTP/1.1" 404 0 "-" "curl/8.0"`,
		`203.0.113.10 - - [30/Jun/2026:10:05:00 +0800] "GET /wp-content/uploads/2026/shell.php?x=1 HTTP/1.1" 404 0 "-" "curl/8.0"`,
		`203.0.113.11 - - [30/Jun/2026:10:06:00 +0800] "POST /wp-content/cache/drop.phtml HTTP/1.1" 404 0 "-" "scanner"`,
		`203.0.113.12 - - [30/Jun/2026:10:07:00 +0800] "GET /wp-content/plugins/plugin/ajax.php HTTP/1.1" 200 0 "-" "browser"`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(logDir, "wp-security.log"), []byte(logContent), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`INSERT INTO websites
		(id, name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type)
		VALUES (1, 'demo', 'example.com', 'active', 'wp_demo', ?, ?, 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf', 'wordpress')`,
		t.TempDir(), logDir); err != nil {
		t.Fatalf("insert website: %v", err)
	}

	oldAllowed := fileSecurityLogDirAllowed
	fileSecurityLogDirAllowed = func(string) bool { return true }
	t.Cleanup(func() { fileSecurityLogDirAllowed = oldAllowed })

	count, err := importSiteRuntimePHPAccessEvents(database.GetDB(), fileSecuritySite{
		ID:     1,
		Domain: "example.com",
		LogDir: logDir,
	})
	if err != nil {
		t.Fatalf("importSiteRuntimePHPAccessEvents() error = %v", err)
	}
	if count != 3 {
		t.Fatalf("imported count = %d, want 3", count)
	}

	events, err := ListFileSecurityEvents(10)
	if err != nil {
		t.Fatalf("ListFileSecurityEvents() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2: %+v", len(events), events)
	}

	var uploadEvent *models.FileSecurityEvent
	for i := range events {
		if events[i].Path == "/wp-content/uploads/2026/shell.php" {
			uploadEvent = &events[i]
			break
		}
	}
	if uploadEvent == nil {
		t.Fatalf("missing upload runtime PHP event: %+v", events)
	}
	if uploadEvent.EventType != FileSecurityEventRuntimePHPAccess || uploadEvent.Source != "nginx" || uploadEvent.EventCount != 2 {
		t.Fatalf("unexpected upload event: %+v", *uploadEvent)
	}
	if uploadEvent.FirstSeen != "2026-06-30T10:00:00Z" || uploadEvent.LastSeen != "2026-06-30T10:05:00Z" {
		t.Fatalf("unexpected seen range: first=%s last=%s", uploadEvent.FirstSeen, uploadEvent.LastSeen)
	}

	if _, err := importSiteRuntimePHPAccessEvents(database.GetDB(), fileSecuritySite{ID: 1, Domain: "example.com", LogDir: logDir}); err != nil {
		t.Fatalf("second import error = %v", err)
	}
	events, err = ListFileSecurityEvents(10)
	if err != nil {
		t.Fatalf("ListFileSecurityEvents() after second import error = %v", err)
	}
	for _, event := range events {
		if event.Path == "/wp-content/uploads/2026/shell.php" && event.EventCount != 2 {
			t.Fatalf("second import should not inflate tailed log count: %+v", event)
		}
	}
}

func TestSiteRelativeSlashPathRejectsEscapes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "site")
	if got := siteRelativeSlashPath(root, filepath.Join(root, "wp-content", "uploads", "shell.php")); got != "/wp-content/uploads/shell.php" {
		t.Fatalf("siteRelativeSlashPath inside root = %q", got)
	}
	if got := siteRelativeSlashPath(root, filepath.Join(filepath.Dir(root), "other", "shell.php")); got != "" {
		t.Fatalf("siteRelativeSlashPath escape = %q, want empty", got)
	}
}

func TestRefreshFileSecurityEventsScansRuntimePHPFiles(t *testing.T) {
	openTestDB(t)

	webRoot := t.TempDir()
	uploads := filepath.Join(webRoot, "wp-content", "uploads", "2026")
	if err := os.MkdirAll(uploads, 0755); err != nil {
		t.Fatal(err)
	}
	shellPath := filepath.Join(uploads, "shell.php")
	if err := os.WriteFile(shellPath, []byte("<?php echo 'x';"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uploads, "photo.jpg"), []byte("jpg"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := database.GetDB().Exec(`INSERT INTO websites
		(name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type)
		VALUES ('demo', 'example.com', 'active', 'wp_demo', ?, ?, 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf', 'wordpress')`,
		webRoot, filepath.Join(t.TempDir(), "logs")); err != nil {
		t.Fatalf("insert website: %v", err)
	}

	summary, err := RefreshFileSecurityEvents()
	if err != nil {
		t.Fatalf("RefreshFileSecurityEvents() error = %v", err)
	}
	if summary.SitesScanned != 1 || summary.FileEvents != 1 {
		t.Fatalf("summary = %+v, want one site and one file event", summary)
	}
	if _, err := RefreshFileSecurityEvents(); err != nil {
		t.Fatalf("second RefreshFileSecurityEvents() error = %v", err)
	}

	events, err := ListFileSecurityEvents(10)
	if err != nil {
		t.Fatalf("ListFileSecurityEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	event := events[0]
	if event.EventType != FileSecurityEventSuspiciousFile || event.Source != "scanner" || event.Path != "/wp-content/uploads/2026/shell.php" {
		t.Fatalf("unexpected event: %+v", event)
	}
	if event.EventCount != 1 {
		t.Fatalf("event count = %d, want 1", event.EventCount)
	}
	if event.ResolvedAt != nil {
		t.Fatalf("event resolved_at = %v, want nil", *event.ResolvedAt)
	}

	if err := os.Remove(shellPath); err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshFileSecurityEvents(); err != nil {
		t.Fatalf("refresh after removal error = %v", err)
	}
	events, err = ListFileSecurityEvents(10)
	if err != nil {
		t.Fatalf("ListFileSecurityEvents() after removal error = %v", err)
	}
	if len(events) != 1 || events[0].ResolvedAt == nil {
		t.Fatalf("removed event should be marked resolved: %+v", events)
	}
}
