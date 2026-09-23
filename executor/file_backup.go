package executor

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

var errScheduledWorkNotAllowed = errors.New("网站当前不允许运行自动任务")

var (
	fileBackupLockWait         = 5 * time.Second
	fileBackupLockTimeout      = 2 * time.Hour
	fileBackupLockFileName     = "file-backup.lock"
	fileBackupAfterInitialGate = func() {}
)

type fileBackupLock struct {
	file *os.File
}

func (l *fileBackupLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}

func fileBackupLockPath() (string, error) {
	if config.AppConfig == nil || strings.TrimSpace(config.AppConfig.Panel.DataDir) == "" {
		return "", errors.New("面板数据目录未配置")
	}
	lockDir := filepath.Join(filepath.Clean(config.AppConfig.Panel.DataDir), "locks")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(lockDir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("备份锁目录不安全")
	}
	if err := os.Chmod(lockDir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(lockDir, fileBackupLockFileName), nil
}

func acquireFileBackupLock() (*fileBackupLock, error) {
	path, err := fileBackupLockPath()
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("备份锁文件不安全")
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return nil, err
	}
	deadline := time.Now().Add(fileBackupLockTimeout)
	for {
		if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return &fileBackupLock{file: file}, nil
		} else if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			file.Close()
			return nil, err
		}
		if !time.Now().Before(deadline) {
			file.Close()
			return nil, fmt.Errorf("等待备份锁超时（有其他备份任务未完成），请稍后重试")
		}
		time.Sleep(fileBackupLockWait)
	}
}

func ExecuteFileBackup(siteID int, mode string, keepCount int) (string, error) {
	return executeFileBackup(siteID, mode, keepCount, false)
}

