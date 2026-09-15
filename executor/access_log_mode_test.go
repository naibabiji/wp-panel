package executor

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
	_ "modernc.org/sqlite"
)

func withAccessLogModeStubs(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE websites (id INTEGER PRIMARY KEY, status TEXT NOT NULL, access_log_mode TEXT NOT NULL, updated_at DATETIME);
		INSERT INTO websites(id,status,access_log_mode) VALUES (1,'active','error_only')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	oldDB := database.DB
	database.DB = db
	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{
		Panel: config.PanelConfig{BackupDir: t.TempDir()},
		Paths: config.PathsConfig{NginxSitesEnabled: t.TempDir()},
	}
	oldPersist, oldApply := persistAccessLogMode, applyAccessLogNginx
	t.Cleanup(func() {
		persistAccessLogMode, applyAccessLogNginx = oldPersist, oldApply
		config.AppConfig = oldConfig
		database.DB = oldDB
		db.Close()
	})
}

func TestSaveAccessLogModeChecksDatabaseResult(t *testing.T) {
	withAccessLogModeStubs(t)
	if err := saveAccessLogMode(1, "full"); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := database.GetDB().QueryRow("SELECT access_log_mode FROM websites WHERE id = 1").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "full" {
		t.Fatalf("mode=%q", mode)
	}
	if err := saveAccessLogMode(999, "off"); err == nil {
		t.Fatal("missing website unexpectedly reported success")
	}
}

func runAccessLogModeTask(t *testing.T, site *models.Website, mode string) TaskResult {
	t.Helper()
	if site.NginxConfPath == "" {
		site.NginxConfPath = filepath.Join(t.TempDir(), "example.com.conf")
	}
	return executeSetAccessLogMode(&Task{Payload: &SetAccessLogModePayload{Site: site, Mode: mode}})
}

func TestSetAccessLogModeDatabaseFailureDoesNotApplyNginx(t *testing.T) {
	withAccessLogModeStubs(t)
	site := &models.Website{ID: 1, Domain: "example.com", AccessLogMode: "error_only"}
	nginxCalled := false
	persistAccessLogMode = func(int, string) error { return errors.New("database failed") }
	applyAccessLogNginx = func(*TemplateEngine, string, string, string) error {
		nginxCalled = true
		return nil
	}

	result := runAccessLogModeTask(t, site, "full")
	if result.Success || nginxCalled {
		t.Fatalf("result=%+v nginxCalled=%v", result, nginxCalled)
	}
}

func TestSetAccessLogModeNginxFailureRestoresDatabase(t *testing.T) {
	withAccessLogModeStubs(t)
	site := &models.Website{ID: 1, Domain: "example.com", AccessLogMode: "error_only"}
	var savedModes []string
	persistAccessLogMode = func(_ int, mode string) error {
		savedModes = append(savedModes, mode)
		return nil
	}
	applyAccessLogNginx = func(*TemplateEngine, string, string, string) error {
		return errors.New("nginx failed")
	}

	result := runAccessLogModeTask(t, site, "full")
	if result.Success {
		t.Fatalf("result=%+v", result)
	}
	if len(savedModes) != 2 || savedModes[0] != "full" || savedModes[1] != "error_only" {
		t.Fatalf("saved modes=%v", savedModes)
	}
}

func TestSetAccessLogModeReportsDatabaseRecoveryFailure(t *testing.T) {
	withAccessLogModeStubs(t)
	site := &models.Website{ID: 1, Domain: "example.com", AccessLogMode: "error_only"}
	call := 0
	persistAccessLogMode = func(int, string) error {
		call++
		if call == 2 {
			return errors.New("restore failed")
		}
		return nil
	}
	applyAccessLogNginx = func(*TemplateEngine, string, string, string) error {
		return errors.New("nginx failed")
	}

	result := runAccessLogModeTask(t, site, "full")
	if result.Success || result.Message != "应用 Nginx 配置失败，访问日志状态恢复失败，请人工检查" {
		t.Fatalf("result=%+v", result)
	}
}

func TestSetAccessLogModeSuccessPersistsAndApplies(t *testing.T) {
	withAccessLogModeStubs(t)
	site := &models.Website{ID: 1, Domain: "example.com", AccessLogMode: "error_only"}
	savedMode := ""
	persistAccessLogMode = func(_ int, mode string) error {
		savedMode = mode
		return nil
	}
	nginxCalled := false
	applyAccessLogNginx = func(*TemplateEngine, string, string, string) error {
		nginxCalled = true
		return nil
	}

	result := runAccessLogModeTask(t, site, "full")
	if !result.Success || savedMode != "full" || !nginxCalled {
		t.Fatalf("result=%+v savedMode=%q nginxCalled=%v", result, savedMode, nginxCalled)
	}
}
