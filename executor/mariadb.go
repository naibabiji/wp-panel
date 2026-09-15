package executor

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

var mysqlIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func isValidMySQLIdentifier(name string) bool {
	return name != "" && len(name) <= 64 && mysqlIdentifierPattern.MatchString(name)
}

func wpOptionsTableName(tablePrefix string) (string, error) {
	tablePrefix = strings.TrimSpace(tablePrefix)
	if !IsValidWPTablePrefix(tablePrefix) {
		return "", fmt.Errorf("invalid WordPress table prefix")
	}
	tableName := tablePrefix + "options"
	if !isValidMySQLIdentifier(tableName) {
		return "", fmt.Errorf("invalid WordPress options table name")
	}
	return tableName, nil
}

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

func runMySQLInput(rootPassword, input string, args ...string) error {
	cmd := exec.Command("mysql", args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+rootPassword)
	cmd.Stdin = strings.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mysql: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

func createMigrationMariaDBDatabase(dbName, dbUser, dbPassword string, cfg *config.Config) error {
	if cfg == nil || !isValidMySQLIdentifier(dbName) || !isValidMySQLIdentifier(dbUser) || len(dbPassword) < 24 || !mysqlIdentifierPattern.MatchString(dbPassword) {
		return fmt.Errorf("invalid migration database identity")
	}
	sqlText := fmt.Sprintf("CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\nDROP USER IF EXISTS '%s'@'localhost';\nCREATE USER '%s'@'localhost' IDENTIFIED BY '%s';\nGRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost';\nFLUSH PRIVILEGES;\n", dbName, dbUser, dbUser, dbPassword, dbName, dbUser)
	return runMySQLInput(cfg.MariaDB.RootPassword, sqlText, "-u", cfg.MariaDB.RootUser)
}

// GetMariaDBDatabaseSizes returns the on-disk table and index size for each
// non-system database. The caller may filter the result to panel-managed sites.
func GetMariaDBDatabaseSizes(cfg *config.Config) (map[string]int64, error) {
	if cfg == nil {
		return nil, fmt.Errorf("panel config is not initialized")
	}
	query := `SELECT TABLE_SCHEMA, COALESCE(SUM(DATA_LENGTH + INDEX_LENGTH), 0)
		FROM INFORMATION_SCHEMA.TABLES
		WHERE TABLE_SCHEMA NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
		GROUP BY TABLE_SCHEMA`
	cmd := exec.Command("mysql", "-u", cfg.MariaDB.RootUser, "-B", "-N", "-e", query)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.MariaDB.RootPassword)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("query database sizes: %s", strings.TrimSpace(stderr.String()))
	}

	sizes := make(map[string]int64)
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(parts) != 2 || parts[0] == "" {
			continue
		}
		size, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil {
			continue
		}
		sizes[parts[0]] = size
	}
	return sizes, nil
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
	return nil
}

type dbPasswordConfigWriter func(string, []byte, os.FileMode) error
type mariaDBPasswordChanger func(string, string, *config.Config) error

func applyWordPressDBPasswordChange(configPath string, oldContent, newContent []byte, site *models.Website, newPassword string, cfg *config.Config, writeConfig dbPasswordConfigWriter, changePassword mariaDBPasswordChanger) TaskResult {
	if err := writeConfig(configPath, newContent, 0600); err != nil {
		log.Printf("更新 wp-config.php 失败: %v", err)
		return TaskResult{Success: false, Message: "更新 wp-config.php 失败"}
	}

	if err := changePassword(site.DBUser, newPassword, cfg); err != nil {
		if rollbackErr := writeConfig(configPath, oldContent, 0600); rollbackErr != nil {
			log.Printf("MariaDB 操作失败，且 wp-config.php 恢复失败: database_error=%v rollback_error=%v", err, rollbackErr)
			return TaskResult{Success: false, Message: "数据库密码修改失败，且 wp-config.php 恢复失败，请立即检查网站数据库配置"}
		}
		log.Printf("MariaDB 操作失败，wp-config.php 已恢复: %v", err)
		return TaskResult{Success: false, Message: "MariaDB 操作失败"}
	}

	masked := maskPassword(newPassword)
	return TaskResult{
		Success: true,
		Message: "数据库密码已更新",
		Data:    map[string]interface{}{"new_password": masked},
	}
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

	result := applyWordPressDBPasswordChange(configPath, content, []byte(newContent), site, newPassword, cfg, os.WriteFile, changeMariaDBPassword)
	if result.Success {
		database.GetDB().Exec("UPDATE websites SET updated_at = CURRENT_TIMESTAMP WHERE id = ?", site.ID)
	}
	return result
}

