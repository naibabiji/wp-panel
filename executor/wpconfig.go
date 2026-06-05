package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var wpTablePrefixPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func phpSingleQuoteEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	return s
}

func IsValidWPTablePrefix(prefix string) bool {
	return prefix != "" && len(prefix) <= 56 && wpTablePrefixPattern.MatchString(prefix)
}

func FixWPConfigCredentials(webRoot, domain, dbName, dbUser, tablePrefix string) error {
	configPath := filepath.Join(webRoot, "wp-config.php")
	content, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("读取 wp-config.php 失败 (路径: %s): %w", configPath, err)
	}
	text := string(content)
	result := text

	// 替换 DB_NAME（支持单引号和双引号格式）
	reNameSingle := regexp.MustCompile(`define\(\s*'DB_NAME'\s*,\s*'[^']*'\s*\)`)
	reNameDouble := regexp.MustCompile(`define\(\s*"DB_NAME"\s*,\s*"[^"]*"\s*\)`)
	if reNameSingle.MatchString(result) {
		result = reNameSingle.ReplaceAllString(result, fmt.Sprintf("define('DB_NAME', '%s')", phpSingleQuoteEscape(dbName)))
	} else if reNameDouble.MatchString(result) {
		result = reNameDouble.ReplaceAllString(result, fmt.Sprintf("define('DB_NAME', '%s')", phpSingleQuoteEscape(dbName)))
	} else {
		return fmt.Errorf("未找到 DB_NAME 定义 (路径: %s)，wp-config.php 可能格式异常或使用了非常规引号", configPath)
	}

	// 替换 DB_USER（支持单引号和双引号格式）
	reUserSingle := regexp.MustCompile(`define\(\s*'DB_USER'\s*,\s*'[^']*'\s*\)`)
	reUserDouble := regexp.MustCompile(`define\(\s*"DB_USER"\s*,\s*"[^"]*"\s*\)`)
	if reUserSingle.MatchString(result) {
		result = reUserSingle.ReplaceAllString(result, fmt.Sprintf("define('DB_USER', '%s')", phpSingleQuoteEscape(dbUser)))
	} else if reUserDouble.MatchString(result) {
		result = reUserDouble.ReplaceAllString(result, fmt.Sprintf("define('DB_USER', '%s')", phpSingleQuoteEscape(dbUser)))
	} else {
		return fmt.Errorf("未找到 DB_USER 定义 (路径: %s)，wp-config.php 可能格式异常或使用了非常规引号", configPath)
	}

	// 如果提供了 tablePrefix，替换 $table_prefix
	if tablePrefix != "" {
		tablePrefix = strings.TrimSpace(tablePrefix)
		if !IsValidWPTablePrefix(tablePrefix) {
			return fmt.Errorf("invalid WordPress table prefix")
		}
		var replaced bool
		result, replaced = replaceWPTablePrefix(result, tablePrefix)
		if !replaced {
			return fmt.Errorf("未找到 table_prefix 定义")
		}
	}

	result, _ = ensureWPConfigCachePrefixes(result, wpCacheKeySalt(domain))

	if err := os.WriteFile(configPath, []byte(result), 0600); err != nil {
		return fmt.Errorf("写入 wp-config.php 失败: %w", err)
	}
	return nil
}

func generateWPConfig(webRoot, domain, dbName, dbUser, dbPassword string) error {
	salts, err := generateWPSalts()
	if err != nil {
		salts = fallbackSalts()
	}
	cacheSalt := wpCacheKeySalt(domain)
	tablePrefix := generateWPTablePrefix()

	config := fmt.Sprintf(`<?php
/**
 * WordPress 基础配置文件（由 WP Panel 自动生成）
 *
 * 此文件包含数据库连接信息和安全密钥。
 * 如需添加自定义配置，请插入到下方 "/* Add any custom values" 标记行之后。
 *
 * @package WordPress
 */

// ** 数据库设置 — 请勿修改以下内容（面板自动管理） ** //
define('DB_NAME', '%s');
define('DB_USER', '%s');
define('DB_PASSWORD', '%s');
define('DB_HOST', 'localhost');
define('DB_CHARSET', 'utf8mb4');
define('DB_COLLATE', '');

/**#@+
 * 身份验证唯一密钥和盐值
 *
 * 每个站点使用独立的随机密钥，由 WordPress.org 密钥生成服务提供。
 * 如需要可手动替换为自定义值。
 */
%s
/**#@-*/

/**
 * WordPress 数据库表前缀
 *
 * 如需在一个数据库中安装多个 WordPress，可为每个站点设置不同的前缀。
 * 只允许数字、字母和下划线。
 */
$table_prefix = '%s';

/**
 * 调试模式
 *
 * 开发时建议开启，生产环境应保持关闭。
 * 如需启用，改为 true。
 */
define('WP_DEBUG', false);

/* Add any custom values between this line and the "stop editing" line. */

define('WP_REDIS_PREFIX', '%s');
define('WP_CACHE_KEY_SALT', '%s');


/* That's all, stop editing! Happy publishing. */

/** WordPress 目录的绝对路径 */
if (!defined('ABSPATH')) {
    define('ABSPATH', __DIR__ . '/');
}

/** 加载 WordPress 设置和引入文件 */
require_once ABSPATH . 'wp-settings.php';
`, phpSingleQuoteEscape(dbName), phpSingleQuoteEscape(dbUser), phpSingleQuoteEscape(dbPassword), salts, phpSingleQuoteEscape(tablePrefix), phpSingleQuoteEscape(cacheSalt), phpSingleQuoteEscape(cacheSalt))

	configPath := filepath.Join(webRoot, "wp-config.php")
	return os.WriteFile(configPath, []byte(config), 0600)
}

