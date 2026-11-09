package executor

import (
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/collector"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

func TestParseAlertDiagnosisContextUsesGeneratedMemoryClueAndExpandsLogWindow(t *testing.T) {
	start := time.Date(2026, 11, 8, 18, 20, 0, 0, time.Local)
	end := start.Add(5 * time.Minute)
	attr := formatMemoryClue(&attributionData{
		start: start, end: end, windows: 5,
		objects: []*objectUsage{{objType: "site", label: "wpp-login-e2e-test.x1g.la", rssSum: 10 << 20, samples: 5}},
	}, "内存线索", false)
	created := end.Add(10 * time.Second)
	item, ok := parseAlertDiagnosisContext(AlertDiagnosisContext{ID: 7, AlertType: "alert_memory", Attribution: attr, CreatedAt: created}, "wpp-login-e2e-test.x1g.la")
	if !ok {
		t.Fatalf("generated attribution was not parsed: %q", attr)
	}
	if !item.AttributionStart.Equal(start) || !item.AttributionEnd.Equal(end) {
		t.Fatalf("attribution window = %v..%v", item.AttributionStart, item.AttributionEnd)
	}
	if !item.LogStart.Equal(start.Add(-5*time.Minute)) || !item.LogEnd.Equal(end.Add(5*time.Minute)) {
		t.Fatalf("log window = %v..%v", item.LogStart, item.LogEnd)
	}
	if item.Relation != "clue" {
		t.Fatalf("relation = %q, want clue", item.Relation)
	}
}

func TestParseAlertDiagnosisContextRejectsSimilarDomainAndBadAlertTime(t *testing.T) {
	start := time.Date(2026, 11, 8, 18, 20, 0, 0, time.Local)
	end := start.Add(5 * time.Minute)
	attr := formatMemoryClue(&attributionData{start: start, end: end, windows: 5, objects: []*objectUsage{{objType: "site", label: "aa.example.com", rssSum: 1, samples: 5}}}, "内存线索", false)
	if _, ok := parseAlertDiagnosisContext(AlertDiagnosisContext{ID: 1, AlertType: "alert_memory", Attribution: attr, CreatedAt: end.Add(10 * time.Second)}, "a.example.com"); ok {
		t.Fatal("similar domain was accepted")
	}
	if _, ok := parseAlertDiagnosisContext(AlertDiagnosisContext{ID: 2, AlertType: "alert_memory", Attribution: attr, CreatedAt: end.Add(2 * time.Hour)}, "aa.example.com"); ok {
		t.Fatal("stale alert window was accepted")
	}
}

func TestParseAlertDiagnosisContextReadsCPUAndServiceGeneratedWindows(t *testing.T) {
	start := time.Date(2026, 11, 30, 23, 58, 0, 0, time.Local)
	end := time.Date(2026, 12, 1, 0, 3, 0, 0, time.Local)
	objects := []*objectUsage{
		{objType: "site", label: "a.example.com", cpuSeconds: 80, samples: 5},
		{objType: "site", label: "aa.example.com", cpuSeconds: 20, samples: 5},
	}
	attr := formatCPUAttribution(&attributionData{start: start, end: end, windows: 5, objects: objects}, false)
	item, ok := parseAlertDiagnosisContext(AlertDiagnosisContext{ID: 3, AlertType: "alert_cpu", Attribution: attr, CreatedAt: end.Add(time.Second)}, "a.example.com")
	if !ok || item.Relation != "attribution" || !item.AttributionStart.Equal(start) || !item.AttributionEnd.Equal(end) {
		t.Fatalf("cpu attribution parse = %+v, ok=%v, text=%q", item, ok, attr)
	}
	serviceAttr := formatServiceClue(&attributionData{start: start, end: end, windows: 5, objects: objects})
	item, ok = parseAlertDiagnosisContext(AlertDiagnosisContext{ID: 4, AlertType: "alert_service", Attribution: serviceAttr, CreatedAt: end.Add(time.Second)}, "aa.example.com")
	if !ok || item.Relation != "clue" {
		t.Fatalf("service clue parse = %+v, ok=%v, text=%q", item, ok, serviceAttr)
	}
}

func TestAlertDiagnosisCandidatesExposeOnlyMatchedResourceAlerts(t *testing.T) {
	db := openAlertTestDB(t)
	defer db.Close()
	start := time.Date(2026, 11, 8, 18, 20, 0, 0, time.Local)
	end := start.Add(5 * time.Minute)
	attr := formatMemoryClue(&attributionData{start: start, end: end, windows: 5, objects: []*objectUsage{{objType: "site", label: "site.example", rssSum: 1, samples: 5}}}, "内存线索", false)
	mustExec(t, db, `CREATE TABLE alert_log(id INTEGER PRIMARY KEY, alert_type TEXT, message TEXT, attribution TEXT, created_at DATETIME)`)
	testCreated := end.Add(10 * time.Second)
	mustExecArgs(t, db, `INSERT INTO alert_log(id,alert_type,message,attribution,created_at) VALUES(?,?,?,?,?)`, 1, "alert_memory", "memory", attr, testCreated.UTC().Format("2006-01-02 15:04:05"))
	mustExecArgs(t, db, `INSERT INTO alert_log(id,alert_type,message,attribution,created_at) VALUES(?,?,?,?,?)`, 2, "alert_ssl", "ssl", attr, testCreated.UTC().Format("2006-01-02 15:04:05"))
	items, err := AlertDiagnosisCandidates(db, &models.Website{ID: 1, Domain: "site.example"}, 20)
	if err != nil || len(items) != 1 || items[0].ID != 1 {
		t.Fatalf("candidates=%+v err=%v", items, err)
	}
	if _, err := LoadAlertDiagnosisContext(db, &models.Website{ID: 2, Domain: "other.example"}, 1); err == nil {
		t.Fatal("alert from another website was accepted")
	}
}

func TestParseAlertDiagnosisContextHandlesOOMAndUnattributedTopList(t *testing.T) {
	db := openAttributionTestDB(t)
	defer db.Close()
	oldDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = oldDB })
	occurredAt := time.Date(2026, 11, 8, 18, 25, 0, 0, time.Local)
	seedAttributionWindow(t, db, occurredAt.Add(-5*time.Minute), 5, 50, 0, []attributionSample{{objType: "site", key: "site.example", label: "site.example", rss: 10 << 20, cpuPerMinute: 1}})
	oldLookup := lookupPIDObject
	lookupPIDObject = func(int) (collector.PIDObject, bool) {
		return collector.PIDObject{Type: collector.ObjectTypeSite, Label: "site.example"}, true
	}
	t.Cleanup(func() { lookupPIDObject = oldLookup })
	attr := buildOOMAttribution(123, occurredAt)
	item, ok := parseAlertDiagnosisContext(AlertDiagnosisContext{ID: 5, AlertType: "alert_oom", Attribution: attr, CreatedAt: occurredAt}, "site.example")
	if !ok || item.Relation != "attribution" {
		t.Fatalf("oom attribution parse = %+v, ok=%v, text=%q", item, ok, attr)
	}

	start := time.Date(2026, 11, 8, 19, 0, 0, 0, time.Local)
	end := start.Add(5 * time.Minute)
	topList := formatCPUAttribution(&attributionData{start: start, end: end, windows: 5, objects: []*objectUsage{
		{objType: "service", label: "Redis", cpuSeconds: 40, samples: 5},
		{objType: "site", label: "site.example", cpuSeconds: 35, samples: 5},
		{objType: "process", label: "php", cpuSeconds: 25, samples: 5},
	}}, false)
	item, ok = parseAlertDiagnosisContext(AlertDiagnosisContext{ID: 6, AlertType: "alert_cpu", Attribution: topList, CreatedAt: end.Add(time.Second)}, "site.example")
	if !ok || item.Relation != "clue" {
		t.Fatalf("unattributed top-list parse = %+v, ok=%v, text=%q", item, ok, topList)
	}
}
