package executor

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/database"
)

func TestRecordFail2banBanUpdatesActiveRecord(t *testing.T) {
	openTestDB(t)
	oldRecordPersistBan := recordPersistBan
	recordPersistBan = func(string) {}
	t.Cleanup(func() { recordPersistBan = oldRecordPersistBan })

	ip := "203.0.113.77"
	if err := RecordFail2banBan(ip, "wppanel-404"); err != nil {
		t.Fatalf("first record failed: %v", err)
	}
	if err := RecordFail2banBan(ip, "wppanel-404"); err != nil {
		t.Fatalf("second record failed: %v", err)
	}

	var rows, level, count int
	var jail string
	if err := database.GetDB().QueryRow(
		`SELECT COUNT(*), MAX(ban_level), MAX(source_jail), MAX(ban_count)
		 FROM firewall_bans WHERE ip_address = ? AND unbanned_at IS NULL`, ip,
	).Scan(&rows, &level, &jail, &count); err != nil {
		t.Fatalf("query records: %v", err)
	}
	if rows != 1 {
		t.Fatalf("expected one active record, got %d", rows)
	}
	if level != 3 || jail != "wppanel-404" || count != 2 {
		t.Fatalf("unexpected active record: level=%d jail=%q count=%d", level, jail, count)
	}

	if err := RecordFail2banBan(ip, "wppanel-404"); err != nil {
		t.Fatalf("third record failed: %v", err)
	}
	if err := database.GetDB().QueryRow(
		`SELECT COUNT(*), MAX(ban_level), MAX(source_jail), MAX(ban_count)
		 FROM firewall_bans WHERE ip_address = ? AND unbanned_at IS NULL`, ip,
	).Scan(&rows, &level, &jail, &count); err != nil {
		t.Fatalf("query records after third event: %v", err)
	}
	if rows != 1 || level != 5 || jail != "wppanel-404" || count != 3 {
		t.Fatalf("unexpected permanent active record: rows=%d level=%d jail=%q count=%d", rows, level, jail, count)
	}
}

func TestRecordFail2banBanKeepsExistingSourceWhenExistingLevelIsHigher(t *testing.T) {
	openTestDB(t)
	oldRecordPersistBan := recordPersistBan
	recordPersistBan = func(string) {}
	t.Cleanup(func() { recordPersistBan = oldRecordPersistBan })

	ip := "203.0.113.78"
	if _, err := database.GetDB().Exec(
		`INSERT INTO firewall_bans (ip_address, ban_level, reason, source_jail, ban_count, expires_at)
		 VALUES (?, 5, '404 泛滥检测（高危：累计3次严重违规，永久封禁）', 'wppanel-404', 3, NULL)`,
		ip,
	); err != nil {
		t.Fatalf("insert active ban: %v", err)
	}
	if err := RecordFail2banBan(ip, "wppanel-sshd"); err != nil {
		t.Fatalf("record sshd event: %v", err)
	}

	var rows, level, count int
	var jail, reason string
	if err := database.GetDB().QueryRow(
		`SELECT COUNT(*), MAX(ban_level), MAX(source_jail), MAX(reason), MAX(ban_count)
		 FROM firewall_bans WHERE ip_address = ? AND unbanned_at IS NULL`, ip,
	).Scan(&rows, &level, &jail, &reason, &count); err != nil {
		t.Fatalf("query active record: %v", err)
	}
	if rows != 1 || level != 5 || jail != "wppanel-404" || count != 4 {
		t.Fatalf("unexpected active record: rows=%d level=%d jail=%q count=%d", rows, level, jail, count)
	}
	if !strings.Contains(reason, "404 泛滥检测") {
		t.Fatalf("expected existing 404 reason to be kept, got %q", reason)
	}
}

