package executor

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func TestAlertRuleSustainedFiring(t *testing.T) {
	start := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	r := &alertRule{thresholdDuration: 5 * time.Minute}

	if r.sustainedFiring(true, start) {
		t.Fatal("first high sample should not alert immediately")
	}
	if r.sustainedFiring(true, start.Add(4*time.Minute+59*time.Second)) {
		t.Fatal("high duration below threshold should not alert")
	}
	if !r.sustainedFiring(true, start.Add(5*time.Minute)) {
		t.Fatal("high duration at threshold should alert")
	}
}

func TestAlertRuleSustainedFiringResets(t *testing.T) {
	start := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	r := &alertRule{thresholdDuration: 5 * time.Minute}

	r.sustainedFiring(true, start)
	if r.sustainedFiring(false, start.Add(2*time.Minute)) {
		t.Fatal("normal sample should not alert")
	}
	if !r.pendingSince.IsZero() {
		t.Fatal("normal sample should reset pending state")
	}
	if r.sustainedFiring(true, start.Add(6*time.Minute)) {
		t.Fatal("new high period should restart the timer")
	}
}

func TestAlertResendIntervalForUpdateAlerts(t *testing.T) {
	if got := alertResendInterval("alert_system_update"); got != 24*time.Hour {
		t.Fatalf("system update alert should resend daily, got %v", got)
	}
	if got := alertResendInterval("alert_panel_update"); got != 24*time.Hour {
		t.Fatalf("panel update alert should resend daily, got %v", got)
	}
	if got := alertResendInterval("alert_disk"); got != 30*time.Minute {
		t.Fatalf("regular alerts should keep 30 minute resend interval, got %v", got)
	}
}

func TestClearSystemUpdateAlertCache(t *testing.T) {
	sysUpdateCache.mu.Lock()
	prevLastAt := sysUpdateCache.lastAt
	prevNames := sysUpdateCache.names
	sysUpdateCache.lastAt = time.Now()
	sysUpdateCache.names = []string{"openssl"}
	sysUpdateCache.mu.Unlock()
	t.Cleanup(func() {
		sysUpdateCache.mu.Lock()
		sysUpdateCache.lastAt = prevLastAt
		sysUpdateCache.names = prevNames
		sysUpdateCache.mu.Unlock()
	})

	ClearSystemUpdateAlertCache()

	sysUpdateCache.mu.Lock()
	defer sysUpdateCache.mu.Unlock()
	if !sysUpdateCache.lastAt.IsZero() {
		t.Fatalf("lastAt should be reset, got %v", sysUpdateCache.lastAt)
	}
	if sysUpdateCache.names != nil {
		t.Fatalf("names should be cleared, got %v", sysUpdateCache.names)
	}
}

func TestClearPanelUpdateAlertCache(t *testing.T) {
	panelUpdateCache.mu.Lock()
	prevLastAt := panelUpdateCache.lastAt
	prevLatest := panelUpdateCache.latest
	prevMessage := panelUpdateCache.message
	panelUpdateCache.lastAt = time.Now()
	panelUpdateCache.latest = "v1.2.3"
	panelUpdateCache.message = "panel update available"
	panelUpdateCache.mu.Unlock()
	t.Cleanup(func() {
		panelUpdateCache.mu.Lock()
		panelUpdateCache.lastAt = prevLastAt
		panelUpdateCache.latest = prevLatest
		panelUpdateCache.message = prevMessage
		panelUpdateCache.mu.Unlock()
	})

	ClearPanelUpdateAlertCache()

	panelUpdateCache.mu.Lock()
	defer panelUpdateCache.mu.Unlock()
	if !panelUpdateCache.lastAt.IsZero() {
		t.Fatalf("lastAt should be reset, got %v", panelUpdateCache.lastAt)
	}
	if panelUpdateCache.latest != "" {
		t.Fatalf("latest should be cleared, got %q", panelUpdateCache.latest)
	}
	if panelUpdateCache.message != "" {
		t.Fatalf("message should be cleared, got %q", panelUpdateCache.message)
	}
}

func TestCheckPanelUpdateUsesCachedMessage(t *testing.T) {
	panelUpdateCache.mu.Lock()
	prevLastAt := panelUpdateCache.lastAt
	prevLatest := panelUpdateCache.latest
	prevMessage := panelUpdateCache.message
	panelUpdateCache.lastAt = time.Now()
	panelUpdateCache.latest = "v1.2.3"
	panelUpdateCache.message = "面板有新版本 v1.2.3 可用，当前版本 v1.2.2。"
	panelUpdateCache.mu.Unlock()
	prevCurrent := panelCurrentVersion
	panelCurrentVersion = "v1.2.2"
	t.Cleanup(func() {
		panelUpdateCache.mu.Lock()
		panelUpdateCache.lastAt = prevLastAt
		panelUpdateCache.latest = prevLatest
		panelUpdateCache.message = prevMessage
		panelUpdateCache.mu.Unlock()
		panelCurrentVersion = prevCurrent
	})

	firing, msg := checkPanelUpdate()
	if !firing {
		t.Fatal("cached panel update message should keep alert firing")
	}
	if !strings.Contains(msg, "v1.2.3") {
		t.Fatalf("message should include cached latest version, got %q", msg)
	}
}

