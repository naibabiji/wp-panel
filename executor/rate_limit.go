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

const (
	rateLimitConfPath        = "/etc/nginx/conf.d/wppanel-ratelimit.conf"
	botRateLimitConfPath     = "/etc/nginx/conf.d/wppanel-botlimit.conf"
	limitReqStatusConfPath   = "/etc/nginx/conf.d/wppanel-limit-status.conf"
	defaultBotRateLimitRPM   = 30
	defaultBotRateLimitBurst = 20
)

func ApplyRateLimitSettings() error {
	ipEnabled, ipRPM, ipBurst := GetRateLimitSettings()
	botEnabled, botRPM, botBurst := GetBotRateLimitSettings()
	return ensureCombinedRateLimits(ipEnabled, ipRPM, ipBurst, botEnabled, botRPM, botBurst)
}

func EnsureLimitReqStatus() error {
	backups := backupRateLimitFiles()
	if err := writeLimitReqStatusConfig(); err != nil {
		return err
	}
	return testAndReloadNginx(backups)
}

func EnsureRateLimit(enabled bool, rpm, burst int) error {
	botEnabled, botRPM, botBurst := GetBotRateLimitSettings()
	return ensureCombinedRateLimits(enabled, rpm, burst, botEnabled, botRPM, botBurst)
}

func EnsureBotRateLimit(enabled bool, rpm, burst int) error {
	ipEnabled, ipRPM, ipBurst := GetRateLimitSettings()
	return ensureCombinedRateLimits(ipEnabled, ipRPM, ipBurst, enabled, rpm, burst)
}

func ensureCombinedRateLimits(ipEnabled bool, ipRPM, ipBurst int, botEnabled bool, botRPM, botBurst int) error {
	backups := backupRateLimitFiles()

	if err := writeLimitReqStatusConfig(); err != nil {
		restoreRateLimitFiles(backups)
		return err
	}

	if ipEnabled {
		if err := writeIPRateLimitConfig(ipRPM); err != nil {
			restoreRateLimitFiles(backups)
			return err
		}
	} else {
		_ = os.Remove(rateLimitConfPath)
	}

	if botEnabled {
		if err := writeBotRateLimitConfig(botRPM); err != nil {
			restoreRateLimitFiles(backups)
			return err
		}
	} else {
		_ = os.Remove(botRateLimitConfPath)
	}

	rewriteRateLimitDirectivesToSites(ipEnabled, ipBurst, botEnabled, botBurst)
	return testAndReloadNginx(backups)
}

