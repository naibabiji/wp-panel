package executor

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

var syncMu sync.Mutex
var manualAddNginxBan = AddNginxBan
var manualRemoveNginxBan = RemoveNginxBan
var syncReplaceNginxBannedIPs = ReplaceNginxBannedIPs
var conditionalRemoveNginxBan = RemoveNginxBan

const fail2banFilterConfig = `# WP Panel Generated — DO NOT EDIT MANUALLY
[Definition]
failregex = ^<HOST> .* "POST /wp-login\.php[^"]*" 200 .*$
            ^<HOST> .* "POST /xmlrpc\.php .*" 403 .*$
            ^<HOST> .* "POST //xmlrpc\.php .*" 403 .*$
            ^<HOST> .* ".*" 429 .*$
            ^<HOST> - - \[.*\] "(GET|POST) .*(\.env|\.git|config\.bak|wp-config\.php|\.sql|\.tar|\.gz|\.zip|\.old|\.swp|\.save|\.DS_Store).*" 404 .*$
ignoreregex =
              ^<HOST> .* "POST /wp-login\.php\?(?:[A-Za-z0-9_.~-]+=[^&"]*&)*action=(?:confirm_admin_email|postpass|logout|lostpassword|retrievepassword|resetpass|rp|register|checkemail|confirmaction|entered_recovery_mode)(?:&(?!action=)[A-Za-z0-9_.~-]+=[^&"]*)* HTTP/[^"]+" 200 .*$
`

func init() {
	database.RegisterUpgrade("1.0.26", cleanupDuplicateActiveFirewallBans)
}

func cleanupDuplicateActiveFirewallBans() error {
	return deduplicateActiveFirewallBans(database.GetDB())
}

type fail2banConfigBackup struct {
	path    string
	data    []byte
	existed bool
}

