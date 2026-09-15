package executor

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func setupCronGateTest(t *testing.T) *sql.DB {
	t.Helper()
	oldLockDir := cronJobLockDir
	cronJobLockDir = t.TempDir()
	t.Cleanup(func() { cronJobLockDir = oldLockDir })
	oldLogFile := cronLogFile
	cronLogFile = filepath.Join(t.TempDir(), "cron.log")
	t.Cleanup(func() { cronLogFile = oldLogFile })
	openTestDB(t)
	insertMinimalWebsite(t, "paused.example.com")
	db := database.GetDB()
	mustExec(t, db, `UPDATE websites SET status='paused',system_user='wp_paused' WHERE id=1`)
	return db
}

func TestRunScheduledCronSkipsPausedSiteWithoutChangingLastResult(t *testing.T) {
	db := setupCronGateTest(t)
	mustExec(t, db, `INSERT INTO cron_jobs
		(id,name,cron_expression,command,task_type,site_id,enabled,last_status,last_output)
		VALUES(11,'paused wp cron','* * * * *','paused.example.com','wp_cron',1,1,'success','previous')`)

	result := RunScheduledCron(11)
	if !result.Success {
		t.Fatalf("RunScheduledCron paused result = %+v", result)
	}
	var lastRun sql.NullTime
	var status, output string
	if err := db.QueryRow(`SELECT last_run_at,last_status,last_output FROM cron_jobs WHERE id=11`).Scan(&lastRun, &status, &output); err != nil {
		t.Fatal(err)
	}
	if lastRun.Valid || status != "success" || output != "previous" {
		t.Fatalf("paused skip changed last result: last=%v status=%q output=%q", lastRun, status, output)
	}
}

func TestCronJobRuntimeSuspendedResolvesRunAsUser(t *testing.T) {
	db := setupCronGateTest(t)
	mustExec(t, db, `INSERT INTO cron_jobs
		(id,name,cron_expression,command,task_type,run_as_user,enabled)
		VALUES(12,'site command','* * * * *','echo test','command','wp_paused',1)`)

	suspended, domain, reason, err := CronJobRuntimeSuspended(12)
	if err != nil || !suspended || domain != "paused.example.com" || reason != "paused" {
		t.Fatalf("runtime gate = suspended:%v domain:%q reason:%q err:%v", suspended, domain, reason, err)
	}

	mustExec(t, db, `INSERT INTO websites
		(id,name,domain,status,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path)
		VALUES(2,'duplicate','duplicate.example.com','active','wp_paused','/www/duplicate','/logs/duplicate','db2','u2','/p2','/n2')`)
	if _, _, _, err := CronJobRuntimeSuspended(12); err == nil {
		t.Fatal("duplicate system_user must fail closed")
	}
}

func TestCronJobRuntimeSuspendedRejectsMissingRunAsUserSite(t *testing.T) {
	db := setupCronGateTest(t)
	mustExec(t, db, `INSERT INTO cron_jobs
		(id,name,cron_expression,command,task_type,run_as_user,enabled)
		VALUES(14,'orphan command','* * * * *','echo test','command','wp_missing',1)`)

	if _, _, _, err := CronJobRuntimeSuspended(14); err == nil {
		t.Fatal("missing system_user owner must fail closed")
	}
}

func TestRunScheduledCronSkipsMigrationLockedSite(t *testing.T) {
	db := setupCronGateTest(t)
	mustExec(t, db, `UPDATE websites SET status='active' WHERE id=1`)
	// The gate only reads site_id/status. Avoid building an unrelated migration task fixture.
	mustExec(t, db, `PRAGMA foreign_keys=OFF`)
	mustExec(t, db, `INSERT INTO site_migration_locks
		(domain,site_id,migration_site_id,direction,status)
		VALUES('paused.example.com',1,'migration_gate_test','source','active')`)
	mustExec(t, db, `INSERT INTO cron_jobs
		(id,name,cron_expression,command,task_type,site_id,enabled,last_status,last_output)
		VALUES(15,'locked command','* * * * *','exit 99','command',1,1,'success','previous')`)

	result := RunScheduledCron(15)
	if !result.Success {
		t.Fatalf("RunScheduledCron migration lock result = %+v", result)
	}
	var lastRun sql.NullTime
	var status, output string
	if err := db.QueryRow(`SELECT last_run_at,last_status,last_output FROM cron_jobs WHERE id=15`).Scan(&lastRun, &status, &output); err != nil {
		t.Fatal(err)
	}
	if lastRun.Valid || status != "success" || output != "previous" {
		t.Fatalf("migration skip changed last result: last=%v status=%q output=%q", lastRun, status, output)
	}
	if suspended, _, reason, err := CronJobRuntimeSuspended(15); err != nil || !suspended || reason != "migration_locked" {
		t.Fatalf("migration runtime state = suspended:%v reason:%q err:%v", suspended, reason, err)
	}
}

