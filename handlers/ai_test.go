package handlers

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"
	_ "modernc.org/sqlite"
)

func TestDiagnoseRejectsLogAnalysisWithoutLogContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/websites/1/ai/diagnose", bytes.NewBufferString(`{"symptom":"log_analysis"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	(&AIHandler{}).Diagnose(c)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "日志分析页面") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLoadAISessionDetailRejectsCrossSiteSession(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ai_sessions (
		id INTEGER PRIMARY KEY, site_id INTEGER NOT NULL, symptom TEXT NOT NULL, status TEXT NOT NULL,
		risk_level TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '', report_json TEXT NOT NULL DEFAULT '',
		raw_text TEXT NOT NULL DEFAULT '', error_message TEXT NOT NULL DEFAULT '', prompt_chars INTEGER NOT NULL DEFAULT 0,
		response_chars INTEGER NOT NULL DEFAULT 0, context_type TEXT NOT NULL DEFAULT '', context_id INTEGER NOT NULL DEFAULT 0,
		context_json TEXT NOT NULL DEFAULT '', focus_kind TEXT NOT NULL DEFAULT '', focus_value TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ai_sessions(id,site_id,symptom,status) VALUES(10,1,'site_500','completed')`); err != nil {
		t.Fatal(err)
	}
	oldDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = oldDB })
	if _, err := loadAISessionDetail(2, 10); err != sql.ErrNoRows {
		t.Fatalf("cross-site session error=%v, want sql.ErrNoRows", err)
	}
}

func TestAIUserErrorSuggestsLogDownloadOnlyForLargeLogTimeout(t *testing.T) {
	err := &executor.AIProviderError{Type: "timeout", Message: "timeout"}
	large := aiUserErrorWithContext("zh-CN", err, 9000, true)
	if !strings.Contains(large, "下载对应日志文件") || !strings.Contains(large, "网页端 AI") {
		t.Fatalf("large log timeout message = %q", large)
	}
	if got := aiUserErrorWithContext("zh-CN", err, 9000, false); strings.Contains(got, "下载") {
		t.Fatalf("non-log timeout should not suggest download: %q", got)
	}
	if got := aiUserErrorWithContext("zh-CN", errors.New("network"), 9000, true); strings.Contains(got, "下载") {
		t.Fatalf("non-provider timeout should not suggest download: %q", got)
	}
}

func TestNormalizeAISettingsDefaultsDeepSeekV4Pro(t *testing.T) {
	settings, err := normalizeAISettingsRequest(models.AISettingsRequest{
		Enabled: true,
	}, false, "zh-CN")
	if err != nil {
		t.Fatalf("normalizeAISettingsRequest() error = %v", err)
	}
	if settings.Provider != "deepseek" {
		t.Fatalf("provider = %q, want deepseek", settings.Provider)
	}
	if settings.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("base url = %q, want DeepSeek base url", settings.BaseURL)
	}
	if settings.Model != "deepseek-v4-pro" {
		t.Fatalf("model = %q, want deepseek-v4-pro", settings.Model)
	}
	if settings.TimeoutSeconds != 180 {
		t.Fatalf("timeout = %d, want 180", settings.TimeoutSeconds)
	}
}

func TestNormalizeAISettingsRejectsUnknownProvider(t *testing.T) {
	_, err := normalizeAISettingsRequest(models.AISettingsRequest{Provider: "bad"}, false, "zh-CN")
	if err == nil {
		t.Fatal("expected unknown provider to be rejected")
	}
}

func TestNormalizeAISettingsCapsTimeout(t *testing.T) {
	settings, err := normalizeAISettingsRequest(models.AISettingsRequest{TimeoutSeconds: 999}, false, "zh-CN")
	if err != nil {
		t.Fatalf("normalizeAISettingsRequest() error = %v", err)
	}
	if settings.TimeoutSeconds != aiProviderMaxTimeoutSeconds {
		t.Fatalf("timeout = %d, want %d", settings.TimeoutSeconds, aiProviderMaxTimeoutSeconds)
	}
}

func TestMaskAIKey(t *testing.T) {
	if got := maskAIKey("sk-1234567890"); got != "sk-1...7890" {
		t.Fatalf("maskAIKey() = %q", got)
	}
	if got := maskAIKey(""); got != "" {
		t.Fatalf("empty mask = %q, want empty", got)
	}
}

func TestAIMessagesPersistAndListNewestInAscendingOrder(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ai_messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id INTEGER NOT NULL,
		role TEXT NOT NULL DEFAULT '',
		content TEXT NOT NULL DEFAULT '',
		prompt_chars INTEGER NOT NULL DEFAULT 0,
		response_chars INTEGER NOT NULL DEFAULT 0,
		error_message TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatal(err)
	}
	oldDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = oldDB })

	for i := 1; i <= 5; i++ {
		if _, err := createAIMessage(10, "user", fmt.Sprintf("msg-%d", i), 0, 0, ""); err != nil {
			t.Fatalf("createAIMessage(%d): %v", i, err)
		}
	}
	messages, err := listAIMessages(10, 3)
	if err != nil {
		t.Fatalf("listAIMessages() error = %v", err)
	}
	got := []string{}
	for _, msg := range messages {
		got = append(got, msg.Content)
	}
	want := []string{"msg-3", "msg-4", "msg-5"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("messages = %v, want %v", got, want)
	}
}
