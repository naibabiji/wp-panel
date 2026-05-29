package executor

import (
	"fmt"
	"html"
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

var alertMgr = &alertManager{stopCh: make(chan struct{})}

func StartAlertMonitor() {
	alertMgr.rules = []*alertRule{
		{key: "alert_cpu", checkFn: checkCPU, thresholdDuration: 5 * time.Minute},
		{key: "alert_memory", checkFn: checkMemory, thresholdDuration: 5 * time.Minute},
		{key: "alert_disk", checkFn: checkDisk},
		{key: "alert_service", checkFn: checkService},
		{key: "alert_ssl", checkFn: checkSSL},
		{key: "alert_backup", checkFn: checkBackup},
		{key: "alert_website_expiry", checkFn: checkWebsiteExpiry},
		{key: "alert_remote_backup", checkFn: checkRemoteBackup},
		{key: "alert_cron_fail", checkFn: checkCronFail},
		{key: "alert_site", checkFn: checkSites},
		{key: "alert_system_update", checkFn: checkSystemUpdate},
	}
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
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg := GetSMTPConfig()
	hasSMTP := cfg != nil && cfg.Host != "" && cfg.AdminEmail != ""

	wCfg := GetWebhookConfig()
	hasWebhook := wCfg != nil && wCfg.Enabled == "true" && wCfg.URL != ""

	for _, r := range m.rules {
		if !isRuleEnabled(r.key) {
			r.firing = false
			r.pendingSince = time.Time{}
			continue
		}

		instantFiring, msg := r.checkFn()
		now := time.Now()
		firing := r.sustainedFiring(instantFiring, now)
		if firing && !r.firing {
			// Transition: normal → alert
			r.firing = true
			r.lastFired = now
			r.lastAlertMsg = msg
			logAlert(r.key, "critical", msg)
			if hasSMTP {
				go SendMail("", getPanelTitle()+" 告警 — "+alertLabel(r.key), formatEmailHTML(alertLabel(r.key), msg, getEmailTip(r.key, false), true))
			}
			if hasWebhook {
				go SendWebhook(getPanelTitle()+" 告警 — "+alertLabel(r.key), msg)
			}
		} else if !firing && r.firing {
			// Transition: alert → normal
			r.firing = false
			recoveryDetail := buildRecoveryDetail(r)
			logAlert(r.key, "info", recoveryDetail)
			// 即时告警（无阈值）直接发送恢复通知，有阈值的等 5 分钟防抖
			sendRecovery := time.Since(r.lastFired) > 5*time.Minute || r.thresholdDuration <= 0
			if hasSMTP && sendRecovery {
				go SendMail("", getPanelTitle()+" 恢复通知", formatEmailHTML(alertLabel(r.key)+" 已恢复正常", recoveryDetail, getEmailTip(r.key, true), false))
			}
			if hasWebhook && sendRecovery {
				go SendWebhook(getPanelTitle()+" 恢复通知", recoveryDetail)
			}
		} else if firing && r.firing {
			r.lastAlertMsg = msg
			// Continuous alert — re-send on each rule's interval.
			if time.Since(r.lastFired) > alertResendInterval(r.key) {
				r.lastFired = time.Now()
				logAlert(r.key, "critical", msg)
				if hasSMTP {
					go SendMail("", getPanelTitle()+" 告警 — "+alertLabel(r.key)+"（持续中）", formatEmailHTML(alertLabel(r.key)+"（持续中）", msg, getEmailTip(r.key, false), true))
				}
				if hasWebhook {
					go SendWebhook(getPanelTitle()+" 告警 — "+alertLabel(r.key)+"（持续中）", msg)
				}
			}
		}
	}
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
	if key == "alert_system_update" {
		return 24 * time.Hour
	}
	return 30 * time.Minute
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
	}
	return key
}

func logAlert(alertType, level, message string) {
	db := database.GetDB()
	if db == nil {
		return
	}
	db.Exec("INSERT INTO alert_log (alert_type, level, message) VALUES (?, ?, ?)", alertType, level, message)
	// Keep last 30
	db.Exec("DELETE FROM alert_log WHERE id NOT IN (SELECT id FROM alert_log ORDER BY id DESC LIMIT 30)")
}

