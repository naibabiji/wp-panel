package handlers

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

type CronHandler struct{}

func (h *CronHandler) List(c *gin.Context) {
	db := database.GetDB()
	rows, err := db.Query(
		`SELECT id, name, cron_expression, command, task_type, backup_mode, keep_count, notify_fail,
		        site_id, run_as_user, enabled, running,
		        last_run_at, last_status, last_output, created_at, updated_at
		 FROM cron_jobs ORDER BY created_at DESC`,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询失败"))
		return
	}
	defer rows.Close()

	var jobs []models.CronJob
	for rows.Next() {
		var j models.CronJob
		var enabled, notifyFail, running int
		if err := rows.Scan(&j.ID, &j.Name, &j.CronExpression, &j.Command,
			&j.TaskType, &j.BackupMode, &j.KeepCount, &notifyFail,
			&j.SiteID, &j.RunAsUser, &enabled, &running, &j.LastRunAt, &j.LastStatus,
			&j.LastOutput, &j.CreatedAt, &j.UpdatedAt); err != nil {
			continue
		}
		j.Enabled = enabled == 1
		j.NotifyFail = notifyFail == 1
		j.Running = running == 1
		jobs = append(jobs, j)
	}
	if jobs == nil {
		jobs = []models.CronJob{}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(jobs))
}

func (h *CronHandler) Create(c *gin.Context) {
	var req models.CreateCronRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	db := database.GetDB()
	enabled := 1
	siteID := interface{}(nil)
	if req.SiteID != nil {
		siteID = *req.SiteID
	}

	taskType := req.TaskType
	if taskType == "" {
		taskType = "command"
	}
	if msg := validateCronInput(req.Name, req.CronExpression, req.Command, taskType, req.BackupMode, req.RunAsUser, req.SiteID); msg != "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(msg))
		return
	}
	notifyFail := 0
	if req.NotifyFail {
		notifyFail = 1
	}
	keepCount := req.KeepCount
	if keepCount <= 0 {
		keepCount = 3
	}
	_, err := db.Exec(
		`INSERT INTO cron_jobs (name, cron_expression, command, task_type, backup_mode, keep_count, notify_fail, site_id, run_as_user, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.Name, req.CronExpression, req.Command, taskType, req.BackupMode, keepCount, notifyFail, siteID, req.RunAsUser, enabled,
	)
	if err != nil {
		log.Printf("创建Cron失败: %v", err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("创建失败"))
		return
	}

	if taskType == "wp_cron" && req.SiteID != nil {
		ensureWPCronDisabled(*req.SiteID)
	}

	task := executor.GlobalQueue.Enqueue(executor.TaskRenderCron, nil)
	<-task.ResultCh

	msg := "Cron任务创建成功"
	if taskType == "wp_cron" {
		msg += "，已自动禁用 WordPress 内置伪 Cron"
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": msg}))
}

func (h *CronHandler) Update(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的任务ID"))
		return
	}

	var req models.UpdateCronRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	db := database.GetDB()
	enabled := 1
	if req.Enabled != nil {
		if !*req.Enabled {
			enabled = 0
		}
	} else {
		db.QueryRow("SELECT enabled FROM cron_jobs WHERE id = ?", id).Scan(&enabled)
	}

	taskType := req.TaskType
	if taskType == "" {
		taskType = "command"
	}
	if msg := validateCronInput(req.Name, req.CronExpression, req.Command, taskType, req.BackupMode, req.RunAsUser, req.SiteID); msg != "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(msg))
		return
	}
	notifyFail := 0
	if req.NotifyFail != nil && *req.NotifyFail {
		notifyFail = 1
	}
	keepCount := 3
	if req.KeepCount != nil && *req.KeepCount > 0 {
		keepCount = *req.KeepCount
	}

	var oldTaskType string
	var oldSiteID int
	db.QueryRow("SELECT task_type, COALESCE(site_id, 0) FROM cron_jobs WHERE id = ?", id).Scan(&oldTaskType, &oldSiteID)

	_, err = db.Exec(
		`UPDATE cron_jobs SET name = ?, cron_expression = ?, command = ?, task_type = ?,
		 backup_mode = ?, keep_count = ?, notify_fail = ?, site_id = ?,
		 run_as_user = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		req.Name, req.CronExpression, req.Command, taskType, req.BackupMode, keepCount, notifyFail, req.SiteID, req.RunAsUser, enabled, id,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("更新失败"))
		return
	}

	if oldTaskType != "wp_cron" && taskType == "wp_cron" && req.SiteID != nil {
		ensureWPCronDisabled(*req.SiteID)
	} else if oldTaskType == "wp_cron" && taskType != "wp_cron" && oldSiteID > 0 {
		removeWPCronIfLast(oldSiteID)
	}

	task := executor.GlobalQueue.Enqueue(executor.TaskRenderCron, nil)
	<-task.ResultCh

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "Cron任务已更新"}))
}

