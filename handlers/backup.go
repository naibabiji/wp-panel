package handlers

import (
	"encoding/json"
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

type BackupHandler struct{}

var mysqlIdentifierRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func (h *BackupHandler) List(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	db := database.GetDB()
	rows, err := db.Query(`SELECT id, site_id, filename, file_size, db_name, auto, transport_status, transport_message, created_at
		FROM db_backups WHERE site_id = ? ORDER BY created_at DESC`, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询失败"))
		return
	}
	defer rows.Close()

	var backups []models.DBBackup
	for rows.Next() {
		var b models.DBBackup
		var auto int
		if rows.Scan(&b.ID, &b.SiteID, &b.Filename, &b.FileSize, &b.DBName, &auto,
			&b.TransportStatus, &b.TransportMessage, &b.CreatedAt) != nil {
			continue
		}
		b.Auto = auto == 1
		backups = append(backups, b)
	}
	if backups == nil {
		backups = []models.DBBackup{}
	}
	c.JSON(http.StatusOK, models.SuccessResponse(backups))
}

func (h *BackupHandler) Create(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}
	payload := &executor.CreateBackupPayload{Site: site, Auto: false}
	task := executor.GlobalQueue.Enqueue(executor.TaskCreateBackup, payload)
	result := <-task.ResultCh
	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": result.Message}))
	} else {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
	}
}

func (h *BackupHandler) Delete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	bid, _ := strconv.Atoi(c.Param("bid"))

	db := database.GetDB()
	var filename string
	err := db.QueryRow("SELECT filename FROM db_backups WHERE id = ? AND site_id = ?", bid, id).Scan(&filename)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("备份记录不存在"))
		return
	}
	executor.ExecuteDeleteBackup(id, filename)
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "已删除"}))
}

func (h *BackupHandler) Download(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	bid, _ := strconv.Atoi(c.Param("bid"))

	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	db := database.GetDB()
	var filename string
	err := db.QueryRow("SELECT filename FROM db_backups WHERE id = ? AND site_id = ?", bid, id).Scan(&filename)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("备份记录不存在"))
		return
	}

	backupDir := filepath.Join("/www/server/panel/backups", site.Domain, "db")
	filePath := filepath.Join(backupDir, filename)
	if _, err := os.Stat(filePath); err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("备份文件不存在"))
		return
	}
	c.FileAttachment(filePath, filename)
}

func (h *BackupHandler) Restore(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	bid, _ := strconv.Atoi(c.Param("bid"))

	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	db := database.GetDB()
	var filename string
	err := db.QueryRow("SELECT filename FROM db_backups WHERE id = ? AND site_id = ?", bid, id).Scan(&filename)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("备份记录不存在"))
		return
	}

	// 检查本地文件是否存在
	backupDir := filepath.Join("/www/server/panel/backups", site.Domain, "db")
	filePath := filepath.Join(backupDir, filename)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		var remoteEnabled int
		database.GetDB().QueryRow("SELECT enabled FROM remote_backup_settings WHERE id = 1").Scan(&remoteEnabled)
		if remoteEnabled == 1 {
			c.JSON(http.StatusNotFound, models.ErrorResponse("该备份已同步至远程服务器，本地文件已按设置删除。请从远程服务器下载后上传恢复。"))
		} else {
			c.JSON(http.StatusNotFound, models.ErrorResponse("备份文件不存在，可能已被清理"))
		}
		return
	}

	payload := &executor.RestoreBackupPayload{Site: site, Filename: filename}
	task := executor.GlobalQueue.Enqueue(executor.TaskRestoreBackup, payload)
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"async":   true,
		"message": "数据库恢复任务已开始",
		"task_id": task.ID,
		"status":  task.Status,
	}))
}

