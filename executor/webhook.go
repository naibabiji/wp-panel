package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
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

func SendWebhook(subject, body string) error {
	cfg := GetWebhookConfig()
	if cfg == nil || cfg.Enabled != "true" || cfg.URL == "" {
		return fmt.Errorf("Webhook 未启用或未配置")
	}

	payload, err := buildPayload(cfg.Channel, subject, body)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 10 * time.Second}
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
	case "bark":
		payload = map[string]interface{}{
			"title": subject,
			"body":  body,
			"group": "WP Panel",
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
	cfg := &WebhookConfig{
		Enabled: "true",
		Channel: channel,
		URL:     url,
	}

	payload, err := buildPayload(cfg.Channel, "WP Panel — 测试消息", "如果您收到这条消息，说明 Webhook 配置正确。")
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 10 * time.Second}
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
