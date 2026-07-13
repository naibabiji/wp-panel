package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func seedWPSecurityEventSite(t *testing.T, logDir string) {
	t.Helper()
	if _, err := database.GetDB().Exec(`INSERT INTO websites
		(id, name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type)
		VALUES (1, 'demo', 'example.com', 'active', 'wp_demo', ?, ?, 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf', 'wordpress')`,
		t.TempDir(), logDir); err != nil {
		t.Fatalf("insert website: %v", err)
	}

	oldAllowed := wpSecurityLogDirAllowed
	wpSecurityLogDirAllowed = func(string) bool { return true }
	t.Cleanup(func() { wpSecurityLogDirAllowed = oldAllowed })
}

func TestIngestWPSecurityEventsOnlyProcessesNewLines(t *testing.T) {
	openTestDB(t)
	logDir := t.TempDir()
	seedWPSecurityEventSite(t, logDir)

	logPath := filepath.Join(logDir, "wp-security.log")
	line1 := `217.216.37.82 - - [15/Jan/2026:10:00:00 +0800] "GET /index.php?id=1%20UNION%20SELECT%201,2,3-- HTTP/1.1" 200 512 "-" "sqlmap/1.6"` + "\n"
	if err := os.WriteFile(logPath, []byte(line1), 0644); err != nil {
		t.Fatal(err)
	}

	n, err := IngestWPSecurityEvents()
	if err != nil {
		t.Fatalf("IngestWPSecurityEvents() error = %v", err)
	}
	if n != 1 {
		t.Fatalf("first ingest count = %d, want 1", n)
	}

	// 再跑一遍，不应该重复入库同一行
	n, err = IngestWPSecurityEvents()
	if err != nil {
		t.Fatalf("IngestWPSecurityEvents() second run error = %v", err)
	}
	if n != 0 {
		t.Fatalf("second ingest (no new lines) count = %d, want 0", n)
	}

	var total int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM wp_security_events`).Scan(&total); err != nil {
		t.Fatalf("count wp_security_events: %v", err)
	}
	if total != 1 {
		t.Fatalf("wp_security_events row count = %d, want 1", total)
	}

	// 追加新的一行，应该只入库新增的这一条（用敏感文件扫描而不是伪装爬虫，
	// 因为伪装爬虫的判定依赖官方 IP 段缓存，这里没有 seed 缓存数据，不应该
	// 依赖那条修复前的错误行为——官方 IP 段缓存为空时不应该把所有 Googlebot
	// UA 都当成伪装，见 TestIsFakeSearchBotDoesNotFlagWhenOfficialRangesUncached）
	line2 := `1.2.3.4 - - [15/Jan/2026:10:01:00 +0800] "GET /.env HTTP/1.1" 404 300 "-" "curl/8.0"` + "\n"
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line2); err != nil {
		t.Fatal(err)
	}
	f.Close()

	n, err = IngestWPSecurityEvents()
	if err != nil {
		t.Fatalf("IngestWPSecurityEvents() third run error = %v", err)
	}
	if n != 1 {
		t.Fatalf("third ingest count = %d, want 1", n)
	}

	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM wp_security_events`).Scan(&total); err != nil {
		t.Fatalf("count wp_security_events: %v", err)
	}
	if total != 2 {
		t.Fatalf("wp_security_events row count after append = %d, want 2", total)
	}
}

