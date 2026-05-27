package executor

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func deployFail2ban(whitelistIPs string, maxRetry, findTime, banTime int) error {
	jailDir := "/etc/fail2ban/jail.d"
	filterDir := "/etc/fail2ban/filter.d"
	actionDir := "/etc/fail2ban/action.d"
	os.MkdirAll(jailDir, 0755)
	os.MkdirAll(filterDir, 0755)
	os.MkdirAll(actionDir, 0755)

	ensureLogFiles()
	_ = EnsureNginxBannedIPsConfig()

	ignoreIPs := "127.0.0.1/8"
	if whitelistIPs != "" {
		for _, ip := range strings.Split(whitelistIPs, "\n") {
			ip = strings.TrimSpace(ip)
			if ip == "" {
				continue
			}
			if strings.ContainsAny(ip, " \t\r") {
				return fmt.Errorf("白名单 IP 格式不正确: %s", ip)
			}
			if strings.Contains(ip, "/") {
				if _, _, err := net.ParseCIDR(ip); err != nil {
					return fmt.Errorf("白名单 IP 格式不正确: %s", ip)
				}
			} else if net.ParseIP(ip) == nil {
				return fmt.Errorf("白名单 IP 格式不正确: %s", ip)
			}
			ignoreIPs += " " + ip
		}
	}

	if maxRetry <= 0 {
		maxRetry = 5
	}
	if findTime <= 0 {
		findTime = 60
	}
	if banTime <= 0 {
		banTime = 600
	}

	jailConfig := fmt.Sprintf(`# WP Panel Generated — DO NOT EDIT MANUALLY
[wppanel]
enabled = true
filter = wppanel
action = nftables-multiport[name=wppanel, port="http,https"]
         wppanel-nginx
logpath = /www/wwwlogs/*/access.log
          /www/wwwlogs/*/error.log
maxretry = %d
findtime = %d
bantime = %d
ignoreip = %s

[wppanel-404]
enabled = true
filter = wppanel-404
action = nftables-multiport[name=wppanel-404, port="http,https"]
         wppanel-nginx
logpath = /www/wwwlogs/*/access.log
maxretry = 30
findtime = 60
bantime = %d
ignoreip = %s

[wppanel-sshd]
enabled = true
filter = sshd
action = nftables-multiport[name=wppanel-sshd, port="ssh"]
logpath = /var/log/auth.log
maxretry = %d
findtime = %d
bantime = %d
ignoreip = %s
`, maxRetry, findTime, banTime, ignoreIPs, banTime, ignoreIPs, maxRetry, findTime, banTime, ignoreIPs)

	if err := os.WriteFile(jailDir+"/wppanel.conf", []byte(jailConfig), 0644); err != nil {
		return fmt.Errorf("写入 jail 配置失败: %w", err)
	}

	actionConfig := `# WP Panel Generated - DO NOT EDIT MANUALLY
[Definition]
actionban = /usr/local/bin/wp-panel --banip-nginx <ip>
actionunban = /usr/local/bin/wp-panel --unbanip-nginx <ip>
`

	if err := os.WriteFile(actionDir+"/wppanel-nginx.conf", []byte(actionConfig), 0644); err != nil {
		return fmt.Errorf("写入 nginx action 配置失败: %w", err)
	}

	filterConfig := `# WP Panel Generated — DO NOT EDIT MANUALLY
[Definition]
failregex = ^<HOST> .* "POST /wp-login\.php .*" .*$
            ^<HOST> .* "POST /xmlrpc\.php .*" .*$
            ^<HOST> .* "POST //xmlrpc\.php .*" .*$
            ^<HOST> .* ".*" 429 .*$
            ^<HOST> - - \[.*\] "(GET|POST) .*(\.env|\.git|config\.bak|wp-config\.php|\.sql|\.tar|\.gz|\.zip|\.old|\.swp|\.save|\.DS_Store).*" 404 .*$
ignoreregex =
`

	if err := os.WriteFile(filterDir+"/wppanel.conf", []byte(filterConfig), 0644); err != nil {
		return fmt.Errorf("写入 filter 配置失败: %w", err)
	}

	filter404Config := `# WP Panel Generated — DO NOT EDIT MANUALLY
[Definition]
failregex = ^<HOST> - - \[.*\] ".*" 404 .*$
ignoreregex =
`

	if err := os.WriteFile(filterDir+"/wppanel-404.conf", []byte(filter404Config), 0644); err != nil {
		return fmt.Errorf("写入 404 filter 配置失败: %w", err)
	}

	if _, err := executeCommand("fail2ban-client", "reload"); err != nil {
		if _, activeErr := executeCommand("systemctl", "is-active", "--quiet", "fail2ban"); activeErr == nil {
			return fmt.Errorf("重载 fail2ban 失败: %w", err)
		}
		if _, startErr := executeCommand("systemctl", "start", "fail2ban"); startErr != nil {
			return fmt.Errorf("重载 fail2ban 失败: %w", err)
		}
	}
	return nil
}

