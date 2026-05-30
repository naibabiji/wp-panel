package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

type WebhookConfig struct {
	Enabled string
	Channel string
	URL     string
}

func GetWebhookConfig() *WebhookConfig {
	db := database.GetDB()
	if db == nil {
		return nil
	}
	cfg := &WebhookConfig{}
	db.QueryRow("SELECT svalue FROM security_settings WHERE skey = 'webhook_enabled'").Scan(&cfg.Enabled)
	db.QueryRow("SELECT svalue FROM security_settings WHERE skey = 'webhook_channel'").Scan(&cfg.Channel)
	db.QueryRow("SELECT svalue FROM security_settings WHERE skey = 'webhook_url'").Scan(&cfg.URL)
	return cfg
}

func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() || ip.IsUnspecified()
}

func isSafeWebhookURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("不支持的协议: %s", u.Scheme)
	}
	host := u.Hostname()
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("无法解析主机名: %s", host)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("不允许的内网地址: %s", ip.String())
		}
	}
	return nil
}

// safeWebhookClient returns an http.Client that checks the destination IP at
// connection time, preventing DNS rebinding attacks that could bypass the
// pre-flight isSafeWebhookURL check.
func safeWebhookClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.LookupIP(host)
				if err != nil {
					return nil, err
				}
				for _, ip := range ips {
					if isBlockedIP(ip) {
						return nil, fmt.Errorf("webhook: 目标 IP 被禁止: %s", ip.String())
					}
				}
				return dialer.DialContext(ctx, network, addr)
			},
		},
	}
}

func SendWebhook(subject, body string) error {
	cfg := GetWebhookConfig()
	if cfg == nil || cfg.Enabled != "true" || cfg.URL == "" {
		return fmt.Errorf("Webhook 未启用或未配置")
	}

	if err := isSafeWebhookURL(cfg.URL); err != nil {
		return fmt.Errorf("Webhook URL 不安全: %w", err)
	}

	client := safeWebhookClient()

	if cfg.Channel == "bark" {
		return sendBark(client, cfg.URL, subject, body)
	}

	payload, err := buildPayload(cfg.Channel, subject, body)
	if err != nil {
		return err
	}

	resp, err := client.Post(cfg.URL, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("Webhook 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("Webhook 返回错误状态: %d", resp.StatusCode)
	}
	return nil
}

func sendBark(client *http.Client, baseURL, title, body string) error {
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("Bark URL 格式错误: %w", err)
	}
	u = u.JoinPath(url.PathEscape(title), url.PathEscape(body))
	resp, err := client.Get(u.String())
	if err != nil {
		return fmt.Errorf("Bark 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("Bark 返回错误状态: %d", resp.StatusCode)
	}
	return nil
}

func buildPayload(channel, subject, body string) ([]byte, error) {
	content := subject
	if body != "" {
		content = subject + "\n" + body
	}

	var payload map[string]interface{}

	switch channel {
	case "wecom":
		payload = map[string]interface{}{
			"msgtype": "text",
			"text": map[string]string{
				"content": content,
			},
		}
	case "dingtalk":
		payload = map[string]interface{}{
			"msgtype": "text",
			"text": map[string]string{
				"content": content,
			},
		}
	case "feishu":
		payload = map[string]interface{}{
			"msg_type": "text",
			"content": map[string]string{
				"text": content,
			},
		}
	case "serverchan":
		payload = map[string]interface{}{
			"title": subject,
			"desp":  body,
		}
	case "custom":
		payload = map[string]interface{}{
			"title":   subject,
			"content": body,
			"time":    time.Now().Format("2006-01-02 15:04:05"),
		}
	default:
		return nil, fmt.Errorf("不支持的推送渠道: %s", channel)
	}

	return json.Marshal(payload)
}

func TestWebhook(channel, url string) error {
	if err := isSafeWebhookURL(url); err != nil {
		return fmt.Errorf("Webhook URL 不安全: %w", err)
	}

	title := getPanelTitle() + " — 测试消息"
	msg := "如果您收到这条消息，说明 Webhook 配置正确。"

	client := safeWebhookClient()

	if channel == "bark" {
		return sendBark(client, url, title, msg)
	}

	payload, err := buildPayload(channel, title, msg)
	if err != nil {
		return err
	}

	resp, err := client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("Webhook 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("Webhook 返回错误状态: %d", resp.StatusCode)
	}
	return nil
}
