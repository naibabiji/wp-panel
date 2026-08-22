package executor

import (
	"context"
	"crypto/tls"
	"fmt"
	"html"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

type alertRule struct {
	key               string
	checkFn           func() (firing bool, msg string)
	thresholdDuration time.Duration
	eventOnly         bool
	sendRecovery      bool
	pendingSince      time.Time
	lastFired         time.Time
	firing            bool
	lastAlertMsg      string
}

type alertManager struct {
	mu     sync.Mutex
	rules  []*alertRule
	stopCh chan struct{}
}

type alertCheckResult struct {
	firing  bool
	message string
}

var (
	alertMgr                 = &alertManager{stopCh: make(chan struct{})}
	panelCurrentVersion      string
	cloudflareProxyDetector  = isLikelyCloudflareProxied
	cloudflareDNSLookup      = net.DefaultResolver.LookupIPAddr
	cloudflareDetectionCache = struct {
		sync.Mutex
		entries map[string]cloudflareDetectionEntry
	}{entries: make(map[string]cloudflareDetectionEntry)}
)

type cloudflareDetectionEntry struct {
	proxied   bool
	checkedAt time.Time
}

func StartAlertMonitor(currentVersion string) {
	panelCurrentVersion = currentVersion
	alertMgr.rules = []*alertRule{
		{key: "alert_cpu", checkFn: checkCPU, thresholdDuration: 5 * time.Minute, sendRecovery: true},
		{key: "alert_memory", checkFn: checkMemory, thresholdDuration: 5 * time.Minute, sendRecovery: true},
		{key: "alert_disk", checkFn: checkDisk, sendRecovery: true},
		{key: "alert_service", checkFn: checkService, sendRecovery: true},
		{key: "alert_ssl", checkFn: checkSSL, sendRecovery: true},
		{key: "alert_backup", checkFn: checkBackup, sendRecovery: true},
		{key: "alert_website_expiry", checkFn: checkWebsiteExpiry, eventOnly: true},
		{key: "alert_remote_backup", checkFn: checkRemoteBackup, sendRecovery: true},
		{key: "alert_cron_fail", checkFn: checkCronFail, sendRecovery: true},
		{key: "alert_site", checkFn: checkSites, sendRecovery: true},
		{key: "alert_system_update", checkFn: checkSystemUpdate},
		{key: "alert_panel_update", checkFn: checkPanelUpdate},
		{key: "alert_wp_sqli_probe", checkFn: checkWPSQLiProbeThreshold},
		{key: "alert_wp_fake_search_bot", checkFn: checkWPFakeSearchBotThreshold},
	}
	loadAlertRuntimeState(alertMgr.rules)
	go alertMgr.loop()
}

func (m *alertManager) loop() {
	// Initial check without sending (warm up)
	time.Sleep(30 * time.Second)
	m.runChecks()

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.runChecks()
		case <-m.stopCh:
			return
		}
	}
}

