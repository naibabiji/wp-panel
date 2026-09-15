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

func withDocumentRootStubs(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE websites (id INTEGER PRIMARY KEY, status TEXT NOT NULL, document_root_subdir TEXT NOT NULL, updated_at DATETIME);
		CREATE TABLE website_ai_development_access (site_id INTEGER PRIMARY KEY);
		CREATE TABLE site_migration_locks (domain TEXT NOT NULL, site_id INTEGER, migration_site_id TEXT NOT NULL, direction TEXT NOT NULL, status TEXT NOT NULL);
		INSERT INTO websites(id,status,document_root_subdir) VALUES (1,'active','')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	oldDB := database.DB
	oldConfig := config.AppConfig
	oldPersist, oldApply := persistDocumentRoot, applyDocumentRootNginx
	database.DB = db
	config.AppConfig = &config.Config{
		Panel: config.PanelConfig{BackupDir: t.TempDir()},
		Paths: config.PathsConfig{NginxSitesEnabled: t.TempDir()},
	}
	t.Cleanup(func() {
		persistDocumentRoot, applyDocumentRootNginx = oldPersist, oldApply
		config.AppConfig = oldConfig
		database.DB = oldDB
		db.Close()
	})
}

func documentRootTestSite(t *testing.T) *models.Website {
	t.Helper()
	return &models.Website{
		ID: 1, Domain: "example.com", SiteType: "php", WebRoot: t.TempDir(),
		NginxConfPath: filepath.Join(t.TempDir(), "example.com.conf"),
	}
}

func runDocumentRootTask(site *models.Website) TaskResult {
	return executeSetDocumentRoot(&Task{Payload: &SetDocumentRootPayload{
		Site: site, DocumentRootSubdir: DocumentRootPublic,
	}})
}

func TestSetDocumentRootDatabaseFailureDoesNotApplyNginx(t *testing.T) {
	withDocumentRootStubs(t)
	nginxCalled := false
	persistDocumentRoot = func(int, string) error { return errors.New("database failed") }
	applyDocumentRootNginx = func(*TemplateEngine, string, string, string) error {
		nginxCalled = true
		return nil
	}
	result := runDocumentRootTask(documentRootTestSite(t))
	if result.Success || nginxCalled {
		t.Fatalf("result=%+v nginxCalled=%v", result, nginxCalled)
	}
}

func TestSetDocumentRootNginxFailureRestoresDatabase(t *testing.T) {
	withDocumentRootStubs(t)
	var saved []string
	persistDocumentRoot = func(_ int, subdir string) error {
		saved = append(saved, subdir)
		return nil
	}
	applyDocumentRootNginx = func(*TemplateEngine, string, string, string) error { return errors.New("nginx failed") }
	result := runDocumentRootTask(documentRootTestSite(t))
	if result.Success || len(saved) != 2 || saved[0] != DocumentRootPublic || saved[1] != "" {
		t.Fatalf("result=%+v saved=%v", result, saved)
	}
}

func TestSetDocumentRootReportsDatabaseRecoveryFailure(t *testing.T) {
	withDocumentRootStubs(t)
	call := 0
	persistDocumentRoot = func(int, string) error {
		call++
		if call == 2 {
			return errors.New("restore failed")
		}
		return nil
	}
	applyDocumentRootNginx = func(*TemplateEngine, string, string, string) error { return errors.New("nginx failed") }
	result := runDocumentRootTask(documentRootTestSite(t))
	if result.Success || result.Message != "应用 Nginx 配置失败，Web 入口目录状态恢复失败，请人工检查" {
		t.Fatalf("result=%+v", result)
	}
}

func TestSetDocumentRootSuccessPersistsAndApplies(t *testing.T) {
	withDocumentRootStubs(t)
	saved := ""
	persistDocumentRoot = func(_ int, subdir string) error {
		saved = subdir
		return nil
	}
	nginxCalled := false
	applyDocumentRootNginx = func(*TemplateEngine, string, string, string) error {
		nginxCalled = true
		return nil
	}
	result := runDocumentRootTask(documentRootTestSite(t))
	if !result.Success || saved != DocumentRootPublic || !nginxCalled {
		t.Fatalf("result=%+v saved=%q nginxCalled=%v", result, saved, nginxCalled)
	}
}

func TestSaveDocumentRootChecksDatabaseResult(t *testing.T) {
	withDocumentRootStubs(t)
	if err := saveDocumentRoot(1, DocumentRootPublic); err != nil {
		t.Fatal(err)
	}
	var subdir string
	if err := database.GetDB().QueryRow("SELECT document_root_subdir FROM websites WHERE id=1").Scan(&subdir); err != nil {
		t.Fatal(err)
	}
	if subdir != DocumentRootPublic {
		t.Fatalf("subdir=%q", subdir)
	}
	if err := saveDocumentRoot(999, ""); err == nil {
		t.Fatal("missing website unexpectedly reported success")
	}
}