func writeLimitReqStatusConfig() error {
	content := "# WP Panel Generated - shared limit_req status\nlimit_req_status 429;\n"
	if err := os.MkdirAll(filepath.Dir(limitReqStatusConfPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(limitReqStatusConfPath, []byte(content), 0644)
}

func writeIPRateLimitConfig(rpm int) error {
	if rpm <= 0 {
		rpm = 60
	}
	content := fmt.Sprintf(`# WP Panel — 请求频率限制
# 已登录 WordPress 用户不限速（检测 wordpress_logged_in cookie）
map $http_cookie $wp_rate_limit_key {
    ~*wordpress_logged_in "";
    default $binary_remote_addr;
}

limit_req_zone $wp_rate_limit_key zone=wp_req_limit:10m rate=%dr/m;
`, rpm)

	if err := os.MkdirAll(filepath.Dir(rateLimitConfPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(rateLimitConfPath, []byte(content), 0644)
}

func writeBotRateLimitConfig(rpm int) error {
	if rpm <= 0 {
		rpm = defaultBotRateLimitRPM
	}
	content := fmt.Sprintf(`# WP Panel Generated - Bot UA rate limiting

geo $wp_verified_search_bot_ip {
    default 0;
%s}

map $http_user_agent $wp_search_bot_ua {
    ~*(googlebot|bingbot) 1;
    default 0;
}

map $http_user_agent $wp_bot_ua {
    ~*(googlebot|bingbot|facebookexternalhit|facebook|meta-externalagent|twitterbot|linkedinbot|slackbot|discordbot|telegrambot|semrushbot|ahrefsbot|mj12bot|dotbot|bot|crawler|spider|scraper|scan) 1;
    default 0;
}

map $host $wp_cdn_realip_compat {
    default 0;
%s}

map "$wp_bot_ua:$wp_search_bot_ua:$wp_verified_search_bot_ip:$wp_cdn_realip_compat" $wp_bot_rate_key {
    "1:1:1:1" "$server_name:bot";
    "1:1:1:0" "";
    "1:1:1:" "";
    ~^1: "$server_name:bot";
    default "";
}

limit_req_zone $wp_bot_rate_key zone=wp_bot_limit:10m rate=%dr/m;
`, renderVerifiedSearchBotGeoEntries(), renderCDNRealIPCompatMapEntries(), rpm)

	if err := os.MkdirAll(filepath.Dir(botRateLimitConfPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(botRateLimitConfPath, []byte(content), 0644)
}

func renderVerifiedSearchBotGeoEntries() string {
	var raw []string
	if db := database.GetDB(); db != nil {
		var googleIPs, bingIPs string
		_ = db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'googlebot_ips'`).Scan(&googleIPs)
		_ = db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'bingbot_ips'`).Scan(&bingIPs)
		raw = append(raw, googleIPs, bingIPs)
	}

	seen := map[string]bool{}
	var b strings.Builder
	for _, block := range raw {
		for _, line := range strings.Split(block, "\n") {
			item := strings.TrimSpace(line)
			if item == "" || seen[item] || !isValidIPOrCIDR(item) {
				continue
			}
			seen[item] = true
			b.WriteString("    ")
			b.WriteString(item)
			b.WriteString(" 1;\n")
		}
	}
	return b.String()
}

func renderCDNRealIPCompatMapEntries() string {
	db := database.GetDB()
	if db == nil {
		return ""
	}
	rows, err := db.Query(`SELECT DISTINCT w.domain, w.aliases
		FROM websites w
		INNER JOIN website_cdn_realip_groups wg ON wg.website_id = w.id
		INNER JOIN cdn_realip_groups g ON g.id = wg.group_id
		WHERE w.cdn_realip_enabled = 1
		  AND g.enabled = 1
		  AND (g.provider = 'compatible' OR (g.provider = 'custom' AND TRIM(g.ip_ranges) = ''))`)
	if err != nil {
		return ""
	}
	defer rows.Close()

	hosts := map[string]bool{}
	for rows.Next() {
		var domain, aliases string
		if err := rows.Scan(&domain, &aliases); err != nil {
			continue
		}
		addNginxMapHost(hosts, domain)
		for _, alias := range splitAliases(aliases) {
			addNginxMapHost(hosts, alias)
		}
	}

	var b strings.Builder
	for host := range hosts {
		b.WriteString("    ")
		b.WriteString(host)
		b.WriteString(" 1;\n")
	}
	return b.String()
}

func addNginxMapHost(hosts map[string]bool, host string) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || !isSafeNginxMapHost(host) {
		return
	}
	hosts[host] = true
}

func isSafeNginxMapHost(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, r := range host {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			continue
		}
		return false
	}
	return true
}

type rateLimitBackup struct {
	path    string
	exists  bool
	content []byte
}

func backupRateLimitFiles() []rateLimitBackup {
	paths := []string{rateLimitConfPath, botRateLimitConfPath, limitReqStatusConfPath}
	entries, err := os.ReadDir("/etc/nginx/sites-available")
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				paths = append(paths, filepath.Join("/etc/nginx/sites-available", entry.Name()))
			}
		}
	}

	backups := make([]rateLimitBackup, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		backups = append(backups, rateLimitBackup{
			path:    path,
			exists:  err == nil,
			content: data,
		})
	}
	return backups
}