func TestIngestWPSecurityEventsSkipsUnclassifiedEvents(t *testing.T) {
	openTestDB(t)
	logDir := t.TempDir()
	seedWPSecurityEventSite(t, logDir)

	logPath := filepath.Join(logDir, "wp-security.log")
	// 普通的 WordPress 异常路径访问（没有命中 4 类明确分类），不应入库
	line := `203.0.113.9 - - [15/Jan/2026:10:00:00 +0800] "GET /wp-content/some-plugin/readme.txt HTTP/1.1" 404 0 "-" "curl/8.0"` + "\n"
	if err := os.WriteFile(logPath, []byte(line), 0644); err != nil {
		t.Fatal(err)
	}

	n, err := IngestWPSecurityEvents()
	if err != nil {
		t.Fatalf("IngestWPSecurityEvents() error = %v", err)
	}
	if n != 0 {
		t.Fatalf("ingest count = %d, want 0 for unclassified event", n)
	}

	var total int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM wp_security_events`).Scan(&total); err != nil {
		t.Fatalf("count wp_security_events: %v", err)
	}
	if total != 0 {
		t.Fatalf("wp_security_events row count = %d, want 0", total)
	}
}

func TestIngestWPSecurityEventsHandlesPartialTrailingLine(t *testing.T) {
	openTestDB(t)
	logDir := t.TempDir()
	seedWPSecurityEventSite(t, logDir)

	logPath := filepath.Join(logDir, "wp-security.log")
	complete := `94.154.43.10 - - [15/Jan/2026:10:00:00 +0800] "GET /.env HTTP/1.1" 404 150 "-" "curl/7.68.0"` + "\n"
	partial := `94.154.43.11 - - [15/Jan/2026:10:01:00 +0800] "GET /.git/config HTTP/1.1" 404 150 "-" "curl/7.68`
	if err := os.WriteFile(logPath, []byte(complete+partial), 0644); err != nil {
		t.Fatal(err)
	}

	n, err := IngestWPSecurityEvents()
	if err != nil {
		t.Fatalf("IngestWPSecurityEvents() error = %v", err)
	}
	if n != 1 {
		t.Fatalf("ingest count = %d, want 1 (partial trailing line must not be consumed)", n)
	}

	// 补全那半行并加上换行符，模拟 nginx 写完了这一行
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(".0\"\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	n, err = IngestWPSecurityEvents()
	if err != nil {
		t.Fatalf("IngestWPSecurityEvents() second run error = %v", err)
	}
	if n != 1 {
		t.Fatalf("ingest count after completing partial line = %d, want 1", n)
	}

	var total int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM wp_security_events`).Scan(&total); err != nil {
		t.Fatalf("count wp_security_events: %v", err)
	}
	if total != 2 {
		t.Fatalf("wp_security_events row count = %d, want 2", total)
	}
}

func TestIngestWPSecurityEventsHandlesLogRotation(t *testing.T) {
	openTestDB(t)
	logDir := t.TempDir()
	seedWPSecurityEventSite(t, logDir)

	logPath := filepath.Join(logDir, "wp-security.log")
	before := `217.216.37.82 - - [15/Jan/2026:10:00:00 +0800] "GET /index.php?id=1%20UNION%20SELECT%201,2,3-- HTTP/1.1" 200 512 "-" "sqlmap/1.6"` + "\n"
	if err := os.WriteFile(logPath, []byte(before), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := IngestWPSecurityEvents(); err != nil {
		t.Fatalf("IngestWPSecurityEvents() error = %v", err)
	}

	// 模拟 copytruncate：文件被截断为一段更短的新内容
	after := `5.6.7.8 - - [16/Jan/2026:00:00:00 +0800] "GET /.env HTTP/1.1" 404 150 "-" "curl/7.68.0"` + "\n"
	if err := os.WriteFile(logPath, []byte(after), 0644); err != nil {
		t.Fatal(err)
	}

	n, err := IngestWPSecurityEvents()
	if err != nil {
		t.Fatalf("IngestWPSecurityEvents() after rotation error = %v", err)
	}
	if n != 1 {
		t.Fatalf("ingest count after rotation = %d, want 1", n)
	}

	var total int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM wp_security_events`).Scan(&total); err != nil {
		t.Fatalf("count wp_security_events: %v", err)
	}
	if total != 2 {
		t.Fatalf("wp_security_events row count after rotation = %d, want 2 (1 before + 1 after)", total)
	}

	var ip string
	if err := database.GetDB().QueryRow(`SELECT ip_address FROM wp_security_events ORDER BY id DESC LIMIT 1`).Scan(&ip); err != nil {
		t.Fatalf("query latest event: %v", err)
	}
	if ip != "5.6.7.8" {
		t.Fatalf("latest event ip = %q, want 5.6.7.8", ip)
	}
}

