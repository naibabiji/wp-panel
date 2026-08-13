package executor

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

type remoteMaintenanceRow struct {
	source    BackupSource
	id        int
	siteID    int
	domain    string
	filename  string
	subdir    string
	mode      string
	status    string
	createdAt time.Time
}

var remoteMaintenanceMu sync.Mutex

type remoteMaintenanceDeps struct {
	enabled      func() bool
	loadRows     func() ([]remoteMaintenanceRow, error)
	loadKeys     func() (string, string, map[string]bool, error)
	localRegular func(remoteMaintenanceRow) (string, bool)
	sync         func(remoteMaintenanceRow, string) bool
	updateStatus func(remoteMaintenanceRow, string, string)
	cleanup      func(int, string, []string) int
	setState     func(int, string, string)
	rebuild      func(int) error
}

func productionRemoteMaintenanceDeps() remoteMaintenanceDeps {
	return remoteMaintenanceDeps{
		enabled:  remoteBackupEnabled,
		loadRows: loadRemoteMaintenanceRows,
		loadKeys: loadCurrentRemoteKeys,
		localRegular: func(row remoteMaintenanceRow) (string, bool) {
			localPath := filepath.Join(backupsRoot, row.domain, row.subdir, row.filename)
			info, err := os.Stat(localPath)
			return localPath, err == nil && info.Mode().IsRegular()
		},
		sync: func(row remoteMaintenanceRow, localPath string) bool {
			return SyncBackupToRemote(localPath, row.source, row.siteID, row.filename)
		},
		updateStatus: func(row remoteMaintenanceRow, status, message string) {
			updateBackupTransportStatus(row.source, row.siteID, row.filename, status, message)
		},
		cleanup: func(siteID int, domain string, files []string) int {
			return cleanupSupersededFileBackupChain(siteID, domain,
				filepath.Join(backupsRoot, domain, "files"), files)
		},
		setState: setRemoteBackupSiteState,
		rebuild: func(siteID int) error {
			_, err := ExecuteFileBackup(siteID, "full", getKeepCount(siteID))
			return err
		},
	}
}

// MaintainRemoteBackups 核对当前远端、补传仍有本地副本的缺失文件，并判断文件增量链是否需要
// 重建全量基线。allowRebuild 只供低峰后台任务使用；HTTP 手动核对不会执行大型全量压缩。
func MaintainRemoteBackups(allowRebuild bool) (int, error) {
	if !remoteMaintenanceMu.TryLock() {
		return 0, fmt.Errorf("远程备份维护任务正在运行")
	}
	defer remoteMaintenanceMu.Unlock()
	return maintainRemoteBackupsWith(allowRebuild, productionRemoteMaintenanceDeps())
}

func maintainRemoteBackupsWith(allowRebuild bool, deps remoteMaintenanceDeps) (int, error) {
	if !deps.enabled() {
		return 0, fmt.Errorf("远程备份未启用")
	}

	rows, err := deps.loadRows()
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	backupType, s3Prefix, remoteKeys, err := deps.loadKeys()
	if err != nil {
		return 0, err
	}

	changed := 0
	for i := range rows {
		row := &rows[i]
		key := row.domain + "/" + row.subdir + "/" + row.filename
		if backupType == "s3" {
			key = s3ObjectKey(s3Prefix, key)
		}
		if remoteKeys[key] {
			if row.status != "synced" {
				deps.updateStatus(*row, "synced", "")
				row.status = "synced"
				changed++
			}
			continue
		}
		if localPath, exists := deps.localRegular(*row); exists {
			if deps.sync(*row, localPath) {
				remoteKeys[key] = true
				row.status = "synced"
				changed++
				continue
			}
		}
		if row.status == "synced" {
			deps.updateStatus(*row, "local", "当前远程目标中未找到备份文件")
			row.status = "local"
			changed++
		}
	}

	fileRowsBySite := map[int][]remoteMaintenanceRow{}
	for _, row := range rows {
		if row.source == BackupSourceFile {
			fileRowsBySite[row.siteID] = append(fileRowsBySite[row.siteID], row)
		}
	}
	for siteID, siteRows := range fileRowsBySite {
		sort.Slice(siteRows, func(i, j int) bool {
			if siteRows[i].createdAt.Equal(siteRows[j].createdAt) {
				return siteRows[i].id < siteRows[j].id
			}
			return siteRows[i].createdAt.Before(siteRows[j].createdAt)
		})
		latestFull, rebuild, message, oldFiles := assessRemoteFileBackupChain(siteRows, func(row remoteMaintenanceRow) bool {
			return remoteRowExists(row, backupType, s3Prefix, remoteKeys)
		})
		status := "healthy"
		if rebuild {
			status = "rebuild_required"
		} else if len(oldFiles) > 0 {
			var cleaned int
			status, message, cleaned = maintainOldChainCleanup(allowRebuild, oldFiles, func(files []string) int {
				return deps.cleanup(siteID, siteRows[latestFull].domain, files)
			})
			changed += cleaned
		}
		deps.setState(siteID, status, message)
		if allowRebuild && rebuild {
			if backupErr := deps.rebuild(siteID); backupErr != nil {
				deps.setState(siteID, "rebuild_required", "自动重建全量基线失败: "+backupErr.Error())
				log.Printf("远程备份自动重建全量基线失败 site_id=%d: %v", siteID, backupErr)
			} else {
				deps.setState(siteID, "healthy", "新的远程全量基线已建立，旧链已清理")
				changed++
			}
		}
	}
	return changed, nil
}

