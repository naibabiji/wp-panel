package executor

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func executeCreateBackup(task *Task) TaskResult {
	payload, ok := task.Payload.(*CreateBackupPayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}

	site := payload.Site
	cfg := config.AppConfig

	backupDir := filepath.Join(cfg.Panel.BackupDir, site.Domain, "db")
	os.MkdirAll(backupDir, 0700)

	ts := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("%s_%s.sql.gz", site.Domain, ts)
	filePath := filepath.Join(backupDir, filename)

	dbPass := readMariaDBPassword()

	cmd := exec.Command("bash", "-c",
		fmt.Sprintf("mysqldump -u root %s | gzip > %s", site.DBName, filePath))
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+dbPass)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return TaskResult{Success: false, Message: fmt.Sprintf("备份失败: %s", string(out))}
	}

	info, _ := os.Stat(filePath)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}

	db := database.GetDB()
	autoVal := 0
	if payload.Auto {
		autoVal = 1
	}
	db.Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name, auto) VALUES (?, ?, ?, ?, ?)`,
		site.ID, filename, size, site.DBName, autoVal)

	go SyncBackupToRemote(filePath)

	return TaskResult{Success: true, Message: "备份完成: " + filename}
}

func executeRestoreBackup(task *Task) TaskResult {
	payload, ok := task.Payload.(*RestoreBackupPayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}

	site := payload.Site
	dbPass := readMariaDBPassword()

	var filePath string
	if payload.FilePath != "" {
		cleanPath := filepath.Clean(payload.FilePath)
		if !strings.HasPrefix(cleanPath, "/tmp/") {
			return TaskResult{Success: false, Message: "恢复失败: 文件路径不合法"}
		}
		filePath = cleanPath
	} else {
		cfg := config.AppConfig
		backupDir := filepath.Join(cfg.Panel.BackupDir, site.Domain, "db")
		filePath = filepath.Join(backupDir, payload.Filename)
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	if ext == ".gz" {
		return restoreFromGz(filePath, site.DBName, dbPass)
	}
	if ext == ".sql" {
		return restoreFromSql(filePath, site.DBName, dbPass)
	}
	if ext == ".zip" {
		return restoreFromZip(filePath, site.DBName, dbPass)
	}
	return TaskResult{Success: false, Message: "不支持的备份文件格式"}
}

func restoreFromGz(filePath, dbName, dbPass string) TaskResult {
	gunzip := exec.Command("gunzip", "-c", filePath)
	mysql := exec.Command("mysql", "-u", "root", dbName)
	mysql.Env = append(os.Environ(), "MYSQL_PWD="+dbPass)
	mysql.Stdin, _ = gunzip.StdoutPipe()
	if err := mysql.Start(); err != nil {
		return TaskResult{Success: false, Message: fmt.Sprintf("恢复失败: %v", err)}
	}
	if err := gunzip.Run(); err != nil {
		return TaskResult{Success: false, Message: fmt.Sprintf("恢复失败: gunzip %v", err)}
	}
	if err := mysql.Wait(); err != nil {
		return TaskResult{Success: false, Message: fmt.Sprintf("恢复失败: mysql %v", err)}
	}
	return TaskResult{Success: true, Message: "数据库恢复成功"}
}

func restoreFromSql(filePath, dbName, dbPass string) TaskResult {
	cmd := exec.Command("mysql", "-u", "root", dbName)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+dbPass)
	f, err := os.Open(filePath)
	if err != nil {
		return TaskResult{Success: false, Message: fmt.Sprintf("读取文件失败: %v", err)}
	}
	defer f.Close()
	cmd.Stdin = f
	out, err := cmd.CombinedOutput()
	if err != nil {
		return TaskResult{Success: false, Message: fmt.Sprintf("恢复失败: %s", string(out))}
	}
	return TaskResult{Success: true, Message: "数据库恢复成功"}
}

func restoreFromZip(filePath, dbName, dbPass string) TaskResult {
	r, err := zip.OpenReader(filePath)
	if err != nil {
		return TaskResult{Success: false, Message: fmt.Sprintf("解压 zip 失败: %v", err)}
	}
	defer r.Close()

	var sqlFile *zip.File
	for _, f := range r.File {
		if !f.FileInfo().IsDir() && strings.HasSuffix(strings.ToLower(f.Name), ".sql") {
			sqlFile = f
			break
		}
	}
	if sqlFile == nil {
		return TaskResult{Success: false, Message: "zip 文件中未找到 .sql 文件"}
	}

	rc, err := sqlFile.Open()
	if err != nil {
		return TaskResult{Success: false, Message: fmt.Sprintf("读取 zip 内文件失败: %v", err)}
	}
	defer rc.Close()

	cmd := exec.Command("mysql", "-u", "root", dbName)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+dbPass)
	cmd.Stdin = rc
	out, err := cmd.CombinedOutput()
	if err != nil {
		return TaskResult{Success: false, Message: fmt.Sprintf("恢复失败: %s", string(out))}
	}
	return TaskResult{Success: true, Message: "数据库恢复成功"}
}

func ExecuteDeleteBackup(siteID int, filename string) error {
	cfg := config.AppConfig
	backupDir := filepath.Join(cfg.Panel.BackupDir, getSiteDomain(siteID), "db")
	filePath := filepath.Join(backupDir, filename)
	os.Remove(filePath)
	db := database.GetDB()
	db.Exec("DELETE FROM db_backups WHERE site_id = ? AND filename = ?", siteID, filename)
	return nil
}

func executeAutoBackups() {
	db := database.GetDB()
	cfg := config.AppConfig

	rows, err := db.Query(`SELECT bs.site_id, bs.keep_count, w.domain, w.db_name FROM backup_settings bs
		JOIN websites w ON w.id = bs.site_id WHERE bs.enabled = 1`)
	if err != nil {
		return
	}
	defer rows.Close()

	dbPass := readMariaDBPassword()

	for rows.Next() {
		var siteID, keepCount int
		var domain, dbName string
		if rows.Scan(&siteID, &keepCount, &domain, &dbName) != nil {
			continue
		}

		backupDir := filepath.Join(cfg.Panel.BackupDir, domain, "db")
		os.MkdirAll(backupDir, 0700)

		ts := time.Now().Format("20060102_150405")
		filename := fmt.Sprintf("%s_%s.sql.gz", domain, ts)
		filePath := filepath.Join(backupDir, filename)

		cmd := exec.Command("bash", "-c",
			fmt.Sprintf("mysqldump -u root %s | gzip > %s", dbName, filePath))
		cmd.Env = append(os.Environ(), "MYSQL_PWD="+dbPass)
		out, err := cmd.CombinedOutput()
		if err != nil {
			continue
		}
		_ = out

		info, _ := os.Stat(filePath)
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		db.Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name, auto) VALUES (?, ?, ?, ?, 1)`,
			siteID, filename, size, dbName)

			go SyncBackupToRemote(filePath)
		cleanupOldBackups(siteID, domain, keepCount)
	}
}

func cleanupOldBackups(siteID int, domain string, keepCount int) {
	db := database.GetDB()
	cfg := config.AppConfig

	var total int
	db.QueryRow("SELECT COUNT(*) FROM db_backups WHERE site_id = ?", siteID).Scan(&total)
	if total <= keepCount {
		return
	}

	rows, err := db.Query(`SELECT id, filename FROM db_backups WHERE site_id = ? ORDER BY created_at ASC LIMIT ?`,
		siteID, total-keepCount)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		var filename string
		if rows.Scan(&id, &filename) != nil {
			continue
		}
		filePath := filepath.Join(cfg.Panel.BackupDir, domain, "db", filename)
		os.Remove(filePath)
		db.Exec("DELETE FROM db_backups WHERE id = ?", id)
	}
}

func StartAutoBackupScheduler() {
	go func() {
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 4, 0, 0, 0, now.Location())
			time.Sleep(next.Sub(now))
			executeAutoBackups()
		}
	}()
}

func getSiteDomain(siteID int) string {
	var domain string
	database.GetDB().QueryRow("SELECT domain FROM websites WHERE id = ?", siteID).Scan(&domain)
	return domain
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
