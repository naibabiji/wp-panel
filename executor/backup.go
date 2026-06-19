package executor

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
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

	if err := dumpDatabaseToGzip(site.DBName, dbPass, filePath); err != nil {
		return TaskResult{Success: false, Message: "备份失败: " + err.Error()}
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
	if dbPass == "" {
		return TaskResult{Success: false, Message: "无法读取 MariaDB root 密码"}
	}

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
	if payload.RemoveFileAfter {
		defer os.Remove(filePath)
	}

	if err := validateRestoreBackupFile(filePath); err != nil {
		return TaskResult{Success: false, Message: "恢复文件校验失败: " + err.Error()}
	}
	if err := ClearDatabaseTables(site.DBName, dbPass); err != nil {
		return TaskResult{Success: false, Message: "清空数据库失败: " + err.Error()}
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
	f, err := os.Open(filePath)
	if err != nil {
		log.Printf("读取备份文件失败: %v", err)
		return TaskResult{Success: false, Message: "读取备份文件失败"}
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		log.Printf("恢复失败 gzip: %v", err)
		return TaskResult{Success: false, Message: "恢复失败，gzip 文件损坏或格式不正确"}
	}
	defer gz.Close()
	return restoreSQLReader(gz, dbName, dbPass)
}

func restoreFromSql(filePath, dbName, dbPass string) TaskResult {
	f, err := os.Open(filePath)
	if err != nil {
		log.Printf("读取备份文件失败: %v", err)
		return TaskResult{Success: false, Message: "读取备份文件失败"}
	}
	defer f.Close()
	return restoreSQLReader(f, dbName, dbPass)
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

	return restoreSQLReader(rc, dbName, dbPass)
}

func restoreSQLReader(r io.Reader, dbName, dbPass string) TaskResult {
	cmd := exec.Command("mysql", "-u", "root", dbName)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+dbPass)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return TaskResult{Success: false, Message: fmt.Sprintf("创建导入管道失败: %v", err)}
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		log.Printf("恢复失败 mysql start: %v", err)
		return TaskResult{Success: false, Message: "恢复失败，mysql 启动失败"}
	}

	if _, err := io.WriteString(stdin, "SET FOREIGN_KEY_CHECKS=0;\n"); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return TaskResult{Success: false, Message: "恢复失败，初始化 mysql 导入失败: " + err.Error()}
	}
	copyErr := writeSanitizedRestoreSQL(stdin, r)
	if copyErr == nil {
		_, copyErr = io.WriteString(stdin, "\nSET FOREIGN_KEY_CHECKS=1;\n")
	}
	closeErr := stdin.Close()
	if copyErr != nil {
		waitErr := cmd.Wait()
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			log.Printf("恢复失败 mysql pipe: %v; mysql: %s", copyErr, msg)
			return TaskResult{Success: false, Message: "恢复失败，mysql 导入错误: " + msg}
		}
		if waitErr != nil {
			log.Printf("恢复失败 mysql pipe: %v; wait: %v", copyErr, waitErr)
			return TaskResult{Success: false, Message: "恢复失败，mysql 导入中断: " + waitErr.Error()}
		}
		return TaskResult{Success: false, Message: "恢复失败，写入 mysql 中断: " + copyErr.Error()}
	}
	if closeErr != nil {
		waitErr := cmd.Wait()
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			log.Printf("恢复失败 mysql close: %v; mysql: %s", closeErr, msg)
			return TaskResult{Success: false, Message: "恢复失败，mysql 导入错误: " + msg}
		}
		if waitErr != nil {
			return TaskResult{Success: false, Message: "恢复失败，mysql 导入中断: " + waitErr.Error()}
		}
		return TaskResult{Success: false, Message: "恢复失败，写入 mysql 失败: " + closeErr.Error()}
	}
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		log.Printf("恢复失败 mysql: %v: %s", err, msg)
		return TaskResult{Success: false, Message: "恢复失败，mysql 导入错误: " + msg}
	}
	return TaskResult{Success: true, Message: "数据库恢复成功"}
}

