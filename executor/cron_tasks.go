package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

var cronLogFile = "/www/server/panel/logs/cron.log"
var cronJobLockDir = "/run/lock"

var errCronJobAlreadyRunning = errors.New("cron job is already running")

var restartCronService = func() (string, error) {
	return executeCommand("systemctl", "restart", "cron")
}

const cronLogKeepLines = 1000

// ReconcileInterruptedManualCronJobs releases claims owned by the main
// process's in-memory queue. The scheduled Cron CLI never sets running, so a
// running=1 row at main-service startup can only be a manual execution lost
// when the previous panel process exited.
func ReconcileInterruptedManualCronJobs(db *sql.DB) (int64, error) {
	if db == nil {
		return 0, errors.New("database unavailable")
	}
	result, err := db.Exec(`UPDATE cron_jobs SET running=0 WHERE running=1`)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}

	now := time.Now().Format("2006-01-02 15:04:05")
	if f, openErr := os.OpenFile(cronLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); openErr == nil {
		_, _ = fmt.Fprintf(f, "[%s] INTERRUPTED 手动任务因面板进程重启中断，共 %d 个\n", now, count)
		_ = f.Close()
		pruneCronLog(cronLogFile, cronLogKeepLines)
	}
	return count, nil
}

func executeRenderCron(task *Task) TaskResult {
	return renderCronConfig()
}

