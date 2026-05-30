package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"

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
	result := <-task.ResultCh
	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": result.Message}))
	} else {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
	}
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
	ext := filepath.Ext(file.Filename)
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
	defer os.Remove(tmpPath)

	payload := &executor.RestoreBackupPayload{Site: site, FilePath: tmpPath}
	task := executor.GlobalQueue.Enqueue(executor.TaskRestoreBackup, payload)
	result := <-task.ResultCh
	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": result.Message}))
	} else {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
	}
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

	dbPass := readMariaDBPassword()
	if dbPass == "" {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("无法读取数据库密码"))
		return
	}

	if site.DBName == "" || len(site.DBName) > 64 || !mysqlIdentifierRe.MatchString(site.DBName) {
		log.Printf("拒绝清空异常数据库名 site=%d db=%q", id, site.DBName)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("数据库名异常，已拒绝执行"))
		return
	}

	cmd := exec.Command("mysql", "-u", "root", "-B", "-N", "-e",
		fmt.Sprintf("SELECT CONCAT('DROP TABLE IF EXISTS `', REPLACE(TABLE_NAME, '`', '``'), '`;') FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = '%s' AND TABLE_TYPE = 'BASE TABLE'", site.DBName))
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+dbPass)
	dropSQL, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("获取表列表失败 site=%s: %s", site.DBName, string(dropSQL))
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("获取表列表失败"))
		return
	}

	mysqlCmd := exec.Command("mysql", "-u", "root", site.DBName)
	mysqlCmd.Env = append(os.Environ(), "MYSQL_PWD="+dbPass)
	stdin, err := mysqlCmd.StdinPipe()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("准备数据库操作失败"))
		return
	}
	var stderr bytes.Buffer
	mysqlCmd.Stderr = &stderr
	if err := mysqlCmd.Start(); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("启动数据库操作失败"))
		return
	}
	fmt.Fprintf(stdin, "SET FOREIGN_KEY_CHECKS = 0;\n%s\nSET FOREIGN_KEY_CHECKS = 1;\n", string(dropSQL))
	stdin.Close()
	if err := mysqlCmd.Wait(); err != nil {
		log.Printf("清空数据库失败 site=%s: %s", site.DBName, stderr.String())
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("清空数据库失败"))
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