func ensureLogFiles() {
	hasLogs := false
	entries, err := os.ReadDir("/www/wwwlogs")
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			touch("/www/wwwlogs/" + e.Name() + "/access.log")
			touch("/www/wwwlogs/" + e.Name() + "/error.log")
			hasLogs = true
		}
	}
	if !hasLogs {
		os.MkdirAll("/www/wwwlogs/_panel_placeholder", 0755)
		touch("/www/wwwlogs/_panel_placeholder/access.log")
		touch("/www/wwwlogs/_panel_placeholder/error.log")
	}
	touch("/var/log/auth.log")
}

func touch(path string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err == nil {
		f.Close()
	}
}

func executeRefreshWhitelist(task *Task) TaskResult {
	var allIPs []string
	var details []string

	if cfIPs, err := fetchCloudflareIPs(); err == nil {
		allIPs = append(allIPs, cfIPs...)
		details = append(details, fmt.Sprintf("Cloudflare: %d 条", len(cfIPs)))
		if err := DeployCloudflareRealIPConfig(cfIPs); err != nil {
			details = append(details, "Cloudflare Real IP: 配置失败")
		} else {
			details = append(details, "Cloudflare Real IP: 已更新")
		}
	} else {
		details = append(details, "Cloudflare: 获取失败")
	}
	if googleIPs, err := fetchGooglebotIPs(); err == nil {
		allIPs = append(allIPs, googleIPs...)
		details = append(details, fmt.Sprintf("Googlebot: %d 条", len(googleIPs)))
	} else {
		details = append(details, "Googlebot: 获取失败")
	}
	if bingIPs, err := fetchBingbotIPs(); err == nil {
		allIPs = append(allIPs, bingIPs...)
		details = append(details, fmt.Sprintf("Bingbot: %d 条", len(bingIPs)))
	} else {
		details = append(details, "Bingbot: 获取失败")
	}

	db := database.GetDB()
	db.Exec(`UPDATE security_settings SET svalue = ?, updated_at = CURRENT_TIMESTAMP WHERE skey = 'official_whitelist_ips'`,
		strings.Join(allIPs, "\n"))
	db.Exec(`UPDATE security_settings SET svalue = datetime('now'), updated_at = CURRENT_TIMESTAMP WHERE skey = 'last_whitelist_update'`)

	if err := ApplyFail2banSettings(); err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}

	return TaskResult{
		Success: true,
		Message: fmt.Sprintf("共获取 %d 条（%s）", len(allIPs), strings.Join(details, "；")),
	}
}

func ApplyFail2banSettings() error {
	db := database.GetDB()

	var officialIPs, customIPs string
	var maxRetry, findTime, banTime string
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'official_whitelist_ips'`).Scan(&officialIPs)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'whitelist_ips'`).Scan(&customIPs)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'fail2ban_maxretry'`).Scan(&maxRetry)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'fail2ban_findtime'`).Scan(&findTime)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'fail2ban_bantime'`).Scan(&banTime)

	mergedIPs := strings.TrimSpace(officialIPs)
	if customIPs != "" {
		if mergedIPs != "" {
			mergedIPs += "\n"
		}
		mergedIPs += customIPs
	}

	mr := parseIntOr(maxRetry, 5)
	ft := parseIntOr(findTime, 60)
	bt := parseIntOr(banTime, 600)

	if err := deployFail2ban(mergedIPs, mr, ft, bt); err != nil {
		return err
	}

	var autoEnabled string
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'auto_whitelist_enabled'`).Scan(&autoEnabled)
	if autoEnabled == "false" {
		executeCommand("systemctl", "stop", "wppanel-whitelist.timer")
		executeCommand("systemctl", "disable", "wppanel-whitelist.timer")
	} else {
		DeployWhitelistTimer()
	}
	return nil
}

