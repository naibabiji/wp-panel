package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func ExecuteFileBackup(siteID int, mode string, keepCount int) (string, error) {
	if keepCount <= 0 {
		keepCount = 3
	}

	// 文件备份排队锁：多个站点同时触发时依次执行，避免并发争抢磁盘/CPU
	lockPath := "/tmp/wp-panel-file-backup.lock"
	myPID := fmt.Sprintf("%d", os.Getpid())
	acquired := false
	for i := 0; i < 1440; i++ { // 最多等2小时（每5秒检查一次）
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0644)
		if err == nil {
			f.WriteString(myPID)
			f.Close()
			acquired = true
			break
		}
		// 检查锁持有者是否还活着
		if stale, _ := os.ReadFile(lockPath); len(stale) > 0 {
			pid := strings.TrimSpace(string(stale))
			if _, err := os.Stat("/proc/" + pid); os.IsNotExist(err) {
				os.Remove(lockPath) // 死锁清理
				continue
			}
		}
		time.Sleep(5 * time.Second)
	}
	if !acquired {
		return "", fmt.Errorf("等待备份锁超时（有其他备份任务未完成），请稍后重试")
	}
	defer os.Remove(lockPath)

	db := database.GetDB()
	var domain, webRoot string
	err := db.QueryRow("SELECT domain, web_root FROM websites WHERE id = ?", siteID).Scan(&domain, &webRoot)
	if err != nil {
		return "", fmt.Errorf("网站不存在")
	}

	backupDir := filepath.Join("/www/server/panel/backups", domain, "files")
	os.MkdirAll(backupDir, 0755)
	stampFile := filepath.Join(backupDir, ".last_backup.stamp")

	// Check disk space: need at least 1GB free after backup
	if !checkDiskSpace(backupDir, 1024*1024*1024) {
		return "", fmt.Errorf("磁盘空间不足，备份取消")
	}

	ts := time.Now().Format("20060102_150405")
	var tarName string
	var fullPath string
	var isFull bool

	if mode == "full" {
		isFull = true
	} else {
		if _, err := os.Stat(stampFile); os.IsNotExist(err) {
			isFull = true
		}
	}

	tarExcludes := []string{
		"--exclude=wp-content/cache",
		"--exclude=wp-content/upgrade",
		"--exclude=wp-content/debug.log",
		"--exclude=*.tmp",
		"--exclude=*.bak",
		"--exclude=*.backup",
		"--exclude=*.swp",
		"--exclude=wp-content/updraft",
		"--exclude=wp-content/ai1wm-backups",
		"--exclude=wp-content/backups-dup-lite",
		"--exclude=wp-content/backups-dup-pro",
		"--exclude=wp-content/wpvivid_backups",
		"--exclude=wp-content/backups",
		"--exclude=wp-content/backup-db",
	}

	if isFull {
		tarName = fmt.Sprintf("file_full_%s.tar.gz", ts)
		fullPath = filepath.Join(backupDir, tarName)
		args := []string{"-czf", fullPath, "--warning=no-file-changed", "--ignore-failed-read"}
		args = append(args, tarExcludes...)
		args = append(args, "-C", filepath.Dir(webRoot), filepath.Base(webRoot))
		cmd := exec.Command("tar", args...)
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
		// Check if there are new files since last backup
		checkCmd := exec.Command("find", uploadsDir, "-newer", stampFile, "-type", "f")
		out, _ := checkCmd.Output()
		if len(out) == 0 {
			os.WriteFile(stampFile, []byte(time.Now().Format(time.RFC3339)), 0644)
			return fmt.Sprintf("%s 文件备份跳过: 无新文件", domain), nil
		}
		script := fmt.Sprintf(
			`find '%s' -newer '%s' -type f | tar -czf '%s' --ignore-failed-read -T -`,
			uploadsDir, stampFile, fullPath,
		)
		out, err = exec.Command("bash", "-c", script).CombinedOutput()
		if err != nil {
			if len(out) == 0 {
				return "", fmt.Errorf("增量备份失败: %v", err)
			}
			return "", fmt.Errorf("增量备份失败: %s", string(out))
		}
	}

	os.WriteFile(stampFile, []byte(time.Now().Format(time.RFC3339)), 0644)

	if isFull {
		cleanOldBackups(backupDir, keepCount)
	}

	SyncBackupToRemote(fullPath)
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
		if !e.IsDir() && strings.HasPrefix(e.Name(), "file_full_") && filepath.Ext(e.Name()) == ".gz" {
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

func checkDiskSpace(backupDir string, minFree int64) bool {
	out, err := exec.Command("df", "-B1", backupDir).Output()
	if err != nil {
		return true // can't check, allow to proceed
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) < 2 {
		return true
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		return true
	}
	free, _ := strconv.ParseInt(fields[3], 10, 64)
	return free >= minFree
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