func (m *alertManager) runChecks() {
	// 站点访问和 SSL/CDN 探测可能等待网络。先在后台启动，让资源与服务规则
	// 立即评估；轮到对应规则时再接收结果，避免网络等待串行叠加。
	var siteResultCh chan alertCheckResult
	if isRuleEnabled("alert_site") {
		siteResultCh = make(chan alertCheckResult, 1)
		go func() {
			firing, message := checkSites()
			siteResultCh <- alertCheckResult{firing: firing, message: message}
		}()
	}
	var sslResultCh chan alertCheckResult
	if isRuleEnabled("alert_ssl") {
		sslResultCh = make(chan alertCheckResult, 1)
		go func() {
			firing, message := checkSSL()
			sslResultCh <- alertCheckResult{firing: firing, message: message}
		}()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	cfg := GetSMTPConfig()
	hasSMTP := cfg != nil && cfg.Host != "" && cfg.AdminEmail != ""

	wCfg := GetWebhookConfig()
	hasWebhook := wCfg != nil && wCfg.Enabled == "true" && wCfg.URL != ""

	for _, r := range m.rules {
		if !isRuleEnabled(r.key) {
			disableAlertRule(r)
			continue
		}

		var instantFiring bool
		var msg string
		if r.key == "alert_site" && siteResultCh != nil {
			result := <-siteResultCh
			instantFiring, msg = result.firing, result.message
		} else if r.key == "alert_ssl" && sslResultCh != nil {
			result := <-sslResultCh
			instantFiring, msg = result.firing, result.message
		} else {
			instantFiring, msg = r.checkFn()
		}
		now := time.Now()
		if r.eventOnly {
			if instantFiring {
				sendAlertMilestone(r.key, msg)
			}
			continue
		}
		wasPending := !r.pendingSince.IsZero()
		firing := r.sustainedFiring(instantFiring, now)
		if firing && !r.firing {
			// Transition: normal → alert
			r.firing = true
			r.lastFired = now
			r.lastAlertMsg = msg
			sendAlertNotificationWithChannels(r.key, msg, false, hasSMTP, hasWebhook)
			persistAlertRuntimeState(r)
		} else if !firing && r.firing {
			// Transition: alert → normal
			r.firing = false
			recoveryDetail := buildRecoveryDetail(r)
			if r.sendRecovery {
				logAlert(r.key, "info", recoveryDetail)
			}
			database.GetDB().Exec("UPDATE alert_log SET resolved = 1 WHERE alert_type = ? AND resolved = 0", r.key)
			// 即时告警（无阈值）直接发送恢复通知，有阈值的等 5 分钟防抖
			sendRecovery := time.Since(r.lastFired) > 5*time.Minute || r.thresholdDuration <= 0
			if r.sendRecovery && hasSMTP && sendRecovery {
				go SendMail("", getPanelTitle()+" 恢复通知", formatEmailHTML(alertLabel(r.key)+" 已恢复正常", recoveryDetail, getEmailTip(r.key, true), false))
			}
			if r.sendRecovery && hasWebhook && sendRecovery {
				go SendWebhook(getPanelTitle()+" 恢复通知", recoveryDetail)
			}
			persistAlertRuntimeState(r)
		} else if firing && r.firing {
			r.lastAlertMsg = msg
			// Continuous alert — re-send on each rule's interval.
			if time.Since(r.lastFired) > alertResendInterval(r.key) {
				r.lastFired = time.Now()
				sendAlertNotificationWithChannels(r.key, msg, true, hasSMTP, hasWebhook)
				persistAlertRuntimeState(r)
			}
		} else if wasPending != !r.pendingSince.IsZero() || (!firing && !r.pendingSince.IsZero()) {
			persistAlertRuntimeState(r)
		}
	}
}

func disableAlertRule(r *alertRule) {
	if r == nil {
		return
	}
	wasActive := r.firing || !r.pendingSince.IsZero()
	r.firing = false
	r.pendingSince = time.Time{}
	if !wasActive {
		return
	}
	if database.GetDB() != nil {
		database.GetDB().Exec("UPDATE alert_log SET resolved = 1 WHERE alert_type = ? AND resolved = 0", r.key)
	}
	persistAlertRuntimeState(r)
}

func (r *alertRule) sustainedFiring(instantFiring bool, now time.Time) bool {
	if r.thresholdDuration <= 0 {
		if !instantFiring {
			r.pendingSince = time.Time{}
		}
		return instantFiring
	}
	if !instantFiring {
		r.pendingSince = time.Time{}
		return false
	}
	if r.pendingSince.IsZero() {
		r.pendingSince = now
		return false
	}
	return now.Sub(r.pendingSince) >= r.thresholdDuration
}

func alertResendInterval(key string) time.Duration {
	switch key {
	case "alert_cpu", "alert_memory", "alert_disk":
		return 2 * time.Hour
	case "alert_service", "alert_site":
		return 6 * time.Hour
	case "alert_ssl", "alert_backup", "alert_remote_backup", "alert_cron_fail",
		"alert_system_update", "alert_panel_update":
		return 24 * time.Hour
	case "alert_wp_sqli_probe", "alert_wp_fake_search_bot":
		// 判定条件本身就是"过去 24 小时内达到阈值"的滚动窗口，只要攻击没有停止，
		// 这个条件会持续成立一整天；用默认的 30 分钟重发会在攻击期间连续发出
		// 几十封"持续中"邮件，这里和系统/面板更新一样按 24 小时重发一次。
		return 24 * time.Hour
	}
	return 30 * time.Minute
}

func loadAlertRuntimeState(rules []*alertRule) {
	db := database.GetDB()
	if db == nil {
		return
	}
	for _, r := range rules {
		if r.eventOnly {
			continue
		}
		var status, pending, fired, message string
		err := db.QueryRow(`SELECT status, pending_since, last_fired_at, last_message
			FROM alert_runtime_state WHERE alert_type = ?`, r.key).Scan(&status, &pending, &fired, &message)
		if err != nil {
			continue
		}
		r.firing = status == "firing"
		r.pendingSince = parseAlertStateTime(pending)
		r.lastFired = parseAlertStateTime(fired)
		r.lastAlertMsg = message
	}
}

func persistAlertRuntimeState(r *alertRule) {
	if r == nil || r.eventOnly || database.GetDB() == nil {
		return
	}
	status := "normal"
	if r.firing {
		status = "firing"
	} else if !r.pendingSince.IsZero() {
		status = "pending"
	}
	_, _ = database.GetDB().Exec(`INSERT INTO alert_runtime_state
		(alert_type, status, pending_since, last_fired_at, last_message, updated_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(alert_type) DO UPDATE SET status=excluded.status,
		pending_since=excluded.pending_since, last_fired_at=excluded.last_fired_at,
		last_message=excluded.last_message, updated_at=CURRENT_TIMESTAMP`,
		r.key, status, formatAlertStateTime(r.pendingSince), formatAlertStateTime(r.lastFired), r.lastAlertMsg)
}

func formatAlertStateTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseAlertStateTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, raw)
	return t
}