func TestRecordFail2banBanDoesNotReuseExpiredActiveRecord(t *testing.T) {
	openTestDB(t)
	oldRecordPersistBan := recordPersistBan
	recordPersistBan = func(string) {}
	t.Cleanup(func() { recordPersistBan = oldRecordPersistBan })

	ip := "203.0.113.79"
	if _, err := database.GetDB().Exec(
		`INSERT INTO firewall_bans (ip_address, ban_level, reason, source_jail, ban_count, expires_at)
		 VALUES (?, 3, 'expired', 'wppanel-404', 1, datetime('now', '-60 seconds'))`,
		ip,
	); err != nil {
		t.Fatalf("insert expired ban: %v", err)
	}
	if err := RecordFail2banBan(ip, "wppanel-404"); err != nil {
		t.Fatalf("record new event: %v", err)
	}

	var totalRows, activeRows int
	if err := database.GetDB().QueryRow(
		`SELECT COUNT(*),
		        SUM(CASE WHEN unbanned_at IS NULL AND (expires_at IS NULL OR expires_at > datetime('now')) THEN 1 ELSE 0 END)
		 FROM firewall_bans WHERE ip_address = ?`, ip,
	).Scan(&totalRows, &activeRows); err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if totalRows != 2 || activeRows != 1 {
		t.Fatalf("expected expired row plus one fresh active row, got total=%d active=%d", totalRows, activeRows)
	}
}

func TestDeduplicateActiveFirewallBansKeepsMostSevereRecord(t *testing.T) {
	openTestDB(t)
	db := database.GetDB()

	seed := []struct {
		ip       string
		level    int
		count    int
		bannedAt string
	}{
		{"203.0.113.10", 5, 25, "2026-07-07 18:00:00"},
		{"203.0.113.10", 5, 30, "2026-07-07 19:00:00"},
		{"203.0.113.10", 3, 24, "2026-07-07 20:00:00"},
		{"203.0.113.11", 3, 1, "2026-07-07 18:00:00"},
	}
	for _, row := range seed {
		if _, err := db.Exec(
			`INSERT INTO firewall_bans (ip_address, ban_level, reason, source_jail, ban_count, banned_at, expires_at)
			 VALUES (?, ?, 'test', 'wppanel-404', ?, ?, NULL)`,
			row.ip, row.level, row.count, row.bannedAt,
		); err != nil {
			t.Fatalf("insert seed row: %v", err)
		}
	}

	if err := deduplicateActiveFirewallBans(db); err != nil {
		t.Fatalf("deduplicate active bans: %v", err)
	}

	var activeRows, level, count int
	var bannedAt string
	if err := db.QueryRow(
		`SELECT COUNT(*), MAX(ban_level), MAX(ban_count), MAX(banned_at)
		 FROM firewall_bans
		 WHERE ip_address = '203.0.113.10' AND unbanned_at IS NULL`,
	).Scan(&activeRows, &level, &count, &bannedAt); err != nil {
		t.Fatalf("query active duplicate group: %v", err)
	}
	if activeRows != 1 || level != 5 || count != 30 || bannedAt != "2026-07-07 19:00:00" {
		t.Fatalf("unexpected kept record: rows=%d level=%d count=%d bannedAt=%q", activeRows, level, count, bannedAt)
	}

	var unbannedRows int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM firewall_bans
		 WHERE ip_address = '203.0.113.10' AND unbanned_at IS NOT NULL`,
	).Scan(&unbannedRows); err != nil {
		t.Fatalf("query unbanned duplicates: %v", err)
	}
	if unbannedRows != 2 {
		t.Fatalf("expected two duplicate rows to be marked unbanned, got %d", unbannedRows)
	}

	var otherActiveRows int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM firewall_bans
		 WHERE ip_address = '203.0.113.11' AND unbanned_at IS NULL`,
	).Scan(&otherActiveRows); err != nil {
		t.Fatalf("query non-duplicate active row: %v", err)
	}
	if otherActiveRows != 1 {
		t.Fatalf("expected unrelated active row to remain, got %d", otherActiveRows)
	}
}

