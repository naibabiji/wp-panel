package executor

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

func setupNginxCustomSaveTest(t *testing.T) (string, *models.Website) {
	t.Helper()
	oldDB := database.DB
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := database.RunMigrations(); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	dir := t.TempDir()
	oldDir := nginxCustomDir
	oldRunner := runNginxCustomCommand
	nginxCustomDir = dir
	t.Cleanup(func() {
		nginxCustomDir = oldDir
		runNginxCustomCommand = oldRunner
		database.Close()
		database.DB = oldDB
	})
	return dir, &models.Website{ID: 1, Domain: "example.com"}
}

func TestSaveNginxCustomReloadFailureRestoresOldFiles(t *testing.T) {
	dir, site := setupNginxCustomSaveTest(t)
	prePath := filepath.Join(dir, "example.com.pre.conf")
	mainPath := filepath.Join(dir, "example.com.conf")
	if err := os.WriteFile(prePath, []byte("old pre"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainPath, []byte("old main"), 0644); err != nil {
		t.Fatal(err)
	}

	var calls [][]string
	runNginxCustomCommand = func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(calls) == 2 {
			return []byte("reload failed"), errors.New("reload failed")
		}
		return nil, nil
	}

	result := executeSaveNginxCustom(&Task{Payload: &SaveNginxCustomPayload{
		Site: site, PreContent: "new pre", Content: "new main",
	}})
	if result.Success || result.Message != "Nginx 重载失败，已恢复保存前的自定义配置" {
		t.Fatalf("result = %+v", result)
	}
	if got, _ := os.ReadFile(prePath); string(got) != "old pre" {
		t.Fatalf("pre file = %q", got)
	}
	if got, _ := os.ReadFile(mainPath); string(got) != "old main" {
		t.Fatalf("main file = %q", got)
	}
	wantCalls := [][]string{{"-t"}, {"-s", "reload"}, {"-t"}, {"-s", "reload"}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestSaveNginxCustomRecoveryFailureNeverReportsSuccess(t *testing.T) {
	dir, site := setupNginxCustomSaveTest(t)
	if err := os.WriteFile(filepath.Join(dir, "example.com.conf"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	call := 0
	runNginxCustomCommand = func(args ...string) ([]byte, error) {
		call++
		if call == 2 || call == 4 {
			return []byte("reload failed"), errors.New("reload failed")
		}
		return nil, nil
	}

	result := executeSaveNginxCustom(&Task{Payload: &SaveNginxCustomPayload{
		Site: site, PreContent: "new pre", Content: "new main",
	}})
	if result.Success || result.Message != "Nginx 重载失败，旧配置恢复未完成，请检查 Nginx 服务" {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "example.com.pre.conf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new pre file was not removed: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "example.com.conf")); string(got) != "old" {
		t.Fatalf("main file = %q", got)
	}
}

func TestSaveNginxCustomSuccessfulReloadKeepsNewFiles(t *testing.T) {
	dir, site := setupNginxCustomSaveTest(t)
	runNginxCustomCommand = func(args ...string) ([]byte, error) { return nil, nil }

	result := executeSaveNginxCustom(&Task{Payload: &SaveNginxCustomPayload{
		Site: site, PreContent: "new pre", Content: "new main",
	}})
	if !result.Success {
		t.Fatalf("result = %+v", result)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "example.com.pre.conf")); string(got) != "new pre" {
		t.Fatalf("pre file = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "example.com.conf")); string(got) != "new main" {
		t.Fatalf("main file = %q", got)
	}
}