func maintainOldChainCleanup(allowCleanup bool, oldFiles []string, cleanup func([]string) int) (status, message string, cleaned int) {
	if len(oldFiles) == 0 {
		return "healthy", "远程文件备份链完整", 0
	}
	if !allowCleanup {
		return "cleanup_pending", fmt.Sprintf("当前远程文件备份链完整，另有 %d 条旧链记录等待低峰清理", len(oldFiles)), 0
	}
	cleaned = cleanup(oldFiles)
	if cleaned == len(oldFiles) {
		return "healthy", "远程文件备份链完整，旧链已清理", cleaned
	}
	return "cleanup_pending", fmt.Sprintf("当前远程文件备份链完整，%d/%d 条旧链记录清理失败，将在下次低峰重试", len(oldFiles)-cleaned, len(oldFiles)), cleaned
}

func assessRemoteFileBackupChain(rows []remoteMaintenanceRow, exists func(remoteMaintenanceRow) bool) (latestFull int, rebuild bool, message string, oldFiles []string) {
	latestFull = -1
	for i, row := range rows {
		if row.mode == "full" && exists(row) {
			latestFull = i
		}
	}
	if latestFull < 0 {
		return -1, true, "当前远程目标缺少可用全量基线", nil
	}
	for _, row := range rows[latestFull+1:] {
		if !exists(row) {
			return latestFull, true, "增量备份链缺失且本地无法补传", nil
		}
	}
	oldFiles = make([]string, 0, latestFull)
	for _, row := range rows[:latestFull] {
		oldFiles = append(oldFiles, row.filename)
	}
	return latestFull, false, "远程文件备份链完整", oldFiles
}

func remoteRowExists(row remoteMaintenanceRow, backupType, s3Prefix string, remoteKeys map[string]bool) bool {
	key := row.domain + "/" + row.subdir + "/" + row.filename
	if backupType == "s3" {
		key = s3ObjectKey(s3Prefix, key)
	}
	return remoteKeys[key]
}

func loadRemoteMaintenanceRows() ([]remoteMaintenanceRow, error) {
	db := database.GetDB()
	rows := []remoteMaintenanceRow{}
	dbRows, err := db.Query(`SELECT b.id,b.site_id,w.domain,b.filename,b.transport_status,b.created_at
		FROM db_backups b JOIN websites w ON w.id=b.site_id ORDER BY b.created_at,b.id`)
	if err != nil {
		return nil, err
	}
	for dbRows.Next() {
		var row remoteMaintenanceRow
		row.source, row.subdir = BackupSourceDB, "db"
		if err := dbRows.Scan(&row.id, &row.siteID, &row.domain, &row.filename, &row.status, &row.createdAt); err != nil {
			dbRows.Close()
			return nil, fmt.Errorf("扫描数据库备份维护记录失败: %w", err)
		}
		rows = append(rows, row)
	}
	if err := dbRows.Err(); err != nil {
		dbRows.Close()
		return nil, fmt.Errorf("读取数据库备份维护记录失败: %w", err)
	}
	dbRows.Close()
	fileRows, err := db.Query(`SELECT b.id,b.site_id,w.domain,b.filename,b.mode,b.transport_status,b.created_at
		FROM file_backups b JOIN websites w ON w.id=b.site_id ORDER BY b.created_at,b.id`)
	if err != nil {
		return nil, err
	}
	for fileRows.Next() {
		var row remoteMaintenanceRow
		row.source, row.subdir = BackupSourceFile, "files"
		if err := fileRows.Scan(&row.id, &row.siteID, &row.domain, &row.filename, &row.mode, &row.status, &row.createdAt); err != nil {
			fileRows.Close()
			return nil, fmt.Errorf("扫描文件备份维护记录失败: %w", err)
		}
		rows = append(rows, row)
	}
	if err := fileRows.Err(); err != nil {
		fileRows.Close()
		return nil, fmt.Errorf("读取文件备份维护记录失败: %w", err)
	}
	fileRows.Close()
	return rows, nil
}

