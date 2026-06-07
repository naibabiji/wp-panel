package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BackupDatabase 使用 VACUUM INTO 对在线数据库做一致性热备
func BackupDatabase(backupDir string) (string, error) {
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return "", fmt.Errorf("创建备份目录失败: %w", err)
	}

	ts := time.Now().Format("20060102_150405")
	backupPath := filepath.Join(backupDir, fmt.Sprintf("panel_%s.db", ts))

	if _, err := DB.Exec("VACUUM INTO ?", backupPath); err != nil {
		os.Remove(backupPath)
		return "", fmt.Errorf("VACUUM INTO 失败: %w", err)
	}

	return backupPath, nil
}

// DBBackupInfo 备份文件信息
type DBBackupInfo struct {
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	SizeText  string `json:"size_text"`
	CreatedAt string `json:"created_at"`
}

// ListDBBackups 列出所有面板数据库备份
func ListDBBackups(backupDir string) ([]DBBackupInfo, error) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []DBBackupInfo{}, nil
		}
		return nil, err
	}

	var backups []DBBackupInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "panel_") || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// 从文件名解析时间: panel_20260107_023000.db
		name := strings.TrimPrefix(e.Name(), "panel_")
		name = strings.TrimSuffix(name, ".db")
		displayTime := name
		if t, err := time.Parse("20060102_150405", name); err == nil {
			displayTime = t.Format("2006-01-02 15:04:05")
		}

		backups = append(backups, DBBackupInfo{
			Filename:  e.Name(),
			Size:      info.Size(),
			SizeText:  formatDBSize(info.Size()),
			CreatedAt: displayTime,
		})
	}

	// 按时间降序
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Filename > backups[j].Filename
	})

	if backups == nil {
		backups = []DBBackupInfo{}
	}
	return backups, nil
}

// CleanupOldDBBackups 保留最近的 keepCount 份备份，删除其余
func CleanupOldDBBackups(backupDir string, keepCount int) int {
	backups, err := ListDBBackups(backupDir)
	if err != nil || len(backups) <= keepCount {
		return 0
	}

	removed := 0
	for i := keepCount; i < len(backups); i++ {
		path := filepath.Join(backupDir, backups[i].Filename)
		if os.Remove(path) == nil {
			removed++
		}
	}
	return removed
}

// RestoreDBBackupPath 返回指定备份文件的完整路径，校验文件存在
func RestoreDBBackupPath(backupDir, filename string) (string, error) {
	// 安全校验：防止路径遍历
	clean := filepath.Clean(filename)
	if clean != filename || strings.Contains(clean, "/") || strings.Contains(clean, "\\") {
		return "", fmt.Errorf("非法文件名")
	}
	if !strings.HasPrefix(clean, "panel_") || !strings.HasSuffix(clean, ".db") {
		return "", fmt.Errorf("非法备份文件名")
	}

	fullPath := filepath.Join(backupDir, clean)
	if _, err := os.Stat(fullPath); err != nil {
		return "", fmt.Errorf("备份文件不存在")
	}
	return fullPath, nil
}

// VerifyDBBackup 打开备份文件执行 PRAGMA integrity_check，校验备份完整性
func VerifyDBBackup(backupPath string) error {
	db, err := sql.Open("sqlite", backupPath)
	if err != nil {
		return fmt.Errorf("打开备份文件失败: %w", err)
	}
	defer db.Close()

	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("完整性校验执行失败: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("备份文件损坏: %s", result)
	}
	return nil
}

func formatDBSize(size int64) string {
	const (
		KB = 1024
		MB = KB * 1024
	)
	switch {
	case size >= MB:
		return fmt.Sprintf("%.1f MB", float64(size)/float64(MB))
	case size >= KB:
		return fmt.Sprintf("%.1f KB", float64(size)/float64(KB))
	default:
		return fmt.Sprintf("%d B", size)
	}
}
