package executor

import (
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
