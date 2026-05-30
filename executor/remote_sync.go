package executor

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

const backupsRoot = "/www/server/panel/backups"

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
		syncLog(fmt.Sprintf("读取远程备份设置失败: %v", err), "failed")
		return
	}
	if enabled == 0 {
		return
	}
	if host == "" {
		syncLog("远程备份已启用但未填写服务器地址", "failed")
		return
	}
	if remotePath == "" {
		remotePath = "/home/" + username + "/backup"
	}

	// 用 /. 标记分离备份根目录和相对路径，rsync -R 保留 ./ 之后的结构
	src := backupsRoot + "/./" + strings.TrimPrefix(localFile, backupsRoot+"/")
	dest := fmt.Sprintf("%s@%s:%s/", username, host, strings.TrimRight(remotePath, "/"))

	sshOpts := fmt.Sprintf("-o UserKnownHostsFile=/www/server/panel/remote_backup_known_hosts -o StrictHostKeyChecking=yes -o ConnectTimeout=10 -p %d", port)
	var cmd *exec.Cmd
	if authType == "key" {
		keyPath := "/www/server/panel/remote_backup_key"
		if _, err := os.Stat(keyPath); err != nil {
			syncLog("SSH 密钥不存在: "+keyPath, "failed")
			return
		}
		if err := os.Chmod(keyPath, 0600); err != nil {
				syncLog(fmt.Sprintf("SSH 密钥权限设置失败: %v", err), "failed")
				return
			}
		cmd = exec.Command("rsync", "-avzR",
			"-e", fmt.Sprintf("ssh -i %s %s", keyPath, sshOpts),
			src, dest)
	} else {
		if _, err := exec.LookPath("sshpass"); err != nil {
			syncLog("sshpass 未安装", "failed")
			return
		}
		cmd = exec.Command("sshpass", "-e", "rsync", "-avzR",
			"-e", fmt.Sprintf("ssh %s", sshOpts),
			src, dest)
		cmd.Env = append(os.Environ(), "SSHPASS="+password)
	}
	out, err := cmd.CombinedOutput()
	relPath := strings.TrimPrefix(localFile, backupsRoot+"/")
	if err != nil {
		syncLog(fmt.Sprintf("远程同步失败: %s — %s", relPath, strings.TrimSpace(string(out))), "failed")
		return
	}
	syncLog(fmt.Sprintf("远程同步成功: %s", relPath), "success")

	if keepLocal == 0 {
		os.Remove(localFile)
	}
}

func syncLog(msg string, status string) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Printf("[WP-Panel] %s %s\n", timestamp, msg)
	database.GetDB().Exec(
		"INSERT INTO operation_logs (operation, target, status, message) VALUES (?, ?, ?, ?)",
		"远程备份", "", status, msg)
}
