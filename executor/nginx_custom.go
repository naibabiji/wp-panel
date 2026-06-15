package executor

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
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
		log.Printf("创建配置目录失败: %v", err)
		return TaskResult{Success: false, Message: "创建配置目录失败"}
	}

	prePath := filepath.Join(nginxCustomDir, domain+".pre.conf")
	mainPath := filepath.Join(nginxCustomDir, domain+".conf")

	oldPre, _ := os.ReadFile(prePath)
	oldMain, _ := os.ReadFile(mainPath)

	if err := os.WriteFile(prePath, []byte(payload.PreContent), 0644); err != nil {
		log.Printf("写入 pre.conf 失败: %v", err)
		return TaskResult{Success: false, Message: "写入 pre.conf 失败"}
	}
	if err := os.WriteFile(mainPath, []byte(payload.Content), 0644); err != nil {
		os.WriteFile(prePath, oldPre, 0644)
		log.Printf("写入 conf 失败: %v", err)
		return TaskResult{Success: false, Message: "写入 conf 失败"}
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

	engine := NewTemplateEngine(cfg.Panel.BackupDir)
	nginxData, err := nginxDataFromSiteChecked(site)
	if err != nil {
		return taskFailure("CDN 真实 IP 配置无效", err)
	}
	nginxData.AccessLogMode = payload.Mode

	nginxConfig, err := engine.RenderNginxConfig(nginxData)
	if err != nil {
		log.Printf("渲染 Nginx 配置失败: %v", err)
		return taskFailure("渲染 Nginx 配置失败", err)
	}

	if err := engine.ApplyNginxConfig(nginxConfig, site.NginxConfPath,
		nginxEnabledPath(cfg, site.NginxConfPath, site.Domain)); err != nil {
		log.Printf("应用 Nginx 配置失败: %v", err)
		return taskFailure("应用 Nginx 配置失败", err)
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

func executeSetCDNRealIP(task *Task) TaskResult {
	payload, ok := task.Payload.(*SetCDNRealIPPayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}
	site := payload.Site
	if site == nil {
		return TaskResult{Success: false, Message: "网站不存在"}
	}

	var groups []models.CDNRealIPGroup
	var err error
	if payload.Enabled {
		groups, err = GetEnabledCDNRealIPGroupsByIDs(payload.GroupIDs)
		if err != nil {
			return TaskResult{Success: false, Message: err.Error()}
		}
		if len(groups) == 0 {
			return TaskResult{Success: false, Message: "启用 CDN 真实 IP 时至少选择一个配置组"}
		}
	}

	siteCopy := *site
	siteCopy.CDNRealIPEnabled = payload.Enabled
	siteCopy.CDNRealIPGroups = groups
	if _, err := ResolveCDNRealIPRuntime(&siteCopy); err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}

	cfg := config.AppConfig
	engine := NewTemplateEngine(cfg.Panel.BackupDir)
	nginxData, err := nginxDataFromSiteChecked(&siteCopy)
	if err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}
	nginxConfig, err := engine.RenderNginxConfig(nginxData)
	if err != nil {
		log.Printf("渲染 Nginx 配置失败: %v", err)
		return taskFailure("渲染 Nginx 配置失败", err)
	}

	oldEnabled := site.CDNRealIPEnabled
	oldGroupIDs := cdnRealIPGroupIDs(site.CDNRealIPGroups)
	oldNginxData, oldDataErr := nginxDataFromSiteChecked(site)
	var oldNginxConfig string
	var oldRenderErr error
	if oldDataErr == nil {
		oldNginxConfig, oldRenderErr = engine.RenderNginxConfig(oldNginxData)
	} else {
		oldRenderErr = oldDataErr
	}
	if err := SaveWebsiteCDNRealIPSettings(site.ID, payload.Enabled, payload.GroupIDs); err != nil {
		return taskFailure("保存 CDN 真实 IP 设置失败", err)
	}
	if err := engine.ApplyNginxConfig(nginxConfig, site.NginxConfPath,
		nginxEnabledPath(cfg, site.NginxConfPath, site.Domain)); err != nil {
		log.Printf("应用 Nginx 配置失败: %v", err)
		_ = SaveWebsiteCDNRealIPSettings(site.ID, oldEnabled, oldGroupIDs)
		return taskFailure("应用 Nginx 配置失败", err)
	}
	if err := ApplyFail2banSettings(); err != nil {
		_ = SaveWebsiteCDNRealIPSettings(site.ID, oldEnabled, oldGroupIDs)
		if oldRenderErr == nil {
			_ = engine.ApplyNginxConfig(oldNginxConfig, site.NginxConfPath,
				nginxEnabledPath(cfg, site.NginxConfPath, site.Domain))
		}
		return taskFailure("CDN 真实 IP 已回滚，Fail2ban 白名单应用失败", err)
	}

	return TaskResult{Success: true, Message: "CDN 真实 IP 设置已保存并生效"}
}

func cdnRealIPGroupIDs(groups []models.CDNRealIPGroup) []int {
	ids := make([]int, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.ID)
	}
	return ids
}

func boolToDBInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