func restoreRateLimitFiles(backups []rateLimitBackup) {
	for _, backup := range backups {
		if backup.exists {
			os.MkdirAll(filepath.Dir(backup.path), 0755)
			os.WriteFile(backup.path, backup.content, 0644)
		} else {
			os.Remove(backup.path)
		}
	}
}

func testAndReloadNginx(backups []rateLimitBackup) error {
	if out, err := exec.Command("nginx", "-t").CombinedOutput(); err != nil {
		restoreRateLimitFiles(backups)
		exec.Command("nginx", "-s", "reload").Run()
		return fmt.Errorf("nginx -t 失败，已回滚: %s", strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("nginx", "-s", "reload").CombinedOutput(); err != nil {
		restoreRateLimitFiles(backups)
		exec.Command("nginx", "-s", "reload").Run()
		return fmt.Errorf("nginx reload 失败，已回滚: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func injectRateLimitToSites(burst int) {
	botEnabled, _, _ := GetBotRateLimitSettings()
	_, _, botBurst := GetBotRateLimitSettings()
	rewriteRateLimitDirectivesToSites(true, burst, botEnabled, botBurst)
}

func stripRateLimitFromSites() {
	botEnabled, _, _ := GetBotRateLimitSettings()
	_, _, botBurst := GetBotRateLimitSettings()
	rewriteRateLimitDirectivesToSites(false, 0, botEnabled, botBurst)
}

func rewriteRateLimitDirectivesToSites(ipEnabled bool, ipBurst int, botEnabled bool, botBurst int) {
	sitesDir := "/etc/nginx/sites-available"
	entries, err := os.ReadDir(sitesDir)
	if err != nil {
		return
	}

	if ipBurst <= 0 {
		ipBurst = 300
	}
	if botBurst <= 0 {
		botBurst = defaultBotRateLimitBurst
	}
	ipLimitLine := "    limit_req zone=wp_req_limit burst=" + strconv.Itoa(ipBurst) + " nodelay;"
	botLimitLine := "    limit_req zone=wp_bot_limit burst=" + strconv.Itoa(botBurst) + " nodelay;"

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

		lines := strings.Split(content, "\n")
		var cleaned []string
		for _, line := range lines {
			if !strings.Contains(line, "limit_req zone=wp_req_limit") &&
				!strings.Contains(line, "limit_req zone=wp_bot_limit") &&
				!strings.Contains(line, "limit_req_status 429") {
				cleaned = append(cleaned, line)
			}
		}
		var result []string
		for _, line := range cleaned {
			result = append(result, line)
			if strings.Contains(line, "server_name") {
				if ipEnabled {
					result = append(result, ipLimitLine)
				}
				if botEnabled {
					result = append(result, botLimitLine)
				}
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
		return true, 60, 300
	}

	var sEnabled, sRPM, sBurst string
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'rate_limit_enabled'`).Scan(&sEnabled)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'rate_limit_rpm'`).Scan(&sRPM)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'rate_limit_burst'`).Scan(&sBurst)

	enabled = sEnabled != "false"
	rpm = parseIntOr(sRPM, 60)
	burst = parseIntOr(sBurst, 300)
	return
}

func GetBotRateLimitSettings() (enabled bool, rpm int, burst int) {
	db := database.GetDB()
	if db == nil {
		return false, defaultBotRateLimitRPM, defaultBotRateLimitBurst
	}

	var sEnabled, sRPM, sBurst string
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'bot_limit_enabled'`).Scan(&sEnabled)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'bot_limit_rpm'`).Scan(&sRPM)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'bot_limit_burst'`).Scan(&sBurst)

	enabled = sEnabled == "true"
	rpm = parseIntOr(sRPM, defaultBotRateLimitRPM)
	burst = parseIntOr(sBurst, defaultBotRateLimitBurst)
	return
}