func (h *CronHandler) Delete(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的任务ID"))
		return
	}

	db := database.GetDB()
	var taskType string
	var siteID int
	db.QueryRow("SELECT task_type, COALESCE(site_id, 0) FROM cron_jobs WHERE id = ?", id).Scan(&taskType, &siteID)

	_, _ = db.Exec("DELETE FROM cron_jobs WHERE id = ?", id)

	if taskType == "wp_cron" && siteID > 0 {
		removeWPCronIfLast(siteID)
	}

	task := executor.GlobalQueue.Enqueue(executor.TaskRenderCron, nil)
	<-task.ResultCh

	msg := "Cron任务已删除"
	if taskType == "wp_cron" && siteID > 0 {
		var count int
		db.QueryRow("SELECT COUNT(*) FROM cron_jobs WHERE task_type = 'wp_cron' AND site_id = ?", siteID).Scan(&count)
		if count == 0 {
			msg += "，已恢复 WordPress 内置 Cron"
		}
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": msg}))
}

func (h *CronHandler) ViewLogs(c *gin.Context) {
	linesStr := c.DefaultQuery("lines", "100")
	lines := 100
	if n, err := strconv.Atoi(linesStr); err == nil && n > 0 && n <= 500 {
		lines = n
	}

	logFile := "/www/server/panel/logs/cron.log"
	content := tailFile(logFile, lines)
	if content == "" {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"content": "（暂无执行记录）"}))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"content": content}))
}

func (h *CronHandler) Run(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的任务ID"))
		return
	}

	db := database.GetDB()
	var name string
	db.QueryRow("SELECT name FROM cron_jobs WHERE id = ?", id).Scan(&name)

	dbResult, _ := db.Exec("UPDATE cron_jobs SET running = 1 WHERE id = ? AND running = 0", id)
	n, _ := dbResult.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusConflict, models.ErrorResponse("任务正在执行中，请稍后再试"))
		return
	}

	payload := &executor.RunCronPayload{JobID: id, Name: name}
	task := executor.GlobalQueue.Enqueue(executor.TaskRunCron, payload)
	result := <-task.ResultCh

	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": result.Message, "output": result.Data}))
	} else {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
	}
}

type systemCronEntry struct {
	Source   string `json:"source"`
	Schedule string `json:"schedule"`
	User     string `json:"user"`
	Command  string `json:"command"`
}

func (h *CronHandler) SystemList(c *gin.Context) {
	var entries []systemCronEntry

	parseCronFile := func(path, source string) {
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 6 {
				continue
			}
			user := ""
			cmdStart := 5
			if source == "/etc/crontab" || strings.HasPrefix(source, "/etc/cron.d/") {
				user = fields[5]
				cmdStart = 6
			}
			if len(fields) < cmdStart+1 {
				continue
			}
			entries = append(entries, systemCronEntry{
				Source:   source,
				Schedule: strings.Join(fields[0:5], " "),
				User:     user,
				Command:  strings.Join(fields[cmdStart:], " "),
			})
		}
	}

	parseCronFile("/etc/crontab", "/etc/crontab")

	if entries, err := os.ReadDir("/etc/cron.d"); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				path := "/etc/cron.d/" + e.Name()
				parseCronFile(path, path)
			}
		}
	}

	parseCronFile("/var/spool/cron/crontabs/root", "root crontab")

	if entries == nil {
		entries = []systemCronEntry{}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(entries))
}

func ensureWPCronDisabled(siteID int) {
	db := database.GetDB()
	var webRoot string
	err := db.QueryRow("SELECT web_root FROM websites WHERE id = ?", siteID).Scan(&webRoot)
	if err != nil {
		return
	}

	configPath := filepath.Join(webRoot, "wp-config.php")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}
	if strings.Contains(string(data), "DISABLE_WP_CRON") {
		return
	}

	insertion := "define('DISABLE_WP_CRON', true);\n"
	content := string(data)
	marker := "/* That's all, stop editing!"
	idx := strings.Index(content, marker)
	if idx < 0 {
		marker = "require_once ABSPATH . 'wp-settings.php';"
		idx = strings.Index(content, marker)
	}
	if idx > 0 {
		newContent := content[:idx] + insertion + content[idx:]
		os.WriteFile(configPath, []byte(newContent), 0644)
	}
}