func TestUpgradeDeduplicatesActiveFirewallBans(t *testing.T) {
	openTestDB(t)
	db := database.GetDB()

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version TEXT NOT NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_version (version) VALUES ('1.0.25')`); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	for _, row := range []struct {
		level    int
		count    int
		bannedAt string
	}{
		{5, 5, "2026-07-07 18:00:00"},
		{5, 9, "2026-07-07 19:00:00"},
		{3, 4, "2026-07-07 20:00:00"},
	} {
		if _, err := db.Exec(
			`INSERT INTO firewall_bans (ip_address, ban_level, reason, source_jail, ban_count, banned_at, expires_at)
			 VALUES ('203.0.113.12', ?, 'test', 'wppanel-404', ?, ?, NULL)`,
			row.level, row.count, row.bannedAt,
		); err != nil {
			t.Fatalf("insert duplicate ban: %v", err)
		}
	}

	if err := database.RunUpgrades(); err != nil {
		t.Fatalf("run upgrades: %v", err)
	}

	var activeRows, count int
	if err := db.QueryRow(
		`SELECT COUNT(*), MAX(ban_count)
		 FROM firewall_bans
		 WHERE ip_address = '203.0.113.12' AND unbanned_at IS NULL`,
	).Scan(&activeRows, &count); err != nil {
		t.Fatalf("query active bans: %v", err)
	}
	if activeRows != 1 || count != 9 {
		t.Fatalf("expected upgrade to keep one active ban with max count, got rows=%d count=%d", activeRows, count)
	}

	var version string
	if err := db.QueryRow(`SELECT version FROM schema_version ORDER BY updated_at DESC, rowid DESC LIMIT 1`).Scan(&version); err != nil {
		t.Fatalf("query schema version: %v", err)
	}
	if version != database.LatestVersion() {
		t.Fatalf("schema_version = %q, want %q", version, database.LatestVersion())
	}
}

func TestExecuteManualBanCreatesSingleManualRecord(t *testing.T) {
	openTestDB(t)

	oldAddNginxBan := manualAddNginxBan
	oldRemoveNginxBan := manualRemoveNginxBan
	oldShellExec := shellExec
	t.Cleanup(func() {
		manualAddNginxBan = oldAddNginxBan
		manualRemoveNginxBan = oldRemoveNginxBan
		shellExec = oldShellExec
	})

	var nginxBanned []string
	manualAddNginxBan = func(ip string) error {
		nginxBanned = append(nginxBanned, ip)
		return nil
	}
	manualRemoveNginxBan = func(string) error { return nil }
	shellExec = func(binary string, args ...string) (string, error) {
		if binary == "fail2ban-client" && strings.Join(args, " ") == "set wppanel banip 203.0.113.88" {
			t.Fatalf("manual ban must not call fail2ban banip")
		}
		return "", errors.New("unexpected command")
	}

	result := executeManualBan(&Task{Payload: &ManualBanPayload{IP: "203.0.113.88", Duration: 86400}})
	if !result.Success {
		t.Fatalf("manual ban failed: %s", result.Message)
	}
	if len(nginxBanned) != 1 || nginxBanned[0] != "203.0.113.88" {
		t.Fatalf("expected one nginx ban, got %v", nginxBanned)
	}

	var count, level, isManual, banCount int
	var reason, jail string
	if err := database.GetDB().QueryRow(
		`SELECT COUNT(*), MAX(ban_level), MAX(is_manual), MAX(ban_count), MAX(reason), MAX(source_jail)
		 FROM firewall_bans WHERE ip_address = ?`,
		"203.0.113.88",
	).Scan(&count, &level, &isManual, &banCount, &reason, &jail); err != nil {
		t.Fatalf("query manual ban: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one ban record, got %d", count)
	}
	if level != 3 || isManual != 1 || banCount != 1 || reason != "管理员手动封禁" || jail != "manual" {
		t.Fatalf("unexpected manual ban record: level=%d manual=%d count=%d reason=%q jail=%q", level, isManual, banCount, reason, jail)
	}
}

func TestSyncFail2banBansKeepsActiveManualBan(t *testing.T) {
	openTestDB(t)

	oldShellExec := shellExec
	oldReplace := syncReplaceNginxBannedIPs
	oldRecordPersistBan := recordPersistBan
	t.Cleanup(func() {
		shellExec = oldShellExec
		syncReplaceNginxBannedIPs = oldReplace
		recordPersistBan = oldRecordPersistBan
	})

	shellExec = func(binary string, args ...string) (string, error) {
		if binary == "fail2ban-client" && len(args) == 2 && args[0] == "status" {
			return "Status\n|- Currently banned: 0\n`- Banned IP list:", nil
		}
		return "", errors.New("unexpected command")
	}
	recordPersistBan = func(string) {}

	var synced map[string]bool
	syncReplaceNginxBannedIPs = func(ips map[string]bool) error {
		synced = map[string]bool{}
		for ip, banned := range ips {
			synced[ip] = banned
		}
		return nil
	}

	if _, err := database.GetDB().Exec(
		`INSERT INTO firewall_bans (ip_address, ban_level, reason, source_jail, is_manual, ban_count, expires_at)
		 VALUES ('203.0.113.99', 2, '管理员手动封禁', 'manual', 1, 1, datetime('now', '+600 seconds'))`,
	); err != nil {
		t.Fatalf("insert manual ban: %v", err)
	}

	SyncFail2banBans()

	var unbannedAt *string
	if err := database.GetDB().QueryRow(`SELECT unbanned_at FROM firewall_bans WHERE ip_address = '203.0.113.99'`).Scan(&unbannedAt); err != nil {
		t.Fatalf("query manual ban: %v", err)
	}
	if unbannedAt != nil {
		t.Fatalf("active manual ban was marked unbanned: %v", *unbannedAt)
	}
	if !synced["203.0.113.99"] {
		t.Fatalf("active manual ban was not synced to nginx set: %v", synced)
	}
}

