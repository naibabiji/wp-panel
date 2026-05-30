package executor

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"log"
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
	if dbPass == "" {
		return TaskResult{Success: false, Message: "无法读取 MariaDB root 密码"}
	}

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
	if _, err := db.Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name, auto) VALUES (?, ?, ?, ?, ?)`,
		site.ID, filename, size, site.DBName, autoVal); err != nil {
		log.Printf("备份记录写入 db_backups 失败 [%s]: %v", site.Domain, err)
		os.Remove(filePath)
		return TaskResult{Success: false, Message: "备份记录保存失败: " + err.Error()}
	}

	SyncBackupToRemote(filePath)

	cleanupOldBackups(site.ID, site.Domain, getKeepCount(site.ID))

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
	var pipeErr error
	mysql.Stdin, pipeErr = gunzip.StdoutPipe()
	if pipeErr != nil {
		return TaskResult{Success: false, Message: fmt.Sprintf("创建管道失败: %v", pipeErr)}
	}
	if err := mysql.Start(); err != nil {
		log.Printf("恢复失败: %v", err)
		return TaskResult{Success: false, Message: "恢复失败"}
	}
	if err := gunzip.Run(); err != nil {
		mysql.Process.Kill()
		mysql.Wait()
		log.Printf("恢复失败 gunzip: %v", err)
		return TaskResult{Success: false, Message: "恢复失败，数据库可能已被部分修改，请清空数据库后重新恢复"}
	}
	if err := mysql.Wait(); err != nil {
		log.Printf("恢复失败 mysql: %v", err)
		return TaskResult{Success: false, Message: "恢复失败"}
	}
	return TaskResult{Success: true, Message: "数据库恢复成功"}
}

func restoreFromSql(filePath, dbName, dbPass string) TaskResult {
	cmd := exec.Command("mysql", "-u", "root", dbName)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+dbPass)
	f, err := os.Open(filePath)
	if err != nil {
		log.Printf("读取备份文件失败: %v", err)
		return TaskResult{Success: false, Message: "读取备份文件失败"}
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
		log.Printf("解压 zip 失败: %v", err)
		return TaskResult{Success: false, Message: "解压 zip 失败"}
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
		log.Printf("读取 zip 内文件失败: %v", err)
		return TaskResult{Success: false, Message: "读取 zip 内文件失败"}
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
		log.Printf("自动备份: 查询 backup_settings 失败: %v", err)
		return
	}
	type backupTask struct {
		siteID    int
		keepCount int
		domain    string
		dbName    string
	}
	var tasks []backupTask
	for rows.Next() {
		var t backupTask
		if rows.Scan(&t.siteID, &t.keepCount, &t.domain, &t.dbName) == nil {
			tasks = append(tasks, t)
		}
	}
	rows.Close()

	dbPass := readMariaDBPassword()
	if dbPass == "" {
		log.Printf("自动备份: 无法读取 MariaDB root 密码，跳过")
		return
	}

	count := 0
	failCount := 0
	for _, t := range tasks {
		siteID := t.siteID
		keepCount := t.keepCount
		domain := t.domain
		dbName := t.dbName

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
			log.Printf("自动备份失败 [%s]: %s", domain, string(out))
			failCount++
			continue
		}

		info, _ := os.Stat(filePath)
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		if _, err = db.Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name, auto) VALUES (?, ?, ?, ?, 1)`,
			siteID, filename, size, dbName); err != nil {
			log.Printf("自动备份: 记录写入 db_backups 失败 [%s]: %v", domain, err)
			os.Remove(filePath)
			failCount++
			continue
		}

		SyncBackupToRemote(filePath)
		if keepCount <= 0 {
			keepCount = 7
		}
		cleanupOldBackups(siteID, domain, keepCount)
		count++
	}
	log.Printf("自动备份完成: 成功 %d, 失败 %d", count, failCount)
}

func getKeepCount(siteID int) int {
	var kc int
	if database.GetDB().QueryRow("SELECT keep_count FROM backup_settings WHERE site_id = ?", siteID).Scan(&kc) != nil || kc <= 0 {
		return 7
	}
	return kc
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
	type oldBackup struct {
		id       int
		filename string
	}
	var backups []oldBackup
	for rows.Next() {
		var b oldBackup
		if rows.Scan(&b.id, &b.filename) == nil {
			backups = append(backups, b)
		}
	}
	rows.Close()

	for _, b := range backups {
		filePath := filepath.Join(cfg.Panel.BackupDir, domain, "db", b.filename)
		os.Remove(filePath)
		db.Exec("DELETE FROM db_backups WHERE id = ?", b.id)
	}
}

// RunAutoBackup 手动触发一次自动备份，用于测试验证。
func RunAutoBackup() {
	log.Println("手动触发自动备份...")
	executeAutoBackups()
}

func StartAutoBackupScheduler() {
	go func() {
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), 4, 0, 0, 0, now.Location())
			if now.After(next) {
				next = next.Add(24 * time.Hour)
			}
			log.Printf("自动备份调度: 下次执行时间 %s", next.Format("2006-01-02 15:04:05"))
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