func deployFail2ban(webWhitelistIPs, sshWhitelistIPs string, maxRetry, findTime, banTime int) error {
	jailDir := "/etc/fail2ban/jail.d"
	filterDir := "/etc/fail2ban/filter.d"
	actionDir := "/etc/fail2ban/action.d"
	os.MkdirAll(jailDir, 0755)
	os.MkdirAll(filterDir, 0755)
	os.MkdirAll(actionDir, 0755)

	ensureLogFiles()
	_ = EnsureNginxBannedIPsConfig()
	jailPath := filepath.Join(jailDir, "wppanel.conf")
	localPath := "/etc/fail2ban/fail2ban.local"
	actionPath := filepath.Join(actionDir, "wppanel-nginx.conf")
	filterPath := filepath.Join(filterDir, "wppanel.conf")
	filter404Path := filepath.Join(filterDir, "wppanel-404.conf")
	backups, err := backupFail2banConfigFiles(jailPath, actionPath, filterPath, filter404Path, localPath)
	if err != nil {
		return err
	}
	rollbackDeploy := func(cause error) error {
		if restoreErr := restoreFail2banConfigFiles(backups); restoreErr != nil {
			return fmt.Errorf("%w; rollback fail2ban config files failed: %v", cause, restoreErr)
		}
		if reloadErr := reloadOrStartFail2ban(); reloadErr != nil {
			return fmt.Errorf("%w; fail2ban config files were rolled back, but reload failed: %v", cause, reloadErr)
		}
		return cause
	}

	webIgnoreIPs, err := buildFail2banIgnoreIPs(webWhitelistIPs)
	if err != nil {
		return err
	}
	sshIgnoreIPs, err := buildFail2banIgnoreIPs(sshWhitelistIPs)
	if err != nil {
		return err
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
         wppanel-nginx[name=wppanel]
logpath = /www/wwwlogs/*/access.log
          /www/wwwlogs/*/error.log
maxretry = %d
findtime = %d
bantime = %d
bantime.increment = true
bantime.multipliers = 1 6 36 144 1008
bantime.maxtime = 7d
bantime.overalljails = false
ignoreip = %s

[wppanel-404]
enabled = true
filter = wppanel-404
action = nftables-multiport[name=wppanel-404, port="http,https"]
         wppanel-nginx[name=wppanel-404]
logpath = /www/wwwlogs/*/access.log
maxretry = 30
findtime = 60
bantime = %d
bantime.increment = true
bantime.multipliers = 1 6 36 144 1008
bantime.maxtime = 7d
bantime.overalljails = false
ignoreip = %s

[wppanel-sshd]
enabled = true
filter = sshd
action = nftables-multiport[name=wppanel-sshd, port="ssh"]
logpath = /var/log/auth.log
maxretry = %d
findtime = %d
bantime = %d
bantime.increment = true
bantime.multipliers = 1 6 36 144 1008
bantime.maxtime = 7d
bantime.overalljails = false
ignoreip = %s
`, maxRetry, findTime, banTime, webIgnoreIPs, banTime, webIgnoreIPs, maxRetry, findTime, banTime, sshIgnoreIPs)
	if err := validateGeneratedFail2banJailConfig(jailConfig); err != nil {
		return err
	}

	if err := os.WriteFile(jailPath, []byte(jailConfig), 0644); err != nil {
		return rollbackDeploy(fmt.Errorf("写入 jail 配置失败: %w", err))
	}

	if err := writeFail2banLocal(localPath); err != nil {
		return rollbackDeploy(fmt.Errorf("写入 fail2ban 本地配置失败: %w", err))
	}

	actionConfig := `# WP Panel Generated - DO NOT EDIT MANUALLY
[Definition]
actionban = /usr/local/bin/wp-panel --banip-nginx <ip> --record-fail2ban <ip> --ban-jail <name> --ban-bantime <bantime> --ban-count <bancount> --ban-restored=<restored>
actionunban = /usr/local/bin/wp-panel --unban-fail2ban <ip> --ban-jail <name>
`

	if err := os.WriteFile(actionPath, []byte(actionConfig), 0644); err != nil {
		return rollbackDeploy(fmt.Errorf("写入 nginx action 配置失败: %w", err))
	}

	if err := os.WriteFile(filterPath, []byte(fail2banFilterConfig), 0644); err != nil {
		return rollbackDeploy(fmt.Errorf("写入 filter 配置失败: %w", err))
	}

	filter404Config := `# WP Panel Generated — DO NOT EDIT MANUALLY
[Definition]
failregex = ^<HOST> - - \[.*\] ".*" 404 .*$
ignoreregex =
`

	if err := os.WriteFile(filter404Path, []byte(filter404Config), 0644); err != nil {
		return rollbackDeploy(fmt.Errorf("写入 404 filter 配置失败: %w", err))
	}

	if _, err := executeCommand("fail2ban-client", "-t"); err != nil {
		return rollbackDeploy(fmt.Errorf("Fail2ban 配置校验失败: %w", err))
	}
	if err := reloadOrStartFail2ban(); err != nil {
		return rollbackDeploy(fmt.Errorf("重载 fail2ban 失败: %w", err))
	}
	return nil
}

func validateGeneratedFail2banJailConfig(config string) error {
	for _, jail := range []string{"wppanel", "wppanel-404", "wppanel-sshd"} {
		header := "[" + jail + "]"
		start := strings.Index(config, header)
		if start < 0 {
			return fmt.Errorf("invalid generated Fail2ban jail config: missing %s", header)
		}
		section := config[start+len(header):]
		if end := strings.Index(section, "\n["); end >= 0 {
			section = section[:end]
		}
		for _, directive := range []string{
			"bantime = 600",
			"bantime.increment = true",
			"bantime.multipliers = 1 6 36 144 1008",
			"bantime.maxtime = 7d",
			"bantime.overalljails = false",
		} {
			if strings.Count(section, directive) != 1 {
				return fmt.Errorf("invalid generated Fail2ban jail config: %s: %s", jail, directive)
			}
		}
	}
	return nil
}

func writeFail2banLocal(path string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines)+3)
	inDefault, foundDefault, wrotePurge := false, false, false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if inDefault && !wrotePurge {
				out = append(out, "dbpurgeage = 30d")
				wrotePurge = true
			}
			if strings.EqualFold(trimmed, "[DEFAULT]") {
				if foundDefault {
					return fmt.Errorf("fail2ban.local contains multiple [DEFAULT] sections")
				}
				foundDefault, inDefault = true, true
			} else {
				inDefault = false
			}
			out = append(out, line)
			continue
		}
		if inDefault {
			key, _, found := strings.Cut(trimmed, "=")
			if found && strings.EqualFold(strings.TrimSpace(key), "dbpurgeage") {
				if !wrotePurge {
					out = append(out, "dbpurgeage = 30d")
					wrotePurge = true
				}
				continue
			}
		}
		out = append(out, line)
	}
	if inDefault && !wrotePurge {
		out = append(out, "dbpurgeage = 30d")
	} else if !foundDefault {
		out = append([]string{"[DEFAULT]", "dbpurgeage = 30d", ""}, out...)
	}
	content := strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
	return os.WriteFile(path, []byte(content), 0644)
}

func backupFail2banConfigFiles(paths ...string) ([]fail2banConfigBackup, error) {
	backups := make([]fail2banConfigBackup, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				backups = append(backups, fail2banConfigBackup{path: path})
				continue
			}
			return nil, fmt.Errorf("读取 Fail2ban 配置备份失败: %w", err)
		}
		backups = append(backups, fail2banConfigBackup{path: path, data: data, existed: true})
	}
	return backups, nil
}

func restoreFail2banConfigFiles(backups []fail2banConfigBackup) error {
	var errs []error
	for _, backup := range backups {
		if backup.existed {
			if err := os.WriteFile(backup.path, backup.data, 0644); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", backup.path, err))
			}
			continue
		}
		if err := os.Remove(backup.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("%s: %w", backup.path, err))
		}
	}
	return errors.Join(errs...)
}

func reloadOrStartFail2ban() error {
	if _, err := executeCommand("fail2ban-client", "reload"); err != nil {
		if _, activeErr := executeCommand("systemctl", "is-active", "--quiet", "fail2ban"); activeErr == nil {
			return err
		}
		if _, startErr := executeCommand("systemctl", "start", "fail2ban"); startErr != nil {
			return startErr
		}
	}
	return nil
}

func buildFail2banIgnoreIPs(whitelistIPs string) (string, error) {
	ignoreIPs := "127.0.0.1/8"
	if whitelistIPs == "" {
		return ignoreIPs, nil
	}
	for _, ip := range strings.Split(whitelistIPs, "\n") {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		if strings.ContainsAny(ip, " \t\r") {
			return "", fmt.Errorf("白名单 IP 格式不正确: %s", ip)
		}
		if strings.Contains(ip, "/") {
			if _, _, err := net.ParseCIDR(ip); err != nil {
				return "", fmt.Errorf("白名单 IP 格式不正确: %s", ip)
			}
		} else if net.ParseIP(ip) == nil {
			return "", fmt.Errorf("白名单 IP 格式不正确: %s", ip)
		}
		ignoreIPs += " " + ip
	}
	return ignoreIPs, nil
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
			touch("/www/wwwlogs/" + e.Name() + "/wp-security.log")
			touch("/www/wwwlogs/" + e.Name() + "/php-error.log")
			touch("/www/wwwlogs/" + e.Name() + "/php-slow.log")
			hasLogs = true
		}
	}
	if !hasLogs {
		os.MkdirAll("/www/wwwlogs/_panel_placeholder", 0755)
		touch("/www/wwwlogs/_panel_placeholder/access.log")
		touch("/www/wwwlogs/_panel_placeholder/error.log")
		touch("/www/wwwlogs/_panel_placeholder/wp-security.log")
		touch("/www/wwwlogs/_panel_placeholder/php-error.log")
		touch("/www/wwwlogs/_panel_placeholder/php-slow.log")
	}
	touch("/var/log/auth.log")
}

func ensureSiteLogFiles(logDir string) {
	if strings.TrimSpace(logDir) == "" {
		return
	}
	touch(filepath.Join(logDir, "access.log"))
	touch(filepath.Join(logDir, "error.log"))
	touch(filepath.Join(logDir, "wp-security.log"))
	touch(filepath.Join(logDir, "php-error.log"))
	touch(filepath.Join(logDir, "php-slow.log"))
}

func touch(path string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err == nil {
		f.Close()
	}
}

func ReloadFail2ban() error {
	if _, err := executeCommand("fail2ban-client", "reload"); err != nil {
		if _, activeErr := executeCommand("systemctl", "is-active", "--quiet", "fail2ban"); activeErr == nil {
			return fmt.Errorf("reload fail2ban failed: %w", err)
		}
	}
	return nil
}

func executeRefreshWhitelist(task *Task) TaskResult {
	var allIPs []string
	var details []string

	if cfIPs, err := fetchCloudflareIPs(); err == nil {
		allIPs = append(allIPs, cfIPs...)
		details = append(details, fmt.Sprintf("Cloudflare: %d 条", len(cfIPs)))
		cacheCloudflareRealIPRanges(cfIPs)
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
		cacheSearchBotIPRanges("googlebot_ips", googleIPs)
	} else {
		details = append(details, "Googlebot: 获取失败")
	}
	if bingIPs, err := fetchBingbotIPs(); err == nil {
		allIPs = append(allIPs, bingIPs...)
		details = append(details, fmt.Sprintf("Bingbot: %d 条", len(bingIPs)))
		cacheSearchBotIPRanges("bingbot_ips", bingIPs)
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

	// googlebot_ips/bingbot_ips 缓存已更新，重新生成日志 map 配置，
	// 让方案 D 阶段二的伪装爬虫探测（$wp_security_verified_bot_ip）使用最新官方 IP 段。
	// 这一步只是让探测规则更及时，不是白名单刷新本身的核心目的：
	// 即使这里失败，Cloudflare/Fail2ban 白名单已经成功更新，不应该让整个任务
	// 报失败——那样会让管理员误以为白名单刷新失败，实际上只是日志规则没同步上。
	if err := EnsureLogMap(); err != nil {
		details = append(details, "安全探测规则同步失败: "+err.Error())
	}

	return TaskResult{
		Success: true,
		Message: fmt.Sprintf("共获取 %d 条（%s）", len(allIPs), strings.Join(details, "；")),
	}
}

func cacheSearchBotIPRanges(key string, ips []string) {
	if database.GetDB() == nil {
		return
	}
	if key != "googlebot_ips" && key != "bingbot_ips" {
		return
	}
	database.GetDB().Exec(`INSERT INTO security_settings (skey, svalue, description, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(skey) DO UPDATE SET svalue = excluded.svalue, updated_at = excluded.updated_at`,
		key, strings.Join(ips, "\n"), key+"官方IP段缓存")
}

func ApplyFail2banSettings() error {
	db := database.GetDB()

	var officialIPs, customIPs, cdnRealIPIPs string
	var maxRetry, findTime string
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'official_whitelist_ips'`).Scan(&officialIPs)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'whitelist_ips'`).Scan(&customIPs)
	cdnRealIPIPs = CombinedCDNRealIPRangesForFail2ban()
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'fail2ban_maxretry'`).Scan(&maxRetry)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'fail2ban_findtime'`).Scan(&findTime)

	baseIPs := strings.TrimSpace(officialIPs)
	if customIPs != "" {
		if baseIPs != "" {
			baseIPs += "\n"
		}
		baseIPs += customIPs
	}
	webIPs := baseIPs
	if cdnRealIPIPs != "" {
		if webIPs != "" {
			webIPs += "\n"
		}
		webIPs += cdnRealIPIPs
	}

	mr := parseIntOr(maxRetry, 5)
	ft := parseIntOr(findTime, 60)
	// The incremental ladder is intentionally fixed at 10m, 1h, 6h, 24h and 7d.
	bt := 600

	if err := deployFail2ban(webIPs, baseIPs, mr, ft, bt); err != nil {
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
	syncMu.Lock()
	defer syncMu.Unlock()

	type jailIP struct{ jail, ip string }
	activeJailIPs := make(map[jailIP]bool)
	jailStatusRead := make(map[string]bool)
	webBannedSet := make(map[string]bool)
	webJailStatusRead := false

	for _, jail := range []string{"wppanel", "wppanel-404", "wppanel-sshd"} {
		out, err := executeCommand("fail2ban-client", "status", jail)
		if err != nil || out == "" {
			log.Printf("Fail2ban 状态同步跳过 %s：无法读取 jail 状态", jail)
			continue
		}
		jailStatusRead[jail] = true
		isWebJail := jail == "wppanel" || jail == "wppanel-404"
		if isWebJail {
			webJailStatusRead = true
		}
		for _, ip := range parseBannedIPs(out) {
			activeJailIPs[jailIP{jail: jail, ip: ip}] = true
			if isWebJail {
				webBannedSet[ip] = true
			}
		}
	}

	db := database.GetDB()

	for pair := range activeJailIPs {
		var count int
		db.QueryRow(`SELECT COUNT(*) FROM firewall_bans
			WHERE ip_address = ? AND source_jail = ? AND unbanned_at IS NULL
				AND (expires_at IS NULL OR expires_at > datetime('now'))`, pair.ip, pair.jail).Scan(&count)
		if count > 0 {
			continue
		}
		_ = RecordFail2banBan(pair.ip, pair.jail, 600, 1, false)
	}

	now := time.Now()
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
		if isManual == 0 && normalizeFail2banJail(jail) != "" && !jailStatusRead[jail] {
			if isWebBanSource(jail) {
				webBannedSet[ip] = true
			}
			continue
		}
		if isManual == 0 && activeJailIPs[jailIP{jail: jail, ip: ip}] {
			if isManual == 0 {
				removeAutomaticPersistBan(db, ip)
			}
			if isWebBanSource(jail) {
				webBannedSet[ip] = true
			}
			continue
		}
		if isManual == 1 {
			if expiresAt != nil && !expiresAt.After(now) {
				RemovePersistBan(ip)
				expiredIDs = append(expiredIDs, id)
				continue
			}
			if level >= 3 {
				AddPersistBan(ip)
			}
			if isWebBanSource(jail) {
				webBannedSet[ip] = true
			}
			continue
		}
		removeAutomaticPersistBan(db, ip)
		expiredIDs = append(expiredIDs, id)
	}

	for _, id := range expiredIDs {
		db.Exec("UPDATE firewall_bans SET unbanned_at = datetime('now') WHERE id = ?", id)
	}
	if webJailStatusRead || len(webBannedSet) > 0 {
		_ = syncReplaceNginxBannedIPs(webBannedSet)
	}
}

func removeAutomaticPersistBan(db *sql.DB, ip string) {
	var manualCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM firewall_bans
		WHERE ip_address=? AND is_manual=1 AND unbanned_at IS NULL
		AND (expires_at IS NULL OR expires_at > datetime('now'))`, ip).Scan(&manualCount)
	if manualCount == 0 {
		RemovePersistBan(ip)
	}
}

func RecordFail2banBan(ip, jail string, banTime, banCount int, restored bool) error {
	db := database.GetDB()
	if db == nil {
		return fmt.Errorf("database not initialized")
	}

	ip = strings.TrimSpace(ip)
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid IP: %s", ip)
	}
	if banTime < 1 || banTime > 7*24*60*60 {
		return fmt.Errorf("invalid Fail2ban ban time: %d", banTime)
	}
	if banCount < 1 || banCount > 1_000_000 {
		return fmt.Errorf("invalid Fail2ban ban count: %d", banCount)
	}

	jail = normalizeFail2banJail(jail)
	if jail == "" {
		jail = detectFail2banJail(ip)
		if jail == "" {
			jail = "wppanel"
		}
	}
	if restored {
		// Fail2ban restart runs actionunban while stopping, then restores active
		// tickets with actionban. Reopen the existing receipt without creating a
		// new history row or incrementing its counter.
		_, err := db.Exec(`UPDATE firewall_bans SET unbanned_at=NULL
			WHERE id = (
				SELECT id FROM firewall_bans
				WHERE ip_address=? AND source_jail=? AND is_manual=0
				ORDER BY id DESC LIMIT 1
			)`, ip, jail)
		return err
	}

	banLevel := fail2banBanLevel(banTime)
	reason := "Fail2ban 自动封禁"
	if jail == "wppanel-404" {
		reason = "404 泛滥检测"
	} else if jail == "wppanel-sshd" {
		reason = "SSH 暴力破解"
	}
	expiresModifier := fmt.Sprintf("+%d seconds", banTime)

	var protectedCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM firewall_bans WHERE ip_address=? AND (is_manual=1 OR ban_level>=5)
		AND unbanned_at IS NULL AND (expires_at IS NULL OR expires_at > datetime('now'))`, ip).Scan(&protectedCount)
	if protectedCount > 0 {
		return nil
	}

	var activeID, activeLevel int
	err := db.QueryRow(
		`SELECT id, ban_level
		 FROM firewall_bans
		 WHERE ip_address = ? AND source_jail = ? AND is_manual=0 AND unbanned_at IS NULL
		   AND (expires_at IS NULL OR expires_at > datetime('now'))
		 ORDER BY id DESC LIMIT 1`,
		ip, jail,
	).Scan(&activeID, &activeLevel)
	if err == nil {
		if activeLevel >= 5 {
			return nil
		}
		if _, err := db.Exec(
			`UPDATE firewall_bans
			 SET ban_level = ?, reason = ?, source_jail = ?, ban_count = ?,
			     banned_at = CURRENT_TIMESTAMP, expires_at = datetime('now', ?)
			 WHERE id = ?`,
			banLevel, reason, jail, banCount, expiresModifier, activeID,
		); err != nil {
			return err
		}
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	if _, err := db.Exec(
		`INSERT INTO firewall_bans (ip_address, ban_level, reason, source_jail, ban_count, expires_at)
		 VALUES (?, ?, ?, ?, ?, datetime('now', ?))`,
		ip, banLevel, reason, jail, banCount, expiresModifier,
	); err != nil {
		return err
	}

	return nil
}

func fail2banBanLevel(banTime int) int {
	if banTime <= 10*60 {
		return 2
	}
	if banTime <= 24*60*60 {
		return 3
	}
	return 4
}

func RecordFail2banUnban(ip, jail string) error {
	ip = strings.TrimSpace(ip)
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid IP: %s", ip)
	}
	jail = normalizeFail2banJail(jail)
	if jail == "" {
		return fmt.Errorf("invalid Fail2ban jail")
	}
	_, err := database.GetDB().Exec(`UPDATE firewall_bans SET unbanned_at=datetime('now')
		WHERE ip_address=? AND source_jail=? AND is_manual=0 AND unbanned_at IS NULL`, ip, jail)
	if err != nil {
		return err
	}
	if isWebBanSource(jail) {
		return MaybeRemoveNginxBan(ip)
	}
	return nil
}

func MaybeRemoveNginxBan(ip string) error {
	ip = strings.TrimSpace(ip)
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid IP: %s", ip)
	}
	var activeWebBans int
	err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM firewall_bans
		WHERE ip_address=? AND source_jail IN ('wppanel','wppanel-404','manual')
		AND unbanned_at IS NULL AND (expires_at IS NULL OR expires_at > datetime('now'))`, ip).Scan(&activeWebBans)
	if err != nil || activeWebBans > 0 {
		return err
	}
	return conditionalRemoveNginxBan(ip)
}

func MaybeRemovePersistBan(ip string) error {
	ip = strings.TrimSpace(ip)
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid IP: %s", ip)
	}
	var activePersistentBans int
	err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM firewall_bans
		WHERE ip_address=? AND (is_manual=1 OR ban_level>=5)
		AND unbanned_at IS NULL AND (expires_at IS NULL OR expires_at > datetime('now'))`, ip).Scan(&activePersistentBans)
	if err != nil || activePersistentBans > 0 {
		return err
	}
	RemovePersistBan(ip)
	return nil
}

type activeFirewallBanCandidate struct {
	id       int
	level    int
	count    int
	bannedAt string
}

func deduplicateActiveFirewallBans(db *sql.DB) error {
	if db == nil {
		return nil
	}
	rows, err := db.Query(`
		SELECT ip_address, source_jail
		FROM firewall_bans
		WHERE unbanned_at IS NULL
			AND (expires_at IS NULL OR expires_at > datetime('now'))
		GROUP BY ip_address, source_jail
		HAVING COUNT(*) > 1`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type duplicateGroup struct{ ip, jail string }
	var groups []duplicateGroup
	for rows.Next() {
		var group duplicateGroup
		if err := rows.Scan(&group.ip, &group.jail); err != nil {
			return err
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, group := range groups {
		if err := deduplicateActiveFirewallBanIP(db, group.ip, group.jail); err != nil {
			return err
		}
	}
	return nil
}

func deduplicateActiveFirewallBanIP(db *sql.DB, ip, jail string) error {
	if db == nil || ip == "" || jail == "" {
		return nil
	}
	rows, err := db.Query(`
		SELECT id, ban_level, ban_count, banned_at
		FROM firewall_bans
		WHERE ip_address = ? AND source_jail = ? AND unbanned_at IS NULL
			AND (expires_at IS NULL OR expires_at > datetime('now'))
		ORDER BY ban_level DESC, banned_at DESC, id DESC`, ip, jail)
	if err != nil {
		return err
	}
	defer rows.Close()

	var bans []activeFirewallBanCandidate
	for rows.Next() {
		var row activeFirewallBanCandidate
		if err := rows.Scan(&row.id, &row.level, &row.count, &row.bannedAt); err != nil {
			return err
		}
		bans = append(bans, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(bans) <= 1 {
		return nil
	}

	keep := bans[0]
	maxCount := keep.count
	var duplicateIDs []int
	for _, ban := range bans[1:] {
		duplicateIDs = append(duplicateIDs, ban.id)
		if ban.count > maxCount {
			maxCount = ban.count
		}
	}
	if _, err := db.Exec(`UPDATE firewall_bans SET ban_count = ? WHERE id = ?`, maxCount, keep.id); err != nil {
		return err
	}
	for _, id := range duplicateIDs {
		if _, err := db.Exec(`UPDATE firewall_bans SET unbanned_at = datetime('now') WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

func normalizeFail2banJail(jail string) string {
	switch strings.TrimSpace(jail) {
	case "wppanel", "wppanel-404", "wppanel-sshd":
		return strings.TrimSpace(jail)
	default:
		return ""
	}
}

func isWebBanSource(jail string) bool {
	return jail == "wppanel" || jail == "wppanel-404" || jail == "manual"
}

func detectFail2banJail(ip string) string {
	for _, jail := range []string{"wppanel", "wppanel-404"} {
		out, err := executeCommand("fail2ban-client", "status", jail)
		if err != nil {
			continue
		}
		for _, bannedIP := range parseBannedIPs(out) {
			if bannedIP == ip {
				return jail
			}
		}
	}
	return ""
}

func EnsurePersistNftables() {
	exec.Command("bash", "-c",
		`nft add table ip wppanel_persist 2>/dev/null
nft add chain ip wppanel_persist input { type filter hook input priority -1\; } 2>/dev/null
nft add set ip wppanel_persist banned_ips { type ipv4_addr\; } 2>/dev/null
nft list chain ip wppanel_persist input 2>/dev/null | grep -q "saddr @banned_ips drop" || nft add rule ip wppanel_persist input ip saddr @banned_ips drop
nft add set ip wppanel_persist ssh_limit { type ipv4_addr\; flags dynamic,timeout\; timeout 1m\; size 65535\; } 2>/dev/null
nft list chain ip wppanel_persist input 2>/dev/null | grep -q "tcp dport 22 ct state new" || nft add rule ip wppanel_persist input tcp dport 22 ct state new add @ssh_limit { ip saddr limit rate over 3/minute } drop`).Run()
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

	db := database.GetDB()
	if db == nil {
		return TaskResult{Success: false, Message: "database not initialized"}
	}

	jail := "manual"
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

	if err := manualAddNginxBan(ip); err != nil {
		return TaskResult{Success: false, Message: "封禁失败: " + err.Error()}
	}

	if _, err := db.Exec(
		`INSERT INTO firewall_bans (ip_address, ban_level, reason, source_jail, is_manual, ban_count, expires_at)
		 VALUES (?, ?, '管理员手动封禁', ?, 1, 1, ?)`,
		ip, banLevel, jail, expires,
	); err != nil {
		_ = manualRemoveNginxBan(ip)
		return TaskResult{Success: false, Message: "封禁记录写入失败"}
	}

	if banLevel >= 3 {
		AddPersistBan(ip)
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

		// Fail2ban-controlled bans are closed by actionunban or SyncFail2banBans.
		// Do not expire them from the panel clock while Fail2ban may still enforce them.
		if jail == "wppanel" || jail == "wppanel-404" || jail == "wppanel-sshd" {
			continue
		}
		db.Exec("UPDATE firewall_bans SET unbanned_at = datetime('now') WHERE id = ?", id)
		RemovePersistBan(ip)
		if isWebBanSource(jail) {
			_ = RemoveNginxBan(ip)
		}
	}
}

func StartFail2banSyncScheduler() {
	GoSafe(func() {
		SyncFail2banBans()
		CleanExpiredBans()
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			SyncFail2banBans()
			CleanExpiredBans()
		}
	})
}
