package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
)

func insertBackupPolicySite(t *testing.T, id int, domain string) {
	t.Helper()
	if _, err := database.GetDB().Exec(`INSERT INTO websites(id,name,domain,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, id, domain, domain, "u1", "/www/"+domain, "/logs/"+domain, "db1", "u1", "/p", "/n"); err != nil {
		t.Fatalf("insert website: %v", err)
	}
}

func TestBackupCycleFromCron(t *testing.T) {
	tests := map[string]string{
		"0 0 * * *":             "daily",
		"0 3 * * 2":             "weekly",
		"0 6 2,17 * *":          "fortnightly",
		"0 8 9 * *":             "monthly",
		"0 10 3 1,4,7,10 *":     "quarterly",
		"*/5 * * * *":           "custom",
		"0 0 1,8,15,22 * *":     "custom",
		"not a cron expression": "custom",
	}
	for expression, want := range tests {
		if got := backupCycleFromCron(expression); got != want {
			t.Errorf("backupCycleFromCron(%q) = %q, want %q", expression, got, want)
		}
	}
}

func TestAllocateBackupCronStaggersAndSkipsFourAM(t *testing.T) {
	occupied := map[string]bool{}
	wants := []string{"0 0 1 * *", "0 1 1 * *", "0 2 1 * *", "0 3 1 * *", "0 5 1 * *"}
	for i, want := range wants {
		got, err := allocateBackupCron("monthly", occupied)
		if err != nil {
			t.Fatalf("allocate %d: %v", i, err)
		}
		if got != want {
			t.Fatalf("allocate %d = %q, want %q", i, got, want)
		}
		occupied[got] = true
	}
}

func TestApplyBackupPolicyCreatesIncrementalAndFullTasks(t *testing.T) {
	setupBackupOverviewTestDB(t)
	insertBackupPolicySite(t, 1, "policy.example.com")
	changed, skipped, err := applyBackupPolicy(database.GetDB(), []backupPolicySiteRequest{{
		SiteID: 1, DBEnabled: true, DBKeepCount: 9,
		IncrementalEnabled: true, IncrementalCycle: "weekly",
		FullEnabled: true, FullCycle: "quarterly",
	}})
	if err != nil || !changed || skipped != 0 {
		t.Fatalf("apply = changed:%v skipped:%d err:%v", changed, skipped, err)
	}
	var enabled, keep int
	if err := database.GetDB().QueryRow(`SELECT enabled,keep_count FROM backup_settings WHERE site_id=1`).Scan(&enabled, &keep); err != nil || enabled != 1 || keep != 9 {
		t.Fatalf("database setting = %d/%d err=%v", enabled, keep, err)
	}
	rows, err := database.GetDB().Query(`SELECT backup_mode,cron_expression FROM cron_jobs WHERE site_id=1 ORDER BY backup_mode`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var mode, expression string
		_ = rows.Scan(&mode, &expression)
		got[mode] = expression
	}
	if backupCycleFromCron(got["incremental"]) != "weekly" || backupCycleFromCron(got["full"]) != "quarterly" {
		t.Fatalf("created tasks = %+v", got)
	}
}

func TestApplyBackupPolicySkipsOrRebuildsDuplicateMode(t *testing.T) {
	setupBackupOverviewTestDB(t)
	insertBackupPolicySite(t, 1, "duplicate.example.com")
	for _, hour := range []int{1, 2} {
		if _, err := database.GetDB().Exec(`INSERT INTO cron_jobs(name,cron_expression,command,task_type,backup_mode,site_id) VALUES(?,?,?,'file_backup','incremental',1)`, "duplicate", fmt.Sprintf("0 %d * * *", hour), backupPolicyCommand); err != nil {
			t.Fatal(err)
		}
	}
	request := backupPolicySiteRequest{SiteID: 1, DBKeepCount: 7, IncrementalEnabled: true, IncrementalCycle: "weekly"}
	_, skipped, err := applyBackupPolicy(database.GetDB(), []backupPolicySiteRequest{request})
	if err != nil || skipped != 1 {
		t.Fatalf("skip duplicates = %d err=%v", skipped, err)
	}
	var count int
	_ = database.GetDB().QueryRow(`SELECT COUNT(*) FROM cron_jobs WHERE site_id=1 AND backup_mode='incremental'`).Scan(&count)
	if count != 2 {
		t.Fatalf("task count after skip = %d, want 2", count)
	}
	request.IncrementalRebuild = true
	_, skipped, err = applyBackupPolicy(database.GetDB(), []backupPolicySiteRequest{request})
	if err != nil || skipped != 0 {
		t.Fatalf("rebuild duplicates = %d err=%v", skipped, err)
	}
	_ = database.GetDB().QueryRow(`SELECT COUNT(*) FROM cron_jobs WHERE site_id=1 AND backup_mode='incremental'`).Scan(&count)
	if count != 1 {
		t.Fatalf("task count after rebuild = %d, want 1", count)
	}
}

func TestLoadBackupPolicyReflectsCurrentSettings(t *testing.T) {
	setupBackupOverviewTestDB(t)
	insertBackupPolicySite(t, 1, "current.example.com")
	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO backup_settings(site_id,enabled,keep_count) VALUES(1,1,11)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE remote_backup_settings SET enabled=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cron_jobs(name,cron_expression,command,task_type,backup_mode,site_id,enabled)
		VALUES('existing','0 3 1 * *',?,'file_backup','incremental',1,1)`, backupPolicyCommand); err != nil {
		t.Fatal(err)
	}
	sites, remoteEnabled, err := loadBackupPolicy(db)
	if err != nil || len(sites) != 1 {
		t.Fatalf("load sites=%d remote=%v err=%v", len(sites), remoteEnabled, err)
	}
	got := sites[0]
	if !remoteEnabled || !got.DBEnabled || got.DBKeepCount != 11 || !got.Incremental.Enabled || got.Incremental.TaskCount != 1 || got.Incremental.Cycle != "monthly" || got.Full.Cycle != "monthly" {
		t.Fatalf("loaded state = %+v remote=%v", got, remoteEnabled)
	}
}

