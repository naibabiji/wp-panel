package handlers

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
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
		`SELECT id, name, cron_expression, command, task_type, backup_mode, notify_fail,
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
			&j.TaskType, &j.BackupMode, &notifyFail,
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
	notifyFail := 0
	if req.NotifyFail {
		notifyFail = 1
	}
	_, err := db.Exec(
		`INSERT INTO cron_jobs (name, cron_expression, command, task_type, backup_mode, notify_fail, site_id, run_as_user, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.Name, req.CronExpression, req.Command, taskType, req.BackupMode, notifyFail, siteID, req.RunAsUser, enabled,
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
	if req.Enabled != nil && !*req.Enabled {
		enabled = 0
	}

	taskType := req.TaskType
	if taskType == "" {
		taskType = "command"
	}
	notifyFail := 0
	if req.NotifyFail != nil && *req.NotifyFail {
		notifyFail = 1
	}

	_, err = db.Exec(
		`UPDATE cron_jobs SET name = ?, cron_expression = ?, command = ?, task_type = ?,
		 backup_mode = ?, notify_fail = ?, site_id = ?,
		 run_as_user = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		req.Name, req.CronExpression, req.Command, taskType, req.BackupMode, notifyFail, req.SiteID, req.RunAsUser, enabled, id,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("更新失败"))
		return
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
	data, err := os.ReadFile(logFile)
	if err != nil {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"content": "（暂无执行记录）"}))
		return
	}

	allLines := strings.Split(string(data), "\n")
	if len(allLines) > lines {
		allLines = allLines[len(allLines)-lines:]
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"content": strings.Join(allLines, "\n")}))
}

func (h *CronHandler) Run(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的任务ID"))
		return
	}

	db := database.GetDB()
	var running int
	db.QueryRow("SELECT running FROM cron_jobs WHERE id = ?", id).Scan(&running)
	if running == 1 {
		c.JSON(http.StatusConflict, models.ErrorResponse("任务正在执行中，请稍后再试"))
		return
	}

	db.Exec("UPDATE cron_jobs SET running = 1 WHERE id = ?", id)

	payload := &executor.RunCronPayload{JobID: id}
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
