package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/config"
)

func TestDownloadDBBackupValidatesFilename(t *testing.T) {
	gin.SetMode(gin.TestMode)

	backupRoot := t.TempDir()
	backupDir := filepath.Join(backupRoot, "panel-db")
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		t.Fatalf("mkdir backup dir: %v", err)
	}
	filename := "panel_20260107_023000.db"
	if err := os.WriteFile(filepath.Join(backupDir, filename), []byte("backup"), 0600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{BackupDir: backupRoot}}
	t.Cleanup(func() { config.AppConfig = oldConfig })

	router := gin.New()
	handler := &SettingsHandler{}
	router.GET("/api/settings/db-backup/:filename/download", handler.DownloadDBBackup)

	req := httptest.NewRequest(http.MethodGet, "/api/settings/db-backup/"+filename+"/download", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("valid download status = %d, want %d", w.Code, http.StatusOK)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, filename) {
		t.Fatalf("Content-Disposition = %q, want filename %q", cd, filename)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/settings/db-backup/evil.db/download", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid download status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}
