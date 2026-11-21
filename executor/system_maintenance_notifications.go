package executor

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/i18n"
)

var maintenanceSendMail = SendMail
var maintenanceSendWebhook = SendWebhook

func SystemMaintenanceNotificationsConfigured() bool {
	return alertSMTPConfigured(GetSMTPConfig()) || webhookConfigured(GetWebhookConfig())
}

func notifyMaintenanceActions(ctx context.Context, db *sql.DB) error {
	if !isRuleEnabled("alert_system_maintenance") {
		return nil
	}
	actions, err := loadMaintenanceActions(db)
	if err != nil {
		return err
	}
	policy, err := ReadSystemMaintenancePolicy(db)
	if err != nil {
		return err
	}
	deliveries := 0
	for _, a := range actions {
		if next, err := time.Parse(time.RFC3339, a.NotifyAfter); err == nil && maintenanceClock().Before(next) {
			continue
		}
		eligible := (a.Kind == "notice" && a.Target == "migration") || ((a.Kind == "service" || a.Kind == "reboot") && (a.Status == "failed" || a.Status == "unknown"))
		if a.Kind == "round" && a.Status == "succeeded" {
			eligible = true
		}
		if a.Kind == "round" && (a.Status == "failed" || a.Status == "unknown") {
			var children int
			if err := db.QueryRow(`SELECT COUNT(*) FROM system_maintenance_actions WHERE round_id=? AND kind IN ('service','reboot') AND status IN ('failed','unknown')`, a.ID).Scan(&children); err != nil {
				return err
			}
			eligible = children == 0
		}
		if a.Kind == "round" && a.Status == "pending" && a.DeferredSince != "" {
			deferred, e := time.Parse(time.RFC3339Nano, a.DeferredSince)
			if e == nil && ((policy.Mode == "immediate" && maintenanceClock().Sub(deferred) >= systemMaintenanceDeferredNotice) || (policy.Mode == "scheduled" && a.DeferredWindows >= 3)) {
				eligible = true
			}
		}
		if !eligible {
			continue
		}
		prefix := fmt.Sprintf("%s:%d:", a.Status, a.Attempts)
		sent := a.NotifiedStatus
		if !strings.HasPrefix(sent, prefix) {
			sent = prefix
		}
		reason := i18n.T(i18n.DefaultLang, SystemMaintenanceReasonKey(a.MessageCode))
		message := fmt.Sprintf("自动系统维护：%s，%s。", a.Target, reason)
		if a.Kind == "notice" {
			message = "原系统自动安装已升级为完整自动维护：默认按服务器时区 04:00–06:00 安装更新，并在必要时重启服务或服务器；可在设置中关闭或调整。"
		}
		if a.Kind == "round" && a.Status == "pending" {
			message = "自动维护连续延期至少 3 天，需要调整任务或时段；阻止原因：" + reason
		} else if a.Kind == "round" {
			message = "自动维护轮次结果：" + reason + "。请在系统更新卡片查看详细结果。"
			if a.Status == "succeeded" && SnapshotSystemMaintenanceInspection().Status == "unknown" {
				message += "仍有运行检查无法确认，不代表全部安全修复已生效。"
			}
		}
		if !strings.Contains(sent, "local,") {
			logAlertEvent("alert_system_maintenance", "info", message, "")
			sent += "local,"
		}
		failed := false
		if smtp := GetSMTPConfig(); alertSMTPConfigured(smtp) && !strings.Contains(sent, "mail,") {
			if err := maintenanceSendMail("", getPanelTitle()+" 自动维护通知", formatEmailHTML("自动系统维护", message, "", a.Status != "succeeded")); err == nil {
				sent += "mail,"
			} else {
				failed = true
				reportAlertStateError("维护邮件发送失败: %v", err)
			}
			deliveries++
		}
		if webhookConfigured(GetWebhookConfig()) && !strings.Contains(sent, "webhook,") {
			if err := maintenanceSendWebhook(getPanelTitle()+" 自动维护通知", message); err == nil {
				sent += "webhook,"
			} else {
				failed = true
				reportAlertStateError("维护 Webhook 发送失败: %v", err)
			}
			deliveries++
		}
		next := ""
		if failed {
			next = maintenanceClock().Add(time.Hour).UTC().Format(time.RFC3339)
		}
		if _, err := db.ExecContext(ctx, `UPDATE system_maintenance_actions SET notified_status=?,notify_after=? WHERE id=? AND status=? AND attempts=?`, sent, next, a.ID, a.Status, a.Attempts); err != nil {
			return err
		}
		if deliveries >= 4 {
			break
		}
	}
	return nil
}

func checkSystemMaintenance() (bool, string) {
	actions, err := loadMaintenanceActions(database.GetDB())
	if err != nil {
		return true, "自动维护状态读取失败，无法确认运行结果，请检查面板。"
	}
	count := 0
	for _, a := range actions {
		if (a.Kind == "service" || a.Kind == "reboot") && (a.Status == "failed" || a.Status == "unknown") {
			count++
		}
	}
	for _, a := range actions {
		if a.Kind != "round" || !strings.HasPrefix(a.MessageCode, "installation_") || (a.Status != "failed" && a.Status != "unknown") {
			continue
		}
		status := ReadSystemPackageUpdateStatus(config.AppConfig)
		started, startErr := time.Parse(time.RFC3339, status.StartedAt)
		roundAt, roundErr := time.Parse(time.RFC3339Nano, a.RequestedAt)
		if status.Status != "success" || startErr != nil || roundErr != nil || started.Before(roundAt.Add(-2*time.Second)) {
			count++
		}
		break
	}
	inspection := SnapshotSystemMaintenanceInspection()
	if inspection.RebootUnknown {
		return true, "自动维护无法确认内核或重启标记的状态，请在系统更新卡片查看结果。"
	}
	if inspection.BudgetBlocked {
		return true, "自动维护存在已尝试的目标，需要人工重新检查并重试。"
	}
	if inspection.Status == "unknown" {
		return true, "自动维护的运行检查无法确认，请在系统更新卡片查看状态。"
	}
	if inspection.Status == "not_checked" {
		var status string
		if err := database.GetDB().QueryRow(`SELECT status FROM alert_runtime_state WHERE alert_type='alert_system_maintenance'`).Scan(&status); err == nil && status == "firing" {
			return true, "自动维护等待重新检查，尚不能确认此前问题已恢复。"
		}
	}
	return count > 0, fmt.Sprintf("自动维护有 %d 项需要人工关注，请在设置的系统更新卡片查看结果。", count)
}
