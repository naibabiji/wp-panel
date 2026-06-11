package executor

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

const backupsRoot = "/www/server/panel/backups"

var (
	remoteUsernamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}$`)
	remoteHostPattern     = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	remotePathPattern     = regexp.MustCompile(`^[/~][A-Za-z0-9._~/-]*$`)
)

func ValidateRemoteBackupSettings(host string, port int, username string, authType string, remotePath string) error {
	host = strings.TrimSpace(host)
	username = strings.TrimSpace(username)
	authType = strings.TrimSpace(authType)
	remotePath = strings.TrimSpace(remotePath)

	if host == "" {
		return fmt.Errorf("远程服务器地址不能为空")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("远程端口无效")
	}
	if !remoteUsernamePattern.MatchString(username) {
		return fmt.Errorf("远程用户名格式无效")
	}
	if authType != "password" && authType != "key" {
		return fmt.Errorf("远程认证方式无效")
	}
	if ip := net.ParseIP(host); ip != nil {
		if strings.Contains(host, ":") {
			return fmt.Errorf("远程服务器地址暂不支持 IPv6")
		}
	} else {
		if !remoteHostPattern.MatchString(host) || strings.Contains(host, "..") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
			return fmt.Errorf("远程服务器地址格式无效")
		}
		for _, label := range strings.Split(host, ".") {
			if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return fmt.Errorf("远程服务器地址格式无效")
			}
		}
	}
	if remotePath != "" {
		if !remotePathPattern.MatchString(remotePath) || strings.Contains(remotePath, "//") {
			return fmt.Errorf("远程备份目录格式无效")
		}
		for _, part := range strings.Split(remotePath, "/") {
			if part == ".." {
				return fmt.Errorf("远程备份目录不能包含 ..")
			}
		}
	}
	return nil
}

func remoteBackupPath(username, remotePath string) string {
	remotePath = strings.TrimSpace(remotePath)
	if remotePath == "" {
		return "/home/" + username + "/backup"
	}
	return strings.TrimRight(remotePath, "/")
}

func localBackupRelPath(localFile string) (string, error) {
	base := filepath.Clean(backupsRoot)
	clean := filepath.Clean(localFile)
	rel, err := filepath.Rel(base, clean)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("本地备份文件路径非法")
	}
	if strings.ContainsAny(rel, "\x00\r\n") {
		return "", fmt.Errorf("本地备份文件路径非法")
	}
	return filepath.ToSlash(rel), nil
}

// SyncBackupToRemote 将单个备份文件同步到远程服务器，保留 domain/db/ 或 domain/files/ 目录结构。
// 若 keep_local=0，同步成功后删除本地文件。
func SyncBackupToRemote(localFile string) {
	db := database.GetDB()
	var enabled, keepLocal, port int
	var host, username, authType, password, remotePath string
	err := db.QueryRow(`SELECT enabled, host, port, username, auth_type, password, remote_path, keep_local
		FROM remote_backup_settings WHERE id = 1`).Scan(
		&enabled, &host, &port, &username, &authType, &password, &remotePath, &keepLocal)
	if err != nil {
		syncLog("", fmt.Sprintf("读取远程备份设置失败: %v", err), "failed")
		return
	}
	if enabled == 0 {
		return
	}
	if host == "" {
		syncLog("", "远程备份已启用但未填写服务器地址", "failed")
		return
	}
	if port == 0 {
		port = 22
	}
	if username == "" {
		username = "root"
	}
	if authType == "" {
		authType = "password"
	}
	remotePath = remoteBackupPath(username, remotePath)
	if err := ValidateRemoteBackupSettings(host, port, username, authType, remotePath); err != nil {
		syncLog("", "远程备份设置无效: "+err.Error(), "failed")
		return
	}
	relPath, err := localBackupRelPath(localFile)
	if err != nil {
		syncLog("", err.Error(), "failed")
		return
	}

	// 用 /. 标记分离备份根目录和相对路径，rsync -R 保留 ./ 之后的结构
	src := backupsRoot + "/./" + relPath
	dest := fmt.Sprintf("%s@%s:%s/", username, host, remotePath)

	sshOpts := fmt.Sprintf("-o UserKnownHostsFile=/www/server/panel/remote_backup_known_hosts -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -p %d", port)
	var cmd *exec.Cmd
	if authType == "key" {
		keyPath := "/www/server/panel/remote_backup_key"
		if _, err := os.Stat(keyPath); err != nil {
			syncLog("", "SSH 密钥不存在: "+keyPath, "failed")
			return
		}
		if err := os.Chmod(keyPath, 0600); err != nil {
			syncLog("", fmt.Sprintf("SSH 密钥权限设置失败: %v", err), "failed")
			return
		}
		cmd = exec.Command("rsync", "-avzR",
			"-e", fmt.Sprintf("ssh -i %s %s", keyPath, sshOpts),
			src, dest)
	} else {
		if _, err := exec.LookPath("sshpass"); err != nil {
			syncLog("", "sshpass 未安装", "failed")
			return
		}
		cmd = exec.Command("sshpass", "-e", "rsync", "-avzR",
			"-e", fmt.Sprintf("ssh %s", sshOpts),
			src, dest)
		cmd.Env = append(os.Environ(), "SSHPASS="+password)
	}
	domain, _, _ := strings.Cut(relPath, "/")

	out, err := cmd.CombinedOutput()
	if err != nil {
		syncLog(domain, fmt.Sprintf("远程同步失败: %s — %s", relPath, strings.TrimSpace(string(out))), "failed")
		return
	}
	syncLog(domain, fmt.Sprintf("远程同步成功: %s", relPath), "success")

	if keepLocal == 0 {
		os.Remove(localFile)
	}
}

func syncLog(domain string, msg string, status string) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Printf("[WP-Panel] %s %s\n", timestamp, msg)
	if domain == "" {
		domain = "—"
	}
	recordOperationLog("远程备份", domain, status, msg)
}
