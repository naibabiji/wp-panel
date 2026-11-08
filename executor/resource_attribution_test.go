package executor

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/collector"
)

type attributionSample struct {
	objType, key, label string
	cpuPerMinute        float64
	rss                 int64
}

func openAttributionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE resource_object_samples (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		window_start DATETIME NOT NULL, window_end DATETIME NOT NULL,
		object_type TEXT NOT NULL, object_key TEXT NOT NULL, label TEXT NOT NULL DEFAULT '',
		cpu_seconds REAL NOT NULL DEFAULT 0, rss_bytes INTEGER NOT NULL DEFAULT 0
	)`)
	mustExec(t, db, `CREATE TABLE monitoring_metrics (
		id INTEGER PRIMARY KEY AUTOINCREMENT, cpu_percent REAL, memory_percent REAL,
		cpu_iowait_percent REAL NOT NULL DEFAULT 0, cpu_steal_percent REAL NOT NULL DEFAULT 0,
		recorded_at DATETIME NOT NULL
	)`)
	prevCPU := attributionCPUCount
	attributionCPUCount = func() int { return 2 }
	t.Cleanup(func() { attributionCPUCount = prevCPU })
	return db
}

func dbTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

// seedAttributionWindow 写入 minutes 个一分钟样本，样本结束时间均落在 (start, start+minutes]。
func seedAttributionWindow(t *testing.T, db *sql.DB, start time.Time, minutes int, hostCPU, iowait float64, objects []attributionSample) {
	t.Helper()
	for i := 0; i < minutes; i++ {
		ws := start.Add(time.Duration(i) * time.Minute)
		we := ws.Add(time.Minute)
		for _, o := range objects {
			mustExecArgs(t, db, `INSERT INTO resource_object_samples (window_start, window_end, object_type, object_key, label, cpu_seconds, rss_bytes)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, dbTime(ws), dbTime(we), o.objType, o.key, o.label, o.cpuPerMinute, o.rss)
		}
		mustExecArgs(t, db, `INSERT INTO monitoring_metrics (cpu_percent, memory_percent, cpu_iowait_percent, cpu_steal_percent, recorded_at)
			VALUES (?, 50, ?, 0, ?)`, hostCPU, iowait, dbTime(we))
	}
}

