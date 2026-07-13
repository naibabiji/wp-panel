package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/database"
)

func TestIsSQLiProbe(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want bool
	}{
		{"union select raw", "/index.php?id=1 UNION SELECT 1,2,3--", true},
		{"union select url-encoded", "/index.php?id=1%20UNION%20SELECT%201,2,3--", true},
		{"union select plus-encoded", "/index.php?id=1+union+select+1,2,3--", true},
		{"sleep probe", "/index.php?id=1 AND SLEEP(5)", true},
		{"benchmark probe", "/index.php?id=1) OR BENCHMARK(5000000,MD5(1))--", true},
		{"information_schema probe", "/index.php?id=1 AND 1=(SELECT COUNT(*) FROM information_schema.tables)", true},
		{"classic quote-or-equals", "/index.php?id=1%27%20OR%20%271%27=%271", true},
		{"long hex literal", "/index.php?id=0x2261646d696e2761646d696e", true},
		{"stacked query drop", "/index.php?id=1%3BDROP%20TABLE%20users", true},
		{"load_file probe", "/index.php?id=1 UNION SELECT load_file(0x2f6574632f706173737764)", true},

		// legitimate requests containing "or" as a substring must not misfire
		{"search color", "/?s=color", false},
		{"search author query", "/?author=5", false},
		{"category editor", "/?category=editor", false},
		{"search sponsor story", "/?s=sponsor+story", false},
		{"normal upload path", "/wp-content/uploads/2026/report.pdf", false},
		{"short hex nonce", "/?wpnonce=1a2b3c4d", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSQLiProbe(tt.uri); got != tt.want {
				t.Fatalf("isSQLiProbe(%q) = %v, want %v", tt.uri, got, tt.want)
			}
		})
	}
}

func TestIsFakeSearchBot(t *testing.T) {
	checker := &searchBotIPChecker{
		googlebot: []string{"66.249.64.0/19"},
		bingbot:   []string{"40.77.167.0/24"},
	}

	tests := []struct {
		name string
		ua   string
		ip   string
		want bool
	}{
		{"real googlebot in range", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", "66.249.66.1", false},
		{"fake googlebot outside range", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", "1.2.3.4", true},
		{"real bingbot in range", "Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)", "40.77.167.10", false},
		{"fake bingbot outside range", "Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)", "5.6.7.8", true},
		{"normal browser", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/100.0", "8.8.8.8", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFakeSearchBot(tt.ua, tt.ip, checker); got != tt.want {
				t.Fatalf("isFakeSearchBot(%q, %q) = %v, want %v", tt.ua, tt.ip, got, tt.want)
			}
		})
	}
}

func TestIsFakeSearchBotDoesNotFlagWhenOfficialRangesUncached(t *testing.T) {
	// 官方 IP 段缓存为空（新装或白名单刷新任务尚未跑过）时，没有可比对的数据，
	// 不能把所有声称是 Googlebot 的 UA 都当成伪装，否则会把真实的 Google 爬虫
	// （如这里的 66.249.66.1，真实 Googlebot 段）误判为伪装。
	checker := &searchBotIPChecker{}
	if isFakeSearchBot("Mozilla/5.0 (compatible; Googlebot/2.1)", "66.249.66.1", checker) {
		t.Fatal("expected googlebot UA NOT to be flagged as fake when official IP cache is empty")
	}
}

func TestIsFakeSearchBotUsesMatchingOfficialRangeCache(t *testing.T) {
	if isFakeSearchBot("Mozilla/5.0 (compatible; bingbot/2.0)", "40.77.167.10", &searchBotIPChecker{
		googlebot: []string{"66.249.64.0/19"},
	}) {
		t.Fatal("expected bingbot UA NOT to be flagged when only Googlebot ranges are cached")
	}
	if isFakeSearchBot("Mozilla/5.0 (compatible; Googlebot/2.1)", "66.249.66.1", &searchBotIPChecker{
		bingbot: []string{"40.77.167.0/24"},
	}) {
		t.Fatal("expected Googlebot UA NOT to be flagged when only Bingbot ranges are cached")
	}
}

