package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestIsLanguageL10nRuntimePHP(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/wp-content/languages/zh_CN.l10n.php", true},
		{"/wp-content/languages/plugins/my-plugin-zh_CN.l10n.php", true},
		{"/wp-content/languages/drop.php", false},
		{"/wp-content/cache/drop.l10n.php", false},
	}

	for _, tt := range tests {
		if got := isLanguageL10nRuntimePHP(tt.path); got != tt.want {
			t.Fatalf("isLanguageL10nRuntimePHP(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestParseFileLockEnabledTime(t *testing.T) {
	tests := []struct {
		input string
		want  string
		isRFC bool
	}{
		{"2026-06-30 10:00:30", "2026-06-30 10:00:30", false},
		{"2026-06-30T10:00:30Z", "2026-06-30T10:00:30Z", true},
		{"2026-06-30T10:00:30", "2026-06-30 10:00:30", false},
		{"", "", false},
		{"invalid", "", false},
	}
	for _, tt := range tests {
		got := parseFileLockEnabledTime(tt.input)
		if tt.want == "" {
			if !got.IsZero() {
				t.Fatalf("parseFileLockEnabledTime(%q) = %v, want zero", tt.input, got)
			}
			continue
		}
		if tt.isRFC {
			if got.Format(time.RFC3339) != tt.want {
				t.Fatalf("parseFileLockEnabledTime(%q) = %s, want %s", tt.input, got.Format(time.RFC3339), tt.want)
			}
			continue
		}
		if got.Format("2006-01-02 15:04:05") != tt.want {
			t.Fatalf("parseFileLockEnabledTime(%q) = %s, want %s", tt.input, got.Format("2006-01-02 15:04:05"), tt.want)
		}
	}
}

func TestImportSiteRuntimePHPAccessEventsAggregatesLogEntries(t *testing.T) {
	openTestDB(t)
	lockAt := "2026-06-30T10:00:30+08:00"

	logDir := t.TempDir()
	logContent := strings.Join([]string{
		`203.0.113.10 - - [30/Jun/2026:10:00:00 +0800] "GET /wp-content/uploads/2026/shell.php HTTP/1.1" 404 0 "-" "curl/8.0"`,
		`203.0.113.10 - - [30/Jun/2026:10:05:00 +0800] "GET /wp-content/uploads/2026/shell.php?x=1 HTTP/1.1" 404 0 "-" "curl/8.0"`,
		`203.0.113.11 - - [30/Jun/2026:10:06:00 +0800] "POST /wp-content/cache/drop.phtml HTTP/1.1" 404 0 "-" "scanner"`,
		`203.0.113.13 - - [30/Jun/2026:10:10:00 +0800] "GET /wp-content/languages/zh_CN.l10n.php HTTP/1.1" 404 0 "-" "curl/8.0"`,
		`203.0.113.12 - - [30/Jun/2026:10:07:00 +0800] "GET /wp-content/plugins/plugin/ajax.php HTTP/1.1" 200 0 "-" "browser"`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(logDir, "wp-security.log"), []byte(logContent), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`INSERT INTO websites
		(id, name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type, file_lock_enabled, file_lock_enabled_at)
		VALUES (1, 'demo', 'example.com', 'active', 'wp_demo', ?, ?, 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf', 'wordpress', 1, ?)`,
		t.TempDir(), logDir, lockAt); err != nil {
		t.Fatalf("insert website: %v", err)
	}

	oldAllowed := fileSecurityLogDirAllowed
	fileSecurityLogDirAllowed = func(string) bool { return true }
	t.Cleanup(func() { fileSecurityLogDirAllowed = oldAllowed })

	count, err := importSiteRuntimePHPAccessEvents(database.GetDB(), fileSecuritySite{
		ID:            1,
		Domain:        "example.com",
		LogDir:        logDir,
		LockEnabledAt: parseFileLockEnabledTime(lockAt),
	})
	if err != nil {
		t.Fatalf("importSiteRuntimePHPAccessEvents() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("imported count = %d, want 2", count)
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
	if uploadEvent.EventType != FileSecurityEventRuntimePHPAccess || uploadEvent.Source != "nginx" || uploadEvent.EventCount != 1 {
		t.Fatalf("unexpected upload event: %+v", *uploadEvent)
	}
	parseSeen := func(raw string) time.Time {
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
			tm, err := time.Parse(layout, raw)
			if err == nil {
				return tm
			}
		}
		t.Fatalf("invalid seen timestamp: %q", raw)
		return time.Time{}
	}
	if got, want := parseSeen(uploadEvent.FirstSeen), parseSeen("2026-06-30 10:05:00"); !got.Equal(want) {
		t.Fatalf("unexpected first seen: got=%s want=%s", uploadEvent.FirstSeen, "2026-06-30 10:05:00")
	}
	if got, want := parseSeen(uploadEvent.LastSeen), parseSeen("2026-06-30 10:05:00"); !got.Equal(want) {
		t.Fatalf("unexpected last seen: got=%s want=%s", uploadEvent.LastSeen, "2026-06-30 10:05:00")
	}

	if _, err := importSiteRuntimePHPAccessEvents(database.GetDB(), fileSecuritySite{
		ID:            1,
		Domain:        "example.com",
		LogDir:        logDir,
		LockEnabledAt: parseFileLockEnabledTime(lockAt),
	}); err != nil {
		t.Fatalf("second import error = %v", err)
	}
	events, err = ListFileSecurityEvents(10)
	if err != nil {
		t.Fatalf("ListFileSecurityEvents() after second import error = %v", err)
	}
	for _, event := range events {
		if event.Path == "/wp-content/uploads/2026/shell.php" && event.EventCount != 1 {
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
	lockAt := formatEventTime(time.Now().Add(-time.Minute))
	if lockAt == "" {
		t.Fatal("lockAt is empty")
	}
	uploads := filepath.Join(webRoot, "wp-content", "uploads", "2026")
	if err := os.MkdirAll(uploads, 0755); err != nil {
		t.Fatal(err)
	}
	shellPath := filepath.Join(uploads, "shell.php")
	oldShellPath := filepath.Join(uploads, "old.php")
	if err := os.WriteFile(shellPath, []byte("<?php echo 'x';"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldShellPath, []byte("<?php echo 'old';"), 0644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour * 2)
	if err := os.Chtimes(oldShellPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(time.Minute)
	if err := os.Chtimes(shellPath, now, now); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uploads, "photo.jpg"), []byte("jpg"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(webRoot, "wp-content", "languages"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "wp-content", "languages", "zh_CN.l10n.php"), []byte("<?php\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := database.GetDB().Exec(`INSERT INTO websites
		(name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type, file_lock_enabled, file_lock_enabled_at)
		VALUES ('demo', 'example.com', 'active', 'wp_demo', ?, ?, 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf', 'wordpress', 1, ?)`,
		webRoot, filepath.Join(t.TempDir(), "logs"), lockAt); err != nil {
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

func TestClearFileSecurityEvents(t *testing.T) {
	openTestDB(t)

	if _, err := database.GetDB().Exec(`INSERT INTO websites
		(name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
		VALUES ('demo', 'example.com', 'active', 'wp_demo', '/tmp/www', '/tmp/log', 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf')`); err != nil {
		t.Fatalf("insert website: %v", err)
	}

	if err := upsertFileSecurityEvent(database.GetDB(), fileSecurityRecord{
		SiteID:        1,
		Domain:        "example.com",
		EventType:     FileSecurityEventSuspiciousFile,
		Source:        "scanner",
		RiskLevel:     "high",
		Path:          "/wp-content/uploads/shell.php",
		RequestMethod: "",
		IPAddress:     "",
		UserAgent:     "unit-test",
		Status:        0,
		FileSize:      20,
		Message:       "test",
	}); err != nil {
		t.Fatalf("upsertFileSecurityEvent() error = %v", err)
	}

	var count int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM file_security_events").Scan(&count); err != nil {
		t.Fatalf("query initial event count: %v", err)
	}
	if count != 1 {
		t.Fatalf("count before clear = %d, want 1", count)
	}

	if err := ClearFileSecurityEvents(); err != nil {
		t.Fatalf("ClearFileSecurityEvents() error = %v", err)
	}

	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM file_security_events").Scan(&count); err != nil {
		t.Fatalf("query cleared event count: %v", err)
	}
	if count != 0 {
		t.Fatalf("count after clear = %d, want 0", count)
	}
}
