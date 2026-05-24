package executor

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func createMariaDBDatabase(dbName, dbUser, dbPassword string, cfg *config.Config) error {
	cred := fmt.Sprintf("-u%s", cfg.MariaDB.RootUser)
	passArg := fmt.Sprintf("-p%s", cfg.MariaDB.RootPassword)

	_, _ = executeCommand("mysql", cred, passArg, "-e",
		fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", dbName))

	_, err := executeCommand("mysql", cred, passArg, "-e",
		fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY '%s'", dbUser, dbPassword))
	if err != nil {
		return err
	}

	_, err = executeCommand("mysql", cred, passArg, "-e",
		fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost'", dbName, dbUser))
	if err != nil {
		return err
	}

	_, err = executeCommand("mysql", cred, passArg, "-e", "FLUSH PRIVILEGES")
	return err
}

func dropMariaDBDatabase(dbName, dbUser string, cfg *config.Config) error {
	cred := fmt.Sprintf("-u%s", cfg.MariaDB.RootUser)
	passArg := fmt.Sprintf("-p%s", cfg.MariaDB.RootPassword)

	executeCommand("mysql", cred, passArg, "-e", fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
	executeCommand("mysql", cred, passArg, "-e", fmt.Sprintf("DROP USER IF EXISTS '%s'@'localhost'", dbUser))
	executeCommand("mysql", cred, passArg, "-e", "FLUSH PRIVILEGES")
	return nil
}

func changeMariaDBPassword(dbUser, newPassword string, cfg *config.Config) error {
	cred := fmt.Sprintf("-u%s", cfg.MariaDB.RootUser)
	passArg := fmt.Sprintf("-p%s", cfg.MariaDB.RootPassword)

	_, err := executeCommand("mysql", cred, passArg, "-e",
		fmt.Sprintf("ALTER USER '%s'@'localhost' IDENTIFIED BY '%s'", dbUser, newPassword))
	if err != nil {
		return fmt.Errorf("修改数据库密码失败: %w", err)
	}

	_, err = executeCommand("mysql", cred, passArg, "-e", "FLUSH PRIVILEGES")
	return err
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

	if err := changeMariaDBPassword(site.DBUser, newPassword, cfg); err != nil {
		log.Printf("MariaDB 操作失败: %v", err)
		return TaskResult{Success: false, Message: "MariaDB 操作失败"}
	}

	if site.SiteType == "php" {
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
		fmt.Sprintf("define('DB_PASSWORD', '%s')", newPassword))

	if newContent == string(content) {
		return TaskResult{Success: false, Message: "未找到 DB_PASSWORD 定义，wp-config.php 可能格式异常"}
	}

	if err := os.WriteFile(configPath, []byte(newContent), 0644); err != nil {
		log.Printf("更新 wp-config.php 失败: %v", err)
		return TaskResult{Success: false, Message: "更新 wp-config.php 失败"}
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