func TestClassifySecurityEvent(t *testing.T) {
	checker := &searchBotIPChecker{googlebot: []string{"66.249.64.0/19"}}

	tests := []struct {
		name     string
		method   string
		uri      string
		ua       string
		ip       string
		status   int
		wantType string
		wantRisk string
	}{
		{
			name:     "sqli probe",
			method:   "GET",
			uri:      "/index.php?id=1%20UNION%20SELECT%201,2,3--",
			ua:       "sqlmap/1.6",
			ip:       "217.216.37.82",
			status:   200,
			wantType: SecurityEventSQLiProbe,
			wantRisk: "high",
		},
		{
			name:     "fake search bot",
			method:   "GET",
			uri:      "/blog/hello-world/",
			ua:       "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
			ip:       "1.2.3.4",
			status:   200,
			wantType: SecurityEventFakeSearchBot,
			wantRisk: "medium",
		},
		{
			name:     "real googlebot not flagged",
			method:   "GET",
			uri:      "/blog/hello-world/",
			ua:       "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
			ip:       "66.249.66.1",
			status:   200,
			wantType: "",
			wantRisk: "low",
		},
		{
			name:     "sensitive file scan",
			method:   "GET",
			uri:      "/.env",
			ua:       "curl/8.0",
			ip:       "94.154.43.10",
			status:   404,
			wantType: SecurityEventSensitiveFileScan,
			wantRisk: "medium",
		},
		{
			name:     "suspicious php probe",
			method:   "GET",
			uri:      "/wp-content/uploads/2026/shell.php",
			ua:       "curl/8.0",
			ip:       "203.0.113.5",
			status:   404,
			wantType: SecurityEventSuspiciousPHP,
			wantRisk: "low",
		},
		{
			name:     "legit search query not misclassified",
			method:   "GET",
			uri:      "/?s=color",
			ua:       "Mozilla/5.0",
			ip:       "203.0.113.6",
			status:   200,
			wantType: "",
			wantRisk: "low",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotRisk, _ := classifySecurityEvent(tt.method, tt.uri, tt.ua, tt.ip, tt.status, checker)
			if gotType != tt.wantType {
				t.Fatalf("classifySecurityEvent() type = %q, want %q", gotType, tt.wantType)
			}
			if gotRisk != tt.wantRisk {
				t.Fatalf("classifySecurityEvent() risk = %q, want %q", gotRisk, tt.wantRisk)
			}
		})
	}
}