func (h *BackupHandler) UploadRestore(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("请选择备份文件"))
		return
	}
	ext := strings.ToLower(filepath.Ext(file.Filename))
	if ext != ".gz" && ext != ".sql" && ext != ".zip" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("仅支持 .sql / .sql.gz / .zip 格式"))
		return
	}

	tmpFile, err := os.CreateTemp("", "wppanel_upload_*"+ext)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("创建临时文件失败"))
		return
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	if err := c.SaveUploadedFile(file, tmpPath); err != nil {
		os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("上传失败"))
		return
	}

	payload := &executor.RestoreBackupPayload{Site: site, FilePath: tmpPath, RemoveFileAfter: true}
	task := executor.GlobalQueue.Enqueue(executor.TaskRestoreBackup, payload)
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"async":   true,
		"message": "数据库恢复任务已开始",
		"task_id": task.ID,
		"status":  task.Status,
	}))
}

func (h *BackupHandler) RestoreStatus(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	taskID := strings.TrimSpace(c.Param("task_id"))
	task, ok := executor.GlobalQueue.GetTask(taskID)
	if !ok {
		c.JSON(http.StatusNotFound, models.ErrorResponse("恢复任务不存在"))
		return
	}
	payload, ok := task.Payload.(*executor.RestoreBackupPayload)
	if !ok || task.Type != executor.TaskRestoreBackup || payload.Site == nil || payload.Site.ID != site.ID {
		c.JSON(http.StatusNotFound, models.ErrorResponse("恢复任务不存在"))
		return
	}

	message := "数据库恢复等待中"
	if task.Status == executor.TaskStatusRunning {
		message = "数据库恢复中"
	}
	success := false
	if task.Result != nil {
		message = task.Result.Message
		success = task.Result.Success
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"task_id": task.ID,
		"status":  task.Status,
		"success": success,
		"message": message,
	}))
}

func (h *BackupHandler) GetSettings(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	db := database.GetDB()
	var enabled, keepCount int
	err := db.QueryRow("SELECT enabled, keep_count FROM backup_settings WHERE site_id = ?", id).Scan(&enabled, &keepCount)
	if err != nil {
		c.JSON(http.StatusOK, models.SuccessResponse(models.BackupSettings{Enabled: false, KeepCount: 7}))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(models.BackupSettings{Enabled: enabled == 1, KeepCount: keepCount}))
}

func (h *BackupHandler) UpdateSettings(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req models.BackupSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	if req.KeepCount < 1 {
		req.KeepCount = 1
	}
	if req.KeepCount > 30 {
		req.KeepCount = 30
	}
	enabledVal := 0
	if req.Enabled {
		enabledVal = 1
	}
	db := database.GetDB()
	db.Exec(`INSERT INTO backup_settings (site_id, enabled, keep_count) VALUES (?, ?, ?)
		ON CONFLICT(site_id) DO UPDATE SET enabled = ?, keep_count = ?`,
		id, enabledVal, req.KeepCount, enabledVal, req.KeepCount)
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "设置已保存"}))
}

func (h *BackupHandler) ClearDatabase(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	if site.DBName == "" || len(site.DBName) > 64 || !mysqlIdentifierRe.MatchString(site.DBName) {
		log.Printf("拒绝清空异常数据库名 site=%d db=%q", id, site.DBName)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("数据库名异常，已拒绝执行"))
		return
	}

	dbPass := readMariaDBPassword()
	if dbPass == "" {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("无法读取数据库密码"))
		return
	}

	if err := executor.ClearDatabaseTables(site.DBName, dbPass); err != nil {
		log.Printf("清空数据库失败 site=%s: %v", site.DBName, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(err.Error()))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "数据库已清空"}))
}

func readMariaDBPassword() string {
	data, err := os.ReadFile("/www/server/panel/config.json")
	if err != nil {
		return ""
	}
	var cfg struct {
		MariaDB struct {
			RootPassword string `json:"root_password"`
		} `json:"mariadb"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.MariaDB.RootPassword == "" {
		return ""
	}
	return cfg.MariaDB.RootPassword
}