func TestRestoreCDNRealIPGroupWithBindings(t *testing.T) {
	openTestDB(t)
	db := database.GetDB()

	for _, site := range []struct {
		id     int
		domain string
	}{
		{101, "one.example.com"},
		{102, "two.example.com"},
	} {
		if _, err := db.Exec(`INSERT INTO websites
			(id, name, domain, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			site.id, site.domain, site.domain, "wpuser", "/www/wwwroot/"+site.domain, "/www/wwwlogs/"+site.domain,
			"db_"+site.domain, "dbu_"+site.domain, "/etc/php/"+site.domain+".conf", "/etc/nginx/sites-available/"+site.domain+".conf"); err != nil {
			t.Fatalf("insert website %s: %v", site.domain, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cdn_realip_groups
		(id, name, provider, header_name, ip_ranges, builtin, enabled, description)
		VALUES (99, 'EdgeOne', 'custom', 'X-Forwarded-For', '203.0.113.0/24', 0, 1, 'test group')`); err != nil {
		t.Fatalf("insert cdn group: %v", err)
	}
	for _, siteID := range []int{101, 102} {
		if _, err := db.Exec(`INSERT INTO website_cdn_realip_groups (website_id, group_id) VALUES (?, 99)`, siteID); err != nil {
			t.Fatalf("insert binding: %v", err)
		}
	}

	group, err := GetCDNRealIPGroup(99)
	if err != nil {
		t.Fatalf("get cdn group: %v", err)
	}
	bindings, err := WebsiteIDsForCDNRealIPGroup(99)
	if err != nil {
		t.Fatalf("get bindings: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM cdn_realip_groups WHERE id = 99`); err != nil {
		t.Fatalf("delete cdn group: %v", err)
	}

	if err := RestoreCDNRealIPGroupWithBindings(group, bindings); err != nil {
		t.Fatalf("restore cdn group: %v", err)
	}
	restoredBindings, err := WebsiteIDsForCDNRealIPGroup(99)
	if err != nil {
		t.Fatalf("get restored bindings: %v", err)
	}
	if len(restoredBindings) != 2 || restoredBindings[0] != 101 || restoredBindings[1] != 102 {
		t.Fatalf("unexpected restored bindings: %v", restoredBindings)
	}
}

func TestReloadOrStartFail2banReturnsStartError(t *testing.T) {
	reloadErr := errors.New("reload failed")
	startErr := errors.New("start failed")

	oldShellExec := shellExec
	t.Cleanup(func() { shellExec = oldShellExec })
	shellExec = func(binary string, args ...string) (string, error) {
		command := binary + " " + strings.Join(args, " ")
		switch command {
		case "fail2ban-client reload":
			return "", reloadErr
		case "systemctl is-active --quiet fail2ban":
			return "", errors.New("inactive")
		case "systemctl start fail2ban":
			return "", startErr
		default:
			t.Fatalf("unexpected command: %s", command)
			return "", nil
		}
	}

	if err := reloadOrStartFail2ban(); !errors.Is(err, startErr) {
		t.Fatalf("expected start error, got %v", err)
	}
}

func TestNginxTemplateErrorOnlyAccessLog(t *testing.T) {
	engine := NewTemplateEngine(t.TempDir())
	config, err := engine.RenderNginxConfig(&NginxSiteData{
		Domain:        "example.com",
		ServerNames:   "example.com",
		WebRoot:       "/www/wwwroot/example.com",
		PHPProxy:      "unix:/run/php/example.sock",
		TemplateVer:   "v1.0",
		AccessLogMode: "error_only",
	})
	if err != nil {
		t.Fatalf("render nginx config: %v", err)
	}
	if !strings.Contains(config, `access_log /www/wwwlogs/example.com/access.log combined if=$wp_loggable;`) {
		t.Fatalf("expected error-only access log in config:\n%s", config)
	}
	if strings.Contains(config, "access_log off;") {
		t.Fatalf("did not expect access_log off in error-only config:\n%s", config)
	}
}

func TestNginxTemplateIncludesFastCGIHeaderBuffers(t *testing.T) {
	engine := NewTemplateEngine(t.TempDir())
	config, err := engine.RenderNginxConfig(&NginxSiteData{
		Domain:        "example.com",
		ServerNames:   "example.com",
		WebRoot:       "/www/wwwroot/example.com",
		PHPProxy:      "unix:/run/php/example.sock",
		TemplateVer:   "v1.0",
		AccessLogMode: "full",
	})
	if err != nil {
		t.Fatalf("render nginx config: %v", err)
	}

	for _, directive := range []string{
		"fastcgi_buffer_size 128k;",
		"fastcgi_buffers 8 128k;",
		"fastcgi_busy_buffers_size 256k;",
	} {
		if !strings.Contains(config, directive) {
			t.Fatalf("expected %q in config:\n%s", directive, config)
		}
	}
}

func TestWordPressTemplateIncludesSecurityLogAndTryFiles(t *testing.T) {
	engine := NewTemplateEngine(t.TempDir())
	config, err := engine.RenderNginxConfig(&NginxSiteData{
		Domain:        "example.com",
		ServerNames:   "example.com",
		WebRoot:       "/www/wwwroot/example.com",
		PHPProxy:      "unix:/run/php/example.sock",
		TemplateVer:   "v1.0",
		AccessLogMode: "error_only",
		SiteType:      "wordpress",
	})
	if err != nil {
		t.Fatalf("render nginx config: %v", err)
	}
	if !strings.Contains(config, `access_log /www/wwwlogs/example.com/wp-security.log combined if=$wp_security_loggable;`) {
		t.Fatalf("expected WordPress security log in config:\n%s", config)
	}
	if !strings.Contains(config, "try_files $uri =404;") {
		t.Fatalf("expected php location to reject missing php files before FastCGI:\n%s", config)
	}
	if !strings.Contains(config, "location ~* /dup-installer/") {
		t.Fatalf("expected explicit dup-installer block before WordPress fallback:\n%s", config)
	}
}

func TestWordPressTemplateKeepsSecurityLogWhenAccessLogIsOff(t *testing.T) {
	engine := NewTemplateEngine(t.TempDir())
	config, err := engine.RenderNginxConfig(&NginxSiteData{
		Domain:        "example.com",
		ServerNames:   "example.com",
		WebRoot:       "/www/wwwroot/example.com",
		PHPProxy:      "unix:/run/php/example.sock",
		TemplateVer:   "v1.0",
		AccessLogMode: "off",
		SiteType:      "wordpress",
	})
	if err != nil {
		t.Fatalf("render nginx config: %v", err)
	}
	if strings.Contains(config, "access_log off;") {
		t.Fatalf("wordpress config must not disable security logs with access_log off:\n%s", config)
	}
	if !strings.Contains(config, `access_log /www/wwwlogs/example.com/access.log combined if=$wp_access_log_disabled;`) {
		t.Fatalf("expected ordinary access log to be disabled by condition:\n%s", config)
	}
	if !strings.Contains(config, `access_log /www/wwwlogs/example.com/wp-security.log combined if=$wp_security_loggable;`) {
		t.Fatalf("expected WordPress security log to remain enabled:\n%s", config)
	}
}

func TestPHPTemplateDoesNotIncludeWordPressSecurityLog(t *testing.T) {
	engine := NewTemplateEngine(t.TempDir())
	config, err := engine.RenderNginxConfig(&NginxSiteData{
		Domain:        "example.com",
		ServerNames:   "example.com",
		WebRoot:       "/www/wwwroot/example.com",
		PHPProxy:      "unix:/run/php/example.sock",
		TemplateVer:   "v1.0",
		AccessLogMode: "error_only",
		SiteType:      "php",
	})
	if err != nil {
		t.Fatalf("render nginx config: %v", err)
	}
	if strings.Contains(config, "wp-security.log") {
		t.Fatalf("did not expect WordPress security log in generic PHP config:\n%s", config)
	}
}

func TestNginxTemplateUsesGlobalLimitStatusAndBotDefaultOff(t *testing.T) {
	openTestDB(t)
	engine := NewTemplateEngine(t.TempDir())
	config, err := engine.RenderNginxConfig(&NginxSiteData{
		Domain:        "example.com",
		ServerNames:   "example.com",
		WebRoot:       "/www/wwwroot/example.com",
		PHPProxy:      "unix:/run/php/example.sock",
		TemplateVer:   "v1.0",
		AccessLogMode: "error_only",
		SiteType:      "wordpress",
	})
	if err != nil {
		t.Fatalf("render nginx config: %v", err)
	}
	if !strings.Contains(config, "limit_req zone=wp_req_limit burst=300 nodelay;") {
		t.Fatalf("expected existing IP rate limit in config:\n%s", config)
	}
	if strings.Contains(config, "limit_req zone=wp_bot_limit") {
		t.Fatalf("bot limit should be disabled by default:\n%s", config)
	}
	if strings.Contains(config, "limit_req_status 429") {
		t.Fatalf("limit_req_status must be managed globally, not per site:\n%s", config)
	}
}

func TestNginxTemplateIncludesBotLimit(t *testing.T) {
	openTestDB(t)
	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = 'true' WHERE skey = 'bot_limit_enabled'`); err != nil {
		t.Fatalf("enable bot limit: %v", err)
	}
	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = '25' WHERE skey = 'bot_limit_burst'`); err != nil {
		t.Fatalf("set bot burst: %v", err)
	}

	engine := NewTemplateEngine(t.TempDir())
	config, err := engine.RenderNginxConfig(&NginxSiteData{
		Domain:           "example.com",
		ServerNames:      "example.com",
		WebRoot:          "/www/wwwroot/example.com",
		PHPProxy:         "unix:/run/php/example.sock",
		TemplateVer:      "v1.0",
		AccessLogMode:    "error_only",
		SiteType:         "wordpress",
		CDNRealIPEnabled: true,
		CDNRealIPHeader:  "X-Forwarded-For",
		CDNRealIPCompat:  true,
	})
	if err != nil {
		t.Fatalf("render nginx config: %v", err)
	}
	if !strings.Contains(config, "limit_req zone=wp_bot_limit burst=25 nodelay;") {
		t.Fatalf("expected bot limit in config:\n%s", config)
	}
	if strings.Contains(config, "limit_req_status 429") {
		t.Fatalf("limit_req_status must be managed globally, not per site:\n%s", config)
	}
}

func TestRenderVerifiedSearchBotGeoEntries(t *testing.T) {
	openTestDB(t)
	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = ? WHERE skey = 'googlebot_ips'`, "66.249.64.0/19\n2001:4860:4801::/48\nbad"); err != nil {
		t.Fatalf("set googlebot ips: %v", err)
	}
	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = ? WHERE skey = 'bingbot_ips'`, "40.77.167.0/24\n66.249.64.0/19"); err != nil {
		t.Fatalf("set bingbot ips: %v", err)
	}
	entries := renderVerifiedSearchBotGeoEntries()
	for _, want := range []string{
		"66.249.64.0/19 1;",
		"2001:4860:4801::/48 1;",
		"40.77.167.0/24 1;",
	} {
		if !strings.Contains(entries, want) {
			t.Fatalf("missing %q in geo entries:\n%s", want, entries)
		}
	}
	if strings.Contains(entries, "bad") {
		t.Fatalf("invalid ranges must not be rendered:\n%s", entries)
	}
	if strings.Count(entries, "66.249.64.0/19 1;") != 1 {
		t.Fatalf("duplicate ranges must be collapsed:\n%s", entries)
	}
}

