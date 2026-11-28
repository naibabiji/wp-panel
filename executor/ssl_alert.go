package executor

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

const sslAlertKey = "alert_ssl"

type sslAlertSite struct {
	id                 int
	domain, renewError string
}

type sslAlertEvent struct {
	Fingerprint    string
	Stage          int
	MailTarget     string
	WebhookTarget  string
	MailPending    string
	WebhookPending string
	RetryAfter     time.Time
	Recovery       bool
	LogIDs         []int64
}

var sslAlertScan struct {
	sync.Mutex
	next time.Time
}

func runPublishedSSLAlerts() {
	if !sslAlertScan.TryLock() {
		return
	}
	defer sslAlertScan.Unlock()
	now := time.Now()
	if now.Before(sslAlertScan.next) {
		return
	}
	sslAlertScan.next = now.Add(10 * time.Minute)

	rule := alertRule{key: sslAlertKey}
	loadAlertRuntimeState([]*alertRule{&rule})
	events := map[int]sslAlertEvent{}
	// The old rule stored a plain-text aggregate. It carried no per-site receipts.
	var legacy string
	if err := json.Unmarshal([]byte(rule.lastAlertMsg), &events); err != nil && rule.firing {
		legacy = rule.lastAlertMsg
	}
	if events == nil {
		events = map[int]sslAlertEvent{}
	}
	if !isRuleEnabled(sslAlertKey) {
		disableAlertRule(&rule)
		if rule.lastAlertMsg != "" {
			rule.lastAlertMsg = ""
			_ = persistAlertRuntimeState(&rule)
		}
		return
	}

	db := database.GetDB()
	rows, err := db.Query(`SELECT id,domain,COALESCE(ssl_last_error,'') FROM websites
		WHERE ssl_enabled=1 AND status='active' ORDER BY id`)
	if err != nil {
		reportAlertStateError("读取证书告警站点失败: %v", err)
		return
	}
	var sites []sslAlertSite
	for rows.Next() {
		var site sslAlertSite
		if err := rows.Scan(&site.id, &site.domain, &site.renewError); err != nil {
			rows.Close()
			return
		}
		sites = append(sites, site)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}

	valid := make(map[int]bool, len(sites))
	certs := make([]*x509.Certificate, len(sites))
	var wg sync.WaitGroup
	for worker := range min(8, len(sites)) {
		wg.Go(func() {
			for i := worker; i < len(sites); i += min(8, len(sites)) {
				certs[i] = readOriginLeafCertificate(sites[i].domain)
			}
		})
	}
	wg.Wait()

	for i, site := range sites {
		valid[site.id] = true
		if _, exists := events[site.id]; !exists && legacy != "" {
			if stage := legacySSLStage(site.domain, legacy); stage != 0 {
				events[site.id] = sslAlertEvent{Stage: stage}
			}
		}
		if certs[i] != nil && isRuleEnabled(sslAlertKey) {
			evaluatePublishedSSL(sites[i], certs[i], now, events)
		}
	}
	for id, event := range events {
		if !valid[id] {
			resolveSSLAlertHistory(event)
			delete(events, id)
		}
	}
	raw, err := json.Marshal(events)
	if err != nil {
		return
	}
	if rule.lastAlertMsg == string(raw) {
		return
	}
	rule.firing = false
	for _, event := range events {
		if !event.Recovery {
			rule.firing = true
			break
		}
	}
	rule.lastAlertMsg = string(raw)
	if !rule.firing {
		_, _ = db.Exec("UPDATE alert_log SET resolved=1 WHERE alert_type=? AND resolved=0", sslAlertKey)
	}
	_ = persistAlertRuntimeState(&rule)
}

func readOriginLeafCertificate(domain string) *x509.Certificate {
	// Observe the certificate actually loaded by local Nginx. Expired certificates
	// must be observable, and the hostname check rejects an unrelated default vhost.
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", "127.0.0.1:443",
		&tls.Config{ServerName: domain, InsecureSkipVerify: true})
	if err != nil {
		return nil
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 || certs[0].VerifyHostname(domain) != nil {
		return nil
	}
	return certs[0]
}

func sslAlertStage(expires, now time.Time, renewalFailed bool) int {
	remaining := expires.Sub(now)
	switch {
	case remaining <= 0:
		return 3
	case renewalFailed && remaining <= 3*24*time.Hour:
		return 2
	case remaining <= 14*24*time.Hour:
		return 1
	default:
		return 0
	}
}

func legacySSLStage(domain, message string) int {
	for _, item := range strings.Split(message, "；") {
		text, ok := strings.CutPrefix(item, domain+" 证书")
		if !ok {
			continue
		}
		if strings.HasPrefix(text, "已过期") {
			return 3
		}
		return 1
	}
	return 0
}

