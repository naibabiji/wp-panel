package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInspectPHPCustomConfigAllowsRelativeExtensionAndFindsPoolOverrides(t *testing.T) {
	oldPath := phpCustomConfigPath
	phpCustomConfigPath = filepath.Join(t.TempDir(), "zz-wppanel-custom.ini")
	t.Cleanup(func() { phpCustomConfigPath = oldPath })

	inspection, err := InspectPHPCustomConfig("extension=imagick.so\nmemory_limit=512M\nallow_url_include=On\n")
	if err != nil {
		t.Fatalf("InspectPHPCustomConfig() error = %v", err)
	}
	if len(inspection.HighRiskPaths) != 0 {
		t.Fatalf("relative extension unexpectedly marked high risk: %#v", inspection.HighRiskPaths)
	}
	if got, want := strings.Join(inspection.PoolOverriddenKeys, ","), "memory_limit,allow_url_include"; got != want {
		t.Fatalf("PoolOverriddenKeys = %q, want %q", got, want)
	}
}

func TestInspectPHPCustomConfigDetectsWritableLoaderPath(t *testing.T) {
	root := t.TempDir()
	loader := filepath.Join(root, "theme", "loader.so")
	if err := os.MkdirAll(filepath.Dir(loader), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loader, []byte("not a real shared object"), 0644); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectPHPCustomConfig("extension=" + loader + "\n")
	if err != nil {
		t.Fatalf("InspectPHPCustomConfig() error = %v", err)
	}
	if len(inspection.HighRiskPaths) != 1 {
		t.Fatalf("HighRiskPaths = %#v, want one risk", inspection.HighRiskPaths)
	}
	if !strings.Contains(strings.Join(inspection.HighRiskPaths[0].Reasons, ";"), "not root-owned") {
		t.Fatalf("risk reasons = %#v, want ownership reason", inspection.HighRiskPaths[0].Reasons)
	}
}