func TestApplyBackupPolicyDisablesTaskWithoutDeletingBackupFiles(t *testing.T) {
	setupBackupOverviewTestDB(t)
	insertBackupPolicySite(t, 1, "disable.example.com")
	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO cron_jobs(name,cron_expression,command,task_type,backup_mode,site_id) VALUES('existing','0 1 * * 1',?,'file_backup','incremental',1)`, backupPolicyCommand); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO file_backups(site_id,filename,file_size,mode) VALUES(1,'file_inc_existing.tar.gz',12,'incremental')`); err != nil {
		t.Fatal(err)
	}
	changed, _, err := applyBackupPolicy(db, []backupPolicySiteRequest{{SiteID: 1, DBKeepCount: 7}})
	if err != nil || !changed {
		t.Fatalf("apply changed=%v err=%v", changed, err)
	}
	var tasks, files int
	_ = db.QueryRow(`SELECT COUNT(*) FROM cron_jobs WHERE site_id=1 AND task_type='file_backup'`).Scan(&tasks)
	_ = db.QueryRow(`SELECT COUNT(*) FROM file_backups WHERE site_id=1`).Scan(&files)
	if tasks != 0 || files != 1 {
		t.Fatalf("tasks=%d files=%d, want 0/1", tasks, files)
	}
}

func TestSaveBackupPolicyRollsBackWhenCronRenderFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupBackupOverviewTestDB(t)
	insertBackupPolicySite(t, 1, "rollback.example.com")
	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO backup_settings(site_id,enabled,keep_count) VALUES(1,0,5)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cron_jobs(id,name,cron_expression,command,task_type,backup_mode,site_id,enabled,last_run_at,last_status,last_output)
		VALUES(42,'old task','0 2 2 * *',?,'file_backup','incremental',1,1,CURRENT_TIMESTAMP,'success','old output')`, backupPolicyCommand); err != nil {
		t.Fatal(err)
	}
	oldRender := renderBackupPolicyCron
	calls := 0
	renderBackupPolicyCron = func() error {
		calls++
		if calls == 1 {
			return errors.New("render failed")
		}
		return nil
	}
	t.Cleanup(func() { renderBackupPolicyCron = oldRender })

	body, _ := json.Marshal(backupPolicyRequest{Sites: []backupPolicySiteRequest{{
		SiteID: 1, DBEnabled: true, DBKeepCount: 7, IncrementalEnabled: true, IncrementalCycle: "weekly",
	}}})
	router := gin.New()
	router.PUT("/api/backups/policy", SaveBackupPolicy)
	req := httptest.NewRequest(http.MethodPut, "/api/backups/policy", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var enabled, keep, jobID int
	var expression, output string
	_ = db.QueryRow(`SELECT enabled,keep_count FROM backup_settings WHERE site_id=1`).Scan(&enabled, &keep)
	_ = db.QueryRow(`SELECT id,cron_expression,last_output FROM cron_jobs WHERE site_id=1 AND task_type='file_backup'`).Scan(&jobID, &expression, &output)
	if enabled != 0 || keep != 5 || jobID != 42 || expression != "0 2 2 * *" || output != "old output" || calls != 2 {
		t.Fatalf("rollback setting=%d/%d job=%d/%q/%q render calls=%d", enabled, keep, jobID, expression, output, calls)
	}
}

func TestSaveBackupPolicyReturnsLocalizedValidationError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body, _ := json.Marshal(backupPolicyRequest{Sites: []backupPolicySiteRequest{{SiteID: 1, DBKeepCount: 31}}})
	router := gin.New()
	router.PUT("/api/backups/policy", SaveBackupPolicy)
	req := httptest.NewRequest(http.MethodPut, "/api/backups/policy?lang=en-US", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Message != "Database backup retention must be between 1 and 30 copies" {
		t.Fatalf("message = %q", response.Message)
	}
}