func TestWriteBotRateLimitConfigUsesVerifiedSearchBotExemption(t *testing.T) {
	openTestDB(t)
	config := renderBotRateLimitConfig(30)
	for _, want := range []string{
		`map "$wp_bot_ua:$wp_search_bot_ua:$wp_verified_search_bot_ip" $wp_bot_rate_key`,
		`"1:1:1" "";`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("missing %q in bot config:\n%s", want, config)
		}
	}
	if strings.Contains(config, "wp_cdn_realip_compat") {
		t.Fatalf("verified search bot exemption must not depend on CDN compat mode:\n%s", config)
	}
}

func TestRewriteRateLimitDirectivesCombinations(t *testing.T) {
	base := `server {
    # server_name ignored.example.com;
    listen 80;
    server_name example.com;
    limit_req zone=wp_req_limit burst=10 nodelay;
    limit_req zone=wp_bot_limit burst=5 nodelay;
    limit_req_status 429;
}`
	ipLine := "    limit_req zone=wp_req_limit burst=300 nodelay;"
	botLine := "    limit_req zone=wp_bot_limit burst=20 nodelay;"

	tests := []struct {
		name      string
		ip        bool
		bot       bool
		wantIP    bool
		wantBot   bool
		wantCount int
	}{
		{"both on", true, true, true, true, 2},
		{"ip only", true, false, true, false, 1},
		{"bot only", false, true, false, true, 1},
		{"both off", false, false, false, false, 0},
	}
	for _, tt := range tests {
		got := rewriteRateLimitDirectives(base, ipLine, botLine, tt.ip, tt.bot)
		if strings.Contains(got, "limit_req_status 429") {
			t.Fatalf("%s: per-site status should be removed:\n%s", tt.name, got)
		}
		if strings.Contains(got, "# server_name ignored.example.com;\n    limit_req") {
			t.Fatalf("%s: must not inject after commented server_name:\n%s", tt.name, got)
		}
		if strings.Contains(got, "zone=wp_req_limit") != tt.wantIP {
			t.Fatalf("%s: IP limit presence mismatch:\n%s", tt.name, got)
		}
		if strings.Contains(got, "zone=wp_bot_limit") != tt.wantBot {
			t.Fatalf("%s: bot limit presence mismatch:\n%s", tt.name, got)
		}
		if count := strings.Count(got, "limit_req zone="); count != tt.wantCount {
			t.Fatalf("%s: limit count = %d, want %d:\n%s", tt.name, count, tt.wantCount, got)
		}
	}
}