func TestIngestWPSecurityEventsHandlesCopytruncateAfterFastGrowth(t *testing.T) {
	openTestDB(t)
	logDir := t.TempDir()
	seedWPSecurityEventSite(t, logDir)

	logPath := filepath.Join(logDir, "wp-security.log")
	before := `217.216.37.82 - - [15/Jan/2026:10:00:00 +0800] "GET /index.php?id=1%20UNION%20SELECT%201,2,3-- HTTP/1.1" 200 512 "-" "sqlmap/1.6"` + "\n"
	if err := os.WriteFile(logPath, []byte(before), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := IngestWPSecurityEvents(); err != nil {
		t.Fatalf("IngestWPSecurityEvents() error = %v", err)
	}

	// copytruncate 后如果攻击流量很快把新 wp-security.log 写到超过旧 offset，
	// 不能只依赖 size < offset；首行指纹变化时仍应从头读取。
	after := `5.6.7.8 - - [16/Jan/2026:00:00:00 +0800] "GET /.env HTTP/1.1" 404 150 "-" "curl/7.68.0"` + "\n" +
		strings.Repeat(`9.9.9.9 - - [16/Jan/2026:00:00:01 +0800] "GET /wp-content/readme.txt HTTP/1.1" 404 150 "-" "curl/7.68.0"`+"\n", 5)
	if len(after) <= len(before) {
		t.Fatalf("test setup invalid: rotated content size %d must exceed old offset %d", len(after), len(before))
	}
	if err := os.WriteFile(logPath, []byte(after), 0644); err != nil {
		t.Fatal(err)
	}

	n, err := IngestWPSecurityEvents()
	if err != nil {
		t.Fatalf("IngestWPSecurityEvents() after fast-growth rotation error = %v", err)
	}
	if n != 1 {
		t.Fatalf("ingest count after fast-growth rotation = %d, want 1", n)
	}

	var latestIP string
	if err := database.GetDB().QueryRow(`SELECT ip_address FROM wp_security_events ORDER BY id DESC LIMIT 1`).Scan(&latestIP); err != nil {
		t.Fatalf("query latest event: %v", err)
	}
	if latestIP != "5.6.7.8" {
		t.Fatalf("latest event ip = %q, want 5.6.7.8", latestIP)
	}
}

func TestIngestWPSecurityEventsDoesNotAdvanceOffsetWhenInsertFails(t *testing.T) {
	openTestDB(t)
	logDir := t.TempDir()
	seedWPSecurityEventSite(t, logDir)

	logPath := filepath.Join(logDir, "wp-security.log")
	line := `217.216.37.82 - - [15/Jan/2026:10:00:00 +0800] "GET /index.php?id=1%20UNION%20SELECT%201,2,3-- HTTP/1.1" 200 512 "-" "sqlmap/1.6"` + "\n"
	if err := os.WriteFile(logPath, []byte(line), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`DROP TABLE wp_security_events`); err != nil {
		t.Fatalf("drop wp_security_events: %v", err)
	}

	n, err := IngestWPSecurityEvents()
	if err != nil {
		t.Fatalf("IngestWPSecurityEvents() with insert failure error = %v", err)
	}
	if n != 0 {
		t.Fatalf("ingest count with missing table = %d, want 0", n)
	}
	var offset int64
	if err := database.GetDB().QueryRow(`SELECT byte_offset FROM wp_security_log_positions WHERE site_id = 1`).Scan(&offset); err != nil {
		t.Fatalf("query log position after insert failure: %v", err)
	}
	if offset != 0 {
		t.Fatalf("offset after insert failure = %d, want 0 so the line can be retried", offset)
	}

	if err := database.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() recreate dropped table: %v", err)
	}
	n, err = IngestWPSecurityEvents()
	if err != nil {
		t.Fatalf("IngestWPSecurityEvents() retry error = %v", err)
	}
	if n != 1 {
		t.Fatalf("retry ingest count = %d, want 1", n)
	}
}