func evaluatePublishedSSL(site sslAlertSite, cert *x509.Certificate, now time.Time, events map[int]sslAlertEvent) {
	if now.Before(cert.NotBefore) {
		return
	}
	fingerprint := notificationTarget(string(cert.Raw))
	old := events[site.id]
	stage := sslAlertStage(cert.NotAfter, now, strings.TrimSpace(site.renewError) != "")
	if old.Recovery {
		if stage == 0 {
			if now.Before(old.RetryAfter) {
				return
			}
			sendSSLAlert("证书已恢复", site.domain+" 已发布新的有效证书。", &old, false, false)
			if old.MailPending != "" || old.WebhookPending != "" {
				old.RetryAfter = now.Add(time.Hour)
				events[site.id] = old
			} else {
				delete(events, site.id)
			}
			return
		}
		delete(events, site.id)
		old = sslAlertEvent{}
	}
	if old.Stage != 0 && old.Fingerprint == "" {
		if stage == 0 {
			delete(events, site.id)
			return
		}
		old.Fingerprint = fingerprint
		events[site.id] = old
	}
	if old.Fingerprint != "" && old.Fingerprint != fingerprint {
		resolveSSLAlertHistory(old)
		if stage == 0 {
			detail := site.domain + " 已发布新的有效证书。"
			logAlertEvent(sslAlertKey, "info", detail, "")
			sendSSLAlert("证书已恢复", detail, &old, false, true)
			if old.MailPending != "" || old.WebhookPending != "" {
				old.Stage, old.Recovery, old.RetryAfter = 0, true, now.Add(time.Hour)
				events[site.id] = old
				return
			}
		}
		delete(events, site.id)
		old = sslAlertEvent{}
	}
	if stage == 0 || stage < old.Stage {
		return
	}
	fresh := stage > old.Stage
	if !fresh && (old.RetryAfter.IsZero() || now.Before(old.RetryAfter)) {
		return
	}
	old.Fingerprint, old.Stage = fingerprint, stage
	reason := []string{"", "证书即将到期", "自动续期未成功且证书接近到期", "证书已经过期"}[stage]
	detail := fmt.Sprintf("%s %s。到期时间 %s。请打开网站详情的 SSL 管理，检查续期结果或更新证书。", site.domain, reason, cert.NotAfter.Local().Format(time.RFC3339))
	if site.renewError != "" && cloudflareProxyDetector(site.domain) {
		detail += "可能使用 Cloudflare。Full (strict) 模式仍需要有效的源站证书。请检查 ACME 路径，或在 SSL 管理手动上传证书。"
	}
	if fresh {
		if id := logAlertWithAttribution(sslAlertKey, "critical", detail, "", false); id != 0 {
			old.LogIDs = append(old.LogIDs, id)
		}
	}
	sendSSLAlert(reason, detail, &old, true, fresh)
	old.RetryAfter = time.Time{}
	if old.MailPending != "" || old.WebhookPending != "" {
		old.RetryAfter = now.Add(time.Hour)
	}
	events[site.id] = old
}

func sendSSLAlert(title, detail string, event *sslAlertEvent, firing, fresh bool) {
	subject := getPanelTitle() + " " + title
	pendingMail, pendingWebhook := event.MailPending, event.WebhookPending
	event.MailPending, event.WebhookPending = "", ""
	if smtp := GetSMTPConfig(); alertSMTPConfigured(smtp) {
		target := notificationTarget(strings.TrimSpace(smtp.AdminEmail))
		if (fresh && (firing || target == event.MailTarget)) || (!fresh && target == pendingMail) {
			err := SendMail(smtp.AdminEmail, subject, formatEmailHTML(title, detail, "", firing))
			if err == nil && firing {
				event.MailTarget = target
			} else if err != nil {
				event.MailPending = target
			}
		}
	}
	if webhook := GetWebhookConfig(); webhookConfigured(webhook) {
		target := notificationTarget(webhook.Channel + "\x00" + strings.TrimSpace(webhook.URL))
		if (fresh && (firing || target == event.WebhookTarget)) || (!fresh && target == pendingWebhook) {
			err := sendWebhookWithConfig(webhook, subject, detail)
			if err == nil && firing {
				event.WebhookTarget = target
			} else if err != nil {
				event.WebhookPending = target
			}
		}
	}
}

func resolveSSLAlertHistory(event sslAlertEvent) {
	for _, id := range event.LogIDs {
		_, _ = database.GetDB().Exec("UPDATE alert_log SET resolved=1 WHERE id=? AND alert_type=?", id, sslAlertKey)
	}
}