func SyncFail2banBans() {
	ipJails := make(map[string]string)
	webBannedSet := make(map[string]bool)
	webJailStatusRead := false

	for _, jail := range []string{"wppanel", "wppanel-404", "wppanel-sshd"} {
		out, err := executeCommand("fail2ban-client", "status", jail)
		if err != nil || out == "" {
			continue
		}
		isWebJail := jail == "wppanel" || jail == "wppanel-404"
		if isWebJail {
			webJailStatusRead = true
		}
		for _, ip := range parseBannedIPs(out) {
			if _, exists := ipJails[ip]; !exists {
				ipJails[ip] = jail
			}
			if isWebJail {
				webBannedSet[ip] = true
			}
		}
	}

	bannedSet := make(map[string]bool, len(ipJails))
	for ip := range ipJails {
		bannedSet[ip] = true
	}

	nftablesSet := getNftablesPersistIPs()

	db := database.GetDB()
	now := time.Now()

	for ip, jail := range ipJails {
		var count int
		db.QueryRow("SELECT COUNT(*) FROM firewall_bans WHERE ip_address = ? AND unbanned_at IS NULL", ip).Scan(&count)
		if count > 0 {
			continue
		}

		prevBans, prevMaxLevel := countBanHistory(ip, now)
		banLevel := 2
		expiresVal := "datetime('now', '+600 seconds')"
		reason := "Fail2ban 自动封禁"
		if jail == "wppanel-404" {
			reason = "404 泛滥检测"
		}
		if jail == "wppanel-sshd" {
			reason = "SSH 暴力破解"
		}

		if prevMaxLevel >= 2 || prevBans > 0 {
			banLevel = 3
			expiresVal = "datetime('now', '+86400 seconds')"
			reason = "Fail2ban 自动封禁（24h内重复违规，升级至24小时）"
			if jail == "wppanel-404" {
				reason = "404 泛滥检测（24h内重复违规，升级至24小时）"
			}
			if jail == "wppanel-sshd" {
				reason = "SSH 暴力破解（24h内重复违规，升级至24小时）"
			}

			l3Count := countLevel3(ip)
			if l3Count >= 2 {
				banLevel = 5
				expiresVal = "NULL"
				reason = "Fail2ban 自动封禁（高危：累计3次严重违规，永久封禁）"
				if jail == "wppanel-404" {
					reason = "404 泛滥检测（高危：累计3次严重违规，永久封禁）"
				}
				if jail == "wppanel-sshd" {
					reason = "SSH 暴力破解（高危：累计3次严重违规，永久封禁）"
				}
			}
		}

		db.Exec(
			`INSERT INTO firewall_bans (ip_address, ban_level, reason, source_jail, ban_count, expires_at)
			 VALUES (?, ?, ?, ?, ?, `+expiresVal+`)`,
			ip, banLevel, reason, jail, prevBans+1,
		)

		if banLevel >= 3 {
			AddPersistBan(ip)
		}
	}

	rows, err := db.Query("SELECT id, ip_address, ban_level, expires_at, is_manual, source_jail FROM firewall_bans WHERE unbanned_at IS NULL")
	if err != nil {
		return
	}
	defer rows.Close()

	var expiredIDs []int
	for rows.Next() {
		var id, level, isManual int
		var ip, jail string
		var expiresAt *time.Time
		if rows.Scan(&id, &ip, &level, &expiresAt, &isManual, &jail) != nil {
			continue
		}
		if bannedSet[ip] {
			if jail == "wppanel" || jail == "wppanel-404" {
				webBannedSet[ip] = true
			}
			continue
		}
		if level >= 3 {
			if expiresAt == nil || expiresAt.After(now) {
				if nftablesSet != nil && nftablesSet[ip] {
					if jail == "wppanel" || jail == "wppanel-404" {
						webBannedSet[ip] = true
					}
					continue
				}
				if nftablesSet != nil && !nftablesSet[ip] {
					AddPersistBan(ip)
					if jail == "wppanel" || jail == "wppanel-404" {
						webBannedSet[ip] = true
					}
					continue
				}
				AddPersistBan(ip)
				if jail == "wppanel" || jail == "wppanel-404" {
					webBannedSet[ip] = true
				}
				continue
			}
			RemovePersistBan(ip)
		}
		expiredIDs = append(expiredIDs, id)
	}

	for _, id := range expiredIDs {
		db.Exec("UPDATE firewall_bans SET unbanned_at = datetime('now') WHERE id = ?", id)
	}
	if webJailStatusRead || len(webBannedSet) > 0 {
		_ = ReplaceNginxBannedIPs(webBannedSet)
	}
}

func countBanHistory(ip string, since time.Time) (count int, maxLevel int) {
	db := database.GetDB()
	cutoff := since.Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	db.QueryRow(
		"SELECT COUNT(*), COALESCE(MAX(ban_level), 0) FROM firewall_bans WHERE ip_address = ? AND banned_at > ?",
		ip, cutoff,
	).Scan(&count, &maxLevel)
	return
}