func TestInspectPHPCustomConfigRejectsSymlinkLoader(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "safe.so")
	link := filepath.Join(root, "loader.so")
	if err := os.WriteFile(target, []byte("loader"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectPHPCustomConfig("extension=" + link + "\n"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("InspectPHPCustomConfig() error = %v, want symlink rejection", err)
	}
}

func TestInspectPHPCustomConfigRejectsInvalidContent(t *testing.T) {
	if _, err := InspectPHPCustomConfig(strings.Repeat("x", phpCustomConfigMaxBytes+1)); err == nil {
		t.Fatal("oversized configuration was accepted")
	}
	if _, err := InspectPHPCustomConfig("memory_limit=256M\x00\n"); err == nil {
		t.Fatal("NUL-containing configuration was accepted")
	}
}

func TestIsSupportedPHPExtension(t *testing.T) {
	for _, id := range []string{"imagick", "soap", "bcmath", "gmp", "memcached"} {
		if !IsSupportedPHPExtension(id) {
			t.Fatalf("IsSupportedPHPExtension(%q) = false", id)
		}
	}
	for _, id := range []string{"ldap", "amqp", "mongodb", "php8.3-evil", ""} {
		if IsSupportedPHPExtension(id) {
			t.Fatalf("IsSupportedPHPExtension(%q) = true", id)
		}
	}
}

func TestParsePHPModuleList(t *testing.T) {
	got := parsePHPModuleList("[PHP Modules]\n\nCore\nredis\nRedis\n[Zend Modules]\nZend OPcache\n")
	want := []string{"Core", "Zend OPcache", "redis"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePHPModuleList() = %#v, want %#v", got, want)
	}
}

func TestSavePHPCustomConfigRequiresConfirmationBeforeWriting(t *testing.T) {
	root := t.TempDir()
	oldPath := phpCustomConfigPath
	phpCustomConfigPath = filepath.Join(root, "zz-wppanel-custom.ini")
	t.Cleanup(func() { phpCustomConfigPath = oldPath })
	loader := filepath.Join(root, "loader.so")
	if err := os.WriteFile(loader, []byte("loader"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := SavePHPCustomConfig(context.Background(), "extension="+loader+"\n", false)
	if !errors.Is(err, ErrPHPHighRiskConfirmationRequired) {
		t.Fatalf("SavePHPCustomConfig() error = %v, want confirmation error", err)
	}
	if _, statErr := os.Stat(phpCustomConfigPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("custom config was written before confirmation, stat error = %v", statErr)
	}
}

func TestSavePHPCustomConfigRollsBackOnReloadFailure(t *testing.T) {
	root := t.TempDir()
	oldPath := phpCustomConfigPath
	oldTest := phpFPMConfigTest
	oldReload := phpFPMReload
	phpCustomConfigPath = filepath.Join(root, "zz-wppanel-custom.ini")
	phpFPMConfigTest = func(context.Context) ([]byte, error) { return []byte("ok"), nil }
	reloadCalls := 0
	phpFPMReload = func(context.Context) ([]byte, error) {
		reloadCalls++
		if reloadCalls == 1 {
			return []byte("reload failed"), errors.New("reload failed")
		}
		return nil, nil
	}
	t.Cleanup(func() {
		phpCustomConfigPath = oldPath
		phpFPMConfigTest = oldTest
		phpFPMReload = oldReload
	})
	if err := os.WriteFile(phpCustomConfigPath, []byte("memory_limit=256M\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := SavePHPCustomConfig(context.Background(), "memory_limit=512M\n", false)
	if err == nil || !strings.Contains(err.Error(), "previous configuration was restored") {
		t.Fatalf("SavePHPCustomConfig() error = %v, want restored reload error", err)
	}
	data, readErr := os.ReadFile(phpCustomConfigPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := string(data); got != "memory_limit=256M\n" {
		t.Fatalf("restored config = %q, want old config", got)
	}
}

func TestSavePHPCustomConfigReportsRestoreFailure(t *testing.T) {
	root := t.TempDir()
	oldPath := phpCustomConfigPath
	oldTest := phpFPMConfigTest
	oldReload := phpFPMReload
	phpCustomConfigPath = filepath.Join(root, "zz-wppanel-custom.ini")
	phpFPMConfigTest = func(context.Context) ([]byte, error) { return nil, nil }
	phpFPMReload = func(context.Context) ([]byte, error) { return []byte("reload failed"), errors.New("reload failed") }
	t.Cleanup(func() {
		phpCustomConfigPath = oldPath
		phpFPMConfigTest = oldTest
		phpFPMReload = oldReload
	})
	if err := os.WriteFile(phpCustomConfigPath, []byte("memory_limit=256M\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := SavePHPCustomConfig(context.Background(), "memory_limit=512M\n", false)
	if err == nil || !strings.Contains(err.Error(), "restore failed") {
		t.Fatalf("SavePHPCustomConfig() error = %v, want restore failure", err)
	}
}

func TestSavePHPCustomConfigEmptyRemovesFile(t *testing.T) {
	root := t.TempDir()
	oldPath := phpCustomConfigPath
	oldTest := phpFPMConfigTest
	oldReload := phpFPMReload
	phpCustomConfigPath = filepath.Join(root, "zz-wppanel-custom.ini")
	phpFPMConfigTest = func(context.Context) ([]byte, error) { return nil, nil }
	phpFPMReload = func(context.Context) ([]byte, error) { return nil, nil }
	t.Cleanup(func() {
		phpCustomConfigPath = oldPath
		phpFPMConfigTest = oldTest
		phpFPMReload = oldReload
	})
	if err := os.WriteFile(phpCustomConfigPath, []byte("memory_limit=256M\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := SavePHPCustomConfig(context.Background(), "", false); err != nil {
		t.Fatalf("SavePHPCustomConfig() error = %v", err)
	}
	if _, err := os.Stat(phpCustomConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("custom config still exists, stat error = %v", err)
	}
}
