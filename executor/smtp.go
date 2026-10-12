package executor

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

const smtpOperationTimeout = 15 * time.Second

type SMTPConfig struct {
	Host       string
	Port       string
	Encryption string
	User       string
	Pass       string
	FromEmail  string
	AdminEmail string
}

func GetSMTPConfig() *SMTPConfig {
	return getSMTPConfig(database.GetDB())
}

func getSMTPConfig(db *sql.DB) *SMTPConfig {
	if db == nil {
		return nil
	}
	cfg := &SMTPConfig{}
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'smtp_host'`).Scan(&cfg.Host)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'smtp_port'`).Scan(&cfg.Port)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'smtp_encryption'`).Scan(&cfg.Encryption)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'smtp_user'`).Scan(&cfg.User)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'smtp_pass'`).Scan(&cfg.Pass)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'smtp_from_email'`).Scan(&cfg.FromEmail)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'admin_email'`).Scan(&cfg.AdminEmail)
	return cfg
}

func SendMail(to, subject, body string) error {
	return sendMail(to, subject, body, true)
}

func sendMail(to, subject, body string, protectSystemQuota bool) error {
	cfg := GetSMTPConfig()
	if cfg == nil || cfg.Host == "" || cfg.User == "" || cfg.Pass == "" {
		return fmt.Errorf("SMTP 未配置")
	}
	if to == "" {
		to = cfg.AdminEmail
	}
	if to == "" {
		return fmt.Errorf("管理员邮箱未设置")
	}
	if protectSystemQuota {
		if err := reserveSystemMailAttempt(database.GetDB(), time.Now()); err != nil {
			return err
		}
	}

	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	from := smtpFromEmail(cfg)
	if from == "" {
		return fmt.Errorf("SMTP 发件邮箱未设置")
	}
	msg := buildMessage(from, to, subject, body)
	ctx, cancel := context.WithTimeout(context.Background(), smtpOperationTimeout)
	defer cancel()
	dialer := &net.Dialer{}

	switch cfg.Encryption {
	case "ssl":
		tlsCfg := &tls.Config{ServerName: cfg.Host}
		rawConn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("连接失败: %w", err)
		}
		defer rawConn.Close()
		_ = rawConn.SetDeadline(time.Now().Add(smtpOperationTimeout))
		conn := tls.Client(rawConn, tlsCfg)
		if err := conn.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("TLS 连接失败: %w", err)
		}
		defer conn.Close()
		client, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return fmt.Errorf("SMTP 客户端创建失败: %w", err)
		}
		defer client.Quit()
		if err := authAndSend(client, cfg, from, to, msg); err != nil {
			return err
		}
	case "none":
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("连接失败: %w", err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(smtpOperationTimeout))
		client, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return fmt.Errorf("SMTP 客户端创建失败: %w", err)
		}
		defer client.Quit()
		if err := authAndSend(client, cfg, from, to, msg); err != nil {
			return err
		}
	default: // starttls
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("连接失败: %w", err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(smtpOperationTimeout))
		client, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return fmt.Errorf("SMTP 客户端创建失败: %w", err)
		}
		defer client.Quit()
		if err := client.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
			return fmt.Errorf("STARTTLS 失败: %w", err)
		}
		if err := authAndSend(client, cfg, from, to, msg); err != nil {
			return err
		}
	}
	return nil
}

func reserveSystemMailAttempt(db *sql.DB, now time.Time) error {
	if db == nil {
		return nil
	}
	managedMailReservationMu.Lock()
	defer managedMailReservationMu.Unlock()
	limit := 20
	var rawLimit, rawState string
	_ = db.QueryRow(`SELECT svalue FROM security_settings WHERE skey='managed_mail_system_day_limit'`).Scan(&rawLimit)
	if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
		limit = parsed
	}
	_ = db.QueryRow(`SELECT svalue FROM security_settings WHERE skey='managed_mail_quota_notice_state'`).Scan(&rawState)
	state := managedMailNoticeState{}
	_ = json.Unmarshal([]byte(rawState), &state)
	day := now.Format("2006-01-02")
	if state.Day != day {
		state = managedMailNoticeState{Day: day, Notices: map[string]bool{}}
	}
	if state.Notices == nil {
		state.Notices = map[string]bool{}
	}
	if state.System >= limit {
		return fmt.Errorf("系统告警邮件已达到每日保护上限 %d", limit)
	}
	state.System++
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	result, err := db.Exec(`UPDATE security_settings SET svalue=?,updated_at=CURRENT_TIMESTAMP WHERE skey='managed_mail_quota_notice_state'`, string(encoded))
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return nil
	}
	return nil
}

func authAndSend(client *smtp.Client, cfg *SMTPConfig, from, to, msg string) error {
	auth := smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("认证失败: %w", err)
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	wc, err := client.Data()
	if err != nil {
		return err
	}
	_, err = wc.Write([]byte(msg))
	if err != nil {
		_ = wc.Close()
		return err
	}
	return wc.Close()
}

func smtpFromEmail(cfg *SMTPConfig) string {
	if cfg == nil {
		return ""
	}
	if validManagedMailAddress(cfg.FromEmail) {
		return strings.TrimSpace(cfg.FromEmail)
	}
	// Preserve upgraded panel-alert behavior until the administrator saves the
	// new independent From Email field. Managed website mail still requires the
	// explicit field and never uses this compatibility fallback.
	if validManagedMailAddress(cfg.User) {
		return strings.TrimSpace(cfg.User)
	}
	return ""
}

func buildMessage(from, to, subject, body string) string {
	subject = strings.NewReplacer("\r", "", "\n", "").Replace(subject)
	to = strings.NewReplacer("\r", "", "\n", "").Replace(to)
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		from, to, subject, body)
}

func TestSMTP(to string) error {
	panelTitle := html.EscapeString(getPanelTitle())
	body := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"></head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; padding: 20px; color: #333;">
<p style="font-size: 15px;">如果您收到这封邮件，说明 SMTP 配置正确。</p>
<p style="font-size: 12px; color: #aaa; margin-top: 20px;">— 来自 %s 面板</p>
</body>
</html>`, panelTitle)
	return sendMail(to, getPanelTitle()+" — 测试邮件", body, false)
}
