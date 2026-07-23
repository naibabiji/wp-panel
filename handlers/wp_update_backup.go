package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"
)

type WPUpdateBackupHandler struct {
	BackupDir string
}

func (h *WPUpdateBackupHandler) List(c *gin.Context) {
	siteID, ok := wpUpdateBackupSiteID(c)
	if !ok {
		return
	}
	rows, err := database.GetDB().QueryContext(c.Request.Context(), `SELECT b.id,b.task_id,t.component_type,t.component_key,
		t.current_version,t.target_version,t.status,t.rollback_status,t.requires_attention,b.kind,b.file_size,b.created_at
		FROM wp_update_task_backups b JOIN wp_update_tasks t ON t.id=b.task_id
		WHERE t.site_id=? AND b.protected=1 AND b.deleted_at IS NULL
		ORDER BY b.created_at DESC,b.id DESC`, siteID)
	if err != nil {
		wpUpdateBackupError(c, http.StatusInternalServerError, "wp_update_backup.load_failed")
		return
	}
	defer rows.Close()
	items := make([]models.WPUpdateBackup, 0)
	for rows.Next() {
		var item models.WPUpdateBackup
		var attention int
		if err := rows.Scan(&item.BackupID, &item.TaskID, &item.ComponentType, &item.ComponentKey,
			&item.CurrentVersion, &item.TargetVersion, &item.TaskStatus, &item.RollbackStatus,
			&attention, &item.Kind, &item.FileSize, &item.CreatedAt); err != nil {
			wpUpdateBackupError(c, http.StatusInternalServerError, "wp_update_backup.load_failed")
			return
		}
		item.RequiresAttention = attention == 1
		item.RestoreAllowed = item.Kind == "database" && item.TaskStatus != "preparing" &&
			item.TaskStatus != "queued" && item.TaskStatus != "running"
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		wpUpdateBackupError(c, http.StatusInternalServerError, "wp_update_backup.load_failed")
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(items))
}

func (h *WPUpdateBackupHandler) Restore(c *gin.Context) {
	siteID, ok := wpUpdateBackupSiteID(c)
	if !ok {
		return
	}
	backupID, err := strconv.ParseInt(c.Param("backup_id"), 10, 64)
	if err != nil || backupID <= 0 {
		wpUpdateBackupError(c, http.StatusBadRequest, "wp_update_backup.invalid_request")
		return
	}
	var req struct {
		Confirm bool `json:"confirm"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !req.Confirm {
		wpUpdateBackupError(c, http.StatusBadRequest, "wp_update_backup.invalid_request")
		return
	}
	site := getWebsiteByID(siteID)
	if site == nil || site.SiteType != "wordpress" || site.Status != models.StatusActive {
		wpUpdateBackupError(c, http.StatusConflict, "wp_update_backup.site_unavailable")
		return
	}
	var active int
	if err := database.GetDB().QueryRowContext(c.Request.Context(), `SELECT COUNT(*) FROM wp_update_tasks
		WHERE site_id=? AND status IN ('preparing','queued','running')`, siteID).Scan(&active); err != nil {
		wpUpdateBackupError(c, http.StatusInternalServerError, "wp_update_backup.restore_failed")
		return
	}
	if active != 0 {
		wpUpdateBackupError(c, http.StatusConflict, "wp_update_backup.update_active")
		return
	}
	var path, expectedSHA string
	err = database.GetDB().QueryRowContext(c.Request.Context(), `SELECT b.file_path,b.sha256
		FROM wp_update_task_backups b JOIN wp_update_tasks t ON t.id=b.task_id
		WHERE b.id=? AND t.site_id=? AND b.kind='database' AND b.protected=1 AND b.deleted_at IS NULL
		  AND t.status NOT IN ('preparing','queued','running')`, backupID, siteID).Scan(&path, &expectedSHA)
	if err != nil {
		wpUpdateBackupError(c, http.StatusNotFound, "wp_update_backup.not_found")
		return
	}
	root := filepath.Join(filepath.Clean(h.BackupDir), "wp-updates")
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) || !backupPathWithin(root, cleanPath) || !validUpdateBackupSHA(expectedSHA) {
		wpUpdateBackupError(c, http.StatusConflict, "wp_update_backup.file_unavailable")
		return
	}
	// Acquire the site lock before the (potentially slow, multi-GB) file hash below
	// rather than after: an update's Confirm() only holds this same lock for the
	// instant it takes to write its task row, so leaving an expensive step between
	// our own active-task check and lock acquisition would reopen a window for a
	// fresh update to be confirmed in between. Re-checking "active" once we hold
	// the lock makes the whole check-then-act sequence atomic against a concurrent
	// Confirm(), which also cannot proceed while this lock is held.
	if !executor.TryAcquireSiteOpLock(siteID, "wp_update_restore") {
		wpUpdateBackupError(c, http.StatusConflict, "wp_update_backup.update_active")
		return
	}
	if err := database.GetDB().QueryRowContext(c.Request.Context(), `SELECT COUNT(*) FROM wp_update_tasks
		WHERE site_id=? AND status IN ('preparing','queued','running')`, siteID).Scan(&active); err != nil {
		executor.ReleaseSiteOpLock(siteID)
		wpUpdateBackupError(c, http.StatusInternalServerError, "wp_update_backup.restore_failed")
		return
	}
	if active != 0 {
		executor.ReleaseSiteOpLock(siteID)
		wpUpdateBackupError(c, http.StatusConflict, "wp_update_backup.update_active")
		return
	}
	if !regularUpdateBackup(cleanPath, expectedSHA) {
		executor.ReleaseSiteOpLock(siteID)
		wpUpdateBackupError(c, http.StatusConflict, "wp_update_backup.file_unavailable")
		return
	}
	task := executor.GlobalQueue.Enqueue(executor.TaskRestoreBackup, &executor.RestoreBackupPayload{
		Site: site, UpdateBackupPath: cleanPath, ExpectedSHA256: expectedSHA,
	})
	c.JSON(http.StatusAccepted, models.SuccessResponse(gin.H{
		"task_id": task.ID,
		"status":  task.Status,
	}))
}

func wpUpdateBackupSiteID(c *gin.Context) (int, bool) {
	siteID, err := strconv.Atoi(c.Param("id"))
	if err != nil || siteID <= 0 {
		wpUpdateBackupError(c, http.StatusBadRequest, "wp_update_backup.invalid_request")
		return 0, false
	}
	return siteID, true
}

func validUpdateBackupSHA(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func regularUpdateBackup(path, expected string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return false
	}
	return hex.EncodeToString(hash.Sum(nil)) == expected
}

func wpUpdateBackupError(c *gin.Context, status int, key string) {
	c.JSON(status, models.ErrorResponse(i18n.TE(c.Request, key)))
}
