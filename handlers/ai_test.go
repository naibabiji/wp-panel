package handlers

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
	_ "modernc.org/sqlite"
)

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
	if settings.TimeoutSeconds != 60 {
		t.Fatalf("timeout = %d, want 60", settings.TimeoutSeconds)
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
