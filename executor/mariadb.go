package executor

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func runMySQL(rootPassword string, args ...string) error {
	cmd := exec.Command("mysql", args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+rootPassword)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mysql: %s", stderr.String())
	}
	return nil
}

func createMariaDBDatabase(dbName, dbUser, dbPassword string, cfg *config.Config) error {
	dbUser = strings.ReplaceAll(dbUser, "'", "''")
	dbPassword = strings.ReplaceAll(dbPassword, "'", "''")

	if err := runMySQL(cfg.MariaDB.RootPassword, "-u", cfg.MariaDB.RootUser, "-e",
		fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", dbName)); err != nil {
		return err
	}

	// DROP + CREATE 确保密码始终一致，避免 IF NOT EXISTS 下旧用户密码不更新的问题
	runMySQL(cfg.MariaDB.RootPassword, "-u", cfg.MariaDB.RootUser, "-e",
		fmt.Sprintf("DROP USER IF EXISTS '%s'@'localhost'", dbUser))
	if err := runMySQL(cfg.MariaDB.RootPassword, "-u", cfg.MariaDB.RootUser, "-e",
		fmt.Sprintf("CREATE USER '%s'@'localhost' IDENTIFIED BY '%s'", dbUser, dbPassword)); err != nil {
		return err
	}

	if err := runMySQL(cfg.MariaDB.RootPassword, "-u", cfg.MariaDB.RootUser, "-e",
		fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost'", dbName, dbUser)); err != nil {
		return err
	}

	return runMySQL(cfg.MariaDB.RootPassword, "-u", cfg.MariaDB.RootUser, "-e", "FLUSH PRIVILEGES")
}

func dropMariaDBDatabase(dbName, dbUser string, cfg *config.Config) error {
	if err := runMySQL(cfg.MariaDB.RootPassword, "-u", cfg.MariaDB.RootUser, "-e", fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName)); err != nil {
		return err
	}
	if err := runMySQL(cfg.MariaDB.RootPassword, "-u", cfg.MariaDB.RootUser, "-e", fmt.Sprintf("DROP USER IF EXISTS '%s'@'localhost'", dbUser)); err != nil {
		return err
	}
	return runMySQL(cfg.MariaDB.RootPassword, "-u", cfg.MariaDB.RootUser, "-e", "FLUSH PRIVILEGES")
}

func changeMariaDBPassword(dbUser, newPassword string, cfg *config.Config) error {
	newPassword = strings.ReplaceAll(newPassword, "'", "''")

	if err := runMySQL(cfg.MariaDB.RootPassword, "-u", cfg.MariaDB.RootUser, "-e",
		fmt.Sprintf("ALTER USER '%s'@'localhost' IDENTIFIED BY '%s'", dbUser, newPassword)); err != nil {
		return fmt.Errorf("修改数据库密码失败: %w", err)
	}

	return runMySQL(cfg.MariaDB.RootPassword, "-u", cfg.MariaDB.RootUser, "-e", "FLUSH PRIVILEGES")
}

func executeChangeDBPassword(task *Task) TaskResult {
	payload, ok := task.Payload.(*ChangeDBPasswordPayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}

	site := payload.Site
	cfg := config.AppConfig

	newPassword := payload.NewPassword
	if newPassword == "" {
		newPassword = generatePassword(24)
	}

	if site.SiteType == "php" {
		if err := changeMariaDBPassword(site.DBUser, newPassword, cfg); err != nil {
			log.Printf("MariaDB 操作失败: %v", err)
			return TaskResult{Success: false, Message: "MariaDB 操作失败"}
		}
		db := database.GetDB()
		db.Exec("UPDATE websites SET updated_at = CURRENT_TIMESTAMP WHERE id = ?", site.ID)
		masked := maskPassword(newPassword)
		return TaskResult{
			Success: true,
			Message: "数据库密码已更新",
			Data:    map[string]interface{}{"new_password": masked},
		}
	}

	configPath := filepath.Join(site.WebRoot, "wp-config.php")
	content, err := os.ReadFile(configPath)
	if err != nil {
		log.Printf("读取 wp-config.php 失败: %v", err)
		return TaskResult{Success: false, Message: "读取 wp-config.php 失败"}
	}

	re := regexp.MustCompile(`define\(\s*'DB_PASSWORD'\s*,\s*'[^']*'\s*\)`)
	newContent := re.ReplaceAllString(string(content),
		fmt.Sprintf("define('DB_PASSWORD', '%s')", phpSingleQuoteEscape(newPassword)))

	if newContent == string(content) {
		return TaskResult{Success: false, Message: "未找到 DB_PASSWORD 定义，wp-config.php 可能格式异常"}
	}

	if err := os.WriteFile(configPath, []byte(newContent), 0600); err != nil {
		log.Printf("更新 wp-config.php 失败: %v", err)
		return TaskResult{Success: false, Message: "更新 wp-config.php 失败"}
	}

	if err := changeMariaDBPassword(site.DBUser, newPassword, cfg); err != nil {
		os.WriteFile(configPath, content, 0600)
		log.Printf("MariaDB 操作失败，已回滚 wp-config.php: %v", err)
		return TaskResult{Success: false, Message: "MariaDB 操作失败"}
	}

	masked := maskPassword(newPassword)

	db := database.GetDB()
	db.Exec("UPDATE websites SET updated_at = CURRENT_TIMESTAMP WHERE id = ?", site.ID)

	return TaskResult{
		Success: true,
		Message: "数据库密码已更新",
		Data:    map[string]interface{}{"new_password": masked},
	}
}

func maskPassword(pw string) string {
	if len(pw) < 8 {
		return "****"
	}
	return pw[:4] + "****" + pw[len(pw)-4:]
}
