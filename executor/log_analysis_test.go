package executor

import (
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/models"
	_ "modernc.org/sqlite"
)

func TestAnalyzeWebsiteLogsScansRotationsAndVerifiesBots(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE security_settings(skey TEXT PRIMARY KEY,svalue TEXT); INSERT INTO security_settings VALUES
		('googlebot_ips','66.249.64.0/19'),('bingbot_ips','40.77.167.0/24'),('last_whitelist_update','2026-08-14 00:00:00')`); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)
	line := func(ip, path, status, ua string, at time.Time) string {
		return fmt.Sprintf(`%s - - [%s] "GET %s HTTP/1.1" %s 123 "-" "%s"`, ip, at.Format("02/Jan/2006:15:04:05 -0700"), path, status, ua)
	}
	current := line("66.249.66.1", "/archives?token=secret", "200", "Googlebot/2.1", now) + "\n" +
		line("1.2.3.4", "/wp-login.php", "404", "Googlebot/2.1", now) + "\n" +
		line("3.4.5.6", "/broken?token=secret", "502", "Mozilla/5.0", now) + "\n" +
		line("2.3.4.5", "/old", "500", "Mozilla/5.0", now.Add(-8*24*time.Hour)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "access.log"), []byte(current), 0600); err != nil {
		t.Fatal(err)
	}
	gzFile, err := os.Create(filepath.Join(dir, "access.log.1.gz"))
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(gzFile)
	_, _ = gz.Write([]byte(line("40.77.167.10", "/news", "200", "bingbot/2.0", now.Add(-2*time.Hour)) + "\n"))
	_ = gz.Close()
	_ = gzFile.Close()
	phpLine := fmt.Sprintf("[%s] PHP Fatal error: test in /www/site/plugin.php on line 2\n", now.Format("02-Jan-2006 15:04:05 MST"))
	if err := os.WriteFile(filepath.Join(dir, "php-error.log"), []byte(phpLine), 0600); err != nil {
		t.Fatal(err)
	}

	report, err := AnalyzeWebsiteLogs(&models.Website{ID: 1, Domain: "example.com", LogDir: dir}, now.Add(-24*time.Hour), now.Add(time.Minute), db, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if report.AccessRequests != 4 || report.UniqueIPs != 4 {
		t.Fatalf("requests=%d unique=%d", report.AccessRequests, report.UniqueIPs)
	}
	if report.VerifiedSearchCount != 2 || report.FakeSearchBotCount != 1 {
		t.Fatalf("verified=%d fake=%d bots=%+v", report.VerifiedSearchCount, report.FakeSearchBotCount, report.Bots)
	}
	if report.PHPFatalCount != 1 {
		t.Fatalf("php fatal=%d", report.PHPFatalCount)
	}
	if len(report.TopPaths) == 0 || report.TopPaths[0].Name == "/archives?token=secret" {
		t.Fatalf("query string was not removed: %+v", report.TopPaths)
	}
	var fiveXX *models.LogAnalysisFinding
	for i := range report.Findings {
		if report.Findings[i].Title == "发现服务器错误响应" {
			fiveXX = &report.Findings[i]
		}
		if strings.Contains(report.Findings[i].Title, "假冒") {
			t.Fatalf("spoofed crawler must not create a finding: %+v", report.Findings[i])
		}
	}
	if fiveXX == nil || len(fiveXX.Evidence) != 1 || !strings.Contains(fiveXX.Evidence[0], `"GET /broken?token=secret HTTP/1.1" 502`) {
		t.Fatalf("missing 5xx evidence: %+v", fiveXX)
	}
}

func TestAnalyzeWebsiteLogsRejectsUnsafeRangeAndNames(t *testing.T) {
	if isAnalyzableLogName("access.log.secret") || isAnalyzableLogName("error.log.1.gz.bad") {
		t.Fatal("unsafe rotated log name accepted")
	}
	now := time.Now()
	_, err := AnalyzeWebsiteLogs(&models.Website{ID: 1, LogDir: t.TempDir()}, now.Add(-8*24*time.Hour), now, nil, "zh-CN")
	if err == nil {
		t.Fatal("expected range validation error")
	}
}

func TestBuildLogAnalysisPromptRedactsSensitiveSamples(t *testing.T) {
	report := &models.LogAnalysisReport{
		TopIPs:   []models.LogAnalysisCount{{Name: "192.0.2.10", Count: 4}},
		Samples:  []string{"192.0.2.10 token=secret /www/wwwroot/example.com/wp.php"},
		Findings: []models.LogAnalysisFinding{{Evidence: []string{"198.51.100.2 password=hunter2 /www/wwwroot/example.com/error.php"}}},
	}
	_, prompt, err := BuildLogAnalysisPrompt(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"192.0.2.10", "token=secret", "198.51.100.2", "password=hunter2", "/www/wwwroot/example.com"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt leaked %q: %s", forbidden, prompt)
		}
	}
}

func TestAnalyzeWebsiteLogDetailsFiltersAndPaginates(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE security_settings(skey TEXT PRIMARY KEY,svalue TEXT);
		CREATE TABLE firewall_bans(id INTEGER PRIMARY KEY,ip_address TEXT,reason TEXT,source_jail TEXT,banned_at DATETIME,expires_at DATETIME,unbanned_at DATETIME,ban_count INTEGER);
		CREATE TABLE wp_security_events(site_id INTEGER,ip_address TEXT,event_type TEXT,occurred_at DATETIME);
		INSERT INTO security_settings VALUES ('googlebot_ips','66.249.64.0/19'),('bingbot_ips','40.77.167.0/24');
		INSERT INTO firewall_bans VALUES (1,'1.2.3.4','login protection','wppanel',CURRENT_TIMESTAMP,NULL,NULL,2);
		INSERT INTO firewall_bans VALUES (2,'1.2.3.4','historical scan','wppanel-404',datetime('now','+10 minutes'),datetime('now','+20 minutes'),datetime('now','+20 minutes'),3);
		INSERT INTO wp_security_events VALUES (1,'1.2.3.4','sensitive_file_scan',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)
	line := func(ip, path, status, ua string) string {
		return fmt.Sprintf(`%s - - [%s] "GET %s HTTP/1.1" %s 123 "-" "%s"`, ip, now.Format("02/Jan/2006:15:04:05 -0700"), path, status, ua)
	}
	content := line("1.2.3.4", "/archives?a=1", "444", "CustomCrawler/1.0") + "\n" +
		line("1.2.3.5", "/archives?a=2", "444", "Mozilla/5.0") + "\n" +
		line("66.249.66.1", "/page", "200", "Googlebot/2.1") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "access.log"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	site := &models.Website{ID: 1, Domain: "example.com", LogDir: dir}
	status, err := AnalyzeWebsiteLogDetails(site, now.Add(-time.Hour), now.Add(time.Hour), db, "status", "444", 1, 1)
	if err != nil || status.Total != 2 || len(status.Lines) != 1 || !strings.Contains(status.Lines[0], "/archives?a=1") {
		t.Fatalf("status detail=%+v err=%v", status, err)
	}
	if status.UniqueIPs != 2 || status.UniquePaths != 1 || len(status.TopIPs) != 2 || !status.TopIPs[0].CurrentlyBanned || !status.TopIPs[0].BannedInRange || status.TopIPs[0].RetainedEventCount != 1 {
		t.Fatalf("status summary=%+v", status)
	}
	if status.TopIPs[0].CurrentBanSource != "wppanel" || status.TopIPs[0].RangeBanSource != "wppanel-404" || status.TopIPs[0].RangeBanCount != 3 {
		t.Fatalf("current and range ban details were not kept separate: %+v", status.TopIPs[0])
	}
	if len(status.IPPathPairs) != 2 || len(status.UserAgents) != 2 || len(status.Methods) != 1 {
		t.Fatalf("status breakdowns=%+v", status)
	}
	second, err := AnalyzeWebsiteLogDetails(site, now.Add(-time.Hour), now.Add(time.Hour), db, "status", "444", 2, 1)
	if err != nil || len(second.Lines) != 1 || !strings.Contains(second.Lines[0], "/archives?a=2") {
		t.Fatalf("second page=%+v err=%v", second, err)
	}
	pathDetail, err := AnalyzeWebsiteLogDetails(site, now.Add(-time.Hour), now.Add(time.Hour), db, "path", "/archives", 1, 20)
	if err != nil || pathDetail.Total != 2 {
		t.Fatalf("path detail=%+v err=%v", pathDetail, err)
	}
	bot, err := AnalyzeWebsiteLogDetails(site, now.Add(-time.Hour), now.Add(time.Hour), db, "bot", "Other bot:unverified", 1, 20)
	if err != nil || bot.Total != 1 || !strings.Contains(bot.Lines[0], "CustomCrawler") {
		t.Fatalf("bot detail=%+v err=%v", bot, err)
	}
	ipDetail, err := AnalyzeWebsiteLogDetails(site, now.Add(-time.Hour), now.Add(time.Hour), db, "ip", "1.2.3.4", 1, 20)
	if err != nil || ipDetail.Total != 1 || len(ipDetail.Lines) != 1 || !strings.Contains(ipDetail.Lines[0], "/archives?a=1") {
		t.Fatalf("ip detail=%+v err=%v", ipDetail, err)
	}
}

func TestOtherBotDetailSplitsUserAgentsForBehaviorAnalysis(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _ = db.Exec(`CREATE TABLE security_settings(skey TEXT PRIMARY KEY,svalue TEXT)`)
	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)
	line := func(ip, path, status, ua string) string {
		return fmt.Sprintf(`%s - - [%s] "GET %s HTTP/1.1" %s 123 "-" "%s"`, ip, now.Format("02/Jan/2006:15:04:05 -0700"), path, status, ua)
	}
	content := strings.Join([]string{
		line("192.0.2.1", "/post-a", "200", "Amazonbot/0.1"),
		line("192.0.2.2", "/post-b", "200", "PetalBot/1.0"),
		line("192.0.2.3", "/wp-login.php", "404", "SuspiciousCrawler/2.0"),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "access.log"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	detail, err := AnalyzeWebsiteLogDetails(&models.Website{ID: 1, LogDir: dir}, now.Add(-time.Hour), now.Add(time.Hour), db, "bot", "Other bot:unverified", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Total != 3 || len(detail.UserAgents) != 3 || detail.UniqueIPs != 3 || len(detail.StatusCodes) != 2 {
		t.Fatalf("Other bot behavior breakdown incomplete: %+v", detail)
	}
	joined, _ := json.Marshal(detail.UserAgents)
	for _, name := range []string{"Amazonbot", "PetalBot", "SuspiciousCrawler"} {
		if !strings.Contains(string(joined), name) {
			t.Fatalf("missing %s in user-agent breakdown: %s", name, joined)
		}
	}
}
