package executor

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/naibabiji/wp-panel/database"
)

func setupReadOnlyCLIState(t *testing.T) {
	t.Helper()
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	_, err := database.GetDB().Exec(`INSERT INTO websites
		(name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path,
		 ssl_enabled, ssl_expires_at, expires_at, ssl_cert_source, ssl_last_error)
		VALUES ('Example', 'Example.com', 'active', 'wp_example', '/srv/example', '/var/log/example',
		 'wp_example', 'wp_example', '/run/php/example.conf', '/etc/nginx/example.conf', 1,
		 '2030-01-02 03:04:05', '2030-02-03 04:05:06', 'acme', '')`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlySiteQueriesReturnRedactedStableSummaries(t *testing.T) {
	setupReadOnlyCLIState(t)
	list, err := GetReadOnlySiteList("dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Sites) != 1 || list.Sites[0].Domain != "Example.com" || list.Sites[0].SSLEnabled != true {
		t.Fatalf("unexpected site list: %+v", list)
	}
	if list.Sites[0].Aliases != "" {
		t.Fatalf("unexpected aliases: %+v", list.Sites[0])
	}
	detail, err := ReadOnlySiteShow("dev", "example.com")
	if err != nil || detail.Site.ID != list.Sites[0].ID {
		t.Fatalf("site show=%+v err=%v", detail, err)
	}
	ssl, err := ReadOnlySSLStatusByDomain("dev", "EXAMPLE.COM")
	if err != nil || !ssl.Enabled || ssl.Source != "acme" || ssl.ExpiresAt == nil {
		t.Fatalf("ssl status=%+v err=%v", ssl, err)
	}
}

func TestGetReadOnlyPanelStatusReturnsHealthSummary(t *testing.T) {
	setupReadOnlyCLIState(t)
	status, err := GetReadOnlyPanelStatus("dev")
	if err != nil {
		t.Fatal(err)
	}
	if !status.OK || status.PanelVersion != "dev" || status.Database.State != "ok" || status.WebsiteCount != 1 {
		t.Fatalf("unexpected panel status: %+v", status)
	}
	if status.CapabilitySync == "" {
		t.Fatal("capability sync status is empty")
	}
	for _, service := range []string{"wp-panel", "nginx", "mariadb"} {
		state, ok := status.Services[service]
		if !ok || state == "" {
			t.Fatalf("missing service state for %s: %+v", service, status.Services)
		}
	}
}

func TestReadOnlyPanelStatusReturnsStableErrorWhenDatabaseUnavailable(t *testing.T) {
	_ = database.Close()
	_, err := GetReadOnlyPanelStatus("dev")
	if ReadOnlyCLIErrorCode(err) != "database_unavailable" {
		t.Fatalf("error=%v code=%s", err, ReadOnlyCLIErrorCode(err))
	}
}

func TestReadOnlySiteListSerializesEmptyArray(t *testing.T) {
	setupReadOnlyCLIState(t)
	if _, err := database.GetDB().Exec("DELETE FROM websites"); err != nil {
		t.Fatal(err)
	}
	result, err := GetReadOnlySiteList("dev")
	if err != nil {
		t.Fatal(err)
	}
	if result.Sites == nil || len(result.Sites) != 0 {
		t.Fatalf("sites=%v, want non-nil empty slice", result.Sites)
	}
}

func TestReadOnlyServiceStatePreservesInactiveAndUnknown(t *testing.T) {
	previous := readOnlySystemctlState
	t.Cleanup(func() { readOnlySystemctlState = previous })
	readOnlySystemctlState = func(string) ([]byte, error) { return []byte("inactive\n"), errors.New("inactive") }
	if got := readOnlyServiceState("nginx"); got != "inactive" {
		t.Fatalf("inactive state=%q", got)
	}
	readOnlySystemctlState = func(string) ([]byte, error) { return nil, errors.New("systemctl unavailable") }
	if got := readOnlyServiceState("nginx"); got != "unknown" {
		t.Fatalf("unknown state=%q", got)
	}
}

func TestReadOnlyBackupListReturnsOnlyBasenamesAndStableKinds(t *testing.T) {
	setupReadOnlyCLIState(t)
	var siteID int
	if err := database.GetDB().QueryRow("SELECT id FROM websites LIMIT 1").Scan(&siteID); err != nil {
		t.Fatal(err)
	}
	_, err := database.GetDB().Exec(`INSERT INTO db_backups
		(site_id, filename, file_size, db_name, auto, transport_status, transport_message)
		VALUES (?, '/private/backup/site.sql.gz', 123, 'wp_example', 1, 'completed', '')`, siteID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.GetDB().Exec(`INSERT INTO file_backups
		(site_id, filename, file_size, mode, transport_status, transport_message)
		VALUES (?, '/private/backup/site.tar.zst', 456, 'full', 'completed', '')`, siteID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ReadOnlyBackupListByDomain("dev", "example.com")
	if err != nil || len(result.Backups) != 2 {
		t.Fatalf("backup list=%+v err=%v", result, err)
	}
	if result.Backups[0].Filename == "/private/backup/site.sql.gz" || result.Backups[1].Filename == "/private/backup/site.tar.zst" {
		t.Fatalf("backup path leaked: %+v", result.Backups)
	}
	seen := map[string]bool{}
	for _, backup := range result.Backups {
		seen[backup.Kind] = true
	}
	if !seen["database"] || !seen["files"] {
		t.Fatalf("backup kinds=%v", seen)
	}
}

func TestReadOnlyCommandsUseStableErrorsForUnknownDomain(t *testing.T) {
	setupReadOnlyCLIState(t)
	_, err := ReadOnlySiteShow("dev", "missing.example")
	if ReadOnlyCLIErrorCode(err) != "site_not_found" {
		t.Fatalf("error=%v code=%s", err, ReadOnlyCLIErrorCode(err))
	}
	_, err = ReadOnlySSLStatusByDomain("dev", "missing.example")
	if ReadOnlyCLIErrorCode(err) != "site_not_found" {
		t.Fatalf("error=%v code=%s", err, ReadOnlyCLIErrorCode(err))
	}
	_, err = ReadOnlyBackupListByDomain("dev", "missing.example")
	if ReadOnlyCLIErrorCode(err) != "site_not_found" {
		t.Fatalf("error=%v code=%s", err, ReadOnlyCLIErrorCode(err))
	}
}
