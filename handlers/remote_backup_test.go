package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/database"

	"github.com/gin-gonic/gin"
)

func TestAutomaticBackupIdentityPaths(t *testing.T) {
	const serverID = "a83f2c91d407"
	if got := automaticBackupUsername(serverID); got != "wpb_a83f2c91d407" {
		t.Fatalf("automaticBackupUsername() = %q", got)
	}
	if got := isolatedRemotePath("/mnt/backup/", serverID); got != "/mnt/backup/wp-panel/a83f2c91d407" {
		t.Fatalf("isolatedRemotePath() = %q", got)
	}
	if got := isolatedS3Prefix("/wp-panel/", serverID); got != "wp-panel/a83f2c91d407" {
		t.Fatalf("isolatedS3Prefix() = %q", got)
	}
}

func TestGetRemoteBackupCreatesStableIdentityAndMasksSecrets(t *testing.T) {
	setupRemoteBackupTestDB(t)
	if _, err := database.GetDB().Exec(`UPDATE remote_backup_settings SET backup_type='s3', password='password-secret', s3_secret_key='s3-secret' WHERE id=1`); err != nil {
		t.Fatal(err)
	}

	first := getRemoteBackupResponse(t)
	second := getRemoteBackupResponse(t)
	firstData := first["data"].(map[string]any)
	secondData := second["data"].(map[string]any)
	serverID, _ := firstData["server_id"].(string)
	if len(serverID) != 12 || secondData["server_id"] != serverID {
		t.Fatalf("server identity is not stable: first=%q second=%v", serverID, secondData["server_id"])
	}
	if firstData["username"] != "wpb_"+serverID || firstData["auth_type"] != "key" {
		t.Fatalf("automatic identity = username %v auth %v", firstData["username"], firstData["auth_type"])
	}
	if firstData["password"] != "已设置" || firstData["s3_secret_key"] != "已设置" {
		t.Fatalf("secrets were not masked: password=%v s3=%v", firstData["password"], firstData["s3_secret_key"])
	}
}

func TestSaveRemoteBackupPreservesLegacyPathAndStoredPassword(t *testing.T) {
	setupRemoteBackupTestDB(t)
	if _, err := database.GetDB().Exec(`UPDATE remote_backup_settings SET connection_mode='legacy', password='stored-secret' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	body := `{"enabled":false,"backup_type":"rsync","connection_mode":"legacy","host":"","port":22,"username":"wpbackup","auth_type":"password","password":"已设置","remote_path":"/mnt/legacy-target","keep_local":true}`
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/settings/remote-backup", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	SaveRemoteBackup(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("SaveRemoteBackup status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var mode, password, remotePath string
	if err := database.GetDB().QueryRow(`SELECT connection_mode, password, remote_path FROM remote_backup_settings WHERE id=1`).Scan(&mode, &password, &remotePath); err != nil {
		t.Fatal(err)
	}
	if mode != "legacy" || password != "stored-secret" || remotePath != "/mnt/legacy-target" {
		t.Fatalf("legacy settings changed: mode=%q password=%q path=%q", mode, password, remotePath)
	}
}

func setupRemoteBackupTestDB(t *testing.T) {
	t.Helper()
	oldDB := database.DB
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Close()
		database.DB = oldDB
	})
}

func getRemoteBackupResponse(t *testing.T) map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/settings/remote-backup", nil)
	GetRemoteBackup(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GetRemoteBackup status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}