func replaceWPTablePrefix(content, tablePrefix string) (string, bool) {
	stmt := fmt.Sprintf("$table_prefix = '%s';", phpSingleQuoteEscape(tablePrefix))
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)\$table_prefix\s*=\s*'[^']*'\s*;`),
		regexp.MustCompile(`(?m)\$table_prefix\s*=\s*"[^"]*"\s*;`),
	} {
		if re.MatchString(content) {
			return re.ReplaceAllLiteralString(content, stmt), true
		}
	}

	// Repair configs damaged by regexp replacement treating "$table_prefix"
	// as a replacement variable and leaving only " = 'prefix';".
	if strings.Contains(content, "WordPress 数据库表前缀") ||
		strings.Contains(content, "WordPress database table prefix") {
		reBroken := regexp.MustCompile(`(?m)^\s*=\s*['"][^'"]*['"]\s*;`)
		if reBroken.MatchString(content) {
			return reBroken.ReplaceAllLiteralString(content, stmt), true
		}
	}

	return content, false
}

func ReadWPTablePrefix(webRoot string) (string, error) {
	configPath := filepath.Join(webRoot, "wp-config.php")
	content, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	prefix, ok := extractWPTablePrefix(string(content))
	if !ok {
		return "", fmt.Errorf("未找到 table_prefix 定义")
	}
	prefix = strings.TrimSpace(prefix)
	if !IsValidWPTablePrefix(prefix) {
		return "", fmt.Errorf("invalid WordPress table prefix")
	}
	return prefix, nil
}

func extractWPTablePrefix(content string) (string, bool) {
	for _, pattern := range []string{
		`(?m)\$table_prefix\s*=\s*'([^']*)'\s*;`,
		`(?m)\$table_prefix\s*=\s*"([^"]*)"\s*;`,
	} {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(content)
		if len(matches) == 2 {
			return matches[1], true
		}
	}
	return "", false
}

func wpCacheKeySalt(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		domain = "wp-panel-site"
	}
	return domain + ":"
}

func generateWPTablePrefix() string {
	return "wp_" + generatePassword(8) + "_"
}

func ensureWPConfigCachePrefixes(content, prefix string) (string, bool) {
	updated := content
	insertedAny := false
	for _, name := range []string{"WP_REDIS_PREFIX", "WP_CACHE_KEY_SALT"} {
		var inserted bool
		updated, inserted = ensureWPConfigStringConstant(updated, name, prefix)
		insertedAny = insertedAny || inserted
	}
	return updated, insertedAny
}

func ensureWPConfigStringConstant(content, name, value string) (string, bool) {
	re := constPattern(name)
	if re.MatchString(content) {
		return content, false
	}
	stmt := fmt.Sprintf("define('%s', '%s');\n", name, phpSingleQuoteEscape(value))
	return insertBeforeMarker(content, stmt), true
}

func generateWPSalts() (string, error) {
	resp, err := executeCommand("curl", "-s", "-f", "-L", "https://api.wordpress.org/secret-key/1.1/salt/")
	if err != nil {
		return "", err
	}
	return resp, nil
}

func fallbackSalts() string {
	keys := []string{
		"AUTH_KEY", "SECURE_AUTH_KEY", "LOGGED_IN_KEY", "NONCE_KEY",
		"AUTH_SALT", "SECURE_AUTH_SALT", "LOGGED_IN_SALT", "NONCE_SALT",
	}
	var lines []string
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("define('%s', '%s');", key, generatePassword(64)))
	}
	return strings.Join(lines, "\n")
}