func TestRunScheduledCronExecutesActiveSiteJob(t *testing.T) {
	db := setupCronGateTest(t)
	mustExec(t, db, `UPDATE websites SET status='active' WHERE id=1`)
	marker := filepath.Join(t.TempDir(), "ran")
	if _, err := db.Exec(`INSERT INTO cron_jobs
		(id,name,cron_expression,command,task_type,site_id,enabled)
		VALUES(16,'active command','* * * * *',?,'command',1,1)`, "printf ran > "+marker); err != nil {
		t.Fatal(err)
	}

	result := RunScheduledCron(16)
	if !result.Success {
		t.Fatalf("RunScheduledCron active result = %+v", result)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "ran" {
		t.Fatalf("active task marker = %q, err=%v", data, err)
	}
}

func TestRunScheduledCronSkipsWhenSameJobIsAlreadyRunning(t *testing.T) {
	db := setupCronGateTest(t)
	mustExec(t, db, `UPDATE websites SET status='active' WHERE id=1`)
	marker := filepath.Join(t.TempDir(), "ran")
	if _, err := db.Exec(`INSERT INTO cron_jobs
		(id,name,cron_expression,command,task_type,site_id,enabled,last_status,last_output)
		VALUES(17,'overlapping command','* * * * *',?,'command',1,1,'success','previous')`, "printf ran > "+marker); err != nil {
		t.Fatal(err)
	}

	oldLockDir := cronJobLockDir
	cronJobLockDir = t.TempDir()
	t.Cleanup(func() { cronJobLockDir = oldLockDir })
	lock, err := acquireCronJobExecutionLock(17)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	result := RunScheduledCron(17)
	if !result.Success || !strings.Contains(result.Message, "已跳过") {
		t.Fatalf("RunScheduledCron overlap result = %+v", result)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("overlapping task executed, marker err=%v", err)
	}
	var lastRun sql.NullTime
	var status, output string
	if err := db.QueryRow(`SELECT last_run_at,last_status,last_output FROM cron_jobs WHERE id=17`).Scan(&lastRun, &status, &output); err != nil {
		t.Fatal(err)
	}
	if lastRun.Valid || status != "success" || output != "previous" {
		t.Fatalf("overlap skip changed last result: last=%v status=%q output=%q", lastRun, status, output)
	}
}

func TestManualCronUsesSameExecutionLockAsScheduledCron(t *testing.T) {
	db := setupCronGateTest(t)
	mustExec(t, db, `UPDATE websites SET status='active' WHERE id=1`)
	mustExec(t, db, `INSERT INTO cron_jobs
		(id,name,cron_expression,command,task_type,site_id,enabled,running)
		VALUES(19,'manual overlap','* * * * *','exit 99','command',1,1,1)`)

	oldLockDir := cronJobLockDir
	cronJobLockDir = t.TempDir()
	t.Cleanup(func() { cronJobLockDir = oldLockDir })
	lock, err := acquireCronJobExecutionLock(19)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	result := executeRunCron(&Task{Payload: &RunCronPayload{JobID: 19, Name: "manual overlap"}})
	if result.Success || !strings.Contains(result.Message, "正在执行") {
		t.Fatalf("executeRunCron overlap result = %+v", result)
	}
	var running int
	if err := db.QueryRow(`SELECT running FROM cron_jobs WHERE id=19`).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if running != 0 {
		t.Fatalf("manual claim was not released: running=%d", running)
	}
}

func TestScheduledCronDoesNotClearManualRunningClaim(t *testing.T) {
	db := setupCronGateTest(t)
	mustExec(t, db, `UPDATE websites SET status='active' WHERE id=1`)
	mustExec(t, db, `INSERT INTO cron_jobs
		(id,name,cron_expression,command,task_type,site_id,enabled,running)
		VALUES(20,'scheduled result','* * * * *','true','command',1,1,1)`)
	oldLockDir := cronJobLockDir
	cronJobLockDir = t.TempDir()
	t.Cleanup(func() { cronJobLockDir = oldLockDir })

	result := RunScheduledCron(20)
	if !result.Success {
		t.Fatalf("RunScheduledCron result = %+v", result)
	}
	var running int
	if err := db.QueryRow(`SELECT running FROM cron_jobs WHERE id=20`).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if running != 1 {
		t.Fatalf("scheduled execution cleared manual claim: running=%d", running)
	}
}

func TestRunScheduledCronLogsGateErrors(t *testing.T) {
	setupCronGateTest(t)
	oldLogFile := cronLogFile
	cronLogFile = filepath.Join(t.TempDir(), "cron.log")
	t.Cleanup(func() { cronLogFile = oldLogFile })

	result := RunScheduledCron(999)
	if result.Success {
		t.Fatalf("missing job result = %+v", result)
	}
	data, err := os.ReadFile(cronLogFile)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(data); !strings.Contains(text, "GATE ERROR job_id=999") || !strings.Contains(text, "查询任务失败") {
		t.Fatalf("gate error log = %q", text)
	}
}

func TestPruneCronLogKeepsConfiguredLineLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cron.log")
	lines := make([]string, cronLogKeepLines+2)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%d", i+1)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	pruneCronLog(path, cronLogKeepLines)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(got) != cronLogKeepLines || got[0] != "line-3" || got[len(got)-1] != fmt.Sprintf("line-%d", cronLogKeepLines+2) {
		t.Fatalf("pruned cron log: lines=%d first=%q last=%q", len(got), got[0], got[len(got)-1])
	}
}