func executeFileBackup(siteID int, mode string, keepCount int, scheduled bool) (string, error) {
	if keepCount <= 0 {
		keepCount = 3
	}
	if scheduled {
		allowed, _, err := siteScheduledWorkAllowed(siteID)
		if err != nil {
			return "", fmt.Errorf("检查网站运行状态失败: %w", err)
		}
		if !allowed {
			return "", errScheduledWorkNotAllowed
		}
		fileBackupAfterInitialGate()
	}

	// Manual requests and short-lived scheduled-CLI processes share this flock.
	// The lock lives below the root-owned panel data directory; legacy /tmp PID
	// files are intentionally ignored and cannot block a new-version backup.
	lock, err := acquireFileBackupLock()
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if scheduled {
		allowed, _, err := siteScheduledWorkAllowed(siteID)
		if err != nil {
			return "", fmt.Errorf("取得备份锁后检查网站运行状态失败: %w", err)
		}
		if !allowed {
			return "", errScheduledWorkNotAllowed
		}
	}

	db := database.GetDB()
	var domain, webRoot string
	err = db.QueryRow("SELECT domain, web_root FROM websites WHERE id = ?", siteID).Scan(&domain, &webRoot)
	if err != nil {
		return "", fmt.Errorf("网站不存在")
	}

	backupDir := filepath.Join(backupsRoot, domain, "files")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("创建文件备份目录失败: %w", err)
	}
	stampFile := filepath.Join(backupDir, ".last_backup.stamp")

	// Check disk space: need at least 1GB free after backup
	if !checkDiskSpace(backupDir, 1024*1024*1024) {
		return "", fmt.Errorf("磁盘空间不足，备份取消")
	}

	backupCutoff := time.Now()
	ts := backupCutoff.Format("20060102_150405")
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

	// 增量备份前确认远程是否已有全量基线：远程服务器被更换或远程数据被清空时，
	// 本地 stamp 文件依然存在，但远程可能只剩增量数据，此时强制转为全量重建基线。
	// 探测失败（连接失败/配置无效）同样按"未确认完整"处理，强制全量。
	forcedFullByRemote := false
	if !isFull {
		hasFull, err := RemoteHasFullFileBackup(domain)
		if err != nil {
			return "", fmt.Errorf("无法确认远程全量基线，已保留现有增量链: %w", err)
		}
		if !hasFull {
			isFull = true
			forcedFullByRemote = true
		}
	}
	oldChain := []string{}
	if isFull {
		oldChain = loadFileBackupChain(siteID)
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
		args := []string{"-czf", fullPath, "--warning=no-file-changed"}
		args = append(args, tarExcludes...)
		args = append(args, "-C", filepath.Dir(webRoot), filepath.Base(webRoot))
		cmd := exec.Command("tar", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			_ = os.Remove(fullPath)
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
		hasFiles, err := createIncrementalFileArchive(uploadsDir, stampFile, fullPath)
		if err != nil {
			return "", err
		}
		if !hasFiles {
			if err := writeBackupStamp(stampFile, backupCutoff); err != nil {
				return "", fmt.Errorf("更新增量备份时间戳失败: %w", err)
			}
			return fmt.Sprintf("%s 文件备份跳过: 无新文件", domain), nil
		}
	}

	if err := verifyFileBackupArchive(fullPath); err != nil {
		_ = os.Remove(fullPath)
		return "", err
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		return "", fmt.Errorf("读取文件备份结果失败: %w", err)
	}
	size := info.Size()
	modeLabel := "incremental"
	if isFull {
		modeLabel = "full"
	}
	if !recordFileBackup(siteID, tarName, size, modeLabel, domain) {
		return "", fmt.Errorf("文件备份已生成，但记录写入失败，未执行远程换代")
	}

	remoteEnabled := remoteBackupEnabled()
	remoteSynced := SyncBackupToRemote(fullPath, BackupSourceFile, siteID, tarName)
	if remoteEnabled && !remoteSynced {
		return "", fmt.Errorf("本地文件备份已生成，但远程同步失败，旧备份链已保留")
	}
	if err := writeBackupStamp(stampFile, backupCutoff); err != nil {
		return "", fmt.Errorf("文件备份已生成，但更新增量备份时间戳失败: %w", err)
	}
	if isFull {
		if remoteEnabled && remoteSynced {
			cleanupSupersededFileBackupChain(siteID, domain, backupDir, oldChain)
		} else if !remoteEnabled {
			cleanOldBackups(backupDir, keepCount, siteID)
		}
	}
	logMsg := fmt.Sprintf("%s 文件备份成功: %s (%s)", domain, tarName, map[bool]string{true: "全量", false: "增量"}[isFull])
	if forcedFullByRemote {
		logMsg += "；检测到远程无全量基线，已自动转为全量备份"
	}
	appendCronLog(logMsg)
	return logMsg, nil
}

func createIncrementalFileArchive(uploadsDir, stampFile, target string) (bool, error) {
	checkCmd := exec.Command("find", uploadsDir, "-newer", stampFile, "-type", "f")
	files, err := checkCmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("扫描增量文件失败: %s", strings.TrimSpace(string(files)))
	}
	if len(files) == 0 {
		return false, nil
	}
	cmd := exec.Command("tar", "-czf", target, "--verbatim-files-from", "-T", "-")
	cmd.Stdin = bytes.NewReader(files)
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(target)
		if len(out) == 0 {
			return false, fmt.Errorf("增量备份失败: %v", err)
		}
		return false, fmt.Errorf("增量备份失败: %s", strings.TrimSpace(string(out)))
	}
	return true, nil
}