func mustExecArgs(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

var attributionStart = time.Date(2026, 11, 8, 0, 55, 0, 0, time.UTC)

func TestCPUAttributionHighConfidenceSite(t *testing.T) {
	db := openAttributionTestDB(t)
	// 2 核、主机 CPU 95%：每分钟约 114 CPU 秒。
	seedAttributionWindow(t, db, attributionStart, 5, 95, 0, []attributionSample{
		{"site", "7", "customwatchjewelry.com", 90, 900 << 20},
		{"service", "MariaDB", "MariaDB", 10, 1 << 30},
		{"other", "_rest", "其他进程", 9, 1 << 20},
	})
	got := buildAlertAttribution("alert_cpu", attributionStart, attributionStart.Add(5*time.Minute), false)
	for _, want := range []string{"初步归因：customwatchjewelry.com（可信度：高）", "第二名 公共服务 MariaDB", "访问日志、PHP 错误日志和 WP Cron", attributionDisclaimerText} {
		if !strings.Contains(got, want) {
			t.Fatalf("attribution missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "其他进程") {
		t.Fatalf("merged rest object should not be ranked:\n%s", got)
	}
}

func TestCPUAttributionSingleLowCoverageObjectIsNotHigh(t *testing.T) {
	db := openAttributionTestDB(t)
	// 只采到一个对象，占 2 核容量 25%（每分钟 30 秒），主机实际 90%。
	seedAttributionWindow(t, db, attributionStart, 5, 90, 0, []attributionSample{
		{"site", "7", "a.example", 30, 100 << 20},
	})
	got := buildAlertAttribution("alert_cpu", attributionStart, attributionStart.Add(5*time.Minute), false)
	if !strings.Contains(got, "a.example（可信度：可能相关）") || !strings.Contains(got, "未采到其他占用对象") {
		t.Fatalf("single low-coverage object should be likely only:\n%s", got)
	}
}

func TestCPUAttributionCloseObjectsAreUnattributed(t *testing.T) {
	db := openAttributionTestDB(t)
	seedAttributionWindow(t, db, attributionStart, 5, 95, 0, []attributionSample{
		{"service", "MariaDB", "MariaDB", 37, 1 << 30},
		{"site", "1", "site-a.com", 33, 1 << 20},
		{"site", "2", "site-b.com", 30, 1 << 20},
	})
	got := buildAlertAttribution("alert_cpu", attributionStart, attributionStart.Add(5*time.Minute), false)
	if !strings.Contains(got, "初步归因：无法归因到单个网站") || !strings.Contains(got, "占用前 3：公共服务 MariaDB 37%、site-a.com 33%、site-b.com 30%") {
		t.Fatalf("close objects should be unattributed:\n%s", got)
	}
}

func TestCPUAttributionServiceLeaderIsNotAssignedToSite(t *testing.T) {
	db := openAttributionTestDB(t)
	seedAttributionWindow(t, db, attributionStart, 5, 95, 0, []attributionSample{
		{"service", "MariaDB", "MariaDB", 100, 1 << 30},
		{"site", "1", "site-a.com", 8, 1 << 20},
	})
	got := buildAlertAttribution("alert_cpu", attributionStart, attributionStart.Add(5*time.Minute), false)
	if !strings.Contains(got, "初步归因：公共服务 MariaDB（可信度：高）") || !strings.Contains(got, "可能由某个网站的请求或查询引起") {
		t.Fatalf("service leader attribution:\n%s", got)
	}
}

func TestCPUAttributionInsufficientSamples(t *testing.T) {
	db := openAttributionTestDB(t)
	seedAttributionWindow(t, db, attributionStart, 2, 95, 0, []attributionSample{
		{"site", "7", "a.example", 100, 1 << 20},
	})
	got := buildAlertAttribution("alert_cpu", attributionStart, attributionStart.Add(5*time.Minute), false)
	if !strings.Contains(got, "初步归因：未知（采样不足）") || strings.Contains(got, "a.example") {
		t.Fatalf("2 of 5 samples should be insufficient:\n%s", got)
	}
}

func TestCPUAttributionIOWaitClueDoesNotHideSite(t *testing.T) {
	db := openAttributionTestDB(t)
	seedAttributionWindow(t, db, attributionStart, 5, 85, 24, []attributionSample{
		{"site", "7", "a.example", 90, 1 << 20},
		{"service", "MariaDB", "MariaDB", 5, 1 << 20},
	})
	got := buildAlertAttribution("alert_cpu", attributionStart, attributionStart.Add(5*time.Minute), false)
	if !strings.Contains(got, "初步归因：a.example（可信度：高）") || !strings.Contains(got, "附加线索：同时段 I/O 等待均值 24%") {
		t.Fatalf("iowait should be an additional clue:\n%s", got)
	}
}

func TestCPUAttributionOngoingUsesRecentWindow(t *testing.T) {
	db := openAttributionTestDB(t)
	resendAt := attributionStart.Add(2 * time.Hour)
	// 首次窗口由 a.example 主导，重发前 5 分钟由 b.example 主导。
	seedAttributionWindow(t, db, attributionStart, 5, 95, 0, []attributionSample{{"site", "1", "a.example", 100, 1 << 20}})
	seedAttributionWindow(t, db, resendAt.Add(-5*time.Minute), 5, 95, 0, []attributionSample{{"site", "2", "b.example", 100, 1 << 20}})
	got := buildAlertAttribution("alert_cpu", attributionStart, resendAt, true)
	if !strings.Contains(got, "时间段（当前窗口）") || !strings.Contains(got, "b.example") || strings.Contains(got, "a.example") {
		t.Fatalf("resend should use only the recent window:\n%s", got)
	}
}

func TestMemoryClueHasNoConfidence(t *testing.T) {
	db := openAttributionTestDB(t)
	seedAttributionWindow(t, db, attributionStart, 5, 20, 0, []attributionSample{
		{"service", "MariaDB", "MariaDB", 1, 1100 << 20},
		{"site", "7", "a.example", 1, 950 << 20},
		{"site", "8", "b.example", 1, 420 << 20},
		{"site", "9", "c.example", 1, 10 << 20},
	})
	got := buildAlertAttribution("alert_memory", attributionStart, attributionStart.Add(5*time.Minute), false)
	if !strings.Contains(got, "内存线索") || !strings.Contains(got, attributionRSSCaveat) || strings.Contains(got, "可信度") || strings.Contains(got, "c.example") {
		t.Fatalf("memory clue:\n%s", got)
	}
	if strings.Index(got, "MariaDB") > strings.Index(got, "a.example") {
		t.Fatalf("memory clue should be ordered by RSS:\n%s", got)
	}
}

func TestAttributionForOtherAlertKinds(t *testing.T) {
	openAttributionTestDB(t)
	now := attributionStart.Add(5 * time.Minute)
	if got := buildAlertAttribution("alert_disk", time.Time{}, now, false); got != diskAlertChecklist {
		t.Fatalf("disk attribution = %q", got)
	}
	for _, key := range []string{"alert_site", "alert_ssl", "alert_backup", "alert_panel_update"} {
		if got := buildAlertAttribution(key, time.Time{}, now, false); got != "" {
			t.Fatalf("%s attribution = %q, want empty", key, got)
		}
	}
}

func TestAttributionQueryFailureReturnsEmpty(t *testing.T) {
	openAlertTestDB(t) // 没有采样表
	if got := buildAlertAttribution("alert_cpu", attributionStart, attributionStart.Add(5*time.Minute), false); got != "" {
		t.Fatalf("attribution without sample table = %q, want empty", got)
	}
}

func TestOOMAttributionUnmatchedPID(t *testing.T) {
	openAttributionTestDB(t)
	got := buildOOMAttribution(999999, attributionStart)
	if !strings.Contains(got, "未能与最近采样匹配") {
		t.Fatalf("OOM attribution = %q", got)
	}
}

func TestAlertLogKeepsMessageAndStoresAttributionSeparately(t *testing.T) {
	db := openAttributionTestDB(t)
	mustExec(t, db, `CREATE TABLE alert_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT, alert_type TEXT NOT NULL, level TEXT NOT NULL,
		message TEXT NOT NULL, resolved INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, attribution TEXT
	)`)
	sendAlertNotificationWithChannelsLimited("alert_cpu", "CPU 使用率 99.9%", "初步归因：a.example", false, false, false, false)
	sendResolvedAlertEvent("alert_wp_maintenance", "WordPress 维护告警", "site=1 event=x", "", "tip")
	rows, err := db.Query("SELECT message, attribution FROM alert_log ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var message string
		var attribution sql.NullString
		if err := rows.Scan(&message, &attribution); err != nil {
			t.Fatal(err)
		}
		got = append(got, message+"|"+attribution.String)
	}
	if strings.Join(got, ";") != "CPU 使用率 99.9%|初步归因：a.example;site=1 event=x|" {
		t.Fatalf("alert_log rows = %v", got)
	}
	if body := composeAlertBody("msg", ""); body != "msg" {
		t.Fatalf("body without attribution = %q", body)
	}
	if body := composeAlertBody("msg", "attr"); body != "msg\n\nattr" {
		t.Fatalf("body with attribution = %q", body)
	}
}

func TestFormatShareShowsTinySharesAsBelowOnePercent(t *testing.T) {
	for _, tc := range []struct {
		part, total float64
		want        string
	}{{0, 10, "0%"}, {0.01, 10, "<1%"}, {8.2, 10, "82%"}, {1, 0, "0%"}} {
		if got := formatShare(tc.part, tc.total); got != tc.want {
			t.Fatalf("formatShare(%v,%v)=%q want %q", tc.part, tc.total, got, tc.want)
		}
	}
}

func TestOOMAttributionMatchedNonSiteObject(t *testing.T) {
	openAttributionTestDB(t)
	prev := lookupPIDObject
	t.Cleanup(func() { lookupPIDObject = prev })

	lookupPIDObject = func(int) (collector.PIDObject, bool) {
		return collector.PIDObject{Type: collector.ObjectTypeService, Key: "MariaDB", Label: "MariaDB"}, true
	}
	if got := buildOOMAttribution(42, attributionStart); !strings.Contains(got, "被终止进程属于公共服务 MariaDB，不属于单个网站，不能据此判断是否与某网站有关") {
		t.Fatalf("service OOM attribution = %q", got)
	}
	lookupPIDObject = func(int) (collector.PIDObject, bool) {
		return collector.PIDObject{Type: collector.ObjectTypeOther, Key: "tar", Label: "tar"}, true
	}
	if got := buildOOMAttribution(42, attributionStart); !strings.Contains(got, "被终止进程属于进程 tar，不属于单个网站") {
		t.Fatalf("other OOM attribution = %q", got)
	}
	lookupPIDObject = func(int) (collector.PIDObject, bool) {
		return collector.PIDObject{Type: collector.ObjectTypeSite, Key: "7", Label: "a.example"}, true
	}
	if got := buildOOMAttribution(42, attributionStart); !strings.Contains(got, "被终止进程所属网站：a.example") || strings.Contains(got, "不能据此判断") {
		t.Fatalf("site OOM attribution = %q", got)
	}
}

func TestCPUAttributionHostUsageUsesSameWindowsAsObjectSamples(t *testing.T) {
	db := openAttributionTestDB(t)
	// 5 分钟窗口只有前 3 分钟有对象样本（满足 60% 门槛）。这 3 分钟主机 CPU 为 20%，
	// 没有对象样本的后 2 分钟主机 CPU 为 100%。网站每分钟 30 CPU 秒，
	// 同窗口主机用量为每分钟 0.2×2 核×60 秒 = 24 CPU 秒，应判为高可信；
	// 若错误地用全时段平均（52%，每分钟 62.4 CPU 秒），则会漏判。
	for i := 0; i < 5; i++ {
		ws := attributionStart.Add(time.Duration(i) * time.Minute)
		we := ws.Add(time.Minute)
		host := 20.0
		if i >= 3 {
			host = 100
		} else {
			mustExecArgs(t, db, `INSERT INTO resource_object_samples (window_start, window_end, object_type, object_key, label, cpu_seconds, rss_bytes)
				VALUES (?, ?, 'site', '7', 'a.example', 30, 1048576)`, dbTime(ws), dbTime(we))
		}
		// 主机指标与对象样本的写入时间相差 1 秒，仍应匹配到同一分钟。
		mustExecArgs(t, db, `INSERT INTO monitoring_metrics (cpu_percent, memory_percent, cpu_iowait_percent, cpu_steal_percent, recorded_at)
			VALUES (?, 50, 0, 0, ?)`, host, dbTime(we.Add(-time.Second)))
	}
	got := buildAlertAttribution("alert_cpu", attributionStart, attributionStart.Add(5*time.Minute), false)
	if !strings.Contains(got, "a.example（可信度：高）") {
		t.Fatalf("host usage should be aggregated over the object sample windows only:\n%s", got)
	}
}
