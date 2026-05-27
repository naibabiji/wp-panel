package executor

import (
	"fmt"
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
			logAlert(r.key, "critical", msg)
			if hasSMTP {
				go SendMail("", getPanelTitle()+" 告警 — "+alertLabel(r.key), msg)
			}
			if hasWebhook {
				go SendWebhook(getPanelTitle()+" 告警 — "+alertLabel(r.key), msg)
			}
		} else if !firing && r.firing {
			// Transition: alert → normal
			r.firing = false
			recoveryMsg := alertLabel(r.key) + " 已恢复正常"
			logAlert(r.key, "info", recoveryMsg)
			if hasSMTP && time.Since(r.lastFired) > 5*time.Minute {
				go SendMail("", getPanelTitle()+" 恢复通知", recoveryMsg)
			}
			if hasWebhook && time.Since(r.lastFired) > 5*time.Minute {
				go SendWebhook(getPanelTitle()+" 恢复通知", recoveryMsg)
			}
		} else if firing && r.firing {
			// Continuous alert — re-send every 30 min
			if time.Since(r.lastFired) > 30*time.Minute {
				r.lastFired = time.Now()
				logAlert(r.key, "critical", msg)
				if hasSMTP {
					go SendMail("", getPanelTitle()+" 告警 — "+alertLabel(r.key)+"（持续中）", msg)
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
		return "内存使用过高"
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
		return true, fmt.Sprintf("内存使用率 %.1f%%（阈值 90%%），于 %s", v, toLocalTime(ts))
	}
	return false, ""
}

func toLocalTime(dbTime string) string {
	t, err := time.Parse("2006-01-02 15:04:05", dbTime)
	if err != nil {
		return dbTime
	}
	return t.Local().Format("2006-01-02 15:04:05")
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
	// Only alert if auto-backups previously ran but recently stopped
	var totalCount int
	db.QueryRow("SELECT COUNT(*) FROM db_backups WHERE auto = 1").Scan(&totalCount)
	if totalCount == 0 {
		return false, ""
	}
	var recentCount int
	db.QueryRow("SELECT COUNT(*) FROM db_backups WHERE auto = 1 AND created_at > datetime('now', '-1 day')").Scan(&recentCount)
	if recentCount == 0 {
		var enabled int
		db.QueryRow("SELECT COUNT(*) FROM backup_settings WHERE enabled = 1").Scan(&enabled)
		if enabled > 0 {
			return true, "最近 24 小时内没有成功执行的数据库自动备份"
		}
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
	var failCount int
	db.QueryRow(`SELECT COUNT(*) FROM cron_jobs
		WHERE enabled = 1 AND notify_fail = 1 AND running = 0
		AND last_status = 'failed' AND last_run_at > datetime('now', '-1 hour')`).Scan(&failCount)
	if failCount > 0 {
		return true, fmt.Sprintf("近1小时内有 %d 个计划任务执行失败", failCount)
	}
	return false, ""
}

var siteLastCheck = make(map[string]time.Time)

func checkSites() (bool, string) {
	db := database.GetDB()
	rows, err := db.Query(`SELECT id, domain, ssl_enabled, monitoring_interval FROM websites WHERE status = 'active' AND monitoring_enabled = 1`)
	if err != nil {
		return false, ""
	}
	defer rows.Close()

	var msgs []string
	for rows.Next() {
		var id, domain string
		var ssl, interval int
		if rows.Scan(&id, &domain, &ssl, &interval) != nil {
			continue
		}
		if interval <= 0 {
			interval = 5
		}

		if len(siteLastCheck) > 100 {
			siteLastCheck = make(map[string]time.Time)
		}
		if last, ok := siteLastCheck[id]; ok && time.Since(last) < time.Duration(interval)*time.Minute {
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
			msgs = append(msgs, fmt.Sprintf("%s 无法访问 (%v)", domain, err))
			continue
		}
		code, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		if code < 200 || code >= 400 {
			msgs = append(msgs, fmt.Sprintf("%s 返回 %d", domain, code))
		}
	}
	if len(msgs) > 0 {
		return true, strings.Join(msgs, "；")
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