func TestCountRecentSecurityEventsByIPRespectsTimeWindow(t *testing.T) {
	openTestDB(t)
	seedWPSecurityEventSite(t, t.TempDir())

	old := time.Now().UTC().Add(-48 * time.Hour).Format("2006-01-02 15:04:05")
	recent := time.Now().UTC().Add(-1 * time.Hour).Format("2006-01-02 15:04:05")

	insert := func(ip, eventType, occurredAt string) {
		if _, err := database.GetDB().Exec(`INSERT INTO wp_security_events
			(site_id, domain, ip_address, event_type, risk_level, method, path, user_agent, status, message, occurred_at)
			VALUES (1, 'example.com', ?, ?, 'high', 'GET', '/x', 'ua', 200, 'msg', ?)`,
			ip, eventType, occurredAt); err != nil {
			t.Fatalf("insert event: %v", err)
		}
	}
	insert("1.1.1.1", SecurityEventSQLiProbe, old)
	insert("2.2.2.2", SecurityEventSQLiProbe, recent)
	insert("2.2.2.2", SecurityEventSQLiProbe, recent)
	insert("2.2.2.2", SecurityEventFakeSearchBot, recent)

	counts, err := CountRecentSecurityEventsByIP(SecurityEventSQLiProbe, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("CountRecentSecurityEventsByIP() error = %v", err)
	}
	if counts["1.1.1.1"] != 0 {
		t.Fatalf("counts[1.1.1.1] = %d, want 0 (outside 24h window)", counts["1.1.1.1"])
	}
	if counts["2.2.2.2"] != 2 {
		t.Fatalf("counts[2.2.2.2] = %d, want 2", counts["2.2.2.2"])
	}
}

