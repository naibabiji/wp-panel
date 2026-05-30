package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func phpSingleQuoteEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	return s
}

func FixWPConfigCredentials(webRoot, dbName, dbUser string) error {
	configPath := filepath.Join(webRoot, "wp-config.php")
	content, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("读取 wp-config.php 失败: %w", err)
	}
	text := string(content)

	reName := regexp.MustCompile(`define\(\s*'DB_NAME'\s*,\s*'[^']*'\s*\)`)
	nameUpdated := reName.ReplaceAllString(text, fmt.Sprintf("define('DB_NAME', '%s')", phpSingleQuoteEscape(dbName)))
	if nameUpdated == text {
		return fmt.Errorf("未找到 DB_NAME 定义，wp-config.php 可能格式异常")
	}

	reUser := regexp.MustCompile(`define\(\s*'DB_USER'\s*,\s*'[^']*'\s*\)`)
	userUpdated := reUser.ReplaceAllString(nameUpdated, fmt.Sprintf("define('DB_USER', '%s')", phpSingleQuoteEscape(dbUser)))
	if userUpdated == nameUpdated {
		return fmt.Errorf("未找到 DB_USER 定义，wp-config.php 可能格式异常")
	}

	if err := os.WriteFile(configPath, []byte(userUpdated), 0600); err != nil {
		return fmt.Errorf("写入 wp-config.php 失败: %w", err)
	}
	return nil
}

func generateWPConfig(webRoot, dbName, dbUser, dbPassword string) error {
	salts, err := generateWPSalts()
	if err != nil {
		salts = fallbackSalts()
	}

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
$table_prefix = 'wp_';

/**
 * 调试模式
 *
 * 开发时建议开启，生产环境应保持关闭。
 * 如需启用，改为 true。
 */
define('WP_DEBUG', false);

/* Add any custom values between this line and the "stop editing" line. */



/* That's all, stop editing! Happy publishing. */

/** WordPress 目录的绝对路径 */
if (!defined('ABSPATH')) {
    define('ABSPATH', __DIR__ . '/');
}

/** 加载 WordPress 设置和引入文件 */
require_once ABSPATH . 'wp-settings.php';
`, phpSingleQuoteEscape(dbName), phpSingleQuoteEscape(dbUser), phpSingleQuoteEscape(dbPassword), salts)

	configPath := filepath.Join(webRoot, "wp-config.php")
	return os.WriteFile(configPath, []byte(config), 0600)
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
