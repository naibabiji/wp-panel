package executor

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var usageNow = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func openSiteUsageTestDB(t *testing.T, samplingStart time.Time, counts map[string]int) *sql.DB {
	t.Helper()
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE resource_object_samples (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		window_start DATETIME NOT NULL, window_end DATETIME NOT NULL,
		object_type TEXT NOT NULL, object_key TEXT NOT NULL, label TEXT NOT NULL DEFAULT '',
		cpu_seconds REAL NOT NULL DEFAULT 0, rss_bytes INTEGER NOT NULL DEFAULT 0
	)`)
	mustExec(t, db, `CREATE TABLE monitoring_metrics (
		id INTEGER PRIMARY KEY AUTOINCREMENT, cpu_percent REAL, memory_total_bytes INTEGER,
		recorded_at DATETIME NOT NULL
	)`)
	prevStart, prevCounts := siteUsageSamplingStart, siteUsageProcessCounts
	siteUsageSamplingStart = func() time.Time { return samplingStart }
	siteUsageProcessCounts = func() map[string]int { return counts }
	t.Cleanup(func() { siteUsageSamplingStart, siteUsageProcessCounts = prevStart, prevCounts })
	return db
}

func insertUsageSample(t *testing.T, db *sql.DB, minuteEnd time.Time, objType, key, label string, cpu float64, rss int64) {
	t.Helper()
	mustExecArgs(t, db, `INSERT INTO resource_object_samples (window_start, window_end, object_type, object_key, label, cpu_seconds, rss_bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, dbTime(minuteEnd.Add(-time.Minute)), dbTime(minuteEnd), objType, key, label, cpu, rss)
}

