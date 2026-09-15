package executor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/models"
)

func TestApplyWordPressDBPasswordChangeSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wp-config.php")
	site := &models.Website{DBUser: "site_user"}
	changed := false

	result := applyWordPressDBPasswordChange(path, []byte("old config"), []byte("new config"), site, "new-password", &config.Config{}, os.WriteFile,
		func(user, password string, _ *config.Config) error {
			changed = user == "site_user" && password == "new-password"
			return nil
		})
	if !result.Success || !changed {
		t.Fatalf("unexpected result or database call: result=%+v changed=%v", result, changed)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new config" {
		t.Fatalf("new config was not written: got %q", got)
	}
}

func TestApplyWordPressDBPasswordChangeRestoresConfigOnDatabaseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wp-config.php")
	oldContent := []byte("old config")
	newContent := []byte("new config")
	site := &models.Website{DBUser: "site_user"}

	result := applyWordPressDBPasswordChange(path, oldContent, newContent, site, "new-password", &config.Config{}, os.WriteFile,
		func(string, string, *config.Config) error { return errors.New("database failed") })
	if result.Success || result.Message != "MariaDB 操作失败" {
		t.Fatalf("unexpected result: %+v", result)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(oldContent) {
		t.Fatalf("config was not restored: got %q", got)
	}
}

func TestApplyWordPressDBPasswordChangeReportsRollbackFailure(t *testing.T) {
	writes := 0
	writeConfig := func(string, []byte, os.FileMode) error {
		writes++
		if writes == 2 {
			return errors.New("rollback failed")
		}
		return nil
	}
	site := &models.Website{DBUser: "site_user"}

	result := applyWordPressDBPasswordChange("wp-config.php", []byte("old"), []byte("new"), site, "new-password", &config.Config{}, writeConfig,
		func(string, string, *config.Config) error { return errors.New("database failed") })
	if result.Success {
		t.Fatalf("expected failure: %+v", result)
	}
	if !strings.Contains(result.Message, "wp-config.php 恢复失败") {
		t.Fatalf("rollback failure was hidden: %q", result.Message)
	}
}

func TestApplyWordPressDBPasswordChangeStopsWhenConfigWriteFails(t *testing.T) {
	databaseCalled := false
	site := &models.Website{DBUser: "site_user"}

	result := applyWordPressDBPasswordChange("wp-config.php", []byte("old"), []byte("new"), site, "new-password", &config.Config{},
		func(string, []byte, os.FileMode) error { return errors.New("write failed") },
		func(string, string, *config.Config) error {
			databaseCalled = true
			return nil
		})
	if result.Success || result.Message != "更新 wp-config.php 失败" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if databaseCalled {
		t.Fatal("database password changed after config write failure")
	}
}