func TestNormalizeWPSecurityLogWhitelist(t *testing.T) {
	patterns, err := NormalizeWPSecurityLogWhitelist("/google*.html\n/BingSiteAuth.xml\n/google*.html")
	if err != nil {
		t.Fatalf("normalize whitelist: %v", err)
	}
	if got := strings.Join(patterns, ","); got != "/google*.html,/BingSiteAuth.xml" {
		t.Fatalf("unexpected normalized whitelist: %s", got)
	}
	if _, err := NormalizeWPSecurityLogWhitelist("relative.txt"); err == nil {
		t.Fatal("expected relative path to be rejected")
	}
	if _, err := NormalizeWPSecurityLogWhitelist("/bad;path"); err == nil {
		t.Fatal("expected dangerous characters to be rejected")
	}
	for _, pattern := range []string{"/foo(.*)", "/foo[bar]", "/foo^bar", "/foo~bar"} {
		if _, err := NormalizeWPSecurityLogWhitelist(pattern); err == nil {
			t.Fatalf("expected %q to be rejected", pattern)
		}
	}
}

func TestBuildWPSecurityLogWhitelistMapEntriesEscapesWildcard(t *testing.T) {
	openTestDB(t)
	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = ? WHERE skey = 'wp_security_log_whitelist'`, "/verify-*.txt"); err != nil {
		t.Fatalf("save whitelist: %v", err)
	}
	entries := buildWPSecurityLogWhitelistMapEntries()
	if !strings.Contains(entries, `~^/verify-[^/]*\.txt$ 0;`) {
		t.Fatalf("expected escaped wildcard map entry, got:\n%s", entries)
	}
}

func TestWPSecurityReportCacheReturnsClone(t *testing.T) {
	wpSecurityReportCacheMu.Lock()
	wpSecurityReportCache = map[int]wpSecurityReportCacheEntry{}
	wpSecurityReportCacheMu.Unlock()

	items := []WPSecurityReportItem{{
		IPAddress:   "203.0.113.10",
		SamplePaths: []string{"GET /test.php x 1"},
		Evidence:    []string{"Primary script unknown"},
	}}
	setWPSecurityReportCache(30, items)

	got, ok := getWPSecurityReportCache(30)
	if !ok {
		t.Fatal("expected cache hit")
	}
	got[0].SamplePaths[0] = "mutated"
	got[0].Evidence[0] = "mutated"

	gotAgain, ok := getWPSecurityReportCache(30)
	if !ok {
		t.Fatal("expected second cache hit")
	}
	if gotAgain[0].SamplePaths[0] == "mutated" || gotAgain[0].Evidence[0] == "mutated" {
		t.Fatalf("cache returned mutable internals: %+v", gotAgain[0])
	}
}

func TestNginxTemplateIncludesCDNRealIPTrustedRanges(t *testing.T) {
	engine := NewTemplateEngine(t.TempDir())
	config, err := engine.RenderNginxConfig(&NginxSiteData{
		Domain:           "example.com",
		ServerNames:      "example.com",
		WebRoot:          "/www/wwwroot/example.com",
		PHPProxy:         "unix:/run/php/example.sock",
		TemplateVer:      "v1.0",
		AccessLogMode:    "error_only",
		SiteType:         "wordpress",
		CDNRealIPEnabled: true,
		CDNRealIPHeader:  "X-Forwarded-For",
		CDNRealIPRanges:  []string{"203.0.113.0/24", "2001:db8::/32"},
		CDNRealIPCompat:  false,
	})
	if err != nil {
		t.Fatalf("render nginx config: %v", err)
	}
	for _, want := range []string{
		"set_real_ip_from 203.0.113.0/24;",
		"set_real_ip_from 2001:db8::/32;",
		"real_ip_header X-Forwarded-For;",
		"real_ip_recursive on;",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("missing %q in config:\n%s", want, config)
		}
	}
}

func TestNginxTemplateIncludesCDNRealIPCompatibleMode(t *testing.T) {
	engine := NewTemplateEngine(t.TempDir())
	config, err := engine.RenderNginxConfig(&NginxSiteData{
		Domain:           "example.com",
		ServerNames:      "example.com",
		WebRoot:          "/www/wwwroot/example.com",
		PHPProxy:         "unix:/run/php/example.sock",
		TemplateVer:      "v1.0",
		AccessLogMode:    "error_only",
		SiteType:         "php",
		CDNRealIPEnabled: true,
		CDNRealIPHeader:  "X-Real-IP",
		CDNRealIPCompat:  true,
	})
	if err != nil {
		t.Fatalf("render nginx config: %v", err)
	}
	for _, want := range []string{
		"set_real_ip_from 0.0.0.0/0;",
		"set_real_ip_from ::/0;",
		"real_ip_header X-Real-IP;",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("missing %q in config:\n%s", want, config)
		}
	}
}

func TestNormalizeCDNRealIPHeaderAndRanges(t *testing.T) {
	if _, err := NormalizeCDNRealIPHeader("X_Real_IP"); err == nil {
		t.Fatal("expected underscore header to be rejected")
	}
	if got, err := NormalizeCDNRealIPHeader("X-Real-IP"); err != nil || got != "X-Real-IP" {
		t.Fatalf("NormalizeCDNRealIPHeader = %q, %v", got, err)
	}
	ranges, err := NormalizeCDNRealIPRanges("203.0.113.0/24\n203.0.113.5\n203.0.113.5")
	if err != nil {
		t.Fatalf("NormalizeCDNRealIPRanges: %v", err)
	}
	if got := strings.Join(ranges, ","); got != "203.0.113.0/24,203.0.113.5" {
		t.Fatalf("unexpected ranges: %s", got)
	}
}

func openTestDB(t *testing.T) {
	t.Helper()

	if database.DB != nil {
		_ = database.Close()
		database.DB = nil
	}
	dbPath := filepath.Join(t.TempDir(), "wp-panel-test.db")
	if err := database.Open(dbPath); err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
		database.DB = nil
	})
	if err := database.RunMigrations(); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
}
