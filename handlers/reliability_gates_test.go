package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
)

func TestChangeDBPasswordRejectsConcurrentSiteOperation(t *testing.T) {
	setupDatabaseManagerTestDB(t)
	if !executor.TryAcquireSiteOpLock(1, "test") {
		t.Fatal("failed to reserve site lock")
	}
	t.Cleanup(func() { executor.ReleaseSiteOpLock(1) })

	router := gin.New()
	router.PUT("/api/websites/:id/db-password", (&WebsiteHandler{}).ChangeDBPassword)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/websites/1/db-password", strings.NewReader(`{"new_password":"new-password"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRemoveSSLRejectsConcurrentSiteOperation(t *testing.T) {
	setupDatabaseManagerTestDB(t)
	if _, err := database.GetDB().Exec(`UPDATE websites SET ssl_enabled=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if !executor.TryAcquireSiteOpLock(1, "test") {
		t.Fatal("failed to reserve site lock")
	}
	t.Cleanup(func() { executor.ReleaseSiteOpLock(1) })

	router := gin.New()
	router.DELETE("/api/websites/:id/ssl", (&WebsiteHandler{}).RemoveSSL)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/websites/1/ssl", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestClearDatabaseRejectsConcurrentSiteOperation(t *testing.T) {
	setupDatabaseManagerTestDB(t)
	if !executor.TryAcquireSiteOpLock(1, "test") {
		t.Fatal("failed to reserve site lock")
	}
	t.Cleanup(func() { executor.ReleaseSiteOpLock(1) })

	router := gin.New()
	router.POST("/api/websites/:id/backups/clear-database", (&BackupHandler{}).ClearDatabase)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/websites/1/backups/clear-database", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestReinstallWordPressRejectsActiveMigration(t *testing.T) {
	setupDatabaseManagerTestDB(t)
	if _, err := database.GetDB().Exec(`
		INSERT INTO site_migration_peers(id,status) VALUES ('peer_0000000001','paired');
		INSERT INTO site_migration_batches(id,peer_id,direction,status) VALUES ('batch_0000000001','peer_0000000001','source','active');
		INSERT INTO site_migration_sites(id,batch_id,source_site_id,source_domain,target_domain,site_type,status,stage)
		VALUES ('migration_0000001','batch_0000000001',1,'example.com','example.com','wordpress','running','source_frozen');
		INSERT INTO site_migration_locks
		(domain,site_id,migration_site_id,direction,status)
		VALUES ('example.com',1,'migration_0000001','source','active')`); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.POST("/api/websites/:id/reinstall-wp", (&WebsiteHandler{}).ReinstallWordPress)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/websites/1/reinstall-wp", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