func removeWPCronIfLast(siteID int) {
	db := database.GetDB()
	var count int
	db.QueryRow("SELECT COUNT(*) FROM cron_jobs WHERE task_type = 'wp_cron' AND site_id = ?", siteID).Scan(&count)
	if count > 0 {
		return
	}

	var webRoot string
	err := db.QueryRow("SELECT web_root FROM websites WHERE id = ?", siteID).Scan(&webRoot)
	if err != nil {
		return
	}

	configPath := filepath.Join(webRoot, "wp-config.php")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}
	content := string(data)
	if !strings.Contains(content, "DISABLE_WP_CRON") {
		return
	}

	lines := strings.Split(content, "\n")
	var newLines []string
	for _, line := range lines {
		if strings.Contains(line, "DISABLE_WP_CRON") {
			continue
		}
		newLines = append(newLines, line)
	}
	os.WriteFile(configPath, []byte(strings.Join(newLines, "\n")), 0644)
}

var cronFieldRe = regexp.MustCompile(`^[0-9*/,\-]+$`)
var cronUserRe = regexp.MustCompile(`^(wp|php)_[a-z0-9_]+$`)

func validateCronInput(name, expr, command, taskType, backupMode, runAsUser string, siteID *int) string {
	if hasLineBreak(name) || strings.ContainsAny(name, "\"`$\\") {
		return "任务名称不能包含换行或特殊字符"
	}
	if !validCronExpression(expr) {
		return "Cron 表达式格式不正确"
	}
	switch taskType {
	case "command":
		if strings.TrimSpace(command) == "" {
			return "请输入要执行的命令"
		}
		if hasLineBreak(command) {
			return "命令不能包含换行"
		}
	case "file_backup":
		if siteID == nil || *siteID <= 0 {
			return "请选择要备份的网站"
		}
		if backupMode != "" && backupMode != "full" && backupMode != "incremental" {
			return "备份模式不正确"
		}
	case "wp_cron":
		if siteID == nil || *siteID <= 0 {
			return "请选择要调用 WP Cron 的网站"
		}
		if hasLineBreak(command) {
			return "WP Cron 目标不能包含换行"
		}
		if !executor.IsValidDomain(command) {
			return "WP Cron 目标必须是有效域名"
		}
	default:
		return "任务类型不正确"
	}
	if runAsUser != "" {
		if !cronUserRe.MatchString(runAsUser) {
			return "运行用户不正确"
		}
		var count int
		database.GetDB().QueryRow("SELECT COUNT(*) FROM websites WHERE system_user = ?", runAsUser).Scan(&count)
		if count == 0 {
			return "运行用户不属于任何站点"
		}
	}
	return ""
}

func validCronExpression(expr string) bool {
	if hasLineBreak(expr) {
		return false
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i, field := range fields {
		if !validCronField(field, ranges[i][0], ranges[i][1]) {
			return false
		}
	}
	return true
}

func validCronField(field string, min, max int) bool {
	if field == "" || !cronFieldRe.MatchString(field) || strings.Contains(field, "//") {
		return false
	}
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return false
		}
		base := part
		if strings.Contains(part, "/") {
			parts := strings.Split(part, "/")
			if len(parts) != 2 || parts[1] == "" {
				return false
			}
			step, err := strconv.Atoi(parts[1])
			if err != nil || step < 1 || step > max {
				return false
			}
			base = parts[0]
		}
		if base == "*" {
			continue
		}
		if strings.Contains(base, "-") {
			parts := strings.Split(base, "-")
			if len(parts) != 2 {
				return false
			}
			start, err1 := strconv.Atoi(parts[0])
			end, err2 := strconv.Atoi(parts[1])
			if err1 != nil || err2 != nil || start < min || end > max || start > end {
				return false
			}
			continue
		}
		value, err := strconv.Atoi(base)
		if err != nil || value < min || value > max {
			return false
		}
	}
	return true
}

func hasLineBreak(s string) bool {
	return strings.ContainsAny(s, "\r\n")
}
