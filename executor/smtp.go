package executor

import (
	"crypto/tls"
	"fmt"
	"html"
	"net"
	"net/smtp"
	"strings"

	"github.com/naibabiji/wp-panel/database"
)

type SMTPConfig struct {
	Host       string
	Port       string
	Encryption string
	User       string
	Pass       string
	AdminEmail string
}

func GetSMTPConfig() *SMTPConfig {
	db := database.GetDB()
	if db == nil {
		return nil
	}
	cfg := &SMTPConfig{}
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'smtp_host'`).Scan(&cfg.Host)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'smtp_port'`).Scan(&cfg.Port)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'smtp_encryption'`).Scan(&cfg.Encryption)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'smtp_user'`).Scan(&cfg.User)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'smtp_pass'`).Scan(&cfg.Pass)
	db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'admin_email'`).Scan(&cfg.AdminEmail)
	return cfg
}

func SendMail(to, subject, body string) error {
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

	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	msg := buildMessage(cfg.User, to, subject, body)

	switch cfg.Encryption {
	case "ssl":
		tlsCfg := &tls.Config{ServerName: cfg.Host}
		conn, err := tls.Dial("tcp", addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("TLS 连接失败: %w", err)
		}
		defer conn.Close()
		client, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return fmt.Errorf("SMTP 客户端创建失败: %w", err)
		}
		defer client.Quit()
		if err := authAndSend(client, cfg, to, msg); err != nil {
			return err
		}
	case "none":
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			return fmt.Errorf("连接失败: %w", err)
		}
		defer conn.Close()
		client, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return fmt.Errorf("SMTP 客户端创建失败: %w", err)
		}
		defer client.Quit()
		if err := authAndSend(client, cfg, to, msg); err != nil {
			return err
		}
	default: // starttls
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			return fmt.Errorf("连接失败: %w", err)
		}
		defer conn.Close()
		client, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return fmt.Errorf("SMTP 客户端创建失败: %w", err)
		}
		defer client.Quit()
		if err := client.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
			return fmt.Errorf("STARTTLS 失败: %w", err)
		}
		if err := authAndSend(client, cfg, to, msg); err != nil {
			return err
		}
	}
	return nil
}

func authAndSend(client *smtp.Client, cfg *SMTPConfig, to, msg string) error {
	auth := smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("认证失败: %w", err)
	}
	if err := client.Mail(cfg.User); err != nil {
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
	wc.Close()
	return err
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
	return SendMail(to, getPanelTitle()+" — 测试邮件", body)
}