func TestRecentSiteResourceUsageWindowAndUnits(t *testing.T) {
	db := openSiteUsageTestDB(t, usageNow.Add(-time.Hour), map[string]int{"7": 3})
	mustExecArgs(t, db, `INSERT INTO monitoring_metrics (cpu_percent, memory_total_bytes, recorded_at) VALUES (50, ?, ?)`, int64(1000<<20), dbTime(usageNow))
	// 第 1 分钟只有 MariaDB；第 2、3 分钟网站 7 出现；第 4 分钟只有其他进程。
	insertUsageSample(t, db, usageNow.Add(-3*time.Minute), "service", "MariaDB", "MariaDB", 30, 300<<20)
	insertUsageSample(t, db, usageNow.Add(-2*time.Minute), "site", "7", "a.example", 90, 200<<20)
	insertUsageSample(t, db, usageNow.Add(-1*time.Minute), "site", "7", "a.example", 90, 200<<20)
	insertUsageSample(t, db, usageNow, "other", "cron", "cron", 1, 1<<20)
	// 窗口外的旧样本不计入。
	insertUsageSample(t, db, usageNow.Add(-6*time.Minute), "site", "8", "old.example", 60, 100<<20)

	got, err := RecentSiteResourceUsage(usageNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("usage = %+v, want only a.example", got)
	}
	u := got[0]
	// 4 个采样分钟共 240 秒：180 CPU 秒 / 240 秒 = 75%（单核百分比）。
	// 内存：400MB / 4 分钟 / 1000MB = 10%。
	if u.SiteID != 7 || u.Domain != "a.example" || u.CPU != 75 || u.Mem != 10 || u.ProcCount != 3 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestRecentSiteResourceUsageCanExceedOneCoreAndSorts(t *testing.T) {
	db := openSiteUsageTestDB(t, usageNow.Add(-time.Hour), nil)
	insertUsageSample(t, db, usageNow, "site", "1", "busy.example", 150, 10<<20)
	insertUsageSample(t, db, usageNow, "site", "2", "tie-high-mem.example", 30, 50<<20)
	insertUsageSample(t, db, usageNow, "site", "3", "tie-low-mem.example", 30, 20<<20)
	got, err := RecentSiteResourceUsage(usageNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Domain != "busy.example" || got[0].CPU != 250 ||
		got[1].Domain != "tie-high-mem.example" || got[2].Domain != "tie-low-mem.example" {
		t.Fatalf("usage order/values = %+v", got)
	}
	// 没有主机总内存记录时内存为 0，不报错。
	if got[0].Mem != 0 {
		t.Fatalf("mem without host total = %v", got[0].Mem)
	}
}

func TestRecentSiteResourceUsageIgnoresSamplesBeforeRestart(t *testing.T) {
	restart := usageNow.Add(-90 * time.Second)
	db := openSiteUsageTestDB(t, restart, nil)
	insertUsageSample(t, db, restart.Add(-30*time.Second), "site", "1", "before.example", 60, 1<<20)
	got, err := RecentSiteResourceUsage(usageNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("pre-restart samples returned: %+v", got)
	}
	insertUsageSample(t, db, restart.Add(time.Minute), "site", "2", "after.example", 60, 1<<20)
	got, err = RecentSiteResourceUsage(usageNow)
	if err != nil || len(got) != 1 || got[0].Domain != "after.example" || got[0].CPU != 100 {
		t.Fatalf("post-restart usage = %+v err=%v", got, err)
	}
}

func TestRecentSiteResourceUsageEmptyAndErrors(t *testing.T) {
	openSiteUsageTestDB(t, time.Time{}, nil)
	got, err := RecentSiteResourceUsage(usageNow)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("before first scan = %+v err=%v, want empty list", got, err)
	}

	openAlertTestDB(t) // 没有采样表
	prev := siteUsageSamplingStart
	siteUsageSamplingStart = func() time.Time { return usageNow.Add(-time.Hour) }
	t.Cleanup(func() { siteUsageSamplingStart = prev })
	if _, err := RecentSiteResourceUsage(usageNow); err == nil {
		t.Fatal("query failure should return an error, not an empty list")
	}
}

func TestSiteResourceUsageJSONFieldsUnchanged(t *testing.T) {
	data, err := json.Marshal(SiteResourceUsage{SiteID: 1, Domain: "a", CPU: 1, Mem: 2, ProcCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"site_id":1,"domain":"a","cpu":1,"mem":2,"proc_count":3}` {
		t.Fatalf("json = %s", data)
	}
}

func TestAISiteResourceSummaryUsesSharedUsage(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE websites (id INTEGER PRIMARY KEY, domain TEXT, status TEXT)`)
	prev := aiRecentSiteResourceUsage
	t.Cleanup(func() { aiRecentSiteResourceUsage = prev })
	aiRecentSiteResourceUsage = func(time.Time) ([]SiteResourceUsage, error) {
		return []SiteResourceUsage{{SiteID: 9, Domain: "x.example", CPU: 12.345, Mem: 1.25, ProcCount: 2}}, nil
	}
	summary := aiSiteResourceSummary(nil)
	if summary["checked"] != true || summary["window"] != "最近 5 分钟平均" {
		t.Fatalf("summary = %+v", summary)
	}
	missing := aiFindSiteResource(nil, 1, "y.example")
	if msg, _ := missing["message"].(string); !strings.Contains(msg, "最近 5 分钟没有该站点的进程采样") {
		t.Fatalf("missing site message = %q", msg)
	}

	aiRecentSiteResourceUsage = func(time.Time) ([]SiteResourceUsage, error) { return nil, sql.ErrConnDone }
	if summary := aiSiteResourceSummary(nil); summary["checked"] != false {
		t.Fatalf("failed summary = %+v, want checked=false", summary)
	}
}

func TestRecentSiteResourceUsageWindowVisibilityVersusCurrentProcesses(t *testing.T) {
	// 网站 7 在窗口内有样本但最近一次扫描已无进程：仍显示，proc_count 为 0。
	// 网站 8 在窗口内没有任何样本：不显示。
	db := openSiteUsageTestDB(t, usageNow.Add(-time.Hour), map[string]int{"8": 2})
	insertUsageSample(t, db, usageNow.Add(-4*time.Minute), "site", "7", "idle-now.example", 30, 1<<20)
	insertUsageSample(t, db, usageNow, "service", "MariaDB", "MariaDB", 1, 1<<20)
	got, err := RecentSiteResourceUsage(usageNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SiteID != 7 || got[0].ProcCount != 0 {
		t.Fatalf("usage = %+v, want only site 7 with proc_count 0", got)
	}
}
