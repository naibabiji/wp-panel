package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"
)

func setupWPUpdateLogTest(t *testing.T) {
	t.Helper()
	setupWPUpdateBackupTest(t) // 复用：开库 + 迁移 + 插入 wordpress 站点 1、2
}

func insertWPUpdateLogTask(t *testing.T, siteID int, taskID, status string, finishedAt *time.Time, requiresAttention bool) {
	t.Helper()
	now := executor.WPUpdateDBTime(time.Now().UTC())
	var finishedArg any
	if finishedAt != nil {
		finishedArg = executor.WPUpdateDBTime(*finishedAt)
	}
	attention := 0
	if requiresAttention {
		attention = 1
	}
	_, err := database.GetDB().Exec(`INSERT INTO wp_update_tasks
		(id,site_id,component_type,component_key,task_kind,trigger_type,status,stage,current_version,target_version,
		 package_source,download_url,downloaded_sha256,verification_level,package_snapshot_path,backup_ready,
		 database_backup_mode,plan_sealed_at,requested_at,finished_at,requires_attention,created_at,updated_at)
		VALUES(?,?,'plugin','sample/sample.php','update','manual',?,?,'1.0.0','1.1.0',
		 'wordpress.org','https://downloads.wordpress.org/plugin/sample.1.1.0.zip',?,'structure_only','/tmp/package.zip',1,
		 'fresh',?,?,?,?,?,?)`,
		taskID, siteID, status, "complete", strings.Repeat("a", 64), now, now, finishedArg, attention, now, now)
	if err != nil {
		t.Fatal(err)
	}
}

func insertWPUpdateLogEvent(t *testing.T, taskID, stage, result, code string, ts time.Time) {
	t.Helper()
	if _, err := database.GetDB().Exec(`INSERT INTO wp_update_task_events(task_id,stage,result,error_code,summary,created_at)
		VALUES(?,?,?,?,?,?)`, taskID, stage, result, code, "", executor.WPUpdateDBTime(ts)); err != nil {
		t.Fatal(err)
	}
}

func decodeWPUpdateLogList(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var resp models.ApiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	arr, ok := resp.Data.([]any)
	if !ok {
		t.Fatalf("data not array: %#v", resp.Data)
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("item not map: %#v", item)
		}
		out = append(out, m)
	}
	return out
}

func newWPUpdateLogTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/websites/:id/wp-update-logs", (&WPUpdateLogHandler{}).List)
	return r
}

func callWPUpdateLog(t *testing.T, router *gin.Engine, siteID int) []map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/websites/"+strconv.Itoa(siteID)+"/wp-update-logs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	return decodeWPUpdateLogList(t, rec.Body.Bytes())
}

func TestWPUpdateLogListReturnsRecentTasksAndEvents(t *testing.T) {
	setupWPUpdateLogTest(t)
	now := time.Now().UTC()
	insertWPUpdateLogTask(t, 1, "wpu_log_recent_success", "success", ptrTime(now.Add(-1*time.Hour)), false)
	insertWPUpdateLogEvent(t, "wpu_log_recent_success", "plan", "info", "", now.Add(-1*time.Hour))
	insertWPUpdateLogEvent(t, "wpu_log_recent_success", "precheck", "success", "", now.Add(-55*time.Minute))
	insertWPUpdateLogEvent(t, "wpu_log_recent_success", "complete", "success", "", now.Add(-50*time.Minute))

	items := callWPUpdateLog(t, newWPUpdateLogTestRouter(), 1)
	if len(items) != 1 {
		t.Fatalf("items=%d want 1: %#v", len(items), items)
	}
	task := items[0]
	if task["task_id"] != "wpu_log_recent_success" {
		t.Fatalf("task_id=%v", task["task_id"])
	}
	if task["component_type"] != "plugin" || task["current_version"] != "1.0.0" || task["target_version"] != "1.1.0" {
		t.Fatalf("task header wrong: %#v", task)
	}
	if task["requires_attention"] != false {
		t.Fatalf("requires_attention=%v", task["requires_attention"])
	}
	events, ok := task["events"].([]any)
	if !ok || len(events) != 3 {
		t.Fatalf("events=%#v", task["events"])
	}
	first := events[0].(map[string]any)
	if first["stage"] != "plan" || first["result"] != "info" {
		t.Fatalf("first event=%#v", first)
	}
	last := events[2].(map[string]any)
	if last["stage"] != "complete" {
		t.Fatalf("events not ordered ascending by id: %#v", events)
	}
}

func TestWPUpdateLogListExcludesTasksOlderThan24h(t *testing.T) {
	setupWPUpdateLogTest(t)
	now := time.Now().UTC()
	insertWPUpdateLogTask(t, 1, "wpu_log_old_failed", "failed", ptrTime(now.Add(-26*time.Hour)), true)
	insertWPUpdateLogEvent(t, "wpu_log_old_failed", "rollback", "failed", "license_invalid", now.Add(-26*time.Hour))

	items := callWPUpdateLog(t, newWPUpdateLogTestRouter(), 1)
	if len(items) != 0 {
		t.Fatalf("items=%d want 0 (expired task must be excluded): %#v", len(items), items)
	}
}

func TestWPUpdateLogListIncludesUnfinishedTask(t *testing.T) {
	setupWPUpdateLogTest(t)
	now := time.Now().UTC()
	insertWPUpdateLogTask(t, 1, "wpu_log_running", "running", nil, false)
	insertWPUpdateLogEvent(t, "wpu_log_running", "claimed", "info", "", now.Add(-1*time.Minute))

	items := callWPUpdateLog(t, newWPUpdateLogTestRouter(), 1)
	if len(items) != 1 {
		t.Fatalf("items=%d want 1 (unfinished task must be included): %#v", len(items), items)
	}
	if items[0]["status"] != "running" {
		t.Fatalf("status=%v", items[0]["status"])
	}
}

func TestWPUpdateLogListIsSiteScoped(t *testing.T) {
	setupWPUpdateLogTest(t)
	now := time.Now().UTC()
	insertWPUpdateLogTask(t, 1, "wpu_log_site1", "success", ptrTime(now.Add(-1*time.Hour)), false)
	insertWPUpdateLogTask(t, 2, "wpu_log_site2", "success", ptrTime(now.Add(-1*time.Hour)), false)

	items := callWPUpdateLog(t, newWPUpdateLogTestRouter(), 1)
	if len(items) != 1 || items[0]["task_id"] != "wpu_log_site1" {
		t.Fatalf("site-scoped items=%#v", items)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
