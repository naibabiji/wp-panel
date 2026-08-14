package executor

import (
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/database"
)

func TestAIIPAliasesRemainStableAndRestoreLocally(t *testing.T) {
	openTestDB(t)
	sessionID := insertAIIPAliasTestSession(t, "alias-one.example")

	first, err := AnonymizeAIText(sessionID, "198.51.100.8 requested /wp-login.php; 2001:db8::8 returned 444")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first, "198.51.100.8") || strings.Contains(first, "2001:db8::8") {
		t.Fatalf("real IP leaked in anonymized text: %s", first)
	}
	if !strings.Contains(first, "IP-01") || !strings.Contains(first, "IP-02") {
		t.Fatalf("expected stable aliases: %s", first)
	}

	second, err := AnonymizeAIText(sessionID, "compare 198.51.100.8 with 203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second, "IP-01") || !strings.Contains(second, "IP-03") {
		t.Fatalf("aliases did not remain stable: %s", second)
	}
	unchanged, err := AnonymizeAIText(sessionID, "invalid address 1198.51.100.80 must stay unchanged")
	if err != nil || unchanged != "invalid address 1198.51.100.80 must stay unchanged" {
		t.Fatalf("invalid address was partially replaced: %q, err=%v", unchanged, err)
	}

	restored, err := RestoreAIIPAliases(sessionID, "IP-01 is noisy; IP-02 looks blocked; IP-03 is new")
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"198.51.100.8", "2001:db8::8", "203.0.113.9"} {
		if !strings.Contains(restored, ip) {
			t.Fatalf("restored response missing %s: %s", ip, restored)
		}
	}
}

func TestAIIPAliasesAreIsolatedBySession(t *testing.T) {
	openTestDB(t)
	firstSession := insertAIIPAliasTestSession(t, "alias-a.example")
	secondSession := insertAIIPAliasTestSession(t, "alias-b.example")

	if _, err := AnonymizeAIText(firstSession, "198.51.100.1 then 198.51.100.2"); err != nil {
		t.Fatal(err)
	}
	second, err := AnonymizeAIText(secondSession, "198.51.100.2")
	if err != nil {
		t.Fatal(err)
	}
	if second != "IP-01" {
		t.Fatalf("second session alias = %q, want IP-01", second)
	}
	restored, err := RestoreAIIPAliases(secondSession, "IP-01")
	if err != nil || restored != "198.51.100.2" {
		t.Fatalf("cross-session restore = %q, err=%v", restored, err)
	}
}

func insertAIIPAliasTestSession(t *testing.T, domain string) int {
	t.Helper()
	result, err := database.DB.Exec(`INSERT INTO websites (name, domain, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path) VALUES (?, ?, 'wp_test', '/srv/test', '/var/log/test', 'wp_test', 'wp_test', '/etc/php/pool.conf', '/etc/nginx/site.conf')`, domain, domain)
	if err != nil {
		t.Fatal(err)
	}
	siteID, _ := result.LastInsertId()
	result, err = database.DB.Exec(`INSERT INTO ai_sessions (site_id, symptom) VALUES (?, 'log_analysis')`, siteID)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, _ := result.LastInsertId()
	return int(sessionID)
}
