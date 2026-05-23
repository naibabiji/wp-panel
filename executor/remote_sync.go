package executor

import (
	"fmt"
	"os"
	"os/exec"

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
	if authType == "key" {
		keyPath := "/www/server/panel/remote_backup_key"
		if _, err := os.Stat(keyPath); err != nil {
			return
		}
		cmd = exec.Command("rsync", "-avz",
			"-e", fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -p %d", keyPath, port),
			localFile, username+"@"+host+":"+remotePath+"/")
	} else {
		cmd = exec.Command("sshpass", "-p", password, "rsync", "-avz",
			"-e", fmt.Sprintf("ssh -o StrictHostKeyChecking=no -p %d", port),
			localFile, username+"@"+host+":"+remotePath+"/")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("[WP-Panel] 远程同步失败: %s\n", string(out))
		return
	}

	if keepLocal == 0 {
		os.Remove(localFile)
	}
}