func TestBuildWPSecurityReportClassifiesEvents(t *testing.T) {
	openTestDB(t)

	logDir := t.TempDir()
	logContent := strings.Join([]string{
		// SQL 注入探测：query string 里带 URL 编码的 UNION SELECT
		`217.216.37.82 - - [15/Jan/2026:10:00:00 +0800] "GET /index.php?id=1%20UNION%20SELECT%201,2,3-- HTTP/1.1" 200 512 "-" "sqlmap/1.6"`,
		// 伪装 Googlebot：UA 声明 Googlebot，但 IP 不在官方段
		`1.2.3.4 - - [15/Jan/2026:10:01:00 +0800] "GET /blog/hello-world/ HTTP/1.1" 200 300 "-" "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"`,
		// 真实 Googlebot：IP 在官方段内，不应被判定为伪装
		`66.249.66.1 - - [15/Jan/2026:10:02:00 +0800] "GET /blog/hello-world/ HTTP/1.1" 200 300 "-" "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"`,
		// 敏感文件扫描
		`94.154.43.10 - - [15/Jan/2026:10:03:00 +0800] "GET /.env HTTP/1.1" 404 150 "-" "curl/7.68.0"`,
		// 合法搜索请求，不应被误判为 SQLi（含 "or" 子串的正常词）
		`203.0.113.6 - - [15/Jan/2026:10:04:00 +0800] "GET /?s=color HTTP/1.1" 200 800 "-" "Mozilla/5.0"`,
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
	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = ? WHERE skey = 'googlebot_ips'`, "66.249.64.0/19"); err != nil {
		t.Fatalf("seed googlebot_ips: %v", err)
	}

	oldAllowed := wpSecurityLogDirAllowed
	wpSecurityLogDirAllowed = func(string) bool { return true }
	t.Cleanup(func() { wpSecurityLogDirAllowed = oldAllowed })

	resetWPSecurityReportCache()

	items, err := BuildWPSecurityReport(30)
	if err != nil {
		t.Fatalf("BuildWPSecurityReport() error = %v", err)
	}

	byIP := map[string]WPSecurityReportItem{}
	for _, item := range items {
		byIP[item.IPAddress] = item
	}

	sqli, ok := byIP["217.216.37.82"]
	if !ok {
		t.Fatal("missing item for sqli probe IP")
	}
	if !containsString(sqli.Types, SecurityEventSQLiProbe) {
		t.Fatalf("sqli item types = %v, want to contain %q", sqli.Types, SecurityEventSQLiProbe)
	}
	if sqli.RiskLevel != "高" {
		t.Fatalf("a single SQLi probe must elevate risk to 高 immediately, got %q", sqli.RiskLevel)
	}

	fakeBot, ok := byIP["1.2.3.4"]
	if !ok {
		t.Fatal("missing item for fake search bot IP")
	}
	if !containsString(fakeBot.Types, SecurityEventFakeSearchBot) {
		t.Fatalf("fake bot item types = %v, want to contain %q", fakeBot.Types, SecurityEventFakeSearchBot)
	}

	realBot, ok := byIP["66.249.66.1"]
	if ok && containsString(realBot.Types, SecurityEventFakeSearchBot) {
		t.Fatalf("real googlebot IP must not be classified as fake_search_bot, got types %v", realBot.Types)
	}

	sensitive, ok := byIP["94.154.43.10"]
	if !ok {
		t.Fatal("missing item for sensitive file scan IP")
	}
	if !containsString(sensitive.Types, SecurityEventSensitiveFileScan) {
		t.Fatalf("sensitive file item types = %v, want to contain %q", sensitive.Types, SecurityEventSensitiveFileScan)
	}

	if legit, ok := byIP["203.0.113.6"]; ok {
		if containsString(legit.Types, SecurityEventSQLiProbe) {
			t.Fatalf("legit search query must not be classified as sqli_probe, got types %v", legit.Types)
		}
	}
}

func resetWPSecurityReportCache() {
	wpSecurityReportCacheMu.Lock()
	defer wpSecurityReportCacheMu.Unlock()
	wpSecurityReportCache = map[int]wpSecurityReportCacheEntry{}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

// TestCloneWPSecurityReportItemsPreservesEmptyTypesAsNonNil 锁定一个真实 bug：
// append([]string(nil), emptySlice...) 会返回 nil 而不是空切片，JSON 序列化后
// 前端拿到的就是 "types": null，导致 Alpine 的 x-for 遍历 null 报错。
// IP 只命中兜底的"WordPress 异常路径访问"（没有 4 类明确分类）时 Types 就是空的，
// 这是很常见的情况，必须保证克隆后仍是非 nil 的空切片。
func TestCloneWPSecurityReportItemsPreservesEmptyTypesAsNonNil(t *testing.T) {
	items := []WPSecurityReportItem{{
		IPAddress: "203.0.113.20",
		Types:     []string{},
	}}

	cloned := cloneWPSecurityReportItems(items)
	if cloned[0].Types == nil {
		t.Fatal("cloneWPSecurityReportItems() turned an empty Types slice into nil; it will serialize as JSON null and break the frontend x-for")
	}
	if len(cloned[0].Types) != 0 {
		t.Fatalf("cloned Types = %v, want empty", cloned[0].Types)
	}

	encoded, err := json.Marshal(cloned[0])
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), `"types":null`) {
		t.Fatalf("encoded item still contains types:null: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"types":[]`) {
		t.Fatalf("encoded item does not contain types:[]: %s", encoded)
	}
}

func TestBuildWPSecurityReportDoesNotFlagSingleMissingStaticAssetAsPHPProbe(t *testing.T) {
	openTestDB(t)
	logDir := t.TempDir()

	if _, err := database.GetDB().Exec(`INSERT INTO websites
		(id, name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type)
		VALUES (1, 'demo', 'example.com', 'active', 'wp_demo', ?, ?, 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf', 'wordpress')`,
		t.TempDir(), logDir); err != nil {
		t.Fatalf("insert website: %v", err)
	}
	oldAllowed := wpSecurityLogDirAllowed
	wpSecurityLogDirAllowed = func(string) bool { return true }
	t.Cleanup(func() { wpSecurityLogDirAllowed = oldAllowed })

	// 一条普通的缺图 404（很常见、无害），不应该被打上 suspicious_php 标签，
	// 单次出现也不应该被判定为中风险。
	errorLog := `2026/01/15 10:00:00 [error] 1234#0: *1 open() "/var/www/example.com/missing.jpg" failed (2: No such file or directory), client: 203.0.113.5, server: example.com, request: "GET /missing.jpg HTTP/1.1", host: "example.com"` + "\n"
	if err := os.WriteFile(filepath.Join(logDir, "error.log"), []byte(errorLog), 0644); err != nil {
		t.Fatal(err)
	}

	resetWPSecurityReportCache()
	items, err := BuildWPSecurityReport(30)
	if err != nil {
		t.Fatalf("BuildWPSecurityReport() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v, want exactly 1", items)
	}
	if len(items[0].Types) != 0 {
		t.Fatalf("missing static asset item types = %v, want empty (not suspicious_php)", items[0].Types)
	}
	if items[0].RiskLevel != "低" {
		t.Fatalf("single missing static asset risk = %q, want 低 (低risk, matches pre-classification behavior)", items[0].RiskLevel)
	}
}

func TestBuildWPSecurityReportFlagsMissingPHPFileAndPrimaryScriptUnknown(t *testing.T) {
	openTestDB(t)
	logDir := t.TempDir()

	if _, err := database.GetDB().Exec(`INSERT INTO websites
		(id, name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type)
		VALUES (1, 'demo', 'example.com', 'active', 'wp_demo', ?, ?, 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf', 'wordpress')`,
		t.TempDir(), logDir); err != nil {
		t.Fatalf("insert website: %v", err)
	}
	oldAllowed := wpSecurityLogDirAllowed
	wpSecurityLogDirAllowed = func(string) bool { return true }
	t.Cleanup(func() { wpSecurityLogDirAllowed = oldAllowed })

	errorLog := strings.Join([]string{
		// 请求路径确实是 .php，应该判定为 suspicious_php / 中风险
		`2026/01/15 10:00:00 [error] 1234#0: *1 open() "/var/www/example.com/shell.php" failed (2: No such file or directory), client: 203.0.113.6, server: example.com, request: "GET /shell.php HTTP/1.1", host: "example.com"`,
		// Primary script unknown，单次命中就应该是高风险
		`2026/01/15 10:00:01 [error] 1234#0: *2 FastCGI sent in stderr: "Primary script unknown" while reading response header from upstream, client: 203.0.113.7, server: example.com, request: "GET /wp-config.php HTTP/1.1", host: "example.com"`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(logDir, "error.log"), []byte(errorLog), 0644); err != nil {
		t.Fatal(err)
	}

	resetWPSecurityReportCache()
	items, err := BuildWPSecurityReport(30)
	if err != nil {
		t.Fatalf("BuildWPSecurityReport() error = %v", err)
	}

	byIP := map[string]WPSecurityReportItem{}
	for _, item := range items {
		byIP[item.IPAddress] = item
	}

	phpProbe, ok := byIP["203.0.113.6"]
	if !ok {
		t.Fatal("missing item for missing .php file IP")
	}
	if !containsString(phpProbe.Types, SecurityEventSuspiciousPHP) {
		t.Fatalf("missing .php file item types = %v, want to contain %q", phpProbe.Types, SecurityEventSuspiciousPHP)
	}
	if phpProbe.RiskLevel != "中" {
		t.Fatalf("missing .php file risk = %q, want 中", phpProbe.RiskLevel)
	}

	primaryScriptUnknown, ok := byIP["203.0.113.7"]
	if !ok {
		t.Fatal("missing item for Primary script unknown IP")
	}
	if primaryScriptUnknown.RiskLevel != "高" {
		t.Fatalf("Primary script unknown risk = %q, want 高 (must be immediate, even on first occurrence)", primaryScriptUnknown.RiskLevel)
	}
}
