package handlers

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

func GetRemoteBackup(c *gin.Context) {
	db := database.GetDB()
	var s models.RemoteBackupSettings
	var enabled, port, keepLocal int
	db.QueryRow(`SELECT enabled, host, port, username, auth_type, password, ssh_key, remote_path, keep_local
		FROM remote_backup_settings WHERE id = 1`).Scan(
		&enabled, &s.Host, &port, &s.Username, &s.AuthType, &s.Password, &s.SSHKey, &s.RemotePath, &keepLocal)
	s.Enabled = enabled == 1
	s.Port = port
	s.KeepLocal = keepLocal == 1
	if s.Port == 0 {
		s.Port = 22
	}
	// 读取公钥
	if s.AuthType == "key" {
		keyData, err := os.ReadFile("/www/server/panel/remote_backup_key.pub")
		if err == nil {
			s.SSHKey = string(keyData)
		}
	}
	c.JSON(http.StatusOK, models.SuccessResponse(s))
}

func SaveRemoteBackup(c *gin.Context) {
	var req models.RemoteBackupSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	if req.Port == 0 {
		req.Port = 22
	}

	if req.AuthType == "key" {
		keyPath := "/www/server/panel/remote_backup_key"
		if _, err := os.Stat(keyPath); err != nil {
			out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", keyPath, "-N", "", "-q").CombinedOutput()
			if err != nil {
				c.JSON(http.StatusInternalServerError, models.ErrorResponse("生成SSH密钥失败: "+string(out)))
				return
			}
		}
	} else if req.Enabled {
		if _, err := exec.LookPath("sshpass"); err != nil {
			exec.Command("apt-get", "update").Run()
			exec.Command("apt-get", "install", "-y", "sshpass").Run()
		}
	}

	enabledVal := 0
	if req.Enabled {
		enabledVal = 1
	}
	keepLocalVal := 0
	if req.KeepLocal {
		keepLocalVal = 1
	}

	db := database.GetDB()
	db.Exec(`UPDATE remote_backup_settings SET enabled=?, host=?, port=?, username=?, auth_type=?, password=?, remote_path=?, keep_local=?, updated_at=CURRENT_TIMESTAMP WHERE id=1`,
		enabledVal, req.Host, req.Port, req.Username, req.AuthType, req.Password, req.RemotePath, keepLocalVal)

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "设置已保存"}))
}

func TestRemoteBackup(c *gin.Context) {
	db := database.GetDB()
	var host, username, authType, password, sshKey, remotePath string
	var port int
	db.QueryRow(`SELECT host, port, username, auth_type, password, ssh_key, remote_path FROM remote_backup_settings WHERE id = 1`).Scan(
		&host, &port, &username, &authType, &password, &sshKey, &remotePath)
	if remotePath == "" {
		remotePath = "/home/" + username + "/backup"
	}
	if host == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("请先填写远程服务器地址"))
		return
	}

	var cmd *exec.Cmd
	if authType == "key" {
		keyPath := "/www/server/panel/remote_backup_key"
		if _, err := os.Stat(keyPath); err != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("SSH密钥不存在，请先保存设置生成密钥"))
			return
		}
		cmd = exec.Command("ssh", "-i", keyPath, "-o", "StrictHostKeyChecking=accept-new",
			"-p", fmt.Sprintf("%d", port), username+"@"+host, "echo WP_PANEL_OK")
	} else {
		cmd = exec.Command("sshpass", "-e", "ssh", "-o", "StrictHostKeyChecking=accept-new",
			"-p", fmt.Sprintf("%d", port), username+"@"+host, "echo WP_PANEL_OK")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(fmt.Sprintf("连接失败: %s", string(out))))
		return
	}
	if !strings.Contains(string(out), "WP_PANEL_OK") {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("连接异常: "+string(out)))
		return
	}

	// 测试 rsync 到远程备份目录
	testFile := "/tmp/wp-panel-rsync-test.txt"
	os.WriteFile(testFile, []byte("WP Panel rsync test"), 0644)
	defer os.Remove(testFile)

	var testCmd *exec.Cmd
	if authType == "key" {
		testCmd = exec.Command("rsync", "-avz", "-e",
			fmt.Sprintf("ssh -i /www/server/panel/remote_backup_key -o StrictHostKeyChecking=accept-new -p %d", port),
			testFile, username+"@"+host+":"+remotePath+"/.wp-panel-rsync-test.txt")
	} else {
		testCmd = exec.Command("sshpass", "-e", "rsync", "-avz", "-e",
			fmt.Sprintf("ssh -o StrictHostKeyChecking=accept-new -p %d", port),
			testFile, username+"@"+host+":"+remotePath+"/.wp-panel-rsync-test.txt")
	}
	testOut, testErr := testCmd.CombinedOutput()
	if testErr != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(fmt.Sprintf("rsync 测试失败: %s", string(testOut))))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "连接测试成功，rsync 可用"}))
}