func loadCurrentRemoteKeys() (string, string, map[string]bool, error) {
	db := database.GetDB()
	var enabled, port int
	var backupType, host, username, authType, password, remotePath string
	var endpoint, bucket, region, accessKeyID, secretKey, s3Prefix string
	if err := db.QueryRow(`SELECT enabled,backup_type,host,port,username,auth_type,password,remote_path,
		s3_endpoint,s3_bucket,s3_region,s3_access_key_id,s3_secret_key,s3_path_prefix
		FROM remote_backup_settings WHERE id=1`).Scan(&enabled, &backupType, &host, &port, &username, &authType, &password, &remotePath,
		&endpoint, &bucket, &region, &accessKeyID, &secretKey, &s3Prefix); err != nil {
		return "", "", nil, err
	}
	if enabled == 0 {
		return "", "", nil, fmt.Errorf("远程备份未启用")
	}
	if backupType == "" {
		backupType = "rsync"
	}
	if backupType == "s3" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		keys, err := listS3ObjectKeys(ctx, endpoint, bucket, region, accessKeyID, secretKey, s3Prefix)
		return backupType, s3Prefix, keys, err
	}
	keys, err := listRsyncRemoteFiles(host, port, username, authType, password, remotePath)
	return backupType, s3Prefix, keys, err
}

func setRemoteBackupSiteState(siteID int, status, message string) {
	rebuildValue := 0
	if status == "rebuild_required" {
		rebuildValue = 1
	}
	_, _ = database.GetDB().Exec(`INSERT INTO remote_backup_site_state
		(site_id,status,rebuild_required,message,last_checked_at,updated_at)
		VALUES (?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
		ON CONFLICT(site_id) DO UPDATE SET status=excluded.status,rebuild_required=excluded.rebuild_required,
		message=excluded.message,last_checked_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP`,
		siteID, status, rebuildValue, strings.TrimSpace(message))
}

// StartRemoteBackupMaintenanceScheduler 启动后做一次延迟轻量维护；每天凌晨按稳定 server_id
// 计算 0-59 分钟错峰，并允许在确认链不可修复时串行重建一次全量基线。
func StartRemoteBackupMaintenanceScheduler() {
	go func() {
		time.Sleep(10 * time.Minute)
		if _, err := MaintainRemoteBackups(false); err != nil {
			log.Printf("远程备份启动核对跳过: %v", err)
		}
		for {
			now := time.Now()
			minute := remoteMaintenanceMinute()
			next := time.Date(now.Year(), now.Month(), now.Day(), 3, minute, 0, 0, now.Location())
			if !now.Before(next) {
				next = next.Add(24 * time.Hour)
			}
			time.Sleep(next.Sub(now))
			if _, err := MaintainRemoteBackups(true); err != nil {
				log.Printf("远程备份自动维护失败: %v", err)
			}
		}
	}()
}

func remoteMaintenanceMinute() int {
	var serverID string
	_ = database.GetDB().QueryRow(`SELECT server_id FROM remote_backup_settings WHERE id=1`).Scan(&serverID)
	if serverID == "" {
		serverID, _ = os.Hostname()
	}
	minute := 0
	for _, b := range []byte(serverID) {
		minute = (minute*33 + int(b)) % 60
	}
	return minute
}
