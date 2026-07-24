package executor

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// WordPress 密码找回保护模式。
//   - allow：默认值，不限制任何用户通过“忘记密码”找回密码。
//   - all：全站禁用，禁止所有用户找回密码，并隐藏登录页的“忘记密码？”链接。
//   - admin：仅禁用管理员账户找回密码，其余用户不受影响。
const (
	PasswordResetModeAllow = "allow"
	PasswordResetModeAll   = "all"
	PasswordResetModeAdmin = "admin"
)

// wpPanelPasswordResetMarker 标记该 mu-plugin 文件由面板全权托管，
// 面板会在 allow 模式下删除它、在其它模式下覆盖它，用户不应手动修改。
const wpPanelPasswordResetMarker = "WP-PANEL-MANAGED:password-reset"

const wpPasswordResetPluginFile = "wp-panel-password-reset.php"

// ValidatePasswordResetMode 校验并返回合法的密码找回模式，非法值返回错误。
func ValidatePasswordResetMode(mode string) (string, error) {
	switch mode {
	case PasswordResetModeAllow, PasswordResetModeAll, PasswordResetModeAdmin:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid password reset mode: %q", mode)
	}
}

// ApplyWPPasswordResetMode 根据模式管理站点 wp-content/mu-plugins 下的托管 mu-plugin：
//   - allow：删除托管文件（若存在），恢复 WordPress 默认行为。
//   - all / admin：写入对应逻辑的 mu-plugin，并确保文件归属站点用户。
//
// mu-plugins 目录在需要时自动创建；webRoot 必须非空且指向真实站点根目录。
func ApplyWPPasswordResetMode(webRoot, systemUser, mode string) error {
	webRoot = strings.TrimSpace(webRoot)
	if webRoot == "" {
		return fmt.Errorf("web root is empty")
	}
	mode, err := ValidatePasswordResetMode(mode)
	if err != nil {
		return err
	}

	muDir := filepath.Join(webRoot, "wp-content", "mu-plugins")
	pluginPath := filepath.Join(muDir, wpPasswordResetPluginFile)

	if mode == PasswordResetModeAllow {
		if _, statErr := os.Stat(pluginPath); statErr == nil {
			if rmErr := os.Remove(pluginPath); rmErr != nil {
				return fmt.Errorf("remove password reset mu-plugin: %w", rmErr)
			}
		}
		return nil
	}

	if err := os.MkdirAll(muDir, 0755); err != nil {
		return fmt.Errorf("create mu-plugins dir: %w", err)
	}
	content := renderWPPasswordResetPlugin(mode)
	if err := os.WriteFile(pluginPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write password reset mu-plugin: %w", err)
	}

	systemUser = strings.TrimSpace(systemUser)
	if systemUser != "" {
		// mu-plugin 文件必须归属站点用户，否则以站点用户运行的 PHP-FPM 无法
		// 读取它，表现为"面板保存成功但实际不生效"。chown 失败应直接报错，
		// 而不是静默吞掉让运维难以排查。
		if chErr := ChownSitePath(pluginPath, webRoot, systemUser); chErr != nil {
			return fmt.Errorf("chown password reset mu-plugin to %s: %w", systemUser, chErr)
		}
		if chErr := ChownSitePath(muDir, webRoot, systemUser); chErr != nil {
			log.Printf("chown mu-plugins dir failed (webRoot=%s): %v", webRoot, chErr)
		}
	}
	return nil
}

// renderWPPasswordResetPlugin 生成带托管标记的 mu-plugin PHP 内容。
// 全站禁用模式额外在登录页注入 CSS 隐藏“忘记密码？”链接（p#nav 同时包含
// 该链接与“返回站点”链接，全站禁用场景下一并隐藏是可接受的）。
func renderWPPasswordResetPlugin(mode string) string {
	var body string
	switch mode {
	case PasswordResetModeAll:
		body = `// 禁止所有用户通过“忘记密码”找回/重置密码（retrieve_password 在发邮件前即中止）。
add_filter('allow_password_reset', '__return_false');

// 隐藏登录页“忘记密码？”链接，避免暴露该功能入口。
add_action('login_head', function () {
    echo '<style>p#nav { display:none !important; }</style>';
});`
	case PasswordResetModeAdmin:
		body = `// 仅禁止具有 administrator 角色的用户找回/重置密码；其余用户不受影响。
add_filter('allow_password_reset', function ($allow, $user_id) {
    $user = get_userdata($user_id);
    if ($user && $user->has_cap('administrator')) {
        return false;
    }
    return $allow;
}, 10, 2);`
	}
	return fmt.Sprintf(`<?php
// %s
// 由 WP Panel 托管：密码找回保护（mode: %s）。请勿手动修改，面板会覆盖本文件。

%s
`, wpPanelPasswordResetMarker, mode, body)
}