func validateRestoreBackupFile(filePath string) error {
	ext := strings.ToLower(filepath.Ext(filePath))
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("文件不存在或不可读取")
	}
	if info.IsDir() || info.Size() == 0 {
		return fmt.Errorf("文件为空或格式不正确")
	}

	switch ext {
	case ".sql":
		f, err := os.Open(filePath)
		if err != nil {
			return fmt.Errorf("读取 SQL 文件失败")
		}
		defer f.Close()
		return validateRestoreSQL(f)
	case ".gz":
		f, err := os.Open(filePath)
		if err != nil {
			return fmt.Errorf("读取 gzip 文件失败")
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("gzip 文件损坏或格式不正确")
		}
		defer gz.Close()
		return validateRestoreSQL(gz)
	case ".zip":
		zr, err := zip.OpenReader(filePath)
		if err != nil {
			return fmt.Errorf("zip 文件损坏或格式不正确")
		}
		defer zr.Close()
		var sqlFile *zip.File
		for _, f := range zr.File {
			if !f.FileInfo().IsDir() && strings.HasSuffix(strings.ToLower(f.Name), ".sql") {
				sqlFile = f
				break
			}
		}
		if sqlFile == nil {
			return fmt.Errorf("zip 文件中未找到 .sql 文件")
		}
		rc, err := sqlFile.Open()
		if err != nil {
			return fmt.Errorf("读取 zip 内 SQL 文件失败")
		}
		defer rc.Close()
		return validateRestoreSQL(rc)
	default:
		return fmt.Errorf("不支持的备份文件格式")
	}
}

func validateRestoreSQL(r io.Reader) error {
	const maxStatementPrefix = 128
	buf := make([]byte, 32*1024)
	sawCreateTable := false
	statementPrefix := ""
	inStatement := false
	inSingleQuote := false
	inDoubleQuote := false
	inBacktick := false
	escaped := false

	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			for _, b := range []byte(chunk) {
				if inSingleQuote || inDoubleQuote {
					if escaped {
						escaped = false
						continue
					}
					if b == '\\' {
						escaped = true
						continue
					}
					if inSingleQuote && b == '\'' {
						inSingleQuote = false
					}
					if inDoubleQuote && b == '"' {
						inDoubleQuote = false
					}
					continue
				}
				if inBacktick {
					if b == '`' {
						inBacktick = false
					}
					continue
				}

				if !inStatement {
					if b == '\r' || b == '\n' || b == '\t' || b == ' ' {
						continue
					}
					inStatement = true
					statementPrefix = ""
				}
				if len(statementPrefix) < maxStatementPrefix {
					statementPrefix += string(b)
					if isCreateTableStatement(statementPrefix) {
						sawCreateTable = true
					}
				}
				if b == '\'' {
					inSingleQuote = true
					continue
				}
				if b == '"' {
					inDoubleQuote = true
					continue
				}
				if b == '`' {
					inBacktick = true
					continue
				}
				if b == ';' {
					inStatement = false
					statementPrefix = ""
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("SQL 文件读取失败")
		}
	}

	if !sawCreateTable {
		return fmt.Errorf("未找到建表语句")
	}
	return nil
}

