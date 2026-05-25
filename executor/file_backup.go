package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func ExecuteFileBackup(siteID int, mode string, keepCount int) (string, error) {
	if keepCount <= 0 {
		keepCount = 3
	}
	db := database.GetDB()
	var domain, webRoot string
	err := db.QueryRow("SELECT domain, web_root FROM websites WHERE id = ?", siteID).Scan(&domain, &webRoot)
	if err != nil {
		return "", fmt.Errorf("网站不存在")
	}

	backupDir := filepath.Join("/www/server/panel/backups", domain, "files")
	os.MkdirAll(backupDir, 0755)
	stampFile := filepath.Join(backupDir, ".last_backup.stamp")

	ts := time.Now().Format("20060102_150405")
	var tarName string
	var fullPath string
	var isFull bool

	if mode == "full" {
		isFull = true
	} else {
		// Check if stamp exists — if not, do full backup
		if _, err := os.Stat(stampFile); os.IsNotExist(err) {
			isFull = true
		}
	}

	if isFull {
		tarName = fmt.Sprintf("file_full_%s.tar.gz", ts)
		fullPath = filepath.Join(backupDir, tarName)
		cmd := exec.Command("tar", "-czf", fullPath, "--warning=no-file-changed",
				"--exclude=wp-content/cache",
				"--exclude=wp-content/upgrade",
				"--exclude=wp-content/debug.log",
				"-C", filepath.Dir(webRoot), filepath.Base(webRoot))
		out, err := cmd.CombinedOutput()
		if err != nil {
				if len(out) == 0 {
					return "", fmt.Errorf("全量备份失败: %v", err)
				}
			return "", fmt.Errorf("全量备份失败: %s", string(out))
		}
	} else {
		tarName = fmt.Sprintf("file_inc_%s.tar.gz", ts)
		fullPath = filepath.Join(backupDir, tarName)
		uploadsDir := filepath.Join(webRoot, "wp-content", "uploads")
		if _, err := os.Stat(uploadsDir); os.IsNotExist(err) {
			return "", fmt.Errorf("uploads 目录不存在")
		}
		// find files newer than stamp, pipe to tar
		script := fmt.Sprintf(
			`find %s -newer %s -type f | tar -czf %s -T - `,
			uploadsDir, stampFile, fullPath,
		)
		out, err := exec.Command("bash", "-c", script).CombinedOutput()
		if err != nil {
				if len(out) == 0 {
					return "", fmt.Errorf("增量备份失败: %v", err)
				}
			return "", fmt.Errorf("增量备份失败: %s", string(out))
		}
	}

	// Update stamp
	os.WriteFile(stampFile, []byte(time.Now().Format(time.RFC3339)), 0644)

	// Clean old backups
	cleanOldBackups(backupDir, keepCount)

	go SyncBackupToRemote(fullPath)
	logMsg := fmt.Sprintf("%s 文件备份成功: %s (%s)", domain, tarName, map[bool]string{true: "全量", false: "增量"}[isFull])
	appendCronLog(logMsg)
	return logMsg, nil
}

func cleanOldBackups(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type tarEntry struct {
		name    string
		modTime time.Time
	}
	var tars []tarEntry
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".gz" {
			info, _ := e.Info()
			mt := time.Time{}
			if info != nil {
				mt = info.ModTime()
			}
			tars = append(tars, tarEntry{name: e.Name(), modTime: mt})
		}
	}
	if len(tars) <= keep {
		return
	}
	sort.Slice(tars, func(i, j int) bool { return tars[i].modTime.Before(tars[j].modTime) })
	for i := 0; i < len(tars)-keep; i++ {
		os.Remove(filepath.Join(dir, tars[i].name))
	}
}

func appendCronLog(msg string) {
	logFile := "/www/server/panel/logs/cron.log"
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), msg))
}