func getEmailTip(key string, isRecovery bool) string {
	switch key {
	case "alert_cpu":
		return "小提示：CPU 持续高负载可能是流量增长或被攻击的信号，建议登录面板查看实时趋势图。"
	case "alert_memory":
		return "小提示：内存不足可能是 PHP 进程或 Redis 占用过高，也可能是恶意爬虫大量请求所致，建议登录面板查看访问日志，排查异常流量。"
	case "alert_disk":
		return "小提示：优先清理旧备份文件和日志通常能快速释放大量空间，比升级硬盘更实际。"
	case "alert_service":
		if isRecovery {
			return "小提示：问题解决后建议回顾日志，了解根因有助于预防再次发生。"
		}
		return "小提示：服务会自动尝试重启，若反复告警请登录面板查看对应日志排查根因。"
	case "alert_ssl":
		if isRecovery {
			return "小提示：建议在日历中标注下次到期日，提前 30 天续签更从容。"
		}
		return "小提示：证书过期会导致浏览器「不安全」警告，影响访客信任和 SEO，建议尽快续签。"
	case "alert_backup":
		return "小提示：养成定期备份网站的好习惯，数据安全有备无患。"
	case "alert_website_expiry":
		if isRecovery {
			return "小提示：养成定期备份网站的好习惯，数据安全有备无患。"
		}
		return "小提示：请及时提醒网站用户续费或备份数据，到期后网站将无法访问。"
	case "alert_remote_backup":
		return "小提示：养成定期备份网站的好习惯，数据安全有备无患。"
	case "alert_cron_fail":
		if isRecovery {
			return "小提示：问题解决后建议回顾日志，了解根因有助于预防再次发生。"
		}
		return "小提示：计划任务失败可能是脚本错误或资源不足，建议查看执行日志定位原因。"
	case "alert_site":
		if isRecovery {
			return "小提示：建议确认网站已可正常访问，并将此次故障情况同步给网站用户。"
		}
		return "小提示：请尽快排查服务器状态、域名解析和网站程序是否正常，避免长时间离线影响用户业务。"
	case "alert_system_update":
		if isRecovery {
			return "小提示：建议定期保持系统更新，这是维护服务器安全最简单有效的方式。"
		}
		return "小提示：建议尽快登录面板设置页执行系统更新。安全更新通常修复已知漏洞，延迟更新会增加被攻击风险。"
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
	rows, err := db.Query(`SELECT domain, ssl_expires_at FROM websites WHERE ssl_enabled = 1 AND ssl_expires_at > datetime('now')`)
	if err != nil {
		return false, ""
	}
	defer rows.Close()

	var msgs []string
	now := time.Now()
	for rows.Next() {
		var domain string
		var expiresAt time.Time
		if rows.Scan(&domain, &expiresAt) != nil {
			continue
		}
		days := int(expiresAt.Sub(now).Hours() / 24)
		if days <= 14 {
			msgs = append(msgs, fmt.Sprintf("%s 证书 %d 天后到期", domain, days))
		}
	}
	if len(msgs) > 0 {
		return true, strings.Join(msgs, "；")
	}
	return false, ""
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
			AND b.created_at > datetime('now', '-1 day')
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
	defer rows.Close()

	var msgs []string
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
		// 检查此域名今天是否已告警过
		var alerted int
		db.QueryRow(`SELECT COUNT(*) FROM alert_log
			WHERE alert_type = 'website_expiry' AND message LIKE ? AND created_at > datetime('now', '-24 hours')`,
			domain+"%").Scan(&alerted)
		if alerted > 0 {
			continue
		}
		msgs = append(msgs, fmt.Sprintf("%s %d 天后到期", domain, days))
	}
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

	// 检查最近1小时的同步日志是否有失败记录
	var failCount int
	db.QueryRow(`SELECT COUNT(*) FROM operation_logs
		WHERE operation = '远程备份' AND message LIKE '远程同步失败%'
		AND created_at > datetime('now', '-1 hour')`).Scan(&failCount)
	if failCount > 0 {
		return true, fmt.Sprintf("近1小时内有 %d 次远程备份同步失败", failCount)
	}
	return false, ""
}

func checkCronFail() (bool, string) {
	db := database.GetDB()
	rows, err := db.Query(`SELECT name FROM cron_jobs
		WHERE enabled = 1 AND notify_fail = 1 AND running = 0
		AND last_status = 'failed' AND last_run_at > datetime('now', '-1 hour')`)
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

	var msgs []string
	seen := make(map[string]bool)
	for rows.Next() {
		var id, domain string
		var ssl, interval int
		if rows.Scan(&id, &domain, &ssl, &interval) != nil {
			continue
		}
		seen[id] = true
		if interval <= 0 {
			interval = 5
		}

		if last, ok := siteLastCheck[id]; ok && time.Since(last) < time.Duration(interval)*time.Minute {
			if msg, ok := siteFailureMessages[id]; ok && siteFailureCounts[id] >= siteFailureAlertThreshold {
				msgs = append(msgs, msg)
			}
			continue
		}
		siteLastCheck[id] = time.Now()

		proto := "http"
		if ssl == 1 {
			proto = "https"
		}
		url := proto + "://" + domain + "/?wp_hc=" + strconv.FormatInt(time.Now().Unix(), 10)
		out, err := exec.Command("curl", "-k", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--max-time", "10", "-A", "WP-Panel-HealthCheck/1.0", url).Output()
		if err != nil {
			msg := fmt.Sprintf("%s 无法访问 (%v)", domain, err)
			siteFailureMessages[id] = msg
			siteFailureCounts[id]++
			if siteFailureCounts[id] >= siteFailureAlertThreshold {
				msgs = append(msgs, msg)
			}
			continue
		}
		code, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		if code < 200 || code >= 400 {
			msg := fmt.Sprintf("%s 返回 %d", domain, code)
			siteFailureMessages[id] = msg
			siteFailureCounts[id]++
			if siteFailureCounts[id] >= siteFailureAlertThreshold {
				msgs = append(msgs, msg)
			}
		} else {
			delete(siteFailureMessages, id)
			delete(siteFailureCounts, id)
		}
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

func ClearSystemUpdateAlertCache() {
	sysUpdateCache.mu.Lock()
	sysUpdateCache.lastAt = time.Time{}
	sysUpdateCache.names = nil
	sysUpdateCache.mu.Unlock()
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