func writeSanitizedRestoreSQL(dst io.Writer, src io.Reader) error {
	const maxStatementPrefix = 256
	buf := make([]byte, 32*1024)
	prefix := ""
	held := make([]byte, 0, maxStatementPrefix)
	inStatement := false
	skipStatement := false
	decisionMade := false
	inSingleQuote := false
	inDoubleQuote := false
	inBacktick := false
	escaped := false

	flushHeld := func() error {
		if len(held) == 0 {
			return nil
		}
		_, err := dst.Write(held)
		held = held[:0]
		return err
	}

	for {
		n, err := src.Read(buf)
		if n > 0 {
			for _, b := range buf[:n] {
				outsideString := !inSingleQuote && !inDoubleQuote && !inBacktick
				if outsideString && !inStatement {
					if b == '\r' || b == '\n' || b == '\t' || b == ' ' {
						if _, err := dst.Write([]byte{b}); err != nil {
							return err
						}
						continue
					}
					inStatement = true
					skipStatement = false
					decisionMade = false
					prefix = ""
					held = held[:0]
				}

				if inStatement && outsideString && !decisionMade && len(prefix) < maxStatementPrefix {
					prefix += string(b)
					held = append(held, b)
					if skip, decided := restoreStatementDecision(prefix); decided {
						decisionMade = true
						skipStatement = skip
						if !skipStatement {
							if err := flushHeld(); err != nil {
								return err
							}
						}
						if skipStatement && b == ';' {
							inStatement = false
							skipStatement = false
							decisionMade = false
							prefix = ""
							held = held[:0]
						}
						continue
					}
					if len(prefix) >= maxStatementPrefix {
						decisionMade = true
						if err := flushHeld(); err != nil {
							return err
						}
					}
					continue
				}

				if !skipStatement {
					if err := flushHeld(); err != nil {
						return err
					}
					if _, err := dst.Write([]byte{b}); err != nil {
						return err
					}
				}

				if inSingleQuote || inDoubleQuote {
					if escaped {
						escaped = false
					} else if b == '\\' {
						escaped = true
					} else if inSingleQuote && b == '\'' {
						inSingleQuote = false
					} else if inDoubleQuote && b == '"' {
						inDoubleQuote = false
					}
				} else if inBacktick {
					if b == '`' {
						inBacktick = false
					}
				} else {
					if b == '\'' {
						inSingleQuote = true
					} else if b == '"' {
						inDoubleQuote = true
					} else if b == '`' {
						inBacktick = true
					} else if b == ';' {
						inStatement = false
						skipStatement = false
						decisionMade = false
						prefix = ""
						held = held[:0]
					}
				}
			}
		}
		if err == io.EOF {
			if !skipStatement {
				if err := flushHeld(); err != nil {
					return err
				}
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func restoreStatementDecision(prefix string) (skip, decided bool) {
	if strings.Contains(strings.ToUpper(prefix), "DEFINER=") {
		return true, true
	}
	normalized := normalizeSQLPrefix(prefix)
	if normalized == "" {
		return false, false
	}
	fields := strings.Fields(normalized)
	if len(fields) == 0 {
		return false, false
	}
	switch fields[0] {
	case "USE":
		return true, true
	case "CREATE":
		if len(fields) < 2 {
			return false, false
		}
		if fields[1] == "DATABASE" || strings.HasPrefix(fields[1], "DEFINER=") {
			return true, true
		}
		if strings.HasPrefix("DATABASE", fields[1]) || strings.HasPrefix("DEFINER=", fields[1]) {
			return false, false
		}
		return false, true
	case "DROP":
		if len(fields) < 2 {
			return false, false
		}
		if strings.HasPrefix("DATABASE", fields[1]) && fields[1] != "DATABASE" {
			return false, false
		}
		return fields[1] == "DATABASE", true
	default:
		if !strings.ContainsAny(prefix, " \t\r\n") {
			if strings.HasPrefix("USE", fields[0]) || strings.HasPrefix("CREATE", fields[0]) || strings.HasPrefix("DROP", fields[0]) {
				return false, false
			}
		}
		return false, true
	}
}

func isCreateTableStatement(prefix string) bool {
	return strings.HasPrefix(normalizeSQLPrefix(prefix), "CREATE TABLE")
}

func normalizeSQLPrefix(s string) string {
	s = strings.ToUpper(s)
	for {
		start := strings.Index(s, "/*")
		if start < 0 {
			break
		}
		end := strings.Index(s[start+2:], "*/")
		if end < 0 {
			s = s[:start]
			break
		}
		s = s[:start] + " " + s[start+2+end+2:]
	}
	for {
		start := strings.Index(s, "--")
		if start < 0 {
			break
		}
		end := strings.IndexByte(s[start:], '\n')
		if end < 0 {
			s = s[:start]
			break
		}
		s = s[:start] + " " + s[start+end+1:]
	}
	for {
		start := strings.Index(s, "#")
		if start < 0 {
			break
		}
		end := strings.IndexByte(s[start:], '\n')
		if end < 0 {
			s = s[:start]
			break
		}
		s = s[:start] + " " + s[start+end+1:]
	}
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// ClearDatabaseTables 清空指定数据库中的所有表（保留数据库本身）
func ClearDatabaseTables(dbName, dbPass string) error {
	if !isValidMySQLIdentifier(dbName) {
		return fmt.Errorf("invalid database name")
	}
	if dbPass == "" {
		return fmt.Errorf("无法读取数据库密码")
	}

	cmd := exec.Command("mysql", "-u", "root", "-B", "-N", "-e",
		fmt.Sprintf("SELECT CONCAT('DROP TABLE IF EXISTS `', REPLACE(TABLE_NAME, '`', '``'), '`;') FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = '%s' AND TABLE_TYPE = 'BASE TABLE'", dbName))
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+dbPass)
	dropSQL, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("获取表列表失败: %s", string(dropSQL))
	}

	mysqlCmd := exec.Command("mysql", "-u", "root", dbName)
	mysqlCmd.Env = append(os.Environ(), "MYSQL_PWD="+dbPass)
	stdin, err := mysqlCmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("准备数据库操作失败")
	}
	var stderr bytes.Buffer
	mysqlCmd.Stderr = &stderr
	if err := mysqlCmd.Start(); err != nil {
		return fmt.Errorf("启动数据库操作失败")
	}
	fmt.Fprintf(stdin, "SET FOREIGN_KEY_CHECKS = 0;\n%s\nSET FOREIGN_KEY_CHECKS = 1;\n", string(dropSQL))
	stdin.Close()
	if err := mysqlCmd.Wait(); err != nil {
		return fmt.Errorf("清空数据库失败: %s", stderr.String())
	}
	return nil
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

		if err := dumpDatabaseToGzip(dbName, dbPass, filePath); err != nil {
			log.Printf("自动备份失败 [%s]: %v", domain, err)
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

func dumpDatabaseToGzip(dbName, dbPass, filePath string) error {
	if !isValidMySQLIdentifier(dbName) {
		return fmt.Errorf("数据库名异常，已拒绝执行")
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0700); err != nil {
		return fmt.Errorf("创建备份目录失败: %w", err)
	}

	outFile, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("创建备份文件失败: %w", err)
	}
	keepFile := false
	fileClosed := false
	defer func() {
		if !fileClosed {
			outFile.Close()
		}
		if !keepFile {
			os.Remove(filePath)
		}
	}()

	cmd := exec.Command("mysqldump", "-u", "root", dbName)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+dbPass)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("准备导出失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 mysqldump 失败: %w", err)
	}

	gz := gzip.NewWriter(outFile)
	copyErr := error(nil)
	if _, err := io.Copy(gz, stdout); err != nil {
		copyErr = err
	}
	closeGzipErr := gz.Close()
	closeFileErr := outFile.Close()
	fileClosed = true
	waitErr := cmd.Wait()

	if copyErr != nil {
		return fmt.Errorf("写入备份失败: %w", copyErr)
	}
	if closeGzipErr != nil {
		return fmt.Errorf("完成 gzip 写入失败: %w", closeGzipErr)
	}
	if closeFileErr != nil {
		return fmt.Errorf("保存备份文件失败: %w", closeFileErr)
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		return fmt.Errorf("mysqldump 失败: %s", msg)
	}

	keepFile = true
	return nil
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

// StartDBBackupScheduler 面板 SQLite 数据库自动备份调度器（每天凌晨 2:30）
func StartDBBackupScheduler() {
	go func() {
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), 2, 30, 0, 0, now.Location())
			if now.After(next) {
				next = next.Add(24 * time.Hour)
			}
			time.Sleep(next.Sub(now))
			autoBackupPanelDB()
		}
	}()
}

func autoBackupPanelDB() {
	cfg := config.AppConfig
	backupDir := filepath.Join(cfg.Panel.BackupDir, "panel-db")

	path, err := database.BackupDatabase(backupDir)
	if err != nil {
		log.Printf("面板数据库自动备份失败: %v", err)
		return
	}
	if err := database.VerifyDBBackup(path); err != nil {
		os.Remove(path)
		log.Printf("面板数据库自动备份校验失败，已删除无效备份: %v", err)
		return
	}
	log.Printf("面板数据库自动备份完成: %s", path)

	if removed := database.CleanupOldDBBackups(backupDir, 7); removed > 0 {
		log.Printf("面板数据库备份清理: 删除 %d 个旧备份", removed)
	}
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