func TestCheckBackupReportsOnlyStaleEnabledSites(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE websites (id INTEGER PRIMARY KEY, domain TEXT)`)
	mustExec(t, db, `CREATE TABLE backup_settings (site_id INTEGER, enabled INTEGER)`)
	mustExec(t, db, `CREATE TABLE db_backups (site_id INTEGER, auto INTEGER, created_at DATETIME)`)
	mustExec(t, db, `INSERT INTO websites (id, domain) VALUES
		(1, 'stale.example'),
		(2, 'recent.example'),
		(3, 'never.example'),
		(4, 'disabled.example')`)
	mustExec(t, db, `INSERT INTO backup_settings (site_id, enabled) VALUES
		(1, 1), (2, 1), (3, 1), (4, 0)`)
	mustExec(t, db, `INSERT INTO db_backups (site_id, auto, created_at) VALUES
		(1, 1, datetime('now', '-2 days')),
		(2, 1, datetime('now', '-1 hour')),
		(4, 1, datetime('now', '-2 days'))`)

	firing, msg := checkBackup()
	if !firing {
		t.Fatal("stale enabled site should alert")
	}
	if !strings.Contains(msg, "stale.example") {
		t.Fatalf("message should include stale site, got %q", msg)
	}
	for _, domain := range []string{"recent.example", "never.example", "disabled.example"} {
		if strings.Contains(msg, domain) {
			t.Fatalf("message should not include %s, got %q", domain, msg)
		}
	}
}

func TestCheckSitesKeepsCachedFailureWhenCheckIsSkipped(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE websites (
		id INTEGER PRIMARY KEY,
		domain TEXT,
		status TEXT,
		ssl_enabled INTEGER,
		monitoring_enabled INTEGER,
		monitoring_interval INTEGER
	)`)
	mustExec(t, db, `INSERT INTO websites
		(id, domain, status, ssl_enabled, monitoring_enabled, monitoring_interval)
		VALUES (1, 'down.example', 'active', 1, 1, 5)`)

	siteLastCheck["1"] = time.Now()
	siteFailureCounts["1"] = siteFailureAlertThreshold
	siteFailureMessages["1"] = "down.example 返回 500"

	firing, msg := checkSites()
	if !firing {
		t.Fatal("cached site failure should keep alert firing while interval skips the check")
	}
	if msg != "down.example 返回 500" {
		t.Fatalf("unexpected cached failure message: %q", msg)
	}
}

func TestCheckSitesDoesNotAlertOnUnconfirmedCachedFailure(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE websites (
		id INTEGER PRIMARY KEY,
		domain TEXT,
		status TEXT,
		ssl_enabled INTEGER,
		monitoring_enabled INTEGER,
		monitoring_interval INTEGER
	)`)
	mustExec(t, db, `INSERT INTO websites
		(id, domain, status, ssl_enabled, monitoring_enabled, monitoring_interval)
		VALUES (1, 'slow.example', 'active', 1, 1, 5)`)

	siteLastCheck["1"] = time.Now()
	siteFailureMessages["1"] = "slow.example timeout"
	siteFailureCounts["1"] = siteFailureAlertThreshold - 1

	firing, msg := checkSites()
	if firing {
		t.Fatalf("unconfirmed cached failure should not alert, got %q", msg)
	}
	if msg != "" {
		t.Fatalf("unconfirmed cached failure should have empty message, got %q", msg)
	}
}

func openAlertTestDB(t *testing.T) *sql.DB {
	t.Helper()

	prevDB := database.DB
	prevSiteLastCheck := siteLastCheck
	prevSiteFailureMessages := siteFailureMessages
	prevSiteFailureCounts := siteFailureCounts

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	database.DB = db
	siteLastCheck = make(map[string]time.Time)
	siteFailureMessages = make(map[string]string)
	siteFailureCounts = make(map[string]int)

	t.Cleanup(func() {
		db.Close()
		database.DB = prevDB
		siteLastCheck = prevSiteLastCheck
		siteFailureMessages = prevSiteFailureMessages
		siteFailureCounts = prevSiteFailureCounts
	})

	return db
}

func mustExec(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatalf("exec failed: %v\nSQL: %s", err, query)
	}
}