func TestPruneWPSecurityEventsRemovesOldRows(t *testing.T) {
	openTestDB(t)
	seedWPSecurityEventSite(t, t.TempDir())

	old := time.Now().UTC().Add(-40 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	recent := time.Now().UTC().Add(-1 * time.Hour).Format("2006-01-02 15:04:05")
	for _, occurredAt := range []string{old, recent} {
		if _, err := database.GetDB().Exec(`INSERT INTO wp_security_events
			(site_id, domain, ip_address, event_type, risk_level, method, path, user_agent, status, message, occurred_at)
			VALUES (1, 'example.com', '1.1.1.1', 'sqli_probe', 'high', 'GET', '/x', 'ua', 200, 'msg', ?)`,
			occurredAt); err != nil {
			t.Fatalf("insert event: %v", err)
		}
	}

	if err := PruneWPSecurityEvents(30); err != nil {
		t.Fatalf("PruneWPSecurityEvents() error = %v", err)
	}

	var total int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM wp_security_events`).Scan(&total); err != nil {
		t.Fatalf("count wp_security_events: %v", err)
	}
	if total != 1 {
		t.Fatalf("wp_security_events row count after prune = %d, want 1", total)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello", 10); got != "hello" {
		t.Fatalf("truncateRunes short string = %q, want unchanged", got)
	}
	if got := truncateRunes("hello world", 5); got != "hello" {
		t.Fatalf("truncateRunes long string = %q, want %q", got, "hello")
	}
	// 多字节字符（中文）不能被从中间切断，必须按 rune 而不是按字节截断
	if got := truncateRunes("你好世界hello", 4); got != "你好世界" {
		t.Fatalf("truncateRunes multibyte = %q, want %q", got, "你好世界")
	}
}

func insertWPSecurityEvent(t *testing.T, ip, eventType, path, occurredAt string) {
	t.Helper()
	if _, err := database.GetDB().Exec(`INSERT INTO wp_security_events
		(site_id, domain, ip_address, event_type, risk_level, method, path, user_agent, status, message, occurred_at)
		VALUES (1, 'example.com', ?, ?, 'high', 'GET', ?, 'ua', 200, 'msg', ?)`,
		ip, eventType, path, occurredAt); err != nil {
		t.Fatalf("insert event: %v", err)
	}
}

func TestTopWPSecurityOffendersFiltersByThresholdAndReturnsTopPaths(t *testing.T) {
	openTestDB(t)
	seedWPSecurityEventSite(t, t.TempDir())

	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format("2006-01-02 15:04:05")
	old := now.Add(-48 * time.Hour).Format("2006-01-02 15:04:05")

	// 1.1.1.1: 3 次，低于阈值 5，不应出现在结果里
	for i := 0; i < 3; i++ {
		insertWPSecurityEvent(t, "1.1.1.1", SecurityEventSQLiProbe, "/a", recent)
	}
	// 2.2.2.2: 6 次达到阈值，其中 /x 命中 4 次、/y 命中 2 次
	for i := 0; i < 4; i++ {
		insertWPSecurityEvent(t, "2.2.2.2", SecurityEventSQLiProbe, "/x", recent)
	}
	for i := 0; i < 2; i++ {
		insertWPSecurityEvent(t, "2.2.2.2", SecurityEventSQLiProbe, "/y", recent)
	}
	// 3.3.3.3: 时间窗口外的历史事件，即使次数够也不该计入
	for i := 0; i < 10; i++ {
		insertWPSecurityEvent(t, "3.3.3.3", SecurityEventSQLiProbe, "/z", old)
	}

	offenders, err := topWPSecurityOffenders(SecurityEventSQLiProbe, now.Add(-24*time.Hour), 5, 3)
	if err != nil {
		t.Fatalf("topWPSecurityOffenders() error = %v", err)
	}
	if len(offenders) != 1 {
		t.Fatalf("offenders = %+v, want exactly 1 entry", offenders)
	}
	if offenders[0].IP != "2.2.2.2" || offenders[0].Count != 6 {
		t.Fatalf("offender = %+v, want ip=2.2.2.2 count=6", offenders[0])
	}
	if len(offenders[0].Paths) == 0 || offenders[0].Paths[0] != "/x × 4" {
		t.Fatalf("offender paths = %v, want top path '/x × 4' first", offenders[0].Paths)
	}
}

func TestCheckWPSecurityEventThresholdReturnsFalseWhenNoOffenders(t *testing.T) {
	openTestDB(t)
	seedWPSecurityEventSite(t, t.TempDir())

	firing, msg := checkWPSQLiProbeThreshold()
	if firing {
		t.Fatalf("checkWPSQLiProbeThreshold() firing = true with no data, want false (msg=%q)", msg)
	}
}

func TestCheckWPSecurityEventThresholdFiresAndIncludesIPAndPaths(t *testing.T) {
	openTestDB(t)
	seedWPSecurityEventSite(t, t.TempDir())

	recent := time.Now().UTC().Add(-1 * time.Hour).Format("2006-01-02 15:04:05")
	for i := 0; i < defaultWPSecurityAlertThreshold; i++ {
		insertWPSecurityEvent(t, "217.216.37.82", SecurityEventSQLiProbe, "/index.php", recent)
	}

	firing, msg := checkWPSQLiProbeThreshold()
	if !firing {
		t.Fatal("checkWPSQLiProbeThreshold() firing = false, want true once threshold reached")
	}
	if !strings.Contains(msg, "217.216.37.82") {
		t.Fatalf("alert message = %q, want it to mention the offending IP", msg)
	}
	if !strings.Contains(msg, "/index.php") {
		t.Fatalf("alert message = %q, want it to mention the offending path", msg)
	}

	// 伪装爬虫规则应该是独立统计的，不应该被 SQLi 事件触发
	firing, _ = checkWPFakeSearchBotThreshold()
	if firing {
		t.Fatal("checkWPFakeSearchBotThreshold() firing = true, want false (only sqli events were inserted)")
	}
}

func TestCheckWPSecurityEventThresholdTruncatesLargeOffenderList(t *testing.T) {
	openTestDB(t)
	seedWPSecurityEventSite(t, t.TempDir())

	recent := time.Now().UTC().Add(-1 * time.Hour).Format("2006-01-02 15:04:05")
	// 分布式扫描：15 个不同 IP 都达到阈值，超过 wpSecurityAlertMaxOffenders（10）。
	const offenderCount = 15
	for i := 0; i < offenderCount; i++ {
		ip := fmt.Sprintf("10.0.0.%d", i+1)
		for j := 0; j < defaultWPSecurityAlertThreshold; j++ {
			insertWPSecurityEvent(t, ip, SecurityEventSQLiProbe, "/index.php", recent)
		}
	}

	firing, msg := checkWPSQLiProbeThreshold()
	if !firing {
		t.Fatal("checkWPSQLiProbeThreshold() firing = false, want true")
	}
	if !strings.Contains(msg, fmt.Sprintf("还有 %d 个 IP 未列出", offenderCount-wpSecurityAlertMaxOffenders)) {
		t.Fatalf("alert message = %q, want it to mention the omitted offender count", msg)
	}
	ipMentions := strings.Count(msg, "10.0.0.")
	if ipMentions != wpSecurityAlertMaxOffenders {
		t.Fatalf("alert message mentions %d IPs, want exactly %d (capped)", ipMentions, wpSecurityAlertMaxOffenders)
	}
}

func TestGetWPSecurityAlertConfigReturnsDefaults(t *testing.T) {
	openTestDB(t)
	// openTestDB 跑过 migrations，security_settings 中已有默认值 '10' / '24'。
	// 这里验证读取流程端到端正确。
	cfg := getWPSecurityAlertConfig()
	if cfg.threshold != defaultWPSecurityAlertThreshold {
		t.Fatalf("cfg.threshold = %d, want default %d", cfg.threshold, defaultWPSecurityAlertThreshold)
	}
	if cfg.window != defaultWPSecurityAlertWindow {
		t.Fatalf("cfg.window = %v, want default %v", cfg.window, defaultWPSecurityAlertWindow)
	}
	if cfg.pathLimit != wpSecurityAlertPathLimit {
		t.Fatalf("cfg.pathLimit = %d, want %d", cfg.pathLimit, wpSecurityAlertPathLimit)
	}
	if cfg.maxOffenders != wpSecurityAlertMaxOffenders {
		t.Fatalf("cfg.maxOffenders = %d, want %d", cfg.maxOffenders, wpSecurityAlertMaxOffenders)
	}
}

func TestGetWPSecurityAlertConfigReadsDBValues(t *testing.T) {
	openTestDB(t)

	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = '5' WHERE skey = 'alert_wp_security_threshold'`); err != nil {
		t.Fatalf("update threshold: %v", err)
	}
	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = '6' WHERE skey = 'alert_wp_security_window_hours'`); err != nil {
		t.Fatalf("update window: %v", err)
	}

	cfg := getWPSecurityAlertConfig()
	if cfg.threshold != 5 {
		t.Fatalf("cfg.threshold = %d, want 5", cfg.threshold)
	}
	if cfg.window != 6*time.Hour {
		t.Fatalf("cfg.window = %v, want 6h", cfg.window)
	}
}

func TestGetWPSecurityAlertConfigClampsInvalidValuesToDefaults(t *testing.T) {
	openTestDB(t)

	// 低于下限：阈值 0、窗口 0 小时
	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = '0' WHERE skey = 'alert_wp_security_threshold'`); err != nil {
		t.Fatalf("update threshold: %v", err)
	}
	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = '0' WHERE skey = 'alert_wp_security_window_hours'`); err != nil {
		t.Fatalf("update window: %v", err)
	}
	cfg := getWPSecurityAlertConfig()
	if cfg.threshold != defaultWPSecurityAlertThreshold {
		t.Fatalf("cfg.threshold with 0 = %d, want default %d (clamped)", cfg.threshold, defaultWPSecurityAlertThreshold)
	}
	if cfg.window != defaultWPSecurityAlertWindow {
		t.Fatalf("cfg.window with 0 = %v, want default %v (clamped)", cfg.window, defaultWPSecurityAlertWindow)
	}

	// 超过上限：阈值 99999、窗口 999 小时
	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = '99999' WHERE skey = 'alert_wp_security_threshold'`); err != nil {
		t.Fatalf("update threshold: %v", err)
	}
	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = '999' WHERE skey = 'alert_wp_security_window_hours'`); err != nil {
		t.Fatalf("update window: %v", err)
	}
	cfg = getWPSecurityAlertConfig()
	if cfg.threshold != defaultWPSecurityAlertThreshold {
		t.Fatalf("cfg.threshold with 99999 = %d, want default %d (clamped)", cfg.threshold, defaultWPSecurityAlertThreshold)
	}
	if cfg.window != defaultWPSecurityAlertWindow {
		t.Fatalf("cfg.window with 999 = %v, want default %v (clamped)", cfg.window, defaultWPSecurityAlertWindow)
	}

	// 非数字字符串
	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = 'abc' WHERE skey = 'alert_wp_security_threshold'`); err != nil {
		t.Fatalf("update threshold: %v", err)
	}
	cfg = getWPSecurityAlertConfig()
	if cfg.threshold != defaultWPSecurityAlertThreshold {
		t.Fatalf("cfg.threshold with 'abc' = %d, want default %d (clamped)", cfg.threshold, defaultWPSecurityAlertThreshold)
	}
}

