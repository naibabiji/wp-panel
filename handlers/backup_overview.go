package handlers

import (
	"net/http"
	"path/filepath"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

// SiteBackupOverview 汇总单个网站的数据库备份和文件备份列表，供备份总览页面展示。
type SiteBackupOverview struct {
	SiteID      int                 `json:"site_id"`
	Domain      string              `json:"domain"`
	DBBackups   []models.DBBackup   `json:"db_backups"`
	FileBackups []models.FileBackup `json:"file_backups"`
}

// GetBackupOverview 返回所有网站的备份总览（数据库备份 + 文件备份）以及面板自身数据库备份列表。
// 只读接口，不提供下载/删除，跳转到具体操作请前往对应网站详情页。
func GetBackupOverview(c *gin.Context) {
	db := database.GetDB()

	sites := []SiteBackupOverview{}
	siteIndex := map[int]int{}

	rows, err := db.Query(`SELECT id, domain FROM websites ORDER BY domain`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询网站列表失败"))
		return
	}
	for rows.Next() {
		var id int
		var domain string
		if rows.Scan(&id, &domain) != nil {
			continue
		}
		siteIndex[id] = len(sites)
		sites = append(sites, SiteBackupOverview{
			SiteID:      id,
			Domain:      domain,
			DBBackups:   []models.DBBackup{},
			FileBackups: []models.FileBackup{},
		})
	}
	rows.Close()

	dbRows, err := db.Query(`SELECT id, site_id, filename, file_size, db_name, auto, transport_status, transport_message, created_at
		FROM db_backups ORDER BY created_at DESC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询数据库备份列表失败"))
		return
	}
	for dbRows.Next() {
		var b models.DBBackup
		var auto int
		if dbRows.Scan(&b.ID, &b.SiteID, &b.Filename, &b.FileSize, &b.DBName, &auto,
			&b.TransportStatus, &b.TransportMessage, &b.CreatedAt) != nil {
			continue
		}
		b.Auto = auto == 1
		if idx, ok := siteIndex[b.SiteID]; ok {
			sites[idx].DBBackups = append(sites[idx].DBBackups, b)
		}
	}
	dbRows.Close()

	fileRows, err := db.Query(`SELECT id, site_id, filename, file_size, mode, transport_status, transport_message, created_at
		FROM file_backups ORDER BY created_at DESC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询文件备份列表失败"))
		return
	}
	for fileRows.Next() {
		var b models.FileBackup
		if fileRows.Scan(&b.ID, &b.SiteID, &b.Filename, &b.FileSize, &b.Mode,
			&b.TransportStatus, &b.TransportMessage, &b.CreatedAt) != nil {
			continue
		}
		if idx, ok := siteIndex[b.SiteID]; ok {
			sites[idx].FileBackups = append(sites[idx].FileBackups, b)
		}
	}
	fileRows.Close()

	cfg := config.AppConfig
	panelBackupDir := filepath.Join(cfg.Panel.BackupDir, "panel-db")
	panelBackups, err := database.ListDBBackups(panelBackupDir)
	if err != nil {
		panelBackups = []database.DBBackupInfo{}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"sites":            sites,
		"panel_db_backups": panelBackups,
	}))
}
