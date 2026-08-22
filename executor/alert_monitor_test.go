package executor

import (
	"context"
	"database/sql"
	"net"
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

func TestAlertResendIntervalsByAlertClass(t *testing.T) {
	if got := alertResendInterval("alert_system_update"); got != 24*time.Hour {
		t.Fatalf("system update alert should resend daily, got %v", got)
	}
	if got := alertResendInterval("alert_panel_update"); got != 24*time.Hour {
		t.Fatalf("panel update alert should resend daily, got %v", got)
	}
	if got := alertResendInterval("alert_ssl"); got != 24*time.Hour {
		t.Fatalf("SSL alert should resend daily, got %v", got)
	}
	if got := alertResendInterval("alert_disk"); got != 2*time.Hour {
		t.Fatalf("resource alerts should resend every 2 hours, got %v", got)
	}
	if got := alertResendInterval("alert_site"); got != 6*time.Hour {
		t.Fatalf("availability alerts should resend every 6 hours, got %v", got)
	}
	if got := alertResendInterval("alert_cron_fail"); got != 24*time.Hour {
		t.Fatalf("operational alerts should resend daily, got %v", got)
	}
	if got := alertResendInterval("alert_wp_sqli_probe"); got != 24*time.Hour {
		t.Fatalf("wp sqli probe alert should resend daily to avoid spamming during a sustained attack, got %v", got)
	}
	if got := alertResendInterval("alert_wp_fake_search_bot"); got != 24*time.Hour {
		t.Fatalf("wp fake search bot alert should resend daily to avoid spamming during a sustained attack, got %v", got)
	}
}

func TestAlertRuntimeStateSurvivesReload(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE alert_runtime_state (
		alert_type TEXT PRIMARY KEY, status TEXT, pending_since TEXT,
		last_fired_at TEXT, last_message TEXT, updated_at DATETIME)`)
	now := time.Date(2026, 8, 23, 3, 4, 5, 0, time.UTC)
	r := &alertRule{key: "alert_ssl", firing: true, lastFired: now, lastAlertMsg: "still expiring"}
	persistAlertRuntimeState(r)

	reloaded := &alertRule{key: "alert_ssl"}
	loadAlertRuntimeState([]*alertRule{reloaded})
	if !reloaded.firing || !reloaded.lastFired.Equal(now) || reloaded.lastAlertMsg != "still expiring" {
		t.Fatalf("runtime state not restored: %+v", reloaded)
	}
}

func TestCheckSSLKeepsLongExpiredCertificateFiring(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE websites (
		domain TEXT, ssl_enabled INTEGER, ssl_expires_at DATETIME, ssl_last_error TEXT)`)
	mustExec(t, db, `INSERT INTO websites VALUES ('expired.example', 1, datetime('now', '-30 days'), '')`)
	previousDetector := cloudflareProxyDetector
	cloudflareProxyDetector = func(string) bool { return false }
	t.Cleanup(func() { cloudflareProxyDetector = previousDetector })

	firing, msg := checkSSL()
	if !firing || !strings.Contains(msg, "已过期") {
		t.Fatalf("long-expired certificate should remain firing, got firing=%v msg=%q", firing, msg)
	}
}

func TestCheckSSLAddsConciseCloudflareGuidance(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE websites (
		domain TEXT, ssl_enabled INTEGER, ssl_expires_at DATETIME, ssl_last_error TEXT)`)
	mustExec(t, db, `INSERT INTO websites VALUES ('cdn.example', 1, datetime('now', '+5 days'), 'renew failed')`)
	previousDetector := cloudflareProxyDetector
	cloudflareProxyDetector = func(string) bool { return true }
	t.Cleanup(func() { cloudflareProxyDetector = previousDetector })

	firing, msg := checkSSL()
	if !firing || !strings.Contains(msg, "自动续签未成功") || !strings.Contains(msg, "Full (strict)") || !strings.Contains(msg, "手动上传证书") {
		t.Fatalf("unexpected Cloudflare SSL guidance: firing=%v msg=%q", firing, msg)
	}
}

func TestCheckSSLRunsCloudflareDetectionsConcurrently(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE websites (
		domain TEXT, ssl_enabled INTEGER, ssl_expires_at DATETIME, ssl_last_error TEXT)`)
	mustExec(t, db, `INSERT INTO websites VALUES
		('one.example', 1, datetime('now', '+5 days'), 'failed'),
		('two.example', 1, datetime('now', '+5 days'), 'failed'),
		('three.example', 1, datetime('now', '+5 days'), 'failed')`)
	previousDetector := cloudflareProxyDetector
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	cloudflareProxyDetector = func(string) bool {
		started <- struct{}{}
		<-release
		return false
	}
	t.Cleanup(func() { cloudflareProxyDetector = previousDetector })

	done := make(chan struct{})
	go func() {
		checkSSL()
		close(done)
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("Cloudflare detections ran serially")
		}
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("checkSSL did not finish after concurrent detections were released")
	}
}