func TestReconcileInterruptedManualCronJobsReleasesClaimAndPreservesLastResult(t *testing.T) {
	db := setupCronGateTest(t)
	mustExec(t, db, `INSERT INTO cron_jobs
		(id,name,cron_expression,command,task_type,enabled,running,last_run_at,last_status,last_output)
		VALUES(18,'interrupted manual','* * * * *','echo test','command',1,1,'2026-09-14 10:00:00','success','previous output')`)
	oldLogFile := cronLogFile
	cronLogFile = filepath.Join(t.TempDir(), "cron.log")
	t.Cleanup(func() { cronLogFile = oldLogFile })

	count, err := ReconcileInterruptedManualCronJobs(db)
	if err != nil || count != 1 {
		t.Fatalf("reconcile count=%d err=%v", count, err)
	}
	var running int
	var lastRun sql.NullTime
	var lastStatus, lastOutput string
	if err := db.QueryRow(`SELECT running,last_run_at,last_status,last_output FROM cron_jobs WHERE id=18`).Scan(&running, &lastRun, &lastStatus, &lastOutput); err != nil {
		t.Fatal(err)
	}
	if running != 0 || !lastRun.Valid || lastRun.Time.Format("2006-01-02 15:04:05") != "2026-09-14 10:00:00" || lastStatus != "success" || lastOutput != "previous output" {
		t.Fatalf("job after reconcile: running=%d last_run=%v status=%q output=%q", running, lastRun, lastStatus, lastOutput)
	}
	data, err := os.ReadFile(cronLogFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "INTERRUPTED 手动任务因面板进程重启中断，共 1 个") {
		t.Fatalf("cron log=%q", data)
	}

	count, err = ReconcileInterruptedManualCronJobs(db)
	if err != nil || count != 0 {
		t.Fatalf("second reconcile count=%d err=%v", count, err)
	}
}

func TestRenderCronUsesUnifiedJobIDEntrypoint(t *testing.T) {
	db := setupCronGateTest(t)
	mustExec(t, db, `INSERT INTO cron_jobs
		(id,name,cron_expression,command,task_type,site_id,enabled)
		VALUES(13,'paused backup','5 2 * * *','wp-panel file backup','file_backup',1,1)`)

	root := t.TempDir()
	oldCfg := config.AppConfig
	config.AppConfig = &config.Config{Paths: config.PathsConfig{CronFile: filepath.Join(root, "wp-panel")}}
	t.Cleanup(func() { config.AppConfig = oldCfg })
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "systemctl"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	if result := renderCronConfig(); !result.Success {
		t.Fatalf("renderCronConfig = %+v", result)
	}
	content, err := os.ReadFile(config.AppConfig.Paths.CronFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "--run-scheduled-cron=13") || strings.Contains(text, "--file-backup=") || strings.Contains(text, "curl ") {
		t.Fatalf("unexpected rendered cron:\n%s", text)
	}
}

func TestRenderCronReportsRestartFailureAndKeepsRenderedTarget(t *testing.T) {
	db := setupCronGateTest(t)
	mustExec(t, db, `INSERT INTO cron_jobs
		(id,name,cron_expression,command,task_type,enabled)
		VALUES(17,'restart failure','10 3 * * *','echo test','command',1)`)

	oldCfg := config.AppConfig
	config.AppConfig = &config.Config{Paths: config.PathsConfig{CronFile: filepath.Join(t.TempDir(), "wp-panel")}}
	t.Cleanup(func() { config.AppConfig = oldCfg })
	oldRestart := restartCronService
	restartCronService = func() (string, error) { return "restart failed", fmt.Errorf("exit status 1") }
	t.Cleanup(func() { restartCronService = oldRestart })

	result := renderCronConfig()
	if result.Success || !strings.Contains(result.Message, "重启Cron服务失败") {
		t.Fatalf("renderCronConfig = %+v", result)
	}
	content, err := os.ReadFile(config.AppConfig.Paths.CronFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "--run-scheduled-cron=17") {
		t.Fatalf("rendered target did not retain database state:\n%s", content)
	}
}

func TestScheduledRemoteMaintenanceRowsExcludePausedSites(t *testing.T) {
	db := setupCronGateTest(t)
	mustExec(t, db, `INSERT INTO db_backups(site_id,filename,file_size,db_name,auto) VALUES(1,'paused.sql.gz',10,'db1',1)`)
	mustExec(t, db, `INSERT INTO file_backups(site_id,filename,file_size,mode) VALUES(1,'paused.tar.gz',10,'full')`)

	rows, err := loadRemoteMaintenanceRows(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("scheduled maintenance rows for paused site = %d, want 0", len(rows))
	}
	rows, err = loadRemoteMaintenanceRows(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("manual maintenance rows for paused site = %d, want 2", len(rows))
	}
}
