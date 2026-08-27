package executor

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
	_ "modernc.org/sqlite"
)

func withAIDevelopmentGateTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE website_ai_development_access (site_id INTEGER PRIMARY KEY); INSERT INTO website_ai_development_access(site_id) VALUES (7)`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() {
		database.DB = previous
		db.Close()
	})
	return db
}

func TestClearDatabaseTablesRejectsActiveAIDevelopmentAccessBeforeMySQL(t *testing.T) {
	withAIDevelopmentGateTestDB(t)
	err := ClearDatabaseTables(7, "wp_example", "password")
	if err == nil || !strings.Contains(err.Error(), "AI 开发访问") {
		t.Fatalf("ClearDatabaseTables() error=%v", err)
	}
}

func TestSetDocumentRootExecutorRejectsActiveAIDevelopmentAccess(t *testing.T) {
	withAIDevelopmentGateTestDB(t)
	result := executeSetDocumentRoot(&Task{Payload: &SetDocumentRootPayload{Site: &models.Website{ID: 7, SiteType: "php"}}})
	if result.Success || !strings.Contains(result.Message, "AI 开发访问") {
		t.Fatalf("executeSetDocumentRoot() result=%+v", result)
	}
}
