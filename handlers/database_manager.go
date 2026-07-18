package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"
)

type databaseListItem struct {
	SiteID          int    `json:"site_id"`
	Domain          string `json:"domain"`
	Status          string `json:"status"`
	SiteType        string `json:"site_type"`
	DBName          string `json:"db_name"`
	DBUser          string `json:"db_user"`
	DatabaseSize    int64  `json:"database_size"`
	SizeAvailable   bool   `json:"size_available"`
	BackupEnabled   bool   `json:"backup_enabled"`
	BackupKeepCount int    `json:"backup_keep_count"`
	BackupCount     int    `json:"backup_count"`
	LatestBackupAt  string `json:"latest_backup_at"`
}

type DatabaseManagerHandler struct{}

var databaseManagerSizeLookup = executor.GetMariaDBDatabaseSizes

func (h *DatabaseManagerHandler) List(c *gin.Context) {
	rows, err := database.GetDB().Query(`
		SELECT w.id, w.domain, w.status, w.site_type, w.db_name, w.db_user,
			COALESCE(bs.enabled, 0), COALESCE(bs.keep_count, 7),
			COUNT(b.id), COALESCE(MAX(b.created_at), '')
		FROM websites w
		LEFT JOIN backup_settings bs ON bs.site_id = w.id
		LEFT JOIN db_backups b ON b.site_id = w.id
		WHERE TRIM(COALESCE(w.db_name, '')) <> ''
		GROUP BY w.id, w.domain, w.status, w.site_type, w.db_name, w.db_user,
			bs.enabled, bs.keep_count, w.created_at
		ORDER BY w.created_at DESC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "database.list_failed")))
		return
	}
	defer rows.Close()

	sizes, sizeErr := databaseManagerSizeLookup(config.AppConfig)
	if sizeErr != nil {
		log.Printf("读取数据库大小失败: %v", sizeErr)
		sizes = map[string]int64{}
	}
	items := make([]databaseListItem, 0)
	for rows.Next() {
		var item databaseListItem
		var backupEnabled int
		if err := rows.Scan(
			&item.SiteID, &item.Domain, &item.Status, &item.SiteType, &item.DBName, &item.DBUser,
			&backupEnabled, &item.BackupKeepCount, &item.BackupCount, &item.LatestBackupAt,
		); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "database.list_failed")))
			return
		}
		item.BackupEnabled = backupEnabled == 1
		if size, ok := sizes[item.DBName]; sizeErr == nil && ok {
			item.DatabaseSize = size
			item.SizeAvailable = true
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "database.list_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(items))
}
