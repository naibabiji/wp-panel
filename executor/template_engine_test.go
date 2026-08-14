package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/database"
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

func TestFastCGICacheTemplatesDoNotCacheRedirects(t *testing.T) {
	for name, tmpl := range map[string]string{
		"wordpress-http":  nginxHTTPTemplate,
		"wordpress-https": nginxHTTPSTemplate,
		"php-http":        phpHTTPTemplate,
		"php-https":       phpHTTPSTemplate,
	} {
		if !strings.Contains(tmpl, "fastcgi_cache_valid 200 {{.FCacheTTL}}s;") {
			t.Fatalf("%s template must cache successful responses", name)
		}
		if strings.Contains(tmpl, "fastcgi_cache_valid 200 301") {
			t.Fatalf("%s template must not cache redirects", name)
		}
	}
}

func TestSSLTemplatesServeHTTPACMEChallengeWithoutRedirect(t *testing.T) {
	for name, tmpl := range map[string]string{
		"wordpress": nginxHTTPSTemplate,
		"php":       phpHTTPSTemplate,
	} {
		firstServerEnd := strings.Index(tmpl, "\n}\n\nserver {")
		if firstServerEnd < 0 {
			t.Fatalf("%s SSL template missing separate HTTP and HTTPS servers", name)
		}
		httpServer := tmpl[:firstServerEnd]

		for _, want := range []string{
			"root {{.WebRoot}};",
			`add_header Cache-Control "no-store, no-cache, max-age=0, must-revalidate" always;`,
			"location ^~ /.well-known/acme-challenge/ {\n        try_files $uri =404;\n    }",
			"location / {\n        return 301 https://$host$request_uri;\n    }",
		} {
			if !strings.Contains(httpServer, want) {
				t.Fatalf("%s SSL template HTTP server missing %q", name, want)
			}
		}
		if strings.Contains(httpServer, "\n    return 301 https://$host$request_uri;\n") {
			t.Fatalf("%s SSL template must not redirect every HTTP request at server scope", name)
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

func TestNginxSecurityProbeMapConfigDefinesIndependentVariables(t *testing.T) {
	config := nginxGlobalLogMapConfig()

	for _, want := range []string{
		"map $uri $wp_uri_security_loggable {",
		"map $request_uri $wp_sqli_probe_hit {",
		"geo $wp_security_verified_googlebot_ip {",
		"geo $wp_security_verified_bingbot_ip {",
		"map $http_user_agent $wp_security_claims_googlebot {",
		"map $http_user_agent $wp_security_claims_bingbot {",
		`map "$wp_security_claims_googlebot:$wp_security_verified_googlebot_ip" $wp_fake_googlebot_hit {`,
		`map "$wp_security_claims_bingbot:$wp_security_verified_bingbot_ip" $wp_fake_bingbot_hit {`,
		`map "$wp_fake_googlebot_hit$wp_fake_bingbot_hit" $wp_fake_search_bot_hit {`,
		`map "$wp_uri_security_loggable$wp_sqli_probe_hit$wp_fake_search_bot_hit" $wp_security_loggable {`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("nginxGlobalLogMapConfig() missing %q", want)
		}
	}

	// 阶段二新增的变量名必须和 rate_limit.go 里 Bot 限流用的变量名完全不同，
	// 否则 Bot 限流关闭时（该文件被删除）或同时启用时会导致 nginx -t 失败。
	for _, forbidden := range []string{"$wp_verified_search_bot_ip", "$wp_search_bot_ua"} {
		if strings.Contains(config, forbidden) {
			t.Fatalf("nginxGlobalLogMapConfig() must not reuse bot rate-limit variable %q", forbidden)
		}
	}
}

func TestNginxSecurityProbeMapConfigSQLiPatterns(t *testing.T) {
	config := nginxSecurityProbeMapConfig()
	for _, want := range []string{
		`~*union(?:\s|%20|\+)+select 1;`,
		`~*sleep\s*\( 1;`,
		`~*information_schema 1;`,
		// nginx map 里含 { } ; 等特殊字符的模式必须加双引号，否则 nginx -t 会报
		// "unexpected {" 或 "invalid number of the map parameters"，导致
		// EnsureLogMap() 整体回滚、方案 D 阶段二在真实服务器上永远不会生效。
		`"~*0x[0-9a-f]{16,}" 1;`,
		`"~*(?:;|%3b)(?:\s|%20|\+)*(?:drop|insert|update|delete)(?:\s|%20|\+)+" 1;`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("nginxSecurityProbeMapConfig() missing %q", want)
		}
	}
}

func TestNginxSecurityProbeMapConfigDisablesFakeBotRuleWhenNoOfficialIPCache(t *testing.T) {
	// 没有初始化数据库时 renderVerifiedSearchBotGeoEntries() 返回空字符串，
	// 等价于官方 IP 段缓存为空（新装/白名单刷新任务尚未跑过）。这种情况下不能
	// 生成 "1:0" -> 1 这条规则，否则每一个真实 Googlebot/Bingbot 都会被判定为伪装。
	config := nginxSecurityProbeMapConfig()
	if strings.Contains(config, `"1:0" 1;`) {
		t.Fatalf("nginxSecurityProbeMapConfig() must not enable the fake-bot rule when the official IP cache is empty:\n%s", config)
	}
	if !strings.Contains(config, "geo $wp_security_verified_googlebot_ip {") {
		t.Fatal("nginxSecurityProbeMapConfig() missing split googlebot geo block even with empty IP cache")
	}
}

func TestNginxSecurityProbeMapConfigEnablesFakeBotRulePerCachedProvider(t *testing.T) {
	openTestDB(t)
	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = ? WHERE skey = 'googlebot_ips'`, "66.249.64.0/19"); err != nil {
		t.Fatalf("seed googlebot_ips: %v", err)
	}
	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = '' WHERE skey = 'bingbot_ips'`); err != nil {
		t.Fatalf("clear bingbot_ips: %v", err)
	}

	config := nginxSecurityProbeMapConfig()
	googleMapStart := strings.Index(config, `map "$wp_security_claims_googlebot:$wp_security_verified_googlebot_ip" $wp_fake_googlebot_hit {`)
	bingMapStart := strings.Index(config, `map "$wp_security_claims_bingbot:$wp_security_verified_bingbot_ip" $wp_fake_bingbot_hit {`)
	if googleMapStart < 0 || bingMapStart < 0 {
		t.Fatalf("missing split fake bot maps:\n%s", config)
	}
	googleMap := config[googleMapStart:bingMapStart]
	bingMapEnd := strings.Index(config[bingMapStart:], `map "$wp_fake_googlebot_hit$wp_fake_bingbot_hit"`)
	if bingMapEnd < 0 {
		t.Fatalf("missing combined fake bot map:\n%s", config)
	}
	bingMap := config[bingMapStart : bingMapStart+bingMapEnd]

	if !strings.Contains(googleMap, `"1:0" 1;`) {
		t.Fatalf("googlebot fake rule should be enabled when google ranges are cached:\n%s", googleMap)
	}
	if strings.Contains(bingMap, `"1:0" 1;`) {
		t.Fatalf("bingbot fake rule must stay disabled when bing ranges are not cached:\n%s", bingMap)
	}
}
