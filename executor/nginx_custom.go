package executor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

const nginxCustomDir = "/www/server/panel/nginx-custom"

func executeSaveNginxCustom(task *Task) TaskResult {
	payload, ok := task.Payload.(*SaveNginxCustomPayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}

	site := payload.Site
	domain := site.Domain

	if err := os.MkdirAll(nginxCustomDir, 0755); err != nil {
		return TaskResult{Success: false, Message: "创建配置目录失败: " + err.Error()}
	}

	prePath := filepath.Join(nginxCustomDir, domain+".pre.conf")
	mainPath := filepath.Join(nginxCustomDir, domain+".conf")

	oldPre, _ := os.ReadFile(prePath)
	oldMain, _ := os.ReadFile(mainPath)

	if err := os.WriteFile(prePath, []byte(payload.PreContent), 0644); err != nil {
		return TaskResult{Success: false, Message: "写入 pre.conf 失败: " + err.Error()}
	}
	if err := os.WriteFile(mainPath, []byte(payload.Content), 0644); err != nil {
		os.WriteFile(prePath, oldPre, 0644)
		return TaskResult{Success: false, Message: "写入 conf 失败: " + err.Error()}
	}

	ngxTest := exec.Command("nginx", "-t")
	out, err := ngxTest.CombinedOutput()
	if err != nil {
		os.WriteFile(prePath, oldPre, 0644)
		os.WriteFile(mainPath, oldMain, 0644)
		return TaskResult{Success: false, Message: "Nginx 语法检查失败:\n" + string(out)}
	}

	exec.Command("nginx", "-s", "reload").Run()

	return TaskResult{Success: true, Message: "Nginx 自定义配置已保存并生效"}
}

func executeSetAccessLogMode(task *Task) TaskResult {
	payload, ok := task.Payload.(*SetAccessLogModePayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}

	site := payload.Site
	cfg := config.AppConfig

	aliasList := []string{}
	if site.Aliases != "" {
		for _, a := range strings.Split(site.Aliases, "\n") {
			a = strings.TrimSpace(a)
			if a != "" {
				aliasList = append(aliasList, a)
			}
		}
	}

	allServerNames := buildServerNames(site.Domain, aliasList)
	phpSockPath := filepath.Join(cfg.Paths.PHPFPMSock, site.Domain+".sock")

	engine := NewTemplateEngine(cfg.Panel.BackupDir)
	nginxData := &NginxSiteData{
		Domain:        site.Domain,
		Aliases:       aliasList,
		ServerNames:   allServerNames,
		WebRoot:       site.WebRoot,
		LogDir:        site.LogDir,
		SystemUser:    site.SystemUser,
		UseSSL:        site.SSLEnabled,
		SiteType:     site.SiteType,
		SSLCertPath:   site.SSLCertPath,
		SSLKeyPath:    site.SSLKeyPath,
		PHPProxy:      "unix:" + phpSockPath,
		TemplateVer:   site.TemplateVersion,
		AccessLogMode: payload.Mode,
	}

	nginxConfig, err := engine.RenderNginxConfig(nginxData)
	if err != nil {
		return TaskResult{Success: false, Message: "渲染 Nginx 配置失败: " + err.Error()}
	}

	if err := engine.ApplyNginxConfig(nginxConfig, site.NginxConfPath,
		filepath.Join(cfg.Paths.NginxSitesEnabled, site.Domain+".conf")); err != nil {
		return TaskResult{Success: false, Message: "应用 Nginx 配置失败: " + err.Error()}
	}

	// Update database
	db := database.GetDB()
	db.Exec("UPDATE websites SET access_log_mode = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", payload.Mode, site.ID)

	// Clear log file when turning off
	if payload.Mode == "off" {
		logFile := filepath.Join(site.LogDir, "access.log")
		os.WriteFile(logFile, []byte{}, 0644)
	}

	modeLabels := map[string]string{
		"off":        "访问日志已关闭",
		"error_only": "访问日志已设为仅记录异常",
		"full":       "访问日志已设为全部记录",
	}
	msg := modeLabels[payload.Mode]
	if msg == "" {
		msg = "访问日志模式已更新"
	}
	return TaskResult{Success: true, Message: msg}
}