func sendAlertMilestone(key, message string) {
	smtp := GetSMTPConfig()
	hasSMTP := smtp != nil && smtp.Host != "" && smtp.AdminEmail != ""
	webhook := GetWebhookConfig()
	hasWebhook := webhook != nil && webhook.Enabled == "true" && webhook.URL != ""
	logAlertEvent(key, "critical", message)
	deliverAlertNotification(key, message, false, hasSMTP, hasWebhook)
}

func sendAlertNotificationWithChannels(key, message string, ongoing, hasSMTP, hasWebhook bool) {
	logAlert(key, "critical", message)
	deliverAlertNotification(key, message, ongoing, hasSMTP, hasWebhook)
}

func deliverAlertNotification(key, message string, ongoing, hasSMTP, hasWebhook bool) {
	label := alertLabel(key)
	title := getPanelTitle() + " 告警 — " + label
	emailTitle := label
	if ongoing {
		title += "（持续中）"
		emailTitle += "（持续中）"
	}
	if hasSMTP {
		go SendMail("", title, formatEmailHTML(emailTitle, message, getEmailTip(key, false), true))
	}
	if hasWebhook {
		go SendWebhook(title, message)
	}
}

func sendResolvedAlertEvent(key, title, message, tip string) {
	logAlertEvent(key, "critical", message)
	subject := getPanelTitle() + " 告警 — " + title
	if cfg := GetSMTPConfig(); cfg != nil && cfg.Host != "" && cfg.AdminEmail != "" {
		go SendMail("", subject, formatEmailHTML(title, message, tip, true))
	}
	if cfg := GetWebhookConfig(); cfg != nil && cfg.Enabled == "true" && cfg.URL != "" {
		go SendWebhook(subject, message)
	}
}

func isAlertRuntimeFiring(key string) bool {
	if database.GetDB() == nil {
		return false
	}
	var status string
	if err := database.GetDB().QueryRow("SELECT status FROM alert_runtime_state WHERE alert_type = ?", key).Scan(&status); err != nil {
		return false
	}
	return status == "firing"
}

func isRuleEnabled(key string) bool {
	var v string
	database.GetDB().QueryRow("SELECT svalue FROM security_settings WHERE skey = ?", key).Scan(&v)
	return v != "false"
}

func alertLabel(key string) string {
	switch key {
	case "alert_cpu":
		return "CPU 高负载"
	case "alert_memory":
		return "可用内存不足"
	case "alert_disk":
		return "磁盘空间不足"
	case "alert_service":
		return "服务进程异常"
	case "alert_ssl":
		return "SSL 证书即将到期"
	case "alert_backup":
		return "数据库备份失败"
	case "alert_website_expiry":
		return "网站即将到期"
	case "alert_remote_backup":
		return "远程备份失败"
	case "alert_cron_fail":
		return "计划任务执行失败"
	case "alert_site":
		return "网站不可用"
	case "alert_system_update":
		return "系统有可用更新"
	case "alert_panel_update":
		return "面板有新版本"
	case "alert_wp_sqli_probe":
		return "WordPress SQL 注入探测"
	case "alert_wp_fake_search_bot":
		return "伪装搜索引擎爬虫"
	}
	return key
}

func logAlert(alertType, level, message string) {
	db := database.GetDB()
	if db == nil {
		return
	}
	db.Exec("INSERT INTO alert_log (alert_type, level, message) VALUES (?, ?, ?)", alertType, level, message)
	// 告警历史按时间保留，避免高频事件在数小时内挤掉所有上下文。
	db.Exec("DELETE FROM alert_log WHERE created_at < datetime('now', '-90 days')")
}

func logAlertEvent(alertType, level, message string) {
	db := database.GetDB()
	if db == nil {
		return
	}
	db.Exec("INSERT INTO alert_log (alert_type, level, message, resolved) VALUES (?, ?, ?, 1)", alertType, level, message)
	db.Exec("DELETE FROM alert_log WHERE created_at < datetime('now', '-90 days')")
}

