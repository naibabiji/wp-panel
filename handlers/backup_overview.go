package handlers

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

// OverviewDBBackup 在共享的 models.DBBackup 基础上附加本地文件是否仍然存在的信息。
// transport_status 记录的是"上次同步尝试的结果"，不代表本地文件现在是否还在
// （keep_local=0 时同步成功后会删除本地文件），两者要结合起来才能看出完整状态。
type OverviewDBBackup struct {
	models.DBBackup
	LocalExists bool `json:"local_exists"`
}

// OverviewFileBackup 同上，针对网站文件备份。
type OverviewFileBackup struct {
	models.FileBackup
	LocalExists bool `json:"local_exists"`
}

// SiteBackupOverview 汇总单个网站的数据库备份和文件备份列表，供备份总览页面展示。
type SiteBackupOverview struct {
	SiteID      int                  `json:"site_id"`
	Domain      string               `json:"domain"`
	DBBackups   []OverviewDBBackup   `json:"db_backups"`
	FileBackups []OverviewFileBackup `json:"file_backups"`
}

// GetBackupOverview 返回所有网站的备份总览（数据库备份 + 文件备份）以及面板自身数据库备份列表。
// 只读接口，不提供下载/删除，跳转到具体操作请前往对应网站详情页。
func GetBackupOverview(c *gin.Context) {
	db := database.GetDB()
	cfg := config.AppConfig

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
		if err := rows.Scan(&id, &domain); err != nil {
			log.Printf("备份总览: 扫描网站列表行失败: %v", err)
			continue
		}
		siteIndex[id] = len(sites)
		sites = append(sites, SiteBackupOverview{
			SiteID:      id,
			Domain:      domain,
			DBBackups:   []OverviewDBBackup{},
			FileBackups: []OverviewFileBackup{},
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
		var b OverviewDBBackup
		var auto int
		if err := dbRows.Scan(&b.ID, &b.SiteID, &b.Filename, &b.FileSize, &b.DBName, &auto,
			&b.TransportStatus, &b.TransportMessage, &b.CreatedAt); err != nil {
			log.Printf("备份总览: 扫描数据库备份行失败: %v", err)
			continue
		}
		b.Auto = auto == 1
		if idx, ok := siteIndex[b.SiteID]; ok {
			localPath := filepath.Join(cfg.Panel.BackupDir, sites[idx].Domain, "db", b.Filename)
			b.LocalExists = localFileExists(localPath)
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
		var b OverviewFileBackup
		if err := fileRows.Scan(&b.ID, &b.SiteID, &b.Filename, &b.FileSize, &b.Mode,
			&b.TransportStatus, &b.TransportMessage, &b.CreatedAt); err != nil {
			log.Printf("备份总览: 扫描文件备份行失败: %v", err)
			continue
		}
		if idx, ok := siteIndex[b.SiteID]; ok {
			localPath := filepath.Join(cfg.Panel.BackupDir, sites[idx].Domain, "files", b.Filename)
			b.LocalExists = localFileExists(localPath)
			sites[idx].FileBackups = append(sites[idx].FileBackups, b)
		}
	}
	fileRows.Close()

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

// localFileExists 只做一次本地文件系统 stat，不涉及远程网络请求，性能开销可忽略。
func localFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
