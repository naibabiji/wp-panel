package executor

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func executeRenderCron(task *Task) TaskResult {
	cfg := config.AppConfig
	if cfg == nil {
		return TaskResult{Success: false, Message: "配置未加载"}
	}

	db := database.GetDB()
	rows, err := db.Query(
		`SELECT name, cron_expression, command, run_as_user, task_type, backup_mode, keep_count, site_id
		 FROM cron_jobs WHERE enabled = 1`,
	)
	if err != nil {
		log.Printf("查询Cron任务失败: %v", err)
		return TaskResult{Success: false, Message: "查询Cron任务失败"}
	}
	defer rows.Close()

	wrapperScript := "/www/server/panel/cron-wrapper.sh"
	wrapperContent := `#!/bin/bash
# WP Panel cron wrapper — auto-generated, do not edit
NAME="$1"; LOGFILE="$2"; shift 2
echo "[$(date)] START $NAME" >> "$LOGFILE"
"$@" >> "$LOGFILE" 2>&1; RC=$?
echo "[$(date)] END $NAME (exit:$RC)" >> "$LOGFILE"
tail -n 300 "$LOGFILE" > "$LOGFILE.tmp" && mv "$LOGFILE.tmp" "$LOGFILE"
exit $RC
`
	os.WriteFile(wrapperScript, []byte(wrapperContent), 0755)

	var cronLines []string
	cronLines = append(cronLines, "# WP Panel Cron Jobs — DO NOT EDIT MANUALLY")
	cronLines = append(cronLines, "SHELL=/bin/bash")
	cronLines = append(cronLines, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	cronLines = append(cronLines, "")

	logFile := "/www/server/panel/logs/cron.log"

	for rows.Next() {
		var name, cronExpr, command, runAsUser, taskType, backupMode string
		var keepCount int
		var siteID sql.NullInt64
		if err := rows.Scan(&name, &cronExpr, &command, &runAsUser, &taskType, &backupMode, &keepCount, &siteID); err != nil {
			continue
		}

		safeName := sanitizeCronArg(name)

		var line string
		switch taskType {
		case "file_backup":
			line = fmt.Sprintf(`%s root %s "%s" "%s" /usr/local/bin/wp-panel --file-backup=%d:%s:%d --config=/www/server/panel/config.json # %s`,
				cronExpr, wrapperScript, safeName, logFile, siteID.Int64, backupMode, keepCount, safeName)
		case "wp_cron":
			if !IsValidDomain(command) {
				// Skip invalid domains — handler should have caught this
				continue
			}
			line = fmt.Sprintf(`%s root %s "%s" "%s" curl -k -s -o /dev/null "https://%s/wp-cron.php?doing_wp_cron" # %s`,
				cronExpr, wrapperScript, safeName, logFile, command, safeName)
		default:
			if runAsUser != "" {
				line = fmt.Sprintf(`%s root %s "%s" "%s" runuser -u %s -- bash -c '%s' # %s`,
					cronExpr, wrapperScript, safeName, logFile, runAsUser,
					strings.ReplaceAll(command, "'", "'\\''"), safeName)
			} else {
				line = fmt.Sprintf(`%s root %s "%s" "%s" bash -c '%s' # %s`,
					cronExpr, wrapperScript, safeName, logFile,
					strings.ReplaceAll(command, "'", "'\\''"), safeName)
			}
		}
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

	_, _ = executeCommand("systemctl", "restart", "cron")

	return TaskResult{Success: true, Message: "Cron配置已更新"}
}

func executeRunCron(task *Task) TaskResult {
	payload, ok := task.Payload.(*RunCronPayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}

	db := database.GetDB()
	var name, cronExpr, command, runAsUser, taskType, backupMode string
	var siteID, keepCount int
	var siteIDNull *int
	err := db.QueryRow(
		`SELECT name, cron_expression, command, run_as_user, task_type, backup_mode, keep_count, site_id FROM cron_jobs WHERE id = ?`,
		payload.JobID,
	).Scan(&name, &cronExpr, &command, &runAsUser, &taskType, &backupMode, &keepCount, &siteIDNull)
	if err != nil {
		log.Printf("查询任务失败: %v", err)
		return TaskResult{Success: false, Message: "查询任务失败"}
	}
	if siteIDNull != nil {
		siteID = *siteIDNull
	}

	now := time.Now().Format("2006-01-02 15:04:05")

	var out string
	var execErr error
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if taskType == "file_backup" {
		if siteIDNull == nil {
			return TaskResult{Success: false, Message: "关联网站已不存在"}
		}
		var msg string
		msg, execErr = ExecuteFileBackup(siteID, backupMode, keepCount)
		if execErr != nil {
			out = execErr.Error()
		} else {
			out = msg
		}
	} else if taskType == "wp_cron" {
		url := "https://" + command + "/wp-cron.php?doing_wp_cron"
		var outBytes []byte
		outBytes, execErr = exec.CommandContext(ctx, "curl", "-k", "-s", "-o", "/dev/null", url).CombinedOutput()
		out = string(outBytes)
	} else if runAsUser != "" {
			var outBytes []byte; outBytes, execErr = exec.CommandContext(ctx, "runuser", "-u", runAsUser, "--", "bash", "-c", command).CombinedOutput(); out = string(outBytes)
	} else {
			var outBytes []byte; outBytes, execErr = exec.CommandContext(ctx, "bash", "-c", command).CombinedOutput(); out = string(outBytes)
	}

	status := "success"
	if execErr != nil {
		status = "failed"
		if out == "" {
			out = execErr.Error()
		}
	}

	_, _ = db.Exec(
		`UPDATE cron_jobs SET last_run_at = ?, last_status = ?, last_output = ?, running = 0 WHERE id = ?`,
		now, status, out, payload.JobID,
	)

	// Append to cron log file, keep last 100 lines
	logFile := "/www/server/panel/logs/cron.log"
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		f.WriteString(fmt.Sprintf("[%s] START %s (manual)\n", now, name))
		f.WriteString(out + "\n")
		f.WriteString(fmt.Sprintf("[%s] END %s (exit:%d)\n", now, name, map[bool]int{true: 0, false: 1}[execErr == nil]))
		f.Close()
	}
	pruneCronLog(logFile, 100)

	return TaskResult{
		Success: execErr == nil,
		Message: fmt.Sprintf("任务 %s 执行%s", name, map[bool]string{true: "成功", false: "失败"}[execErr == nil]),
		Data:    map[string]interface{}{"output": out, "status": status, "run_at": now},
	}
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
