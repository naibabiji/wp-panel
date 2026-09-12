package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSitePluginIdentityIncludesApplicationPasswordPolicy(t *testing.T) {
	root := useTemporarySiteSecretsRoot(t)
	if err := WriteSitePluginIdentity("example.com", "root", "https://127.0.0.1:8443/panel", strings.Repeat("a", 32), true); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "example.com", sitePluginConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	var identity sitePluginIdentity
	if err := json.Unmarshal(content, &identity); err != nil {
		t.Fatal(err)
	}
	if !identity.DisableApplicationPasswords {
		t.Fatal("application passwords policy missing from site identity")
	}
}

func useTemporarySiteSecretsRoot(t *testing.T) string {
	t.Helper()
	oldRoot := siteSecretsRoot
	siteSecretsRoot = t.TempDir()
	t.Cleanup(func() { siteSecretsRoot = oldRoot })
	return siteSecretsRoot
}

func TestRenderPHPFPMPoolPublishesPluginIdentityPath(t *testing.T) {
	root := useTemporarySiteSecretsRoot(t)
	engine := NewTemplateEngine(t.TempDir())
	rendered, err := engine.RenderPHPFPMPool(&PHPFPMPoolData{
		Domain: "example.com", PoolName: "example", SystemUser: "example",
		WebRoot: "/www/wwwroot/example.com", SocketPath: "/run/php", MaxChildren: "10",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "env[WP_PANEL_CONFIG_PATH] = " + filepath.Join(root, "example.com", sitePluginConfigFileName)
	if !strings.Contains(rendered, want) {
		t.Fatalf("rendered PHP-FPM pool does not contain %q:\n%s", want, rendered)
	}
}

func TestMoveSitePluginIdentityNeverReplacesTarget(t *testing.T) {
	root := useTemporarySiteSecretsRoot(t)
	oldDir := filepath.Join(root, "old.example.com")
	newDir := filepath.Join(root, "new.example.com")
	if err := os.MkdirAll(oldDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, sitePluginConfigFileName), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, sitePluginConfigFileName), []byte("target"), 0600); err != nil {
		t.Fatal(err)
	}

	moved, err := moveSitePluginIdentity("old.example.com", "new.example.com")
	if err == nil || moved {
		t.Fatalf("move should reject an existing target, moved=%v err=%v", moved, err)
	}
	content, readErr := os.ReadFile(filepath.Join(newDir, sitePluginConfigFileName))
	if readErr != nil || string(content) != "target" {
		t.Fatalf("existing target was changed: content=%q err=%v", content, readErr)
	}
}

func TestMoveSitePluginIdentitySupportsMissingAndRollback(t *testing.T) {
	root := useTemporarySiteSecretsRoot(t)
	moved, err := moveSitePluginIdentity("missing.example.com", "new.example.com")
	if err != nil || moved {
		t.Fatalf("missing source should be a no-op, moved=%v err=%v", moved, err)
	}

	oldDir := filepath.Join(root, "old.example.com")
	if err := os.MkdirAll(oldDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, sitePluginConfigFileName), []byte("identity"), 0600); err != nil {
		t.Fatal(err)
	}
	moved, err = moveSitePluginIdentity("old.example.com", "new.example.com")
	if err != nil || !moved {
		t.Fatalf("move failed, moved=%v err=%v", moved, err)
	}
	moved, err = moveSitePluginIdentity("new.example.com", "old.example.com")
	if err != nil || !moved {
		t.Fatalf("rollback move failed, moved=%v err=%v", moved, err)
	}
	if content, err := os.ReadFile(filepath.Join(oldDir, sitePluginConfigFileName)); err != nil || string(content) != "identity" {
		t.Fatalf("identity was not restored: content=%q err=%v", content, err)
	}
}
