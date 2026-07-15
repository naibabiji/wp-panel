package handlers

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
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
// 列表接口只做本地状态汇总；下载/删除由独立备份操作接口处理。
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
			b.LocalExists = fileBackupLocalExists(sites[idx].Domain, b.Filename)
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

// ReconcileBackupStatus 由管理员在备份总览页面显式点击"核对远程状态"触发：核对历史备份记录
// （transport_status 还停留在默认值 local）是否其实已经同步到远程，命中的回写为 synced。
// 不放在 GetBackupOverview 里自动触发——远程慢或不可达时会拖慢一个本该只读、快速的列表接口，
// 而且确实从未同步过远程的记录会一直停在 local，等于每次打开页面都重新发一次远程列表请求。
func ReconcileBackupStatus(c *gin.Context) {
	updated, err := executor.ReconcileBackupTransportStatus()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(err.Error()))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"updated": updated}))
}

// localFileExists 只做一次本地文件系统 stat，不涉及远程网络请求，性能开销可忽略。
// 只有明确确认文件不存在（os.IsNotExist）才判定为不存在；权限不足、IO 错误等无法确认的情况
// 一律当作"存在"处理，避免把"查不了"误报成"本地文件缺失"这种更容易引起恐慌的结论。
func localFileExists(path string) bool {
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	log.Printf("备份总览: 检查本地文件状态失败 %s: %v", path, err)
	return true
}

// fileBackupLocalExists 检查网站文件备份对应的本地文件是否还在。文件备份固定存放在
// config.DefaultBackupDir（与 executor/file_backup.go 的生成路径、executor/remote_sync.go
// 的远程同步根目录保持一致），不是 cfg.Panel.BackupDir——两者在正常安装下值相同，
// 但文件备份的落盘路径从来没有真正读取过 Panel.BackupDir，用错会导致误报"本地文件缺失"。
func fileBackupLocalExists(domain, filename string) bool {
	return fileBackupLocalExistsFromRoot(config.DefaultBackupDir, domain, filename)
}

// fileBackupLocalExistsFromRoot 是 fileBackupLocalExists 的可测试实现，root 参数便于单元测试
// 注入临时目录；生产代码固定通过 fileBackupLocalExists 传入 config.DefaultBackupDir。
func fileBackupLocalExistsFromRoot(root, domain, filename string) bool {
	return localFileExists(filepath.Join(root, domain, "files", filename))
}
