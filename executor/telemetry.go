package executor

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

const defaultTelemetryURL = "https://stats.wp-panel.org"

type heartbeatPayload struct {
	AnonymousID string `json:"anonymous_id"`
	Version     string `json:"version"`
}

var telemetryVersion string

// StartTelemetry 启动匿名统计：新装面板立即上报一次，此后每天 UTC 00:00 附近上报。
// 上报内容仅含匿名 ID（machine-id 的 SHA256 前 16 字节）和面板版本号。
func StartTelemetry(version string) {
	telemetryVersion = version

	if !isTelemetryEnabled() {
		log.Println("[遥测] 匿名统计已关闭，跳过")
		return
	}

	go func() {
		// 新装面板（从未成功上报过）立即上报，更新/重启则跳过
		if isFirstHeartbeat() {
			sendHeartbeat()
		}

		// 计算距下一个 UTC 00:00 的间隔，加 ±5 分钟随机抖动
		now := time.Now().UTC()
		midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
		jitter := time.Duration(rand.Intn(600)-300) * time.Second
		waitDur := midnight.Sub(now) + jitter
		if waitDur < 0 {
			waitDur = 0
		}

		<-time.After(waitDur)
		sendHeartbeat()

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			sendHeartbeat()
		}
	}()
}

// isFirstHeartbeat 检查是否从未成功上报过心跳（新装面板）。
func isFirstHeartbeat() bool {
	db := database.GetDB()
	if db == nil {
		return true
	}
	var val string
	err := db.QueryRow("SELECT svalue FROM security_settings WHERE skey = 'telemetry_first_sent'").Scan(&val)
	return err != nil || val == ""
}

func sendHeartbeat() {
	if !isTelemetryEnabled() {
		return
	}

	anonID := generateAnonymousID()
	if anonID == "" {
		return
	}

	url := getTelemetryURL()
	payload := heartbeatPayload{AnonymousID: anonID, Version: telemetryVersion}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url+"/api/heartbeat", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("[遥测] 上报失败: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[遥测] 上报返回非预期状态: %d", resp.StatusCode)
		return
	}

	// 标记首次心跳已发送，后续重启不再立即上报
	db := database.GetDB()
	if db != nil {
		db.Exec("INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('telemetry_first_sent', ?, '首次心跳上报时间')", time.Now().UTC().Format(time.RFC3339))
	}

	log.Println("[遥测] 匿名心跳上报成功")
}

func generateAnonymousID() string {
	data, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		data, err = os.ReadFile("/var/lib/dbus/machine-id")
		if err != nil {
			return ""
		}
	}
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash[:16])
}

func getTelemetryURL() string {
	db := database.GetDB()
	if db == nil {
		return defaultTelemetryURL
	}
	var url string
	db.QueryRow("SELECT svalue FROM security_settings WHERE skey = 'telemetry_url'").Scan(&url)
	if url == "" {
		return defaultTelemetryURL
	}
	return url
}

func isTelemetryEnabled() bool {
	db := database.GetDB()
	if db == nil {
		return true
	}
	var val string
	db.QueryRow("SELECT svalue FROM security_settings WHERE skey = 'telemetry_enabled'").Scan(&val)
	return val != "false"
}
