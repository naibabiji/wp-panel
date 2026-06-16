package executor

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/database"
)

func TestRecordFail2banBanKeepsRepeatedHistory(t *testing.T) {
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

	rows, err := database.GetDB().Query(
		`SELECT ban_level, source_jail, ban_count FROM firewall_bans
		 WHERE ip_address = ? ORDER BY id`, ip,
	)
	if err != nil {
		t.Fatalf("query records: %v", err)
	}
	defer rows.Close()

	var levels, counts []int
	var jails []string
	for rows.Next() {
		var level, count int
		var jail string
		if err := rows.Scan(&level, &jail, &count); err != nil {
			t.Fatalf("scan record: %v", err)
		}
		levels = append(levels, level)
		jails = append(jails, jail)
		counts = append(counts, count)
	}
	if len(levels) != 2 {
		t.Fatalf("expected two history records, got %d", len(levels))
	}
	if levels[0] != 2 || levels[1] != 3 {
		t.Fatalf("expected levels [2 3], got %v", levels)
	}
	if counts[0] != 1 || counts[1] != 2 {
		t.Fatalf("expected ban counts [1 2], got %v", counts)
	}
	for _, jail := range jails {
		if jail != "wppanel-404" {
			t.Fatalf("expected jail wppanel-404, got %q", jail)
		}
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