func verifyFileBackupArchive(path string) error {
	cmd := exec.Command("tar", "-tzf", path)
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("文件备份归档校验失败: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

func writeBackupStamp(path string, cutoff time.Time) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".last-backup-stamp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(cutoff.Format(time.RFC3339Nano)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0644); err != nil {
		return err
	}
	if err := os.Chtimes(tmpPath, cutoff, cutoff); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func remoteBackupEnabled() bool {
	var enabled int
	return database.GetDB().QueryRow(`SELECT enabled FROM remote_backup_settings WHERE id=1`).Scan(&enabled) == nil && enabled == 1
}

func loadFileBackupChain(siteID int) []string {
	rows, err := database.GetDB().Query(`SELECT filename FROM file_backups WHERE site_id=? ORDER BY created_at, id`, siteID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var files []string
	for rows.Next() {
		var filename string
		if rows.Scan(&filename) == nil {
			files = append(files, filename)
		}
	}
	return files
}

// cleanupSupersededFileBackupChain 仅清理新全量开始前冻结的旧链名单。远端删除成功后才删除
// 本地文件和数据库记录；失败项保留，供后台维护重试。
func cleanupSupersededFileBackupChain(siteID int, domain, backupDir string, oldChain []string) int {
	return cleanupSupersededFileBackupChainWith(siteID, domain, backupDir, oldChain,
		deleteRemoteBackupFile,
		func(path string) error { return os.Remove(path) },
		func(siteID int, filename string) error {
			_, err := database.GetDB().Exec(`DELETE FROM file_backups WHERE site_id=? AND filename=?`, siteID, filename)
			return err
		})
}

func cleanupSupersededFileBackupChainWith(siteID int, domain, backupDir string, oldChain []string,
	deleteRemote func(string) error, removeLocal func(string) error, deleteRecord func(int, string) error) int {
	cleaned := 0
	for _, filename := range oldChain {
		relPath := domain + "/files/" + filename
		if err := deleteRemote(relPath); err != nil {
			log.Printf("清理远程旧文件备份失败 [%s]: %v", relPath, err)
			continue
		}
		localPath := filepath.Join(backupDir, filename)
		if err := removeLocal(localPath); err != nil && !os.IsNotExist(err) {
			log.Printf("清理本地旧文件备份失败 [%s]: %v", localPath, err)
			continue
		}
		if err := deleteRecord(siteID, filename); err != nil {
			log.Printf("清理旧文件备份记录失败 [%s]: %v", relPath, err)
			continue
		}
		cleaned++
	}
	return cleaned
}

func cleanOldBackups(dir string, keep int, siteID int) {
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
	db := database.GetDB()
	for i := 0; i < len(tars)-keep; i++ {
		// 文件确实还在磁盘上删除失败（权限/占用/文件系统异常）时，不能删 file_backups 记录，
		// 否则会出现"磁盘有文件但总览查不到"的反向不一致；下次轮转会重试。
		// 文件本就已经不存在（IsNotExist）则视为清理成功，照常删除记录。
		if err := os.Remove(filepath.Join(dir, tars[i].name)); err != nil && !os.IsNotExist(err) {
			log.Printf("清理过期文件备份失败 [%s]: %v", tars[i].name, err)
			continue
		}
		// 保持 file_backups 表和磁盘一致，避免备份总览页面展示已被轮转清理的记录。
		db.Exec(`DELETE FROM file_backups WHERE site_id = ? AND filename = ?`, siteID, tars[i].name)
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

// recordFileBackup 把生成的文件备份写入 file_backups 表，供备份总览页面展示。
// 写入失败不影响已经生成的备份文件（文件备份成本较高，不因记录写入失败而丢弃），
// 但必须可见地记录下来，避免总览页面缺记录却无人知晓。
func recordFileBackup(siteID int, filename string, size int64, mode, domain string) bool {
	if _, err := database.GetDB().Exec(`INSERT INTO file_backups (site_id, filename, file_size, mode) VALUES (?, ?, ?, ?)`,
		siteID, filename, size, mode); err != nil {
		msg := fmt.Sprintf("%s 文件备份记录写入失败: %v；该备份不会出现在备份总览页面，后续远程同步状态也无法回写", domain, err)
		log.Printf("文件备份记录写入 file_backups 失败 [%s]: %v", domain, err)
		appendCronLog(msg)
		return false
	}
	return true
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
