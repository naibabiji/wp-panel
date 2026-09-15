package executor

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
	_ "modernc.org/sqlite"
)

func withPausedConfigurationTestDB(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE websites (id INTEGER PRIMARY KEY, status TEXT NOT NULL, maintenance_security TEXT NOT NULL DEFAULT '{}');
		CREATE TABLE website_ai_development_access (site_id INTEGER PRIMARY KEY);
		CREATE TABLE site_migration_locks (domain TEXT NOT NULL, site_id INTEGER, migration_site_id TEXT NOT NULL, direction TEXT NOT NULL, status TEXT NOT NULL);
		INSERT INTO websites(id,status) VALUES (7,'paused')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() {
		database.DB = previous
		db.Close()
	})
}

func TestUpdateDomainsRejectsActiveMigrationBeforeConfigurationChanges(t *testing.T) {
	withPausedConfigurationTestDB(t)
	if _, err := database.GetDB().Exec(`UPDATE websites SET status='active' WHERE id=7;
		INSERT INTO site_migration_locks(domain,site_id,migration_site_id,direction,status)
		VALUES ('example.com',7,'migration_0000001','source','active')`); err != nil {
		t.Fatal(err)
	}
	result := executeUpdateDomains(&Task{Payload: &UpdateDomainsPayload{
		Site:      &models.Website{ID: 7, Domain: "example.com"},
		NewDomain: "new.example.com",
	}})
	if result.Success || !strings.Contains(result.Message, "迁移") {
		t.Fatalf("result=%+v", result)
	}
}

func TestSetDocumentRootRejectsMigrationBeforeCreatingPublicDirectory(t *testing.T) {
	withPausedConfigurationTestDB(t)
	if _, err := database.GetDB().Exec(`UPDATE websites SET status='active' WHERE id=7;
		INSERT INTO site_migration_locks(domain,site_id,migration_site_id,direction,status)
		VALUES ('example.com',7,'migration_0000001','source','active')`); err != nil {
		t.Fatal(err)
	}
	webRoot := t.TempDir()
	result := executeSetDocumentRoot(&Task{Payload: &SetDocumentRootPayload{
		Site:               &models.Website{ID: 7, Domain: "example.com", SiteType: "php", WebRoot: webRoot},
		DocumentRootSubdir: DocumentRootPublic,
	}})
	if result.Success || !strings.Contains(result.Message, "迁移") {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat(filepath.Join(webRoot, DocumentRootPublic)); !os.IsNotExist(err) {
		t.Fatalf("public directory was created before migration rejection: %v", err)
	}
}

func TestRemoveSSLRejectsActiveMigrationBeforeSideEffects(t *testing.T) {
	withPausedConfigurationTestDB(t)
	if _, err := database.GetDB().Exec(`UPDATE websites SET status='active' WHERE id=7;
		INSERT INTO site_migration_locks(domain,site_id,migration_site_id,direction,status)
		VALUES ('example.com',7,'migration_0000001','source','active')`); err != nil {
		t.Fatal(err)
	}
	result := executeRemoveSSL(&Task{Payload: &RemoveSSLPayload{Site: &models.Website{
		ID: 7, Domain: "example.com", SSLEnabled: true,
	}}})
	if result.Success || !strings.Contains(result.Message, "搬家") {
		t.Fatalf("result=%+v", result)
	}
}

func TestPausedSiteRejectsOnlyAffectedNginxConfigurationTasks(t *testing.T) {
	withPausedConfigurationTestDB(t)
	site := &models.Website{ID: 7, Domain: "example.com", SiteType: "php"}
	tests := []struct {
		name string
		run  func() TaskResult
	}{
		{name: "access log", run: func() TaskResult {
			return executeSetAccessLogMode(&Task{Payload: &SetAccessLogModePayload{Site: site, Mode: "off"}})
		}},
		{name: "CDN real IP", run: func() TaskResult { return executeSetCDNRealIP(&Task{Payload: &SetCDNRealIPPayload{Site: site}}) }},
		{name: "document root", run: func() TaskResult { return executeSetDocumentRoot(&Task{Payload: &SetDocumentRootPayload{Site: site}}) }},
		{name: "enable SSL", run: func() TaskResult {
			return executeEnableSSL(&Task{Payload: &EnableSSLPayload{Site: site, Mode: "manual"}})
		}},
		{name: "remove SSL", run: func() TaskResult { return executeRemoveSSL(&Task{Payload: &RemoveSSLPayload{Site: site}}) }},
		{name: "domains", run: func() TaskResult {
			return executeUpdateDomains(&Task{Payload: &UpdateDomainsPayload{Site: site, NewDomain: site.Domain}})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.run()
			if result.Success || !strings.Contains(result.Message, "网站已暂停") {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}
