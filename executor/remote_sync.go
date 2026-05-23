package executor

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

// SyncBackupToRemote 将单个备份文件同步到远程服务器。若 keep_local=0，同步成功后删除本地文件。
func SyncBackupToRemote(localFile string) {
	db := database.GetDB()
	var enabled, keepLocal, port int
	var host, username, authType, password, remotePath string
	err := db.QueryRow(`SELECT enabled, host, port, username, auth_type, password, remote_path, keep_local
		FROM remote_backup_settings WHERE id = 1`).Scan(
		&enabled, &host, &port, &username, &authType, &password, &remotePath, &keepLocal)
	if err != nil || enabled == 0 || host == "" || remotePath == "" {
		return
	}

	var cmd *exec.Cmd
	sshOpts := fmt.Sprintf("-o StrictHostKeyChecking=no -o ConnectTimeout=10 -p %d", port)
	if authType == "key" {
		keyPath := "/www/server/panel/remote_backup_key"
		if _, err := os.Stat(keyPath); err != nil {
			syncLog("SSH 密钥不存在: " + keyPath)
			return
		}
		os.Chmod(keyPath, 0600)
		cmd = exec.Command("rsync", "-avz",
			"-e", fmt.Sprintf("ssh -i %s %s", keyPath, sshOpts),
			localFile, username+"@"+host+":"+remotePath+"/")
	} else {
		if _, err := exec.LookPath("sshpass"); err != nil {
			syncLog("sshpass 未安装，请保存设置或手动执行 apt install sshpass")
			return
		}
		cmd = exec.Command("sshpass", "-p", password, "rsync", "-avz",
			"-e", fmt.Sprintf("ssh %s", sshOpts),
			localFile, username+"@"+host+":"+remotePath+"/")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		syncLog(fmt.Sprintf("远程同步失败: %s", string(out)))
		return
	}

	syncLog(fmt.Sprintf("远程同步成功: %s", localFile))

	if keepLocal == 0 {
		os.Remove(localFile)
	}
}

// syncLog 写入操作日志表，用户可在面板「面板设置 -> 最近操作日志」查看
func syncLog(msg string) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Printf("[WP-Panel] %s %s\n", timestamp, msg)
	database.GetDB().Exec(
		"INSERT INTO operation_logs (operation, target, status, message) VALUES (?, ?, ?, ?)",
		"远程备份", "", "info", msg)
}
