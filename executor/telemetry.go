package executor

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
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

// StartTelemetry 启动匿名统计：立即上报一次，此后每 24 小时上报一次。
// 上报内容仅含匿名 ID（machine-id 的 SHA256 前 16 字节）和面板版本号。
func StartTelemetry(version string) {
	telemetryVersion = version

	if !isTelemetryEnabled() {
		log.Println("[遥测] 匿名统计已关闭，跳过")
		return
	}

	go func() {
		sendHeartbeat()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			sendHeartbeat()
		}
	}()
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
