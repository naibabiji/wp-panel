package executor

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

type watchdogRollbackResult struct {
	Trigger, Version, Stage, Message string
	Time                             time.Time
	LastAttempt                      time.Time
	Submitted                        bool
	MailTarget                       string
}

type watchdogMailReservation struct {
	config *SMTPConfig
	title  string
	day    string
}

func reserveWatchdogMail() *watchdogMailReservation {
	db, smtp := database.GetDB(), GetSMTPConfig()
	if db == nil || !alertSMTPConfigured(smtp) {
		return nil
	}
	var quotaState string
	if err := db.QueryRow(`SELECT svalue FROM security_settings WHERE skey='managed_mail_quota_notice_state'`).Scan(&quotaState); err != nil {
		return nil
	}
	now := time.Now()
	if err := reserveSystemMailAttempt(db, now); err != nil {
		return nil
	}
	return &watchdogMailReservation{config: smtp, title: getPanelTitle(), day: now.Format("2006-01-02")}
}

func reportWatchdogRollback(cfg *config.Config, plan rollbackPlan, stage, detail string, reservedMail *watchdogMailReservation) {
	message := "回退失败，需要人工处理。请联系管理员或使用服务器提供商控制台检查。"
	if stage == "rolled_back" {
		message = "更新失败，旧版已恢复并通过健康检查。请到设置中的面板更新查看详情。"
	}
	result := watchdogRollbackResult{
		Trigger: plan.Trigger, Version: plan.TargetVersion, Stage: stage,
		Message: message + " " + detail, Time: time.Now().UTC(),
	}
	if smtp := GetSMTPConfig(); alertSMTPConfigured(smtp) {
		result.MailTarget = notificationTarget(strings.TrimSpace(smtp.AdminEmail))
	} else if reservedMail != nil {
		result.MailTarget = notificationTarget(strings.TrimSpace(reservedMail.config.AdminEmail))
	}
	log.Printf("[更新守护] %s", result.Message)
	if cfg == nil || cfg.Panel.DataDir == "" {
		return
	}
	directory := filepath.Join(cfg.Panel.DataDir, "update-watchdog-results")
	if err := os.MkdirAll(directory, 0700); err != nil {
		log.Printf("[更新守护] 创建终态目录失败: %v", err)
		return
	}
	name := notificationTarget(plan.CreatedAt + "\x00" + plan.TargetVersion)
	path := filepath.Join(directory, name+".json")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := writeWatchdogRollbackResult(path, result); err != nil {
			log.Printf("[更新守护] 保存回退终态失败: %v", err)
			return
		}
	}
	// Database restore closes this process's handle. Reopen only an existing
	// live database so SMTP and quota checks still run if the panel HTTP is down.
	if database.GetDB() == nil && cfg.SQLite.Path != "" {
		if _, err := os.Stat(cfg.SQLite.Path); err == nil {
			if err := database.Open(cfg.SQLite.Path); err != nil {
				log.Printf("[更新守护] 重新打开通知数据库失败: %v", err)
			}
		}
	}
	if database.GetDB() != nil {
		recordPanelUpdateStage(plan.Trigger, plan.TargetVersion, stage, "failed", result.Message)
	}
	if reservedMail != nil && database.GetDB() == nil && reservedMail.day == time.Now().Format("2006-01-02") {
		if lock, err := tryMaintenanceFileLock(filepath.Join(directory, "delivery.lock")); err == nil {
			submitReservedWatchdogMail(path, reservedMail)
			lock.Close()
		}
	}
	retryWatchdogRollbackMail(cfg)
}

var watchdogSendReservedMail = sendMailWithConfig

func submitReservedWatchdogMail(path string, reserved *watchdogMailReservation) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var result watchdogRollbackResult
	if json.Unmarshal(data, &result) != nil || result.Submitted || !result.LastAttempt.IsZero() {
		return
	}
	result.LastAttempt = time.Now().UTC()
	if err := writeWatchdogRollbackResult(path, result); err != nil {
		return
	}
	if err := watchdogSendReservedMail(reserved.config, "", reserved.title+" 自动更新失败",
		formatEmailHTML("自动更新失败", result.Version+" · "+result.Message, "", true), false); err != nil {
		log.Printf("[更新守护] 回退通知发送失败: %v", err)
		return
	}
	result.Submitted = true
	if err := writeWatchdogRollbackResult(path, result); err != nil {
		log.Printf("[更新守护] 保存回退通知提交状态失败: %v", err)
	}
}

func writeWatchdogRollbackResult(path string, result watchdogRollbackResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0600)
}

func retryWatchdogRollbackMail(cfg *config.Config) {
	smtp := GetSMTPConfig()
	if cfg == nil || cfg.Panel.DataDir == "" {
		return
	}
	directory := filepath.Join(cfg.Panel.DataDir, "update-watchdog-results")
	lock, err := tryMaintenanceFileLock(filepath.Join(directory, "delivery.lock"))
	if err != nil {
		return
	}
	defer lock.Close()
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var result watchdogRollbackResult
		if json.Unmarshal(data, &result) != nil || result.Time.IsZero() {
			continue
		}
		if now.Sub(result.Time) > 7*24*time.Hour {
			_ = os.Remove(path)
			continue
		}
		if !alertSMTPConfigured(smtp) || result.Trigger != "auto" || result.Submitted || now.Sub(result.LastAttempt) < time.Hour {
			continue
		}
		target := notificationTarget(strings.TrimSpace(smtp.AdminEmail))
		if result.MailTarget != "" && result.MailTarget != target {
			continue
		}
		result.MailTarget, result.LastAttempt = target, now
		if err := writeWatchdogRollbackResult(path, result); err != nil {
			log.Printf("[更新守护] 保存通知尝试失败: %v", err)
			return
		}
		title := getPanelTitle() + " 自动更新失败"
		body := formatEmailHTML("自动更新失败", result.Version+" · "+result.Message, "", true)
		if err := sendMailWithConfig(smtp, "", title, body, true); err == nil {
			result.Submitted = true
			if err := writeWatchdogRollbackResult(path, result); err != nil {
				log.Printf("[更新守护] 保存通知提交结果失败: %v", err)
			}
		} else {
			log.Printf("[更新守护] 回退通知发送失败: %v", err)
		}
		return
	}
}