func countLevel3(ip string) int {
	db := database.GetDB()
	cutoff := time.Now().Add(-30 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	var c int
	db.QueryRow("SELECT COUNT(*) FROM firewall_bans WHERE ip_address = ? AND ban_level >= 3 AND banned_at > ?", ip, cutoff).Scan(&c)
	return c
}

func EnsurePersistNftables() {
	exec.Command("bash", "-c",
		`nft add table ip wppanel_persist 2>/dev/null
nft add chain ip wppanel_persist input { type filter hook input priority -1\; } 2>/dev/null
nft add set ip wppanel_persist banned_ips { type ipv4_addr\; } 2>/dev/null
nft list chain ip wppanel_persist input 2>/dev/null | grep -q "saddr @banned_ips drop" || nft add rule ip wppanel_persist input ip saddr @banned_ips drop`).Run()
}

func AddPersistBan(ip string) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return
	}
	if parsed := net.ParseIP(ip); parsed == nil {
		return
	}
	EnsurePersistNftables()
	exec.Command("nft", "add", "element", "ip", "wppanel_persist", "banned_ips", "{", ip, "}").Run()
}

func RemovePersistBan(ip string) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return
	}
	if parsed := net.ParseIP(ip); parsed == nil {
		return
	}
	exec.Command("nft", "delete", "element", "ip", "wppanel_persist", "banned_ips", "{", ip, "}").Run()
}

func getNftablesPersistIPs() map[string]bool {
	outBytes, err := exec.Command("bash", "-c",
		`nft list set ip wppanel_persist banned_ips 2>/dev/null | grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}'`).CombinedOutput()
	out := strings.TrimSpace(string(outBytes))
	if err != nil || out == "" {
		return nil
	}
	ips := make(map[string]bool)
	for _, ip := range strings.Fields(out) {
		ips[strings.TrimSpace(ip)] = true
	}
	return ips
}

func parseBannedIPs(status string) []string {
	var ips []string
	for _, line := range strings.Split(status, "\n") {
		idx := strings.Index(line, "Banned IP list:")
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(line[idx+len("Banned IP list:"):])
		for _, ip := range strings.Fields(rest) {
			ip = strings.TrimSpace(ip)
			if ip != "" {
				ips = append(ips, ip)
			}
		}
	}
	return ips
}

func fetchCloudflareIPs() ([]string, error) {
	var ips []string
	for _, url := range []string{
		"https://www.cloudflare.com/ips-v4/",
		"https://www.cloudflare.com/ips-v6/",
	} {
		out, err := executeCommand("curl", "-s", "-f", "-L", url)
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					ips = append(ips, line)
				}
			}
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("无法获取 Cloudflare IP 段")
	}
	return ips, nil
}

func fetchGooglebotIPs() ([]string, error) {
	out, err := executeCommand("curl", "-s", "-f", "-L", "https://developers.google.com/search/apis/ipranges/googlebot.json")
	if err != nil {
		return nil, err
	}
	var data struct {
		Prefixes []struct {
			IPv4Prefix string `json:"ipv4Prefix"`
			IPv6Prefix string `json:"ipv6Prefix"`
		} `json:"prefixes"`
	}
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		return nil, err
	}
	var ips []string
	for _, p := range data.Prefixes {
		if p.IPv4Prefix != "" {
			ips = append(ips, p.IPv4Prefix)
		}
		if p.IPv6Prefix != "" {
			ips = append(ips, p.IPv6Prefix)
		}
	}
	return ips, nil
}

func fetchBingbotIPs() ([]string, error) {
	out, err := executeCommand("curl", "-s", "-f", "-L", "https://www.bing.com/toolbox/bingbot.json")
	if err != nil {
		return nil, err
	}
	var data struct {
		Prefixes []struct {
			IPv4Prefix string `json:"ipv4Prefix"`
			IPv6Prefix string `json:"ipv6Prefix"`
		} `json:"prefixes"`
	}
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		return nil, err
	}
	var ips []string
	for _, p := range data.Prefixes {
		if p.IPv4Prefix != "" {
			ips = append(ips, p.IPv4Prefix)
		}
		if p.IPv6Prefix != "" {
			ips = append(ips, p.IPv6Prefix)
		}
	}
	return ips, nil
}