func getEmailTip(key string, isRecovery bool) string {
	if isRecovery {
		return ""
	}
	switch key {
	case "alert_cpu":
		return "请在面板查看资源趋势和异常流量。"
	case "alert_memory":
		return "请检查高占用进程和访问流量。"
	case "alert_disk":
		return "请清理无用备份或日志，并确认磁盘余量。"
	case "alert_service":
		return "请查看服务日志和自动恢复结果。"
	case "alert_ssl":
		return "请完成续签，或手动上传有效证书。"
	case "alert_backup":
		return "请检查自动备份记录并确认下一次备份成功。"
	case "alert_website_expiry":
		return "请及时续期或备份网站数据。"
	case "alert_remote_backup":
		return "请检查远程连接，并确认失败文件重新同步成功。"
	case "alert_cron_fail":
		return "请查看任务日志并确认下一次执行成功。"
	case "alert_site":
		return "请检查域名解析、服务器状态和网站程序。"
	case "alert_system_update":
		return "请在合适的维护窗口执行系统更新。"
	case "alert_panel_update":
		return "请在面板设置页查看并执行更新。"
	case "alert_wp_sqli_probe":
		return "面板不会自动封禁；请在安全防御页面核对来源。"
	case "alert_wp_fake_search_bot":
		return "面板不会自动封禁；请在安全防御页面核对来源。"
	}
	return ""
}

func extractDomains(msg string) string {
	parts := strings.Split(msg, "；")
	var domains []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if idx := strings.Index(p, " "); idx > 0 {
			domains = append(domains, p[:idx])
		}
	}
	return strings.Join(domains, "、")
}

func buildRecoveryDetail(r *alertRule) string {
	if r.key == "alert_site" && r.lastAlertMsg != "" {
		domains := extractDomains(r.lastAlertMsg)
		if domains != "" {
			return domains + " 已恢复正常"
		}
	}
	if r.key == "alert_system_update" {
		return "系统所有软件包已更新完毕，当前为最新版本"
	}
	if r.key == "alert_panel_update" {
		return "面板已更新到最新版本"
	}
	return alertLabel(r.key) + " 已恢复正常"
}