func TestCheckWPSecurityEventThresholdUsesConfiguredThreshold(t *testing.T) {
	openTestDB(t)
	seedWPSecurityEventSite(t, t.TempDir())

	// 把阈值改为 3，少于默认 10
	if _, err := database.GetDB().Exec(`UPDATE security_settings SET svalue = '3' WHERE skey = 'alert_wp_security_threshold'`); err != nil {
		t.Fatalf("update threshold: %v", err)
	}

	recent := time.Now().UTC().Add(-1 * time.Hour).Format("2006-01-02 15:04:05")
	// 插入 3 条（达到新阈值，但低于默认 10）
	for i := 0; i < 3; i++ {
		insertWPSecurityEvent(t, "217.216.37.82", SecurityEventSQLiProbe, "/index.php", recent)
	}

	firing, msg := checkWPSQLiProbeThreshold()
	if !firing {
		t.Fatal("checkWPSQLiProbeThreshold() firing = false, want true with configured threshold=3 and 3 events")
	}
	if !strings.Contains(msg, "达到阈值（3 次）") {
		t.Fatalf("alert message = %q, want it to mention configured threshold 3", msg)
	}

	// 伪装爬虫规则不应被触发（事件类型独立统计）
	firing, _ = checkWPFakeSearchBotThreshold()
	if firing {
		t.Fatal("checkWPFakeSearchBotThreshold() firing = true, want false (only sqli events were inserted)")
	}
}

func TestStopWPSecurityEventIngestorExitsCleanly(t *testing.T) {
	openTestDB(t)

	prev := wpSecurityIngestorInterval
	wpSecurityIngestorInterval = 10 * time.Millisecond
	t.Cleanup(func() { wpSecurityIngestorInterval = prev })

	StartWPSecurityEventIngestor()
	wpSecurityIngestorMu.Lock()
	done := wpSecurityIngestorDone
	wpSecurityIngestorMu.Unlock()

	// 等待初始 cycle 完成（openTestDB 没有 websites，IngestWPSecurityEvents 立即返回）
	time.Sleep(50 * time.Millisecond)

	StopWPSecurityEventIngestor()
	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("ingestor did not exit within 2s after Stop")
	}

	// 二次 Stop 应幂等，不 panic
	StopWPSecurityEventIngestor()
}