func executeManualBan(task *Task) TaskResult {
	payload, ok := task.Payload.(*ManualBanPayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}

	ip := strings.TrimSpace(payload.IP)
	if ip == "" {
		return TaskResult{Success: false, Message: "IP 地址不能为空"}
	}

	if net.ParseIP(ip) == nil {
		return TaskResult{Success: false, Message: "IP 地址格式不正确"}
	}

	jail := "wppanel"
	out, err := executeCommand("fail2ban-client", "set", jail, "banip", ip)
	if err != nil {
		return TaskResult{Success: false, Message: "封禁失败: " + out}
	}

	db := database.GetDB()
	banLevel := 2
	duration := 600
	if payload.Duration == 3600 {
		duration = 3600
		banLevel = 3
	} else if payload.Duration == 86400 {
		duration = 86400
		banLevel = 3
	} else if payload.Duration == 0 {
		duration = -1
		banLevel = 5
	}

	var expires interface{}
	if duration < 0 {
		expires = nil
	} else {
		expires = time.Now().Add(time.Duration(duration) * time.Second)
	}

	db.Exec(
		`INSERT INTO firewall_bans (ip_address, ban_level, reason, source_jail, is_manual, ban_count, expires_at)
		 VALUES (?, ?, '管理员手动封禁', ?, 1, 1, ?)`,
		ip, banLevel, jail, expires,
	)

	if banLevel >= 3 {
		AddPersistBan(ip)
	}
	if jail == "wppanel" || jail == "wppanel-404" {
		_ = AddNginxBan(ip)
	}

	msg := fmt.Sprintf("IP %s 已封禁", ip)
	if payload.Duration == 0 {
		msg += "（永久）"
	} else if payload.Duration >= 3600 {
		msg += fmt.Sprintf("（%d 小时）", payload.Duration/3600)
	} else {
		msg += fmt.Sprintf("（%d 分钟）", payload.Duration/60)
	}

	return TaskResult{Success: true, Message: msg}
}

func parseIntOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func RunWhitelistRefresh() string {
	return executeRefreshWhitelist(&Task{ID: "cli-refresh", Type: TaskRefreshWhitelist}).Message
}

func DeployWhitelistTimer() {
	timerUnit := `[Unit]
Description=WP Panel Weekly Whitelist Refresh
Requires=wppanel-whitelist.service

[Timer]
OnCalendar=Mon *-*-* 04:00:00
Persistent=true

[Install]
WantedBy=timers.target
`

	serviceUnit := `[Unit]
Description=WP Panel Whitelist Refresh

[Service]
Type=oneshot
ExecStart=/usr/local/bin/wp-panel --refresh-whitelist --config=/www/server/panel/config.json
`

	os.WriteFile("/etc/systemd/system/wppanel-whitelist.timer", []byte(timerUnit), 0644)
	os.WriteFile("/etc/systemd/system/wppanel-whitelist.service", []byte(serviceUnit), 0644)
	executeCommand("systemctl", "daemon-reload")
	executeCommand("systemctl", "enable", "wppanel-whitelist.timer")
	executeCommand("systemctl", "start", "wppanel-whitelist.timer")
}

func UnbanAllIPs() string {
	db := database.GetDB()

	unbanned, _ := db.Exec("UPDATE firewall_bans SET unbanned_at = datetime('now') WHERE unbanned_at IS NULL")
	unbanCount := int64(0)
	if unbanned != nil {
		unbanCount, _ = unbanned.RowsAffected()
	}

	exec.Command("bash", "-c", "nft flush set ip wppanel_persist banned_ips 2>/dev/null; true").Run()
	_ = ReplaceNginxBannedIPs(map[string]bool{})

	for _, jail := range []string{"wppanel", "wppanel-404", "wppanel-sshd"} {
		out, err := executeCommand("fail2ban-client", "status", jail)
		if err == nil && out != "" {
			for _, ip := range parseBannedIPs(out) {
				executeCommand("fail2ban-client", "set", jail, "unbanip", ip)
			}
		}
	}

	return fmt.Sprintf("已清空所有封禁规则，共解封 %d 条记录", unbanCount)
}

func CleanExpiredBans() {
	db := database.GetDB()

	rows, err := db.Query(`SELECT id, ip_address, source_jail FROM firewall_bans
		WHERE unbanned_at IS NULL AND expires_at IS NOT NULL AND expires_at <= datetime('now')`)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		var ip, jail string
		if rows.Scan(&id, &ip, &jail) != nil {
			continue
		}

		db.Exec("UPDATE firewall_bans SET unbanned_at = datetime('now') WHERE id = ?", id)

		if jail == "panel_scan" || jail == "panel" {
			RemovePersistBan(ip)
		}
		if jail == "wppanel" || jail == "wppanel-404" {
			_ = RemoveNginxBan(ip)
		}
	}
}