func formatEmailHTML(title, detail, tip string, isAlert bool) string {
	icon := "ℹ️"
	titleColor := "#1976d2"
	if isAlert {
		icon = "⚠️"
		titleColor = "#d32f2f"
	}
	panelTitle := html.EscapeString(getPanelTitle())
	detail = html.EscapeString(detail)
	tip = html.EscapeString(tip)

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"></head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Helvetica Neue', sans-serif; max-width: 560px; margin: 0 auto; padding: 24px; color: #333;">
`)
	fmt.Fprintf(&b, `<h2 style="color: %s; margin: 0 0 16px 0; font-size: 18px;">%s %s</h2>`+"\n", titleColor, icon, title)
	fmt.Fprintf(&b, `<p style="font-size: 15px; line-height: 1.7; margin: 0 0 24px 0; color: #444;">%s</p>`+"\n", detail)
	if tip != "" {
		b.WriteString(`<hr style="border: none; border-top: 1px solid #e0e0e0; margin: 24px 0;">` + "\n")
		fmt.Fprintf(&b, `<p style="font-size: 13px; line-height: 1.6; color: #888; margin: 0;">%s</p>`+"\n", tip)
	}
	fmt.Fprintf(&b, `<p style="font-size: 12px; color: #aaa; margin: 20px 0 0 0;">— 来自 %s 面板</p>`+"\n", panelTitle)
	b.WriteString(`</body>
</html>`)
	return b.String()
}

// --- Checkers ---

func checkCPU() (bool, string) {
	db := database.GetDB()
	var cpu, ts string
	db.QueryRow("SELECT cpu_percent, recorded_at FROM monitoring_metrics ORDER BY id DESC LIMIT 1").Scan(&cpu, &ts)
	v, _ := strconv.ParseFloat(cpu, 64)
	if v > 80 {
		return true, fmt.Sprintf("CPU 使用率 %.1f%%（阈值 80%%），于 %s", v, toLocalTime(ts))
	}
	return false, ""
}

func checkMemory() (bool, string) {
	db := database.GetDB()
	var mem, ts string
	db.QueryRow("SELECT memory_percent, recorded_at FROM monitoring_metrics ORDER BY id DESC LIMIT 1").Scan(&mem, &ts)
	v, _ := strconv.ParseFloat(mem, 64)
	if v > 90 {
		return true, fmt.Sprintf("可用内存低于 10%%（当前使用率 %.1f%%），于 %s", v, toLocalTime(ts))
	}
	return false, ""
}

func toLocalTime(dbTime string) string {
	layouts := []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
	}
	for _, layout := range layouts {
		t, err := time.Parse(layout, dbTime)
		if err == nil {
			return t.Local().Format("2006-01-02 15:04:05")
		}
	}
	return dbTime
}

func checkDisk() (bool, string) {
	out, err := exec.Command("df", "-h", "/").Output()
	if err != nil {
		return false, ""
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) < 2 {
		return false, ""
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 5 {
		return false, ""
	}
	useStr := strings.TrimSuffix(fields[4], "%")
	use, _ := strconv.Atoi(useStr)
	if use > 90 {
		return true, fmt.Sprintf("磁盘使用率 %d%%（阈值 90%%），剩余 %s", use, fields[3])
	}
	return false, ""
}

func checkService() (bool, string) {
	svcs := GetGuardStatus()
	var msgs []string
	for _, s := range svcs {
		if !s.Running && !s.Paused && s.Restarts > 0 {
			msgs = append(msgs, fmt.Sprintf("%s 异常（已自动重启 %d 次，最近: %s）", s.Name, s.Restarts, s.LastIncident))
		}
	}
	if len(msgs) > 0 {
		return true, strings.Join(msgs, "；")
	}
	return false, ""
}

func checkSSL() (bool, string) {
	db := database.GetDB()
	rows, err := db.Query(`SELECT domain, ssl_expires_at, COALESCE(ssl_last_error, '')
		FROM websites WHERE ssl_enabled = 1 AND ssl_expires_at IS NOT NULL`)
	if err != nil {
		return false, ""
	}
	type sslAlertCandidate struct {
		domain           string
		message          string
		detectCloudflare bool
		cloudflare       bool
	}
	var candidates []sslAlertCandidate
	now := time.Now()
	for rows.Next() {
		var domain, lastError string
		var expiresAt time.Time
		if rows.Scan(&domain, &expiresAt, &lastError) != nil {
			continue
		}
		days := int(expiresAt.Sub(now).Hours() / 24)
		message := ""
		if days < 0 {
			message = fmt.Sprintf("%s 证书已过期 %d 天", domain, -days)
		} else if days <= 14 {
			message = fmt.Sprintf("%s 证书 %d 天后到期", domain, days)
		}
		if message == "" {
			continue
		}
		detectCloudflare := false
		if strings.TrimSpace(lastError) != "" {
			message += "，自动续签未成功"
			detectCloudflare = true
		}
		candidates = append(candidates, sslAlertCandidate{domain: domain, message: message, detectCloudflare: detectCloudflare})
	}
	rows.Close()

	var wg sync.WaitGroup
	for i := range candidates {
		if !candidates[i].detectCloudflare {
			continue
		}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			candidates[index].cloudflare = cloudflareProxyDetector(candidates[index].domain)
		}(i)
	}
	wg.Wait()

	msgs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		message := candidate.message
		if candidate.cloudflare {
			message += "。可能使用 Cloudflare；Full (strict) 仍需有效源站证书，可放行 ACME 路径或手动上传证书"
		}
		msgs = append(msgs, message)
	}
	if len(msgs) > 0 {
		return true, strings.Join(msgs, "；")
	}
	return false, ""
}

func isLikelyCloudflareProxied(domain string) bool {
	cloudflareDetectionCache.Lock()
	if entry, ok := cloudflareDetectionCache.entries[domain]; ok && time.Since(entry.checkedAt) < 6*time.Hour {
		cloudflareDetectionCache.Unlock()
		return entry.proxied
	}
	cloudflareDetectionCache.Unlock()

	raw := cachedCloudflareRealIPRanges()
	var ranges []*net.IPNet
	for _, line := range strings.Fields(raw) {
		_, network, err := net.ParseCIDR(strings.TrimSpace(line))
		if err == nil {
			ranges = append(ranges, network)
		}
	}
	if len(ranges) == 0 {
		return cacheCloudflareDetection(domain, false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := cloudflareDNSLookup(ctx, domain)
	if err != nil || len(addrs) == 0 {
		return cacheCloudflareDetection(domain, false)
	}
	for _, addr := range addrs {
		matched := false
		for _, network := range ranges {
			if network.Contains(addr.IP) {
				matched = true
				break
			}
		}
		if !matched {
			return cacheCloudflareDetection(domain, false)
		}
	}
	return cacheCloudflareDetection(domain, true)
}

func cacheCloudflareDetection(domain string, proxied bool) bool {
	cloudflareDetectionCache.Lock()
	cloudflareDetectionCache.entries[domain] = cloudflareDetectionEntry{proxied: proxied, checkedAt: time.Now()}
	cloudflareDetectionCache.Unlock()
	return proxied
}

func checkBackup() (bool, string) {
	db := database.GetDB()
	rows, err := db.Query(`SELECT w.domain FROM backup_settings bs
		JOIN websites w ON w.id = bs.site_id
		WHERE bs.enabled = 1
		AND EXISTS (
			SELECT 1 FROM db_backups b
			WHERE b.site_id = bs.site_id AND b.auto = 1
		)
		AND NOT EXISTS (
			SELECT 1 FROM db_backups b
			WHERE b.site_id = bs.site_id AND b.auto = 1
			AND b.created_at > datetime('now', '-1 day', '-5 minutes')
		)
		ORDER BY w.domain`)
	if err != nil {
		return false, ""
	}
	defer rows.Close()

	var domains []string
	for rows.Next() {
		var d string
		if rows.Scan(&d) == nil {
			domains = append(domains, d)
		}
	}
	if len(domains) > 0 {
		return true, strings.Join(domains, "、") + " 最近 24 小时内没有成功的自动备份"
	}
	return false, ""
}

func checkWebsiteExpiry() (bool, string) {
	db := database.GetDB()
	rows, err := db.Query(`SELECT domain, expires_at FROM websites WHERE expires_at IS NOT NULL AND expires_at > datetime('now')`)
	if err != nil {
		return false, ""
	}
	type expiryMilestone struct {
		domain    string
		expiresAt time.Time
		days      int
	}
	var candidates []expiryMilestone
	now := time.Now()
	milestones := map[int]bool{14: true, 7: true, 3: true, 1: true}

	for rows.Next() {
		var domain string
		var expiresAt time.Time
		if rows.Scan(&domain, &expiresAt) != nil {
			continue
		}
		days := int(expiresAt.Sub(now).Hours() / 24)
		if !milestones[days] {
			continue
		}
		candidates = append(candidates, expiryMilestone{domain: domain, expiresAt: expiresAt, days: days})
	}
	rows.Close()

	var msgs []string
	for _, candidate := range candidates {
		eventKey := candidate.domain + "|" + candidate.expiresAt.UTC().Format(time.RFC3339Nano) + "|" + strconv.Itoa(candidate.days)
		result, insertErr := db.Exec(`INSERT OR IGNORE INTO alert_event_markers (alert_type, event_key)
			VALUES ('alert_website_expiry', ?)`, eventKey)
		if insertErr != nil {
			continue
		}
		inserted, rowsErr := result.RowsAffected()
		if rowsErr != nil || inserted == 0 {
			continue
		}
		msgs = append(msgs, fmt.Sprintf("%s %d 天后到期", candidate.domain, candidate.days))
	}
	db.Exec("DELETE FROM alert_event_markers WHERE created_at < datetime('now', '-2 years')")
	if len(msgs) > 0 {
		return true, strings.Join(msgs, "；")
	}
	return false, ""
}

func checkRemoteBackup() (bool, string) {
	db := database.GetDB()
	var enabled int
	db.QueryRow("SELECT enabled FROM remote_backup_settings WHERE id = 1").Scan(&enabled)
	if enabled == 0 {
		return false, ""
	}

	// 失败记录只有在对应备份后续同步成功、transport_status 被更新后才算恢复。
	var failCount int
	db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM db_backups WHERE transport_status = 'failed') +
		(SELECT COUNT(*) FROM file_backups WHERE transport_status = 'failed')`).Scan(&failCount)
	if failCount > 0 {
		return true, fmt.Sprintf("有 %d 个远程备份文件同步失败", failCount)
	}
	return false, ""
}

