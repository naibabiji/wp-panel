package handlers

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/naibabiji/wp-panel/config"
)

func writeSettingsConfigFixture(t *testing.T, path, username string) {
	t.Helper()
	content := []byte(`{"basic_auth":{"username":"` + username + `","password_hash":"old-hash"},"custom":{"preserved":"yes"}}`)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsBasicAuthUsesConfiguredPath(t *testing.T) {
	root := t.TempDir()
	customPath := filepath.Join(root, "custom-config.json")
	decoyPath := filepath.Join(root, "default-config.json")
	writeSettingsConfigFixture(t, customPath, "custom-old")
	writeSettingsConfigFixture(t, decoyPath, "default-unchanged")
	decoyBefore, err := os.ReadFile(decoyPath)
	if err != nil {
		t.Fatal(err)
	}

	oldCfg := config.AppConfig
	config.AppConfig = &config.Config{}
	t.Cleanup(func() { config.AppConfig = oldCfg })

	handler := &SettingsHandler{ConfigPath: customPath}
	recorder := updateSystemSettingWithHandler(t, handler, `{"basic_auth_user":"custom-new"}`)
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := readConfigValue(customPath, "basic_auth", "username"); got != "custom-new" {
		t.Fatalf("custom username=%q", got)
	}
	decoyAfter, err := os.ReadFile(decoyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoyBefore, decoyAfter) {
		t.Fatal("unrelated default config was modified")
	}
}

func TestSettingsConfigPathDefaultsForExistingCallers(t *testing.T) {
	if got := new(SettingsHandler).configPath(); got != defaultPanelConfigPath {
		t.Fatalf("default config path=%q", got)
	}
}