func renderCronConfig() TaskResult {
	cfg := config.AppConfig
	if cfg == nil {
		return TaskResult{Success: false, Message: "配置未加载"}
	}

	db := database.GetDB()
	rows, err := db.Query(
		`SELECT id,name,cron_expression FROM cron_jobs WHERE enabled=1`,
	)
	if err != nil {
		log.Printf("查询Cron任务失败: %v", err)
		return TaskResult{Success: false, Message: "查询Cron任务失败"}
	}
	defer rows.Close()

	var cronLines []string
	cronLines = append(cronLines, "# WP Panel Cron Jobs — DO NOT EDIT MANUALLY")
	cronLines = append(cronLines, "SHELL=/bin/bash")
	cronLines = append(cronLines, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	cronLines = append(cronLines, "")

	for rows.Next() {
		var id int
		var name, cronExpr string
		if err := rows.Scan(&id, &name, &cronExpr); err != nil {
			continue
		}
		safeName := sanitizeCronArg(name)
		line := fmt.Sprintf(`%s root /usr/local/bin/wp-panel --run-scheduled-cron=%d --config=/www/server/panel/config.json # %s`, cronExpr, id, safeName)
		if !strings.HasSuffix(line, "\n") {
			line += "\n"
		}
		cronLines = append(cronLines, line)
	}

	cronContent := strings.Join(cronLines, "\n") + "\n"

	if err := os.WriteFile(cfg.Paths.CronFile, []byte(cronContent), 0644); err != nil {
		log.Printf("写入Cron文件失败: %v", err)
		return TaskResult{Success: false, Message: "写入Cron文件失败"}
	}

	if output, err := restartCronService(); err != nil {
		log.Printf("重启Cron服务失败: %v, output: %s", err, strings.TrimSpace(output))
		return TaskResult{Success: false, Message: "Cron配置已写入，但重启Cron服务失败"}
	}

	return TaskResult{Success: true, Message: "Cron配置已更新"}
}

func executeRunCron(task *Task) TaskResult {
	payload, ok := task.Payload.(*RunCronPayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}
	// The HTTP handler claims running=1 before enqueueing. Always release that
	// claim, including a state change between confirmation and queue execution.
	defer database.GetDB().Exec(`UPDATE cron_jobs SET running=0 WHERE id=?`, payload.JobID)
	lock, err := acquireCronJobExecutionLock(payload.JobID)
	if err != nil {
		if errors.Is(err, errCronJobAlreadyRunning) {
			return TaskResult{Success: false, Message: "任务正在执行中，请稍后再试"}
		}
		return TaskResult{Success: false, Message: "取得任务运行锁失败"}
	}
	defer lock.Close()

	suspended, _, reason, err := CronJobRuntimeSuspended(payload.JobID)
	if err != nil {
		return TaskResult{Success: false, Message: "检查任务关联网站状态失败"}
	}
	if suspended && (reason != "paused" || !payload.ConfirmPaused) {
		return TaskResult{Success: false, Message: "关联网站当前不允许运行该任务"}
	}
	return runCronJob(payload.JobID, false)
}

type cronJobExecution struct {
	id, keepCount                                  int
	name, command, runAsUser, taskType, backupMode string
	siteID                                         sql.NullInt64
}

func loadCronJob(jobID int) (cronJobExecution, bool, error) {
	var job cronJobExecution
	var enabled int
	err := database.GetDB().QueryRow(`SELECT id,name,command,run_as_user,task_type,backup_mode,keep_count,site_id,enabled
		FROM cron_jobs WHERE id=?`, jobID).Scan(&job.id, &job.name, &job.command, &job.runAsUser,
		&job.taskType, &job.backupMode, &job.keepCount, &job.siteID, &enabled)
	return job, enabled == 1, err
}

func cronJobSite(job cronJobExecution) (int, string, error) {
	if job.siteID.Valid {
		var domain string
		err := database.GetDB().QueryRow(`SELECT domain FROM websites WHERE id=?`, job.siteID.Int64).Scan(&domain)
		return int(job.siteID.Int64), domain, err
	}
	if strings.TrimSpace(job.runAsUser) == "" {
		return 0, "", nil
	}
	rows, err := database.GetDB().Query(`SELECT id,domain FROM websites WHERE system_user=?`, job.runAsUser)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	var id int
	var domain string
	count := 0
	for rows.Next() {
		if err := rows.Scan(&id, &domain); err != nil {
			return 0, "", err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	if count != 1 {
		return 0, "", fmt.Errorf("运行用户的网站归属异常")
	}
	return id, domain, nil
}

func siteScheduledWorkAllowed(siteID int) (bool, string, error) {
	var status string
	if err := database.GetDB().QueryRow(`SELECT status FROM websites WHERE id=?`, siteID).Scan(&status); err != nil {
		return false, "", err
	}
	if status != "active" {
		return false, status, nil
	}
	var locked int
	if err := database.GetDB().QueryRow(`SELECT EXISTS(SELECT 1 FROM site_migration_locks WHERE site_id=? AND status='active')`, siteID).Scan(&locked); err != nil {
		return false, "", err
	}
	return locked == 0, map[bool]string{true: "migration_locked", false: ""}[locked != 0], nil
}

// CronJobRuntimeSuspended reports whether an enabled job is temporarily held by its site's runtime state.
func CronJobRuntimeSuspended(jobID int) (bool, string, string, error) {
	job, _, err := loadCronJob(jobID)
	if err != nil {
		return false, "", "", err
	}
	siteID, domain, err := cronJobSite(job)
	if err != nil {
		return false, "", "", err
	}
	if siteID == 0 {
		return false, "", "", nil
	}
	allowed, reason, err := siteScheduledWorkAllowed(siteID)
	return !allowed, domain, reason, err
}

// RunScheduledCron is the short-lived system Cron entrypoint. It must remain before migrations in main.
func RunScheduledCron(jobID int) TaskResult {
	lock, err := acquireCronJobExecutionLock(jobID)
	if err != nil {
		if errors.Is(err, errCronJobAlreadyRunning) {
			appendCronMessage("SKIPPED", jobID, "任务仍在执行，本次自动触发已跳过")
			return TaskResult{Success: true, Message: "任务仍在执行，本次自动触发已跳过"}
		}
		message := "取得任务运行锁失败: " + err.Error()
		appendCronGateError(jobID, message)
		return TaskResult{Success: false, Message: message}
	}
	defer lock.Close()

	job, enabled, err := loadCronJob(jobID)
	if err != nil {
		message := "查询任务失败: " + err.Error()
		appendCronGateError(jobID, message)
		return TaskResult{Success: false, Message: message}
	}
	if !enabled {
		return TaskResult{Success: true, Message: "任务已禁用"}
	}
	siteID, _, err := cronJobSite(job)
	if err != nil {
		message := "解析任务网站失败: " + err.Error()
		appendCronGateError(jobID, message)
		return TaskResult{Success: false, Message: message}
	}
	if siteID > 0 {
		allowed, _, err := siteScheduledWorkAllowed(siteID)
		if err != nil {
			message := "检查网站运行状态失败: " + err.Error()
			appendCronGateError(jobID, message)
			return TaskResult{Success: false, Message: message}
		}
		if !allowed {
			return TaskResult{Success: true, Message: "网站当前不允许运行自动任务"}
		}
	}
	return runCronJob(jobID, true)
}

func runCronJob(jobID int, scheduled bool) TaskResult {
	db := database.GetDB()
	job, _, err := loadCronJob(jobID)
	if err != nil {
		log.Printf("查询任务失败: %v", err)
		return TaskResult{Success: false, Message: "查询任务失败"}
	}

	now := time.Now().Format("2006-01-02 15:04:05")

	var out string
	var execErr error
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if job.taskType == "file_backup" {
		if !job.siteID.Valid {
			return TaskResult{Success: false, Message: "关联网站已不存在"}
		}
		var msg string
		msg, execErr = executeFileBackup(int(job.siteID.Int64), job.backupMode, job.keepCount, scheduled)
		if errors.Is(execErr, errScheduledWorkNotAllowed) {
			return TaskResult{Success: true, Message: "网站当前不允许运行自动任务，文件备份已跳过"}
		}
		if execErr != nil {
			out = execErr.Error()
		} else {
			out = msg
		}
	} else if job.taskType == "wp_cron" {
		url := "https://" + job.command + "/wp-cron.php?doing_wp_cron"
		var outBytes []byte
		outBytes, execErr = exec.CommandContext(ctx, "curl", "-k", "-s", "-o", "/dev/null", url).CombinedOutput()
		out = string(outBytes)
	} else if job.runAsUser != "" {
		var outBytes []byte
		outBytes, execErr = exec.CommandContext(ctx, "runuser", "-u", job.runAsUser, "--", "bash", "-c", job.command).CombinedOutput()
		out = string(outBytes)
	} else {
		var outBytes []byte
		outBytes, execErr = exec.CommandContext(ctx, "bash", "-c", job.command).CombinedOutput()
		out = string(outBytes)
	}

	status := "success"
	if execErr != nil {
		status = "failed"
		if out == "" {
			out = execErr.Error()
		}
	}

	if scheduled {
		_, _ = db.Exec(
			`UPDATE cron_jobs SET last_run_at = ?, last_status = ?, last_output = ? WHERE id = ?`,
			now, status, out, jobID,
		)
	} else {
		_, _ = db.Exec(
			`UPDATE cron_jobs SET last_run_at = ?, last_status = ?, last_output = ?, running = 0 WHERE id = ?`,
			now, status, out, jobID,
		)
	}

	// Append to cron log file, keep the latest bounded history.
	f, err := os.OpenFile(cronLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		mode := "manual"
		if scheduled {
			mode = "scheduled"
		}
		f.WriteString(fmt.Sprintf("[%s] START %s (%s)\n", now, job.name, mode))
		f.WriteString(out + "\n")
		f.WriteString(fmt.Sprintf("[%s] END %s (exit:%d)\n", now, job.name, map[bool]int{true: 0, false: 1}[execErr == nil]))
		f.Close()
	}
	pruneCronLog(cronLogFile, cronLogKeepLines)

	return TaskResult{
		Success: execErr == nil,
		Message: fmt.Sprintf("任务 %s 执行%s", job.name, map[bool]string{true: "成功", false: "失败"}[execErr == nil]),
		Data:    map[string]interface{}{"output": out, "status": status, "run_at": now},
	}
}

func appendCronGateError(jobID int, message string) {
	appendCronMessage("GATE ERROR", jobID, message)
}

func appendCronMessage(kind string, jobID int, message string) {
	now := time.Now().Format("2006-01-02 15:04:05")
	f, err := os.OpenFile(cronLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "[%s] %s job_id=%d: %s\n", now, kind, jobID, message)
	_ = f.Close()
	pruneCronLog(cronLogFile, cronLogKeepLines)
}

type cronJobExecutionLock struct {
	file *os.File
}

func acquireCronJobExecutionLock(jobID int) (*cronJobExecutionLock, error) {
	if jobID <= 0 {
		return nil, errors.New("invalid cron job ID")
	}
	path := filepath.Join(cronJobLockDir, fmt.Sprintf("wp-panel-cron-%d.lock", jobID))
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errCronJobAlreadyRunning
		}
		return nil, err
	}
	return &cronJobExecutionLock{file: file}, nil
}

func (l *cronJobExecutionLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}

func pruneCronLog(path string, keep int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= keep {
		return
	}
	os.WriteFile(path, []byte(strings.Join(lines[len(lines)-keep:], "\n")+"\n"), 0644)
}

// sanitizeCronArg escapes shell metacharacters for use inside double quotes in bash.
func sanitizeCronArg(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "$", "\\$")
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return s
}