func checkCronFail() (bool, string) {
	db := database.GetDB()
	rows, err := db.Query(`SELECT name FROM cron_jobs
		WHERE enabled = 1 AND notify_fail = 1 AND running = 0
		AND last_status = 'failed'`)
	if err != nil {
		return false, ""
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			names = append(names, "「"+name+"」")
		}
	}
	if len(names) > 0 {
		return true, "计划任务 " + strings.Join(names, "、") + " 执行失败"
	}
	return false, ""
}

const siteFailureAlertThreshold = 2

var siteLastCheck = make(map[string]time.Time)
var siteFailureMessages = make(map[string]string)
var siteFailureCounts = make(map[string]int)

func checkSites() (bool, string) {
	db := database.GetDB()
	rows, err := db.Query(`SELECT id, domain, ssl_enabled, monitoring_interval FROM websites WHERE status = 'active' AND monitoring_enabled = 1`)
	if err != nil {
		return false, ""
	}
	defer rows.Close()

	type siteInfo struct {
		id       string
		domain   string
		ssl      int
		interval int
	}
	var sites []siteInfo
	seen := make(map[string]bool)
	for rows.Next() {
		var s siteInfo
		if rows.Scan(&s.id, &s.domain, &s.ssl, &s.interval) != nil {
			continue
		}
		seen[s.id] = true
		if s.interval <= 0 {
			s.interval = 5
		}
		sites = append(sites, s)
	}

	for id := range siteFailureMessages {
		if !seen[id] {
			delete(siteFailureMessages, id)
			delete(siteFailureCounts, id)
		}
	}
	if len(siteLastCheck) > 100 {
		for id := range siteLastCheck {
			if !seen[id] {
				delete(siteLastCheck, id)
			}
		}
	}

	type checkTarget struct {
		id     string
		domain string
		url    string
	}
	var toCheck []checkTarget
	var msgs []string
	for _, s := range sites {
		if last, ok := siteLastCheck[s.id]; ok && time.Since(last) < time.Duration(s.interval)*time.Minute {
			if msg, ok := siteFailureMessages[s.id]; ok && siteFailureCounts[s.id] >= siteFailureAlertThreshold {
				msgs = append(msgs, msg)
			}
			continue
		}
		siteLastCheck[s.id] = time.Now()
		proto := "http"
		if s.ssl == 1 {
			proto = "https"
		}
		url := proto + "://" + s.domain + "/?wp_hc=" + strconv.FormatInt(time.Now().Unix(), 10)
		toCheck = append(toCheck, checkTarget{id: s.id, domain: s.domain, url: url})
	}

	if len(toCheck) == 0 {
		if len(msgs) > 0 {
			return true, strings.Join(msgs, "；")
		}
		return false, ""
	}

	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	type result struct {
		id     string
		domain string
		code   int
		err    error
	}
	resultCh := make(chan result, len(toCheck))
	for _, t := range toCheck {
		go func(t checkTarget) {
			resp, err := httpClient.Get(t.url)
			if err != nil {
				resultCh <- result{id: t.id, domain: t.domain, err: err}
				return
			}
			resp.Body.Close()
			resultCh <- result{id: t.id, domain: t.domain, code: resp.StatusCode}
		}(t)
	}

	for range toCheck {
		r := <-resultCh
		if r.err != nil {
			msg := fmt.Sprintf("%s 无法访问 (%v)", r.domain, r.err)
			siteFailureMessages[r.id] = msg
			siteFailureCounts[r.id]++
			if siteFailureCounts[r.id] >= siteFailureAlertThreshold {
				msgs = append(msgs, msg)
			}
		} else if r.code < 200 || r.code >= 400 {
			msg := fmt.Sprintf("%s 返回 %d", r.domain, r.code)
			siteFailureMessages[r.id] = msg
			siteFailureCounts[r.id]++
			if siteFailureCounts[r.id] >= siteFailureAlertThreshold {
				msgs = append(msgs, msg)
			}
		} else {
			delete(siteFailureMessages, r.id)
			delete(siteFailureCounts, r.id)
		}
	}

	if len(msgs) > 0 {
		return true, strings.Join(msgs, "；")
	}
	return false, ""
}

