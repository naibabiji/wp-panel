package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/naibabiji/wp-panel/database"
)

const rateLimitConfPath = "/etc/nginx/conf.d/wppanel-ratelimit.conf"

func EnsureRateLimit(enabled bool, rpm, burst int) error {
	if !enabled {
		os.Remove(rateLimitConfPath)
		exec.Command("nginx", "-s", "reload").Run()
		return nil
	}

	content := fmt.Sprintf(`# WP Panel — 请求频率限制
# 已登录 WordPress 用户不限速（检测 wordpress_logged_in cookie）
map $http_cookie $wp_rate_limit_key {
    ~*wordpress_logged_in "";
    default $binary_remote_addr;
}

limit_req_zone $wp_rate_limit_key zone=wp_req_limit:10m rate=%dr/m;
`, rpm)

	if err := os.WriteFile(rateLimitConfPath, []byte(content), 0644); err != nil {
		return err
	}

	injectRateLimitToSites(burst)
	exec.Command("nginx", "-s", "reload").Run()
	return nil
}

func injectRateLimitToSites(burst int) {
	sitesDir := "/etc/nginx/sites-available"
	entries, err := os.ReadDir(sitesDir)
	if err != nil {
		return
	}

	burstStr := strconv.Itoa(burst)
	limitLine := "    limit_req zone=wp_req_limit burst=" + burstStr + " nodelay;"
	statusLine := "    limit_req_status 429;"

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		configPath := filepath.Join(sitesDir, entry.Name())
		data, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}
		content := string(data)

		if strings.Contains(content, "limit_req zone=wp_req_limit") {
			serverCount := strings.Count(content, "server_name")
			limitCount := strings.Count(content, "limit_req zone=wp_req_limit")
			if serverCount == limitCount {
				updateRateLimitLine(configPath, content, burstStr)
				continue
			}
		}

		// Strip existing limit_req lines, then inject fresh after every server_name
		lines := strings.Split(content, "\n")
		var cleaned []string
		for _, line := range lines {
			if !strings.Contains(line, "limit_req zone=wp_req_limit") &&
				!strings.Contains(line, "limit_req_status 429") {
				cleaned = append(cleaned, line)
			}
		}
		var result []string
		for _, line := range cleaned {
			result = append(result, line)
			if strings.Contains(line, "server_name") {
				result = append(result, limitLine, statusLine)
			}
		}
		os.WriteFile(configPath, []byte(strings.Join(result, "\n")), 0644)
	}
}

func updateRateLimitLine(path, content, burst string) {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.Contains(line, "limit_req zone=wp_req_limit") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + "limit_req zone=wp_req_limit burst=" + burst + " nodelay;"
		}
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
}

func GetRateLimitSettings() (enabled bool, rpm int, burst int) {
	db := database.GetDB()
	if db == nil {
		return true, 60, 30
	}

	var sEnabled, sRPM, sBurst string
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'rate_limit_enabled'`).Scan(&sEnabled)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'rate_limit_rpm'`).Scan(&sRPM)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'rate_limit_burst'`).Scan(&sBurst)

	enabled = sEnabled != "false"
	rpm = parseIntOr(sRPM, 60)
	burst = parseIntOr(sBurst, 200)
	return
}
