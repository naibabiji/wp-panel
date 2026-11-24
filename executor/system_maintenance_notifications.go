package executor

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/i18n"
)

const maintenanceAlertKey = "alert_system_maintenance"

var maintenanceSendMail = SendMail
var maintenanceSendWebhook = SendWebhook

func SystemMaintenanceNotificationsConfigured() bool {
	return alertSMTPConfigured(GetSMTPConfig()) || webhookConfigured(GetWebhookConfig())
}

func notifyMaintenanceActions(ctx context.Context, db *sql.DB, actions []systemMaintenanceAction, policy SystemMaintenancePolicy, awaitingRound int64) error {
	deliveries := 0
	for _, a := range actions {
		if a.ID == awaitingRound {
			continue
		}
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
		prefix := maintenanceNotificationPrefix(a)
		sent := a.NotifiedStatus
		if !strings.HasPrefix(sent, prefix) {
			sent = prefix
		}
		reason := i18n.T(i18n.DefaultLang, SystemMaintenanceReasonKey(a.MessageCode))
		title := "自动维护需要关注"
		message := fmt.Sprintf("自动系统维护：%s，%s。请在设置的系统更新卡片查看动作结果。", a.Target, reason)
		if a.Kind == "notice" {
			title = "自动维护设置已迁移"
			message = "原系统自动安装已升级为完整自动维护：默认按服务器时区 04:00–06:00 安装更新，并在必要时重启服务或服务器；可在设置中关闭或调整。"
		}
		if a.Kind == "round" && a.Status == "pending" {
			title = "自动维护已延期"
			message = "自动维护连续延期至少 3 天，需要调整任务或时段；阻止原因：" + reason
		} else if a.Kind == "round" {
			if a.Status == "succeeded" {
				title = "自动维护已完成"
			}
			message = "自动维护轮次结果：" + reason + "。请在系统更新卡片查看详细结果。"
			if strings.Contains(sent, "recovered,") {
				title = "自动维护已完成，告警已解除"
				message += "后续运行检查已确认此前维护告警解除。"
				var targets []string
				for _, child := range actions {
					if child.RoundID == a.ID && child.Status == "succeeded" && maintenanceTargetName.MatchString(child.Target) {
						targets = append(targets, child.Target)
						if len(targets) == 3 {
							break
						}
					}
				}
				if len(targets) > 0 {
					message += "已核验的部分维护目标包括 " + strings.Join(targets, "、") + "。"
				}
			} else if a.Status == "succeeded" && SnapshotSystemMaintenanceInspection().Status == "unknown" {
				message += "仍有运行检查无法确认。"
			}
		}
		if eventAt := maintenanceActionTime(a); !eventAt.IsZero() {
			message += " 记录时间 " + maintenanceNoticeTime(eventAt) + "。"
		}
		if !strings.Contains(sent, "local,") {
			logAlertEvent(maintenanceAlertKey, "info", message, "")
			sent += "local,"
		}
		failed := false
		if smtp := GetSMTPConfig(); alertSMTPConfigured(smtp) && !strings.Contains(sent, "mail,") {
			if err := maintenanceSendMail("", getPanelTitle()+" "+title, formatEmailHTML(title, message, "", a.Status != "succeeded")); err == nil {
				sent += "mail,"
			} else {
				failed = true
				reportAlertStateError("维护邮件发送失败: %v", err)
			}
			deliveries++
		}
		if webhookConfigured(GetWebhookConfig()) && !strings.Contains(sent, "webhook,") {
			if err := maintenanceSendWebhook(getPanelTitle()+" "+title, message); err == nil {
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

func maintenanceNotificationPrefix(a systemMaintenanceAction) string {
	return fmt.Sprintf("%s:%d:", a.Status, a.Attempts)
}

func maintenanceActionTime(a systemMaintenanceAction) time.Time {
	if finished, err := time.Parse(time.RFC3339Nano, a.FinishedAt); err == nil {
		return finished
	}
	requested, _ := time.Parse(time.RFC3339Nano, a.RequestedAt)
	return requested
}

func maintenanceNoticeTime(at time.Time) string {
	if location, err := currentMaintenanceLocation(); err == nil {
		at = at.In(location)
	}
	return at.Format("2006-01-02 15:04 MST")
}