var sysUpdateCache struct {
	mu     sync.Mutex
	lastAt time.Time
	names  []string
}

var panelUpdateCache struct {
	mu      sync.Mutex
	lastAt  time.Time
	latest  string
	message string
}

func ClearSystemUpdateAlertCache() {
	sysUpdateCache.mu.Lock()
	sysUpdateCache.lastAt = time.Time{}
	sysUpdateCache.names = nil
	sysUpdateCache.mu.Unlock()
}

func ClearPanelUpdateAlertCache() {
	panelUpdateCache.mu.Lock()
	panelUpdateCache.lastAt = time.Time{}
	panelUpdateCache.latest = ""
	panelUpdateCache.message = ""
	panelUpdateCache.mu.Unlock()
}

func checkSystemUpdate() (bool, string) {
	sysUpdateCache.mu.Lock()
	if time.Since(sysUpdateCache.lastAt) < 24*time.Hour {
		names := sysUpdateCache.names
		sysUpdateCache.mu.Unlock()
		if len(names) > 0 {
			return true, fmt.Sprintf("系统有 %d 个可用更新：%s", len(names), strings.Join(names, "、"))
		}
		return false, ""
	}
	sysUpdateCache.mu.Unlock()

	out, err := exec.Command("bash", "-c", "apt list --upgradable 2>/dev/null").Output()
	if err != nil {
		return false, ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var names []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Listing...") {
			continue
		}
		parts := strings.SplitN(line, "/", 2)
		if len(parts) > 0 {
			names = append(names, parts[0])
		}
	}

	sysUpdateCache.mu.Lock()
	sysUpdateCache.lastAt = time.Now()
	sysUpdateCache.names = names
	sysUpdateCache.mu.Unlock()

	if len(names) > 0 {
		return true, fmt.Sprintf("系统有 %d 个可用更新：%s", len(names), strings.Join(names, "、"))
	}
	return false, ""
}

func checkPanelUpdate() (bool, string) {
	if panelCurrentVersion == "" || panelCurrentVersion == "dev" {
		return false, ""
	}

	panelUpdateCache.mu.Lock()
	if time.Since(panelUpdateCache.lastAt) < 24*time.Hour {
		msg := panelUpdateCache.message
		panelUpdateCache.mu.Unlock()
		return msg != "", msg
	}
	panelUpdateCache.mu.Unlock()

	latest, err := FetchLatestPanelRelease("")
	if err != nil || latest == nil || latest.TagName == "" {
		return false, ""
	}

	msg := ""
	if CompareVersions(latest.TagName, panelCurrentVersion) > 0 {
		msg = fmt.Sprintf("面板有新版本 %s 可用，当前版本 %s。建议尽快到面板设置页更新，避免跨多个版本升级。", latest.TagName, panelCurrentVersion)
	}

	panelUpdateCache.mu.Lock()
	panelUpdateCache.lastAt = time.Now()
	panelUpdateCache.latest = latest.TagName
	panelUpdateCache.message = msg
	panelUpdateCache.mu.Unlock()

	return msg != "", msg
}

