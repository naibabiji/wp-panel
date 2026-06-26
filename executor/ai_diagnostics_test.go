package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

func TestParseAIReportJSON(t *testing.T) {
	report, raw, ok := ParseAIReport(`{"summary":"发现 PHP Fatal","risk_level":"high","likely_causes":[],"recommended_actions":[],"needs_more_info":false,"user_friendly_explanation":"请查看错误日志"}`)
	if !ok {
		t.Fatalf("ParseAIReport() ok = false, raw=%q", raw)
	}
	if report.Summary != "发现 PHP Fatal" || report.RiskLevel != "high" {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestParseAIReportFlexibleStringEvidence(t *testing.T) {
	report, raw, ok := ParseAIReport(`{
		"summary": "站点未开启缓存",
		"risk_level": "medium",
		"likely_causes": [
			{
				"title": "未启用 FastCGI 缓存且无 WordPress 缓存插件",
				"confidence": "high",
				"evidence": "fastcgi_cache_enabled 为 false，page_cache_plugins 为空"
			}
		],
		"recommended_actions": [
			{
				"label": "启用 FastCGI 缓存",
				"description": "降低 PHP 动态请求压力",
				"risk": "low",
				"manual_steps": "进入网站详情启用 FastCGI 缓存",
				"panel_action_hint": "网站详情 -> WordPress优化"
			}
		],
		"needs_more_info": false,
		"user_friendly_explanation": "当前缺少缓存。"
	}`)
	if !ok {
		t.Fatalf("ParseAIReport() ok = false, raw=%q", raw)
	}
	if report == nil || len(report.LikelyCauses) != 1 || len(report.LikelyCauses[0].Evidence) != 1 {
		t.Fatalf("unexpected report causes: %#v", report)
	}
	if report.LikelyCauses[0].Evidence[0] != "fastcgi_cache_enabled 为 false，page_cache_plugins 为空" {
		t.Fatalf("evidence = %#v", report.LikelyCauses[0].Evidence)
	}
	if len(report.RecommendedActions) != 1 || len(report.RecommendedActions[0].ManualSteps) != 1 {
		t.Fatalf("unexpected report actions: %#v", report.RecommendedActions)
	}
}

func TestParseAIReportFallbackRawText(t *testing.T) {
	report, raw, ok := ParseAIReport("not json")
	if ok || report != nil {
		t.Fatalf("expected parse failure, got ok=%v report=%#v", ok, report)
	}
	if raw != "not json" {
		t.Fatalf("raw = %q", raw)
	}
}

func TestAIExtractChatContentStandardChoice(t *testing.T) {
	content, err := aiExtractChatContent([]byte(`{
		"choices": [
			{"message": {"content": "{\"summary\":\"ok\"}"}}
		]
	}`))
	if err != nil {
		t.Fatalf("aiExtractChatContent() error = %v", err)
	}
	if content != `{"summary":"ok"}` {
		t.Fatalf("content = %q", content)
	}
}

func TestAIExtractChatContentArrayParts(t *testing.T) {
	content, err := aiExtractChatContent([]byte(`{
		"choices": [
			{"message": {"content": [
				{"type": "text", "text": "part one"},
				{"type": "text", "text": "part two"}
			]}}
		]
	}`))
	if err != nil {
		t.Fatalf("aiExtractChatContent() error = %v", err)
	}
	if content != "part one\npart two" {
		t.Fatalf("content = %q", content)
	}
}

func TestAIExtractChatContentFallbackOutputText(t *testing.T) {
	content, err := aiExtractChatContent([]byte(`{"output_text":"fallback text"}`))
	if err != nil {
		t.Fatalf("aiExtractChatContent() error = %v", err)
	}
	if content != "fallback text" {
		t.Fatalf("content = %q", content)
	}
}

func TestAIExtractChatContentSSE(t *testing.T) {
	content, err := aiExtractChatContent([]byte(strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"part one "}}]}`,
		`data: {"choices":[{"delta":{"content":"part two"}}]}`,
		`data: [DONE]`,
	}, "\n")))
	if err != nil {
		t.Fatalf("aiExtractChatContent() error = %v", err)
	}
	if content != "part one part two" {
		t.Fatalf("content = %q", content)
	}
}

func TestAIExtractChatContentReportsNonJSONPreview(t *testing.T) {
	_, err := aiExtractChatContent([]byte("<html><title>Proxy Error</title></html>"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "响应片段：<html><title>Proxy Error</title></html>") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestCallAIChatRetriesBadResponse(t *testing.T) {
	oldDelay := aiProviderRetryDelay
	aiProviderRetryDelay = time.Millisecond
	t.Cleanup(func() { aiProviderRetryDelay = oldDelay })

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("temporary gateway response"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	t.Cleanup(server.Close)

	settings := &models.AISettings{
		BaseURL:        server.URL,
		Model:          "test-model",
		TimeoutSeconds: 5,
	}
	content, _, err := CallAIChat(context.Background(), settings, "system", "user")
	if err != nil {
		t.Fatalf("CallAIChat() error = %v", err)
	}
	if content != "ok" {
		t.Fatalf("content = %q, want ok", content)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestBuildAIDiagnosticPromptRedactsWPSecrets(t *testing.T) {
	stubAIHTTPProbe(t, 200, 302)

	root := t.TempDir()
	logDir := t.TempDir()
	config := `<?php
define('DB_NAME', 'db_example');
define('DB_USER', 'user_example');
define('DB_PASSWORD', 'super-secret');
define('AUTH_KEY', 'auth-secret');
$table_prefix = 'wp_';
define('WP_DEBUG', true);
`
	if err := os.WriteFile(filepath.Join(root, "wp-config.php"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "php-error.log"), []byte("PHP Fatal error: test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	site := &models.Website{
		ID:            1,
		Domain:        "example.com",
		SiteType:      "wordpress",
		WebRoot:       root,
		LogDir:        logDir,
		DBName:        "db_example",
		DBUser:        "user_example",
		PHPPoolPath:   filepath.Join(root, "pool.conf"),
		NginxConfPath: filepath.Join(root, "nginx.conf"),
	}
	_, prompt, err := BuildAIDiagnosticPrompt(site, models.AIDiagnosisSite500)
	if err != nil {
		t.Fatalf("BuildAIDiagnosticPrompt() error = %v", err)
	}
	if strings.Contains(prompt, "super-secret") || strings.Contains(prompt, "auth-secret") {
		t.Fatalf("prompt leaked secret:\n%s", prompt)
	}
	if !strings.Contains(prompt, "PHP Fatal error") {
		t.Fatalf("prompt missing log evidence:\n%s", prompt)
	}
	if !strings.Contains(prompt, `"contains_db_password": "redacted"`) {
		t.Fatalf("prompt missing redaction marker:\n%s", prompt)
	}
}

func TestBuildAIDiagnosticPromptIncludesWPConfigSyntaxError(t *testing.T) {
	stubAIHTTPProbe(t, 500, 500)

	oldRunPHPLint := aiRunPHPLint
	aiRunPHPLint = func(path string) (*ExecResult, error) {
		return &ExecResult{
			Stderr:   "PHP Parse error: syntax error, unexpected end of file in " + path + " on line 12\nErrors parsing " + path,
			ExitCode: 255,
		}, errors.New("命令 php 执行失败")
	}
	t.Cleanup(func() { aiRunPHPLint = oldRunPHPLint })

	root := t.TempDir()
	logDir := t.TempDir()
	configPath := filepath.Join(root, "wp-config.php")
	if err := os.WriteFile(configPath, []byte("<?php\ndefine('DB_NAME', 'db_example';\n$table_prefix = 'wp_';\n"), 0600); err != nil {
		t.Fatal(err)
	}
	site := &models.Website{
		ID:            1,
		Domain:        "example.com",
		SiteType:      "wordpress",
		WebRoot:       root,
		LogDir:        logDir,
		DBName:        "db_example",
		DBUser:        "user_example",
		PHPPoolPath:   filepath.Join(root, "pool.conf"),
		NginxConfPath: filepath.Join(root, "nginx.conf"),
	}

	_, prompt, err := BuildAIDiagnosticPrompt(site, models.AIDiagnosisSite500)
	if err != nil {
		t.Fatalf("BuildAIDiagnosticPrompt() error = %v", err)
	}
	for _, want := range []string{`"php_syntax_check"`, `"ok": false`, "PHP Parse error", "wp-config.php PHP 语法检查失败"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, configPath) {
		t.Fatalf("prompt leaked absolute config path:\n%s", prompt)
	}
}

func TestAICodeSuspectsDetectsActiveThemeWPDie(t *testing.T) {
	oldOptions := aiReadWPDiagnosticOptions
	aiReadWPDiagnosticOptions = func(dbName, tablePrefix string, cfg *config.Config) (map[string]string, error) {
		return map[string]string{
			"template":       "demo-theme",
			"stylesheet":     "demo-theme",
			"active_plugins": `a:0:{}`,
		}, nil
	}
	t.Cleanup(func() { aiReadWPDiagnosticOptions = oldOptions })

	oldRunPHPLint := aiRunPHPLint
	aiRunPHPLint = func(path string) (*ExecResult, error) {
		return &ExecResult{Stdout: "No syntax errors detected in " + path}, nil
	}
	t.Cleanup(func() { aiRunPHPLint = oldRunPHPLint })

	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{}
	t.Cleanup(func() { config.AppConfig = oldConfig })

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "wp-config.php"), []byte("<?php\n$table_prefix = 'wp_';\n"), 0600); err != nil {
		t.Fatal(err)
	}
	themeDir := filepath.Join(root, "wp-content", "themes", "demo-theme")
	if err := os.MkdirAll(themeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeDir, "functions.php"), []byte("<?php\nadd_action('init', function () {});\nwp_die();\n"), 0644); err != nil {
		t.Fatal(err)
	}

	site := &models.Website{
		ID:       1,
		Domain:   "example.com",
		SiteType: "wordpress",
		WebRoot:  root,
		DBName:   "db_example",
		DBUser:   "user_example",
	}
	ctx := aiCodeSuspects(site)
	data, err := json.Marshal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	for _, want := range []string{"wp_die(", "wp-content/themes/demo-theme/functions.php", "顶层 wp_die"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("code suspects missing %q:\n%s", want, raw)
		}
	}
	if strings.Contains(raw, root) {
		t.Fatalf("code suspects leaked absolute root:\n%s", raw)
	}
}

func TestAIScanPHPFileClassifiesConditionalTerminationAsLow(t *testing.T) {
	oldRunPHPLint := aiRunPHPLint
	aiRunPHPLint = func(path string) (*ExecResult, error) {
		return &ExecResult{Stdout: "No syntax errors detected in " + path}, nil
	}
	t.Cleanup(func() { aiRunPHPLint = oldRunPHPLint })

	root := t.TempDir()
	pluginDir := filepath.Join(root, "wp-content", "plugins", "wpterm")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	pluginPath := filepath.Join(pluginDir, "wpterm.php")
	code := `<?php
if (defined('DISALLOW_FILE_MODS') && DISALLOW_FILE_MODS) {
	wp_die('File mods disabled');
}
die('top level stop');
`
	if err := os.WriteFile(pluginPath, []byte(code), 0644); err != nil {
		t.Fatal(err)
	}

	suspects := aiScanPHPFileForSuspects(root, pluginPath, "active_plugin_main")
	if len(suspects) != 2 {
		t.Fatalf("suspects count = %d, want 2: %#v", len(suspects), suspects)
	}
	conditional := suspects[0]
	if conditional["pattern"] != "wp_die(" || conditional["severity"] != "low" || conditional["context"] != "conditional_block" {
		t.Fatalf("conditional wp_die suspect = %#v, want low conditional_block", conditional)
	}
	topLevel := suspects[1]
	if topLevel["pattern"] != "die(" || topLevel["severity"] != "high" {
		t.Fatalf("top-level die suspect = %#v, want high", topLevel)
	}
	if _, ok := topLevel["context"]; ok {
		t.Fatalf("top-level die should not have conditional context: %#v", topLevel)
	}
}

func TestAIPromptWithinBudgetPreservesHighCodeSuspect(t *testing.T) {
	longLine := strings.Repeat("fatal context ", 200)
	var logLines []string
	for i := 0; i < 80; i++ {
		logLines = append(logLines, longLine)
	}
	var recentFiles []map[string]interface{}
	for i := 0; i < 20; i++ {
		recentFiles = append(recentFiles, map[string]interface{}{
			"file":     fmt.Sprintf("wp-content/themes/demo/file-%02d.php", i),
			"modified": time.Now().Format(time.RFC3339),
			"size":     1234,
		})
	}
	ctx := aiDiagnosticContext{
		DiagnosisType:  models.AIDiagnosisSite500,
		DiagnosisLabel: "网站 500 / 白屏",
		PanelContext:   aiPanelContext(),
		SiteSummary:    map[string]interface{}{"domain": "example.com"},
		LocalChecks:    map[string]interface{}{"has_hits": true},
		Logs: map[string]aiLogSnippet{
			"nginx_error": {Source: "error.log", Status: "ok", Lines: logLines},
			"php_error":   {Source: "php-error.log", Status: "ok", Lines: logLines},
			"wp_security": {Source: "wp-security.log", Status: "ok", Lines: logLines},
			"access_5xx":  {Source: "access.log", Status: "ok", Lines: logLines},
		},
		WPConfigSummary: map[string]interface{}{"checked": true},
		DBCheck:         map[string]interface{}{"checked": true, "ok": true},
		ServiceChecks:   map[string]interface{}{"web_root_exists": true},
		CodeSuspects: map[string]interface{}{
			"checked": true,
			"suspects": []map[string]interface{}{
				{
					"scope":    "active_theme_functions",
					"file":     "wp-content/themes/demo/functions.php",
					"line":     12,
					"pattern":  "wp_die(",
					"severity": "high",
					"reason":   "文件中存在顶层 wp_die 调用，可能直接终止 WordPress 请求并造成白屏/500",
					"snippet":  strings.Repeat("wp_die();\n", 200),
				},
				{
					"scope":    "active_plugin_main",
					"file":     "wp-content/plugins/demo/demo.php",
					"line":     8,
					"pattern":  "include/require",
					"severity": "low",
					"reason":   "低风险 include 线索",
					"snippet":  strings.Repeat("require 'optional.php';\n", 200),
				},
			},
			"recent_php_files": recentFiles,
			"debug_log": map[string]interface{}{
				"checked": true,
				"exists":  true,
				"lines":   logLines,
			},
		},
		RecentPanelOperations: []map[string]string{
			{"operation": "wp_optimizations", "message": strings.Repeat("保存优化 ", 100)},
			{"operation": "set_cdn_realip", "message": strings.Repeat("保存 CDN ", 100)},
		},
		Constraints:  map[string]interface{}{"phase": "readonly_diagnosis"},
		OutputSchema: aiOutputSchema(),
	}

	prompt, err := aiPromptWithinBudgetLimit(&ctx, 7000)
	if err != nil {
		t.Fatalf("aiPromptWithinBudgetLimit() error = %v", err)
	}
	if len(prompt) > 7000 {
		t.Fatalf("prompt length = %d, want <= 7000", len(prompt))
	}
	for _, want := range []string{"wp_die(", "wp-content/themes/demo/functions.php", "prompt_notes"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildAIDiagnosticPromptIncludesWPPanelBoundaries(t *testing.T) {
	stubAIHTTPProbe(t, 200, 302)

	root := t.TempDir()
	logDir := t.TempDir()
	site := &models.Website{
		ID:            1,
		Domain:        "example.com",
		SiteType:      "wordpress",
		WebRoot:       root,
		LogDir:        logDir,
		DBName:        "db_example",
		DBUser:        "user_example",
		PHPPoolPath:   filepath.Join(root, "pool.conf"),
		NginxConfPath: filepath.Join(root, "nginx.conf"),
	}

	systemPrompt, userPrompt, err := BuildAIDiagnosticPrompt(site, models.AIDiagnosisWPAdminDown)
	if err != nil {
		t.Fatalf("BuildAIDiagnosticPrompt() error = %v", err)
	}
	for _, want := range []string{"宝塔面板", "1Panel", "cPanel", "Plesk", "WP Panel 的实际产品能力", "不要把它表述为“服务未宕机”", "软件管理", "context=conditional_block", "不要编造“仪表盘 -> 监控设置”"} {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, systemPrompt)
		}
	}
	for _, want := range []string{"panel_context", "仪表盘", "网站管理 -> 对应站点 -> 详情", "网站详情 -> 基本信息 -> 文件管理", "网站详情 -> 网站监控", "软件管理", "不要声称已经完成运行状态检查", "不要写“登录宝塔面板”", "WP Panel 没有这个入口", "不是资源监控或性能数据采集开关"} {
		if !strings.Contains(userPrompt, want) {
			t.Fatalf("user prompt missing %q:\n%s", want, userPrompt)
		}
	}
}

func TestBuildAIDiagnosticPromptIncludesPerformanceSummary(t *testing.T) {
	stubAIHTTPProbe(t, 200, 302)
	setupAIPerformanceTestDB(t)

	oldRunProcessList := aiRunProcessList
	aiRunProcessList = func() ([]byte, error) {
		return []byte(strings.Join([]string{
			"USER                              %CPU %MEM COMMAND",
			"wp_demo                           37.5  5.5 php-fpm8.3",
			"wp_noisy                          82.0 12.0 php-fpm8.3",
		}, "\n")), nil
	}
	t.Cleanup(func() { aiRunProcessList = oldRunProcessList })

	oldOptions := aiReadWPDiagnosticOptions
	aiReadWPDiagnosticOptions = func(dbName, tablePrefix string, cfg *config.Config) (map[string]string, error) {
		return map[string]string{
			"template":   "twentytwentyfive",
			"stylesheet": "twentytwentyfive",
			"active_plugins": `a:4:{i:0;s:23:"elementor/elementor.php";i:1;s:23:"wp-rocket/wp-rocket.php";` +
				`i:2;s:27:"autoptimize/autoptimize.php";i:3;s:27:"woocommerce/woocommerce.php";}`,
		}, nil
	}
	t.Cleanup(func() { aiReadWPDiagnosticOptions = oldOptions })

	oldRunPHPLint := aiRunPHPLint
	aiRunPHPLint = func(path string) (*ExecResult, error) {
		return &ExecResult{Stdout: "No syntax errors detected in " + path}, nil
	}
	t.Cleanup(func() { aiRunPHPLint = oldRunPHPLint })

	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{}
	t.Cleanup(func() { config.AppConfig = oldConfig })

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "wp-config.php"), []byte("<?php\n$table_prefix = 'wp_';\n"), 0600); err != nil {
		t.Fatal(err)
	}
	site := &models.Website{
		ID:                 1,
		Domain:             "demo.com",
		SiteType:           "wordpress",
		Status:             "active",
		SystemUser:         "wp_demo",
		WebRoot:            root,
		DBName:             "db_example",
		DBUser:             "user_example",
		FCacheEnabled:      false,
		FCacheTTL:          300,
		MonitoringEnabled:  true,
		AccessLogMode:      "full",
		WPMemoryLimit:      "256M",
		PHPPoolPath:        filepath.Join(root, "pool.conf"),
		NginxConfPath:      filepath.Join(root, "nginx.conf"),
		LogDir:             t.TempDir(),
		WPDebugEnabled:     false,
		XMLRPCEnabled:      true,
		MonitoringInterval: 5,
	}

	systemPrompt, userPrompt, err := BuildAIDiagnosticPrompt(site, models.AIDiagnosisPerformance)
	if err != nil {
		t.Fatalf("BuildAIDiagnosticPrompt() error = %v", err)
	}
	for _, want := range []string{
		"diagnosis_profile",
		`"profile": "performance"`,
		"performance_summary",
		"server_resource_summary",
		"site_resource_summary",
		"top_php_fpm_sites",
		"noisy.com",
		"page_cache_plugins",
		"asset_optimization_plugins",
		"WP Rocket",
		"Autoptimize",
		`"multiple_page_cache_plugins": false`,
		`"fastcgi_cache_enabled": false`,
	} {
		if !strings.Contains(userPrompt, want) {
			t.Fatalf("performance prompt missing %q:\n%s", want, userPrompt)
		}
	}
	if !strings.Contains(systemPrompt, "不要把性能问题默认当成 500 或服务宕机") {
		t.Fatalf("system prompt missing performance rule:\n%s", systemPrompt)
	}
}

func TestBuildAIDiagnosticPromptForbidsPageCachePluginWhenFastCGIEnabled(t *testing.T) {
	stubAIHTTPProbe(t, 200, 302)

	oldRunProcessList := aiRunProcessList
	aiRunProcessList = func() ([]byte, error) {
		return []byte("USER                              %CPU %MEM COMMAND\nwp_demo                            2.0  1.0 php-fpm8.3\n"), nil
	}
	t.Cleanup(func() { aiRunProcessList = oldRunProcessList })

	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{}
	t.Cleanup(func() { config.AppConfig = oldConfig })

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "wp-config.php"), []byte("<?php\n$table_prefix = 'wp_';\n"), 0600); err != nil {
		t.Fatal(err)
	}
	site := &models.Website{
		ID:            1,
		Domain:        "demo.com",
		SiteType:      "wordpress",
		Status:        "active",
		SystemUser:    "wp_demo",
		WebRoot:       root,
		LogDir:        t.TempDir(),
		DBName:        "db_example",
		DBUser:        "user_example",
		FCacheEnabled: true,
		FCacheTTL:     300,
		PHPPoolPath:   filepath.Join(root, "pool.conf"),
		NginxConfPath: filepath.Join(root, "nginx.conf"),
	}

	systemPrompt, userPrompt, err := BuildAIDiagnosticPrompt(site, models.AIDiagnosisPerformance)
	if err != nil {
		t.Fatalf("BuildAIDiagnosticPrompt() error = %v", err)
	}
	for _, want := range []string{
		"不能建议安装或启用 WordPress 页面缓存插件",
		"例如 WP Super Cache",
		"验证 FastCGI 缓存命中",
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, systemPrompt)
		}
	}
	for _, want := range []string{
		`"fastcgi_cache_enabled": true`,
		"cache_recommendation_policy",
		"WP Panel FastCGI 缓存已开启时，不要建议安装或启用 WordPress 页面缓存插件。",
		"WP Super Cache 页面缓存",
		"验证 X-FastCGI-Cache 是否命中",
		"检查 FastCGI 缓存 TTL、清理机制和绕过规则",
	} {
		if !strings.Contains(userPrompt, want) {
			t.Fatalf("user prompt missing %q:\n%s", want, userPrompt)
		}
	}
}

func TestBuildAIDiagnosticPromptIncludesCurrentHTTPChecksOverHistorical5xx(t *testing.T) {
	stubAIHTTPProbe(t, 200, 302)

	root := t.TempDir()
	logDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "wp-config.php"), []byte("<?php\n$table_prefix = 'wp_';\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "access.log"), []byte(`127.0.0.1 - - [26/Jun/2026:00:01:00 +0800] "GET / HTTP/1.1" 500 12 "-" "test"`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	site := &models.Website{
		ID:            1,
		Domain:        "example.com",
		SiteType:      "wordpress",
		WebRoot:       root,
		LogDir:        logDir,
		DBName:        "db_example",
		DBUser:        "user_example",
		PHPPoolPath:   filepath.Join(root, "pool.conf"),
		NginxConfPath: filepath.Join(root, "nginx.conf"),
	}

	systemPrompt, userPrompt, err := BuildAIDiagnosticPrompt(site, models.AIDiagnosisSite500)
	if err != nil {
		t.Fatalf("BuildAIDiagnosticPrompt() error = %v", err)
	}
	for _, want := range []string{
		"current_http_checks",
		`"status_code": 200`,
		`"status_code": 302`,
		"access_5xx",
		"不能单独证明当前仍然 500",
	} {
		if !strings.Contains(userPrompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, userPrompt)
		}
	}
	if !strings.Contains(systemPrompt, "不要声称“当前网站 500”") {
		t.Fatalf("system prompt missing current HTTP rule:\n%s", systemPrompt)
	}
}

func TestBuildAIFollowupPromptIncludesConversationAndCurrentContext(t *testing.T) {
	stubAIHTTPProbe(t, 200, 302)

	root := t.TempDir()
	logDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "wp-config.php"), []byte("<?php\n$table_prefix = 'wp_';\n"), 0600); err != nil {
		t.Fatal(err)
	}
	site := &models.Website{
		ID:            1,
		Domain:        "example.com",
		SiteType:      "wordpress",
		WebRoot:       root,
		LogDir:        logDir,
		DBName:        "db_example",
		DBUser:        "user_example",
		PHPPoolPath:   filepath.Join(root, "pool.conf"),
		NginxConfPath: filepath.Join(root, "nginx.conf"),
	}
	session := &models.AISessionDetail{
		ID:        10,
		SiteID:    1,
		Symptom:   models.AIDiagnosisSite500,
		Status:    models.AISessionCompleted,
		RiskLevel: "high",
		Summary:   "之前发现 500",
		Report: &models.AIDiagnosticReport{
			Summary: "主题 functions.php 有 wp_die",
		},
	}
	messages := []models.AIMessage{
		{Role: "assistant", Content: "建议开启调试模式", CreatedAt: time.Now()},
		{Role: "user", Content: "我已经开启了调试模式", CreatedAt: time.Now()},
	}

	systemPrompt, userPrompt, err := BuildAIFollowupPrompt(site, session, messages, "你再次检查")
	if err != nil {
		t.Fatalf("BuildAIFollowupPrompt() error = %v", err)
	}
	for _, want := range []string{
		"诊断追问助手",
		"current_site_context",
		"recent_conversation",
		"latest_user_message",
		"你再次检查",
		"建议开启调试模式",
		`"status_code": 200`,
		"用中文直接回答用户本轮反馈，不要输出 JSON",
	} {
		combined := systemPrompt + "\n" + userPrompt
		if !strings.Contains(combined, want) {
			t.Fatalf("followup prompt missing %q:\n%s", want, combined)
		}
	}
}

func TestAIPluginPerformanceClassificationSeparatesCacheTypes(t *testing.T) {
	activePlugins := []string{
		"wp-rocket/wp-rocket.php",
		"redis-cache/redis-cache.php",
		"autoptimize/autoptimize.php",
	}

	pageCachePlugins := aiClassifyPlugins(activePlugins, aiPageCachePluginPatterns())
	objectCachePlugins := aiClassifyPlugins(activePlugins, aiObjectCachePluginPatterns())
	assetOptimizationPlugins := aiClassifyPlugins(activePlugins, aiAssetOptimizationPluginPatterns())

	if len(pageCachePlugins) != 1 || pageCachePlugins[0]["label"] != "WP Rocket" {
		t.Fatalf("page cache plugins = %#v, want only WP Rocket", pageCachePlugins)
	}
	if len(objectCachePlugins) != 1 || objectCachePlugins[0]["label"] != "Redis Object Cache" {
		t.Fatalf("object cache plugins = %#v, want only Redis Object Cache", objectCachePlugins)
	}
	if len(assetOptimizationPlugins) != 2 {
		t.Fatalf("asset optimization plugins = %#v, want WP Rocket and Autoptimize", assetOptimizationPlugins)
	}
}

func stubAIHTTPProbe(t *testing.T, homeStatus, adminStatus int) {
	t.Helper()
	oldProbe := aiProbeHTTP
	aiProbeHTTP = func(target string) map[string]interface{} {
		status := homeStatus
		if strings.Contains(target, "/wp-admin/") {
			status = adminStatus
		}
		return map[string]interface{}{
			"checked":                       true,
			"url":                           target,
			"method":                        http.MethodHead,
			"status_code":                   status,
			"status":                        fmt.Sprintf("%d stub", status),
			"status_family":                 aiHTTPStatusFamily(status),
			"is_5xx":                        status >= 500 && status <= 599,
			"is_currently_available_signal": status > 0 && status < 500,
		}
	}
	t.Cleanup(func() { aiProbeHTTP = oldProbe })
}

func TestAIOperationLabel(t *testing.T) {
	if got := aiOperationLabel("wp_optimizations"); got != "WordPress 优化设置" {
		t.Fatalf("wp_optimizations label = %q", got)
	}
	if got := aiOperationLabel("set_cdn_realip"); got != "CDN 真实 IP 设置" {
		t.Fatalf("set_cdn_realip label = %q", got)
	}
	if got := aiOperationLabel("custom_op"); got != "custom_op" {
		t.Fatalf("custom_op label = %q", got)
	}
}

func setupAIPerformanceTestDB(t *testing.T) {
	t.Helper()
	oldDB := database.DB
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.RunMigrations(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
		database.DB = oldDB
	})

	_, err := database.GetDB().Exec(`
		INSERT INTO websites (id, name, domain, aliases, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type)
		VALUES
		(1, 'demo', 'demo.com', '', 'active', 'wp_demo', '/tmp/demo', '/tmp/logs/demo', 'db_example', 'user_example', '/tmp/demo.conf', '/tmp/demo.nginx', 'wordpress'),
		(2, 'noisy', 'noisy.com', '', 'active', 'wp_noisy', '/tmp/noisy', '/tmp/logs/noisy', 'db_noisy', 'user_noisy', '/tmp/noisy.conf', '/tmp/noisy.nginx', 'wordpress')
	`)
	if err != nil {
		t.Fatalf("insert websites: %v", err)
	}

	now := time.Now().UTC()
	for i, row := range []struct {
		cpu, mem, load float64
	}{
		{42, 55, 1.2},
		{96, 91, 3.5},
		{65, 70, 2.1},
	} {
		recordedAt := now.Add(time.Duration(-i) * time.Minute).Format("2006-01-02 15:04:05")
		if _, err := database.GetDB().Exec(`
			INSERT INTO monitoring_metrics (cpu_percent, memory_percent, memory_used_bytes, memory_total_bytes, disk_read_bytes, disk_write_bytes, load_avg_1, load_avg_5, load_avg_15, recorded_at)
			VALUES (?, ?, 1000, 2000, 0, 0, ?, ?, ?, ?)
		`, row.cpu, row.mem, row.load, row.load, row.load, recordedAt); err != nil {
			t.Fatalf("insert monitoring metric: %v", err)
		}
	}
}

func TestAIReadLogSnippetDistinguishesMissingAndSymlinkEscape(t *testing.T) {
	logDir := t.TempDir()
	missing := aiReadLogSnippet(logDir, "missing.log")
	if missing.Status != "not_found" {
		t.Fatalf("missing log status = %q, want not_found", missing.Status)
	}

	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("secret\n"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(logDir, "error.log")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	got := aiReadLogSnippet(logDir, "error.log")
	if got.Status != "forbidden" {
		t.Fatalf("symlink escape status = %q, want forbidden", got.Status)
	}
}

func TestAITailInterestingLinesKeepsNewestLinesWhenCapped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "php-error.log")
	content := strings.Join([]string{
		"old line 1 with enough text",
		"old line 2 with enough text",
		"PHP Fatal error: old plugin failure",
		"recent context before fatal",
		"PHP Fatal error: latest plugin failure",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	lines, truncated, err := aiTailInterestingLines(path, 5, 70, false)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("expected truncation")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "latest plugin failure") {
		t.Fatalf("expected latest fatal to be kept, got:\n%s", joined)
	}
	if strings.Contains(joined, "old line 1") {
		t.Fatalf("expected oldest line to be dropped, got:\n%s", joined)
	}
}