// DetectDBTablePrefix 查询数据库中实际的 WordPress 表前缀
// 返回推荐前缀、所有候选前缀列表、错误
func DetectDBTablePrefix(dbName string, cfg *config.Config) (string, []string, error) {
	if !isValidMySQLIdentifier(dbName) {
		return "", nil, fmt.Errorf("invalid database name")
	}
	cmd := exec.Command("mysql", "-u", cfg.MariaDB.RootUser, "-N", "-e",
		fmt.Sprintf("SHOW TABLES FROM `%s` LIKE '%%options'", dbName))
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.MariaDB.RootPassword)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", nil, fmt.Errorf("查询失败: %s", strings.TrimSpace(stderr.String()))
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		return "", nil, fmt.Errorf("未找到 options 表，数据库可能为空或不是 WordPress 数据库")
	}

	prefixSet := make(map[string]struct{})
	for _, line := range strings.Split(output, "\n") {
		tableName := strings.TrimSpace(line)
		if prefix, ok := tablePrefixFromOptionsTable(tableName); ok {
			prefixSet[prefix] = struct{}{}
		}
	}

	if len(prefixSet) == 0 {
		return "", nil, fmt.Errorf("无法解析表前缀")
	}

	var candidates []string
	for p := range prefixSet {
		candidates = append(candidates, p)
	}
	sort.Strings(candidates)

	return candidates[0], candidates, nil
}

func tablePrefixFromOptionsTable(tableName string) (string, bool) {
	if !strings.HasSuffix(tableName, "options") {
		return "", false
	}
	prefix := strings.TrimSuffix(tableName, "options")
	if !IsValidWPTablePrefix(prefix) {
		return "", false
	}
	return prefix, true
}

// ReadWPSiteURLs 从 wp_options 读取 siteurl 和 home
func ReadWPSiteURLs(dbName, tablePrefix string, cfg *config.Config) (siteURL, homeURL string, err error) {
	if !isValidMySQLIdentifier(dbName) {
		return "", "", fmt.Errorf("invalid database name")
	}
	tableName, err := wpOptionsTableName(tablePrefix)
	if err != nil {
		return "", "", err
	}
	query := fmt.Sprintf(
		"SELECT option_name, option_value FROM `%s`.`%s` WHERE option_name IN ('siteurl','home')",
		dbName, tableName)
	cmd := exec.Command("mysql", "-u", cfg.MariaDB.RootUser, "-N", "-e", query)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.MariaDB.RootPassword)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("查询失败: %s", strings.TrimSpace(stderr.String()))
	}

	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "siteurl":
			siteURL = parts[1]
		case "home":
			homeURL = parts[1]
		}
	}
	return siteURL, homeURL, nil
}

func ReadWPDiagnosticOptions(dbName, tablePrefix string, cfg *config.Config) (map[string]string, error) {
	if cfg == nil {
		return nil, fmt.Errorf("面板配置未初始化")
	}
	if !isValidMySQLIdentifier(dbName) {
		return nil, fmt.Errorf("invalid database name")
	}
	tableName, err := wpOptionsTableName(tablePrefix)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(
		"SELECT option_name, option_value FROM `%s`.`%s` WHERE option_name IN ('template','stylesheet','active_plugins')",
		dbName, tableName)
	cmd := exec.Command("mysql", "-u", cfg.MariaDB.RootUser, "-N", "-e", query)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.MariaDB.RootPassword)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("查询失败: %s", strings.TrimSpace(stderr.String()))
	}

	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "template", "stylesheet", "active_plugins":
			result[parts[0]] = parts[1]
		}
	}
	return result, nil
}

// UpdateWPSiteURLs 更新 wp_options 中的 siteurl 和 home（仅更新非空字段）
func UpdateWPSiteURLs(dbName, tablePrefix, newSiteURL, newHomeURL string, cfg *config.Config) error {
	if newSiteURL == "" && newHomeURL == "" {
		return fmt.Errorf("至少需要提供一个 URL")
	}
	if !isValidMySQLIdentifier(dbName) {
		return fmt.Errorf("invalid database name")
	}
	tableName, err := wpOptionsTableName(tablePrefix)
	if err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("面板配置未初始化")
	}
	currentSiteURL, currentHomeURL, err := ReadWPSiteURLs(dbName, tablePrefix, cfg)
	if err != nil {
		return err
	}
	if currentSiteURL == "" || currentHomeURL == "" {
		return fmt.Errorf("WordPress siteurl 或 home 记录不存在")
	}
	if newSiteURL == "" {
		newSiteURL = currentSiteURL
	}
	if newHomeURL == "" {
		newHomeURL = currentHomeURL
	}

	escSiteURL := strings.ReplaceAll(newSiteURL, "'", "''")
	escHomeURL := strings.ReplaceAll(newHomeURL, "'", "''")
	query := fmt.Sprintf(
		"UPDATE `%s`.`%s` SET option_value = CASE option_name WHEN 'siteurl' THEN '%s' WHEN 'home' THEN '%s' END WHERE option_name IN ('siteurl','home')",
		dbName, tableName, escSiteURL, escHomeURL)
	if err := runMySQL(cfg.MariaDB.RootPassword, "-u", cfg.MariaDB.RootUser, "-e", query); err != nil {
		return fmt.Errorf("更新 WordPress 站点 URL 失败: %w", err)
	}
	actualSiteURL, actualHomeURL, err := ReadWPSiteURLs(dbName, tablePrefix, cfg)
	if err != nil {
		return fmt.Errorf("核对 WordPress 站点 URL 失败: %w", err)
	}
	if actualSiteURL != newSiteURL || actualHomeURL != newHomeURL {
		return fmt.Errorf("WordPress 站点 URL 核对失败")
	}
	return nil
}

func maskPassword(pw string) string {
	if len(pw) < 8 {
		return "****"
	}
	return pw[:4] + "****" + pw[len(pw)-4:]
}