func TestCloudflareDetectionRequiresEveryResolvedAddressInOfficialRanges(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE security_settings (skey TEXT PRIMARY KEY, svalue TEXT)`)
	mustExec(t, db, `INSERT INTO security_settings VALUES ('cloudflare_realip_ips', '104.16.0.0/12 2606:4700::/32')`)
	previousLookup := cloudflareDNSLookup
	t.Cleanup(func() { cloudflareDNSLookup = previousLookup })
	resetCloudflareDetectionCache := func() {
		cloudflareDetectionCache.Lock()
		cloudflareDetectionCache.entries = make(map[string]cloudflareDetectionEntry)
		cloudflareDetectionCache.Unlock()
	}
	resetCloudflareDetectionCache()
	cloudflareDNSLookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("104.21.2.219")}, {IP: net.ParseIP("203.0.113.10")}}, nil
	}
	if isLikelyCloudflareProxied("mixed.example") {
		t.Fatal("mixed Cloudflare/origin answers must not be classified as proxied")
	}

	resetCloudflareDetectionCache()
	cloudflareDNSLookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("104.21.2.219")}, {IP: net.ParseIP("2606:4700::6815:2db")}}, nil
	}
	if !isLikelyCloudflareProxied("proxied.example") {
		t.Fatal("all-official Cloudflare answers should be classified as proxied")
	}
}

func TestCheckCronFailurePersistsUntilSuccessfulRun(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE cron_jobs (
		name TEXT, enabled INTEGER, notify_fail INTEGER, running INTEGER,
		last_status TEXT, last_run_at DATETIME)`)
	mustExec(t, db, `INSERT INTO cron_jobs VALUES ('nightly', 1, 1, 0, 'failed', datetime('now', '-2 days'))`)

	firing, _ := checkCronFail()
	if !firing {
		t.Fatal("failed cron should remain firing until a successful run")
	}
	mustExec(t, db, `UPDATE cron_jobs SET last_status='success', last_run_at=datetime('now')`)
	firing, _ = checkCronFail()
	if firing {
		t.Fatal("successful cron run should clear the alert")
	}
}

func TestWebsiteExpiryMilestoneDeduplicatesEveryDomainInMergedMessage(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE websites (domain TEXT, expires_at DATETIME)`)
	mustExec(t, db, `CREATE TABLE alert_event_markers (
		alert_type TEXT, event_key TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (alert_type, event_key))`)
	expiresAt := time.Now().Add(7*24*time.Hour + 12*time.Hour)
	if _, err := db.Exec(`INSERT INTO websites VALUES ('a.example', ?), ('b.example', ?)`, expiresAt, expiresAt); err != nil {
		t.Fatal(err)
	}
	var scannedExpiry time.Time
	if err := db.QueryRow("SELECT expires_at FROM websites LIMIT 1").Scan(&scannedExpiry); err != nil {
		t.Fatalf("scan expiry: %v", err)
	}
	if days := int(scannedExpiry.Sub(time.Now()).Hours() / 24); days != 7 {
		t.Fatalf("fixture days=%d expiry=%v", days, scannedExpiry)
	}

	firing, msg := checkWebsiteExpiry()
	if !firing || !strings.Contains(msg, "a.example") || !strings.Contains(msg, "b.example") {
		t.Fatalf("first milestone should merge both domains, firing=%v msg=%q", firing, msg)
	}
	firing, msg = checkWebsiteExpiry()
	if firing || msg != "" {
		t.Fatalf("second check should deduplicate every merged domain, firing=%v msg=%q", firing, msg)
	}
	var markers int
	if err := db.QueryRow("SELECT COUNT(*) FROM alert_event_markers").Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if markers != 2 {
		t.Fatalf("markers=%d, want 2", markers)
	}
}

func TestDisableAlertRuleResolvesOpenAlerts(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE alert_log (
		id INTEGER PRIMARY KEY, alert_type TEXT, level TEXT, message TEXT,
		resolved INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)
	mustExec(t, db, `CREATE TABLE alert_runtime_state (
		alert_type TEXT PRIMARY KEY, status TEXT, pending_since TEXT,
		last_fired_at TEXT, last_message TEXT, updated_at DATETIME)`)
	mustExec(t, db, `INSERT INTO alert_log (alert_type, level, message) VALUES ('alert_cpu', 'critical', 'high')`)
	r := &alertRule{key: "alert_cpu", firing: true, lastFired: time.Now(), lastAlertMsg: "high"}
	disableAlertRule(r)

	var resolved int
	if err := db.QueryRow("SELECT resolved FROM alert_log").Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if resolved != 1 || r.firing || !r.pendingSince.IsZero() {
		t.Fatalf("disabled rule not closed cleanly: resolved=%d rule=%+v", resolved, r)
	}
	var status string
	if err := db.QueryRow("SELECT status FROM alert_runtime_state WHERE alert_type='alert_cpu'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "normal" {
		t.Fatalf("runtime status=%q, want normal", status)
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
