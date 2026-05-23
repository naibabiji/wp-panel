package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/naibabiji/wp-panel/database"
)

// SyncToRemote 将本地备份目录同步到远程服务器
func SyncToRemote(localPath string) error {
	db := database.GetDB()
	var enabled, keepLocal, port int
	var host, username, authType, password, sshKey, remotePath string
	err := db.QueryRow(`SELECT enabled, host, port, username, auth_type, password, ssh_key, remote_path, keep_local
		FROM remote_backup_settings WHERE id = 1`).Scan(
		&enabled, &host, &port, &username, &authType, &password, &sshKey, &remotePath, &keepLocal)
	if err != nil || enabled == 0 || host == "" || remotePath == "" {
		return nil
	}

	var cmd *exec.Cmd
	if authType == "key" {
		keyPath := "/www/server/panel/remote_backup_key"
		if _, err := os.Stat(keyPath); err != nil {
			return fmt.Errorf("SSH密钥不存在: %s", keyPath)
		}
		cmd = exec.Command("rsync", "-avz", "--delete",
			"-e", fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -p %d", keyPath, port),
			localPath+"/", username+"@"+host+":"+remotePath+"/")
	} else {
		cmd = exec.Command("sshpass", "-p", password, "rsync", "-avz", "--delete",
			"-e", fmt.Sprintf("ssh -o StrictHostKeyChecking=no -p %d", port),
			localPath+"/", username+"@"+host+":"+remotePath+"/")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("远程同步失败: %s", string(out))
	}

	if keepLocal == 0 {
		os.RemoveAll(localPath)
		os.MkdirAll(localPath, 0700)
	}

	return nil
}

// SyncBackupsDir 同步整个备份目录
func SyncBackupsDir() error {
	return SyncToRemote("/www/server/panel/backups")
}

// SyncBackupFile 同步单个备份文件
func SyncBackupFile(localFile string) error {
	db := database.GetDB()
	var enabled int
	var host, remotePath string
	err := db.QueryRow(`SELECT enabled, host, remote_path FROM remote_backup_settings WHERE id = 1`).Scan(&enabled, &host, &remotePath)
	if err != nil || enabled == 0 || host == "" || remotePath == "" {
		return nil
	}

	var username, authType, password, sshKey string
	var port int
	db.QueryRow(`SELECT port, username, auth_type, password, ssh_key FROM remote_backup_settings WHERE id = 1`).Scan(
		&port, &username, &authType, &password, &sshKey)

	remoteFile := filepath.Join(remotePath, filepath.Base(localFile))
	var cmd *exec.Cmd
	if authType == "key" {
		keyPath := "/www/server/panel/remote_backup_key"
		cmd = exec.Command("rsync", "-avz",
			"-e", fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -p %d", keyPath, port),
			localFile, username+"@"+host+":"+remoteFile)
	} else {
		cmd = exec.Command("sshpass", "-p", password, "rsync", "-avz",
			"-e", fmt.Sprintf("ssh -o StrictHostKeyChecking=no -p %d", port),
			localFile, username+"@"+host+":"+remoteFile)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("远程同步失败: %s", string(out))
	}
	_ = out
	return nil
}