// 方案 D 阶段四：SQL 注入探测 / 伪装搜索引擎爬虫告警，默认关闭。
// 只做"记录 → 超过阈值提醒管理员"，不自动封禁，最终是否封禁仍由管理员在
// 「安全防御」页面手动决定。
//
// threshold / window 通过 security_settings 表中的 alert_wp_security_threshold
// 与 alert_wp_security_window_hours 配置（审核优化项 3.1）；这里的常量仅作为
// DB 读取失败或值非法时的 fallback。pathLimit / maxOffenders 是展示细节，
// 不对用户开放，保持硬编码。
const (
	defaultWPSecurityAlertThreshold = 10
	defaultWPSecurityAlertWindow    = 24 * time.Hour
	wpSecurityAlertPathLimit        = 3
	wpSecurityAlertMaxOffenders     = 10
)

// wpSecurityAlertConfig 汇总一次告警判定所需的全部参数。
type wpSecurityAlertConfig struct {
	threshold    int
	window       time.Duration
	pathLimit    int
	maxOffenders int
}

// getWPSecurityAlertConfig 从 security_settings 读取阈值与窗口，DB 不可用或
// 值非法时回退到包级默认值。每次告警判定（最多每分钟一次）调用一次，无需缓存。
func getWPSecurityAlertConfig() wpSecurityAlertConfig {
	cfg := wpSecurityAlertConfig{
		threshold:    defaultWPSecurityAlertThreshold,
		window:       defaultWPSecurityAlertWindow,
		pathLimit:    wpSecurityAlertPathLimit,
		maxOffenders: wpSecurityAlertMaxOffenders,
	}
	db := database.GetDB()
	if db == nil {
		return cfg
	}
	var s string
	if db.QueryRow("SELECT svalue FROM security_settings WHERE skey = 'alert_wp_security_threshold'").Scan(&s) == nil {
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= 10000 {
			cfg.threshold = n
		}
	}
	if db.QueryRow("SELECT svalue FROM security_settings WHERE skey = 'alert_wp_security_window_hours'").Scan(&s) == nil {
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= 168 {
			cfg.window = time.Duration(n) * time.Hour
		}
	}
	return cfg
}

func checkWPSQLiProbeThreshold() (bool, string) {
	return checkWPSecurityEventThreshold(SecurityEventSQLiProbe, "SQL 注入探测")
}

func checkWPFakeSearchBotThreshold() (bool, string) {
	return checkWPSecurityEventThreshold(SecurityEventFakeSearchBot, "伪装搜索引擎爬虫")
}

func checkWPSecurityEventThreshold(eventType, label string) (bool, string) {
	cfg := getWPSecurityAlertConfig()
	since := time.Now().UTC().Add(-cfg.window)
	offenders, err := topWPSecurityOffenders(eventType, since, cfg.threshold, cfg.pathLimit)
	if err != nil || len(offenders) == 0 {
		return false, ""
	}

	// 分布式扫描可能同时有几十上百个 IP 超过阈值，不截断的话邮件正文会膨胀到
	// 几百 KB，Webhook 走 URL 路径段的渠道（如 Bark）还会直接投递失败。
	// topWPSecurityOffenders 已按次数降序排列，只取影响最大的前 N 个即可。
	omitted := 0
	if len(offenders) > cfg.maxOffenders {
		omitted = len(offenders) - cfg.maxOffenders
		offenders = offenders[:cfg.maxOffenders]
	}

	// 邮件正文按单个 <p> 段落渲染，换行符不会被转成 <br>，因此和其余告警规则一样
	// 使用「；」分隔多条信息，避免所有 IP 挤成一整行无法阅读。
	entries := make([]string, 0, len(offenders))
	for _, o := range offenders {
		paths := "（无样本）"
		if len(o.Paths) > 0 {
			paths = strings.Join(o.Paths, "、")
		}
		entries = append(entries, fmt.Sprintf("%s（%d 次，热门路径：%s）", o.IP, o.Count, paths))
	}
	suffix := "。"
	if omitted > 0 {
		suffix = fmt.Sprintf("（还有 %d 个 IP 未列出，请到「安全防御」页面查看完整列表）。", omitted)
	}
	msg := fmt.Sprintf("过去 %d 小时内以下 IP 触发「%s」次数达到阈值（%d 次）：%s%s面板仅记录、不自动封禁，请结合 IP 来源在「安全防御」页面手动决定是否封禁。",
		int(cfg.window.Hours()), label, cfg.threshold, strings.Join(entries, "；"), suffix)
	return true, msg
}

func getPanelTitle() string {
	db := database.GetDB()
	if db == nil {
		return "WP Panel"
	}
	var title string
	db.QueryRow("SELECT svalue FROM security_settings WHERE skey = 'panel_title'").Scan(&title)
	if title == "" {
		return "WP Panel"
	}
	return title
}
