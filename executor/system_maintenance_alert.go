package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

var maintenanceAlerts = maintenanceAlertMonitor{rule: alertRule{key: maintenanceAlertKey}}

const maintenanceReminderInterval = 24 * time.Hour

type maintenanceAlertMonitor struct {
	rule          alertRule
	pendingSample time.Time
	pendingReason string
	dispatch      func(func())
}

// This value occupies last_message for the maintenance rule only. That rule
// has no consumer of the previous display text. History still stores plain text
// and old binaries can load the existing normal/pending/firing row.
type maintenanceAlertState struct {
	Kind      string   `json:"kind"`
	Units     []string `json:"units,omitempty"`
	ActionIDs []int64  `json:"actions,omitempty"`
	Reminded  bool     `json:"reminded,omitempty"`
}

func (m *maintenanceAlertMonitor) run(ctx context.Context, db *sql.DB) error {
	retryDirtyAlertRuntimeState(&m.rule)
	if !isRuleEnabled(maintenanceAlertKey) {
		disableAlertRule(&m.rule)
		m.pendingSample = time.Time{}
		return nil
	}
	actions, actionErr := loadMaintenanceActions(db)
	policy, policyErr := ReadSystemMaintenancePolicy(db)
	location, locationErr := currentMaintenanceLocation()
	now := maintenanceClock()
	inspection := SnapshotSystemMaintenanceInspection()
	awaitingRound, err := m.evaluate(db, actions, inspection, policy, location, now, errors.Join(actionErr, policyErr))
	if err != nil || actionErr != nil || policyErr != nil {
		return errors.Join(err, actionErr, policyErr)
	}
	if locationErr != nil && policy.Mode == "scheduled" {
		reportAlertStateError("维护提醒时区不可用: %v", locationErr)
	}
	return notifyMaintenanceActions(ctx, db, actions, policy, awaitingRound)
}

func (m *maintenanceAlertMonitor) evaluate(db *sql.DB, actions []systemMaintenanceAction, inspection SystemMaintenanceInspection, policy SystemMaintenancePolicy, location *time.Location, now time.Time, readErr error) (int64, error) {
	r := &m.rule
	var state maintenanceAlertState
	if r.firing {
		// A pre-upgrade row has plain text. Its firing status and time remain
		// authoritative, but the old text cannot identify an action or unit.
		_ = json.Unmarshal([]byte(r.lastAlertMsg), &state)
	}
	packageStatus := ReadSystemPackageUpdateStatus(config.AppConfig)
	actionIDs, actionPending := maintenanceActiveActions(actions, packageStatus)
	kind, detail := maintenanceInspectionNotice(inspection, readErr)
	evidenceAt := inspection.sampledAt
	if readErr != nil {
		// The failing read was attempted in this evaluation.
		evidenceAt = now
	}
	if kind == "" && inspection.checkFailed && len(actionIDs) == 0 {
		kind, detail = "check_failed", "自动维护检查未能完成，运行状态无法确认。"
		evidenceAt = parseAlertStateTime(inspection.CheckedAt)
	}
	if kind != "" {
		// Independent actionable states take precedence over routine sampling.
		if state.Kind != kind {
			r.pendingSince = now
			return 0, m.raise(now, maintenanceAlertState{Kind: kind}, "自动维护需要人工关注", detail)
		}
		due := r.lastFired.Add(maintenanceReminderInterval)
		if !r.lastFired.IsZero() && !now.Before(due) && !evidenceAt.Before(due) {
			return 0, m.raise(now, state, "自动维护仍需人工关注", detail)
		}
		return 0, nil
	}
	if len(actionIDs) > 0 {
		if state.Kind != "action" || !slices.Equal(state.ActionIDs, actionIDs) {
			return 0, m.raise(now, maintenanceAlertState{Kind: "action", ActionIDs: actionIDs}, "", "")
		}
		return 0, nil
	}
	round, lastActionAt := maintenanceRecoveryRound(actions, state, r.pendingSince)
	if round.ID != 0 && (!inspection.sampledAt.After(r.lastFired) || !inspection.sampledAt.After(lastActionAt)) {
		// A related round waits for newer evidence before choosing its message.
		return round.ID, nil
	}
	if readErr == nil && inspection.Status == "unknown" && !inspection.sampledAt.IsZero() {
		// Adopt an existing alert without a second first notification on upgrade.
		if r.firing && state.Kind == "" {
			r.pendingSince = r.lastFired
			return 0, m.saveState(maintenanceAlertState{Kind: "unknown", Units: maintenanceKnownUnknownUnits(inspection)})
		}
		if state.Kind != "unknown" {
			if m.pendingSample.IsZero() || m.pendingReason != inspection.unknownReason {
				m.pendingSample, m.pendingReason = inspection.sampledAt, inspection.unknownReason
				if !r.firing {
					r.pendingSince = inspection.sampledAt
				}
				return 0, persistAlertRuntimeState(r)
			}
			if !inspection.sampledAt.After(m.pendingSample) {
				return 0, nil
			}
			r.pendingSince = m.pendingSample
			return 0, m.raise(now, maintenanceAlertState{Kind: "unknown", Units: maintenanceKnownUnknownUnits(inspection)}, "自动维护待确认", maintenanceUnknownNotice(inspection, packageStatus, policy, location, now))
		}
		if maintenanceReminderDue(state, r, policy, location, inspection.sampledAt, now) {
			state.Reminded = true
			state.Units = maintenanceKnownUnknownUnits(inspection)
			return 0, m.raise(now, state, "自动维护仍待确认", maintenanceUnknownNotice(inspection, packageStatus, policy, location, now))
		}
		return 0, nil
	}
	if inspection.sampledAt.IsZero() {
		return 0, nil
	}
	m.pendingSample = time.Time{}
	if !r.firing {
		if !r.pendingSince.IsZero() {
			r.pendingSince = time.Time{}
			return 0, persistAlertRuntimeState(r)
		}
		return 0, nil
	}
	if actionPending || inspection.Status != "checked" {
		return 0, nil
	}
	if !inspection.sampledAt.After(r.lastFired) || !inspection.sampledAt.After(lastActionAt) {
		return 0, nil
	}
	if round.ID != 0 {
		marker := maintenanceNotificationPrefix(round) + "recovered,"
		result, err := db.Exec(`UPDATE system_maintenance_actions SET notified_status=? WHERE id=? AND notified_status=?`, marker, round.ID, round.NotifiedStatus)
		if err != nil {
			return 0, err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return 0, errors.Join(err, errors.New("maintenance recovery round changed during notification"))
		}
		for i := range actions {
			if actions[i].ID == round.ID {
				actions[i].NotifiedStatus = marker
				break
			}
		}
	}
	if _, err := db.Exec(`UPDATE alert_log SET resolved=1 WHERE alert_type=? AND resolved=0`, maintenanceAlertKey); err != nil {
		return 0, err
	}
	r.firing, r.pendingSince, r.lastAlertMsg = false, time.Time{}, ""
	if round.ID == 0 {
		m.deliver("系统自动维护告警已解除", "后续运行检查已确认此前维护告警解除。检查时间 "+maintenanceNoticeTime(inspection.sampledAt)+"。详情见设置的系统更新卡片。", false)
	}
	return 0, persistAlertRuntimeState(r)
}

func (m *maintenanceAlertMonitor) raise(now time.Time, state maintenanceAlertState, title, detail string) error {
	r := &m.rule
	if r.pendingSince.IsZero() {
		r.pendingSince = now
	}
	r.firing, r.lastFired = true, now
	m.pendingSample = time.Time{}
	if title != "" {
		m.deliver(title, detail+" 首次发现时间 "+maintenanceNoticeTime(r.pendingSince)+"。请在设置的系统更新卡片查看详情。", true)
	}
	return m.saveState(state)
}

func (m *maintenanceAlertMonitor) saveState(state maintenanceAlertState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	m.rule.lastAlertMsg = string(raw)
	return persistAlertRuntimeState(&m.rule)
}

func (m *maintenanceAlertMonitor) deliver(title, detail string, firing bool) {
	if firing {
		logAlert(maintenanceAlertKey, "critical", detail)
	} else {
		logAlertEvent(maintenanceAlertKey, "info", detail, "")
	}
	hasMail, hasWebhook := alertSMTPConfigured(GetSMTPConfig()), webhookConfigured(GetWebhookConfig())
	if !hasMail && !hasWebhook {
		return
	}
	subject := getPanelTitle() + " " + title
	sendMail, sendWebhook := maintenanceSendMail, maintenanceSendWebhook
	dispatch := m.dispatch
	if dispatch == nil {
		dispatch = func(send func()) { go send() }
	}
	if hasMail {
		dispatch(func() {
			if err := sendMail("", subject, formatEmailHTML(title, detail, "", firing)); err != nil {
				reportAlertStateError("维护邮件发送失败: %v", err)
			}
		})
	}
	if hasWebhook {
		dispatch(func() {
			if err := sendWebhook(subject, detail); err != nil {
				reportAlertStateError("维护 Webhook 发送失败: %v", err)
			}
		})
	}
}

func maintenanceBlockedActionsOwnNotices(blocked []maintenanceTarget, actions []systemMaintenanceAction) bool {
	for _, target := range blocked {
		if !slices.ContainsFunc(actions, func(action systemMaintenanceAction) bool {
			return action.Kind == target.Kind && action.Target == target.Target && (action.Status == "failed" || action.Status == "unknown")
		}) {
			return false
		}
	}
	return true
}

func maintenanceActiveActions(actions []systemMaintenanceAction, status SystemPackageUpdateStatus) ([]int64, bool) {
	var ids []int64
	pending, foundInstall := false, false
	for _, a := range actions {
		if a.Status == "requested" && (a.Kind == "service" || a.Kind == "reboot") {
			pending = true
		}
		if a.Status != "failed" && a.Status != "unknown" {
			continue
		}
		if a.Kind == "service" || a.Kind == "reboot" {
			ids = append(ids, a.ID)
		}
		if a.Kind == "round" && !foundInstall && strings.HasPrefix(a.MessageCode, "installation_") {
			foundInstall = true
			started, startErr := time.Parse(time.RFC3339Nano, status.StartedAt)
			roundAt, roundErr := time.Parse(time.RFC3339Nano, a.RequestedAt)
			if status.Status != "success" || startErr != nil || roundErr != nil || started.Before(roundAt.Add(-2*time.Second)) {
				ids = append(ids, a.ID)
			}
		}
	}
	slices.Sort(ids)
	return ids, pending
}

func maintenanceInspectionNotice(inspection SystemMaintenanceInspection, readErr error) (string, string) {
	switch {
	case readErr != nil:
		return "unreadable", "自动维护状态读取失败，无法确认运行结果。"
	case inspection.RebootUnknown:
		return "reboot_unknown", "自动维护无法确认内核或重启标记，需要检查服务器。"
	case inspection.BudgetBlocked && !inspection.budgetOwned:
		return "budget", "自动维护存在已尝试的目标，需要人工重新检查并重试。"
	default:
		return "", ""
	}
}

func maintenanceKnownUnknownUnits(inspection SystemMaintenanceInspection) []string {
	if inspection.UnknownUnits != len(inspection.unknownUnits) || slices.Contains(inspection.unknownUnits, "*") {
		return nil
	}
	return inspection.unknownUnits
}

func maintenanceUnknownNotice(inspection SystemMaintenanceInspection, packageStatus SystemPackageUpdateStatus, policy SystemMaintenancePolicy, location *time.Location, now time.Time) string {
	message := fmt.Sprintf("运行检查有 %d 个单元无法确认。", inspection.UnknownUnits)
	if packageStatus.Status == "success" {
		message += "最近一次软件包安装任务已完成。"
	}
	var units []string
	for _, unit := range inspection.unknownUnits {
		if maintenanceServiceTarget.MatchString(unit) {
			units = append(units, unit)
		}
	}
	if len(units) > 0 {
		message += "涉及 " + strings.Join(units, "、") + "。"
	}
	switch inspection.unknownReason {
	case "mapping_path_missing", "mapping_unreadable", "process_unreadable", "non_host_view":
		message += "无法从受支持的进程视图核验至少一项文件映射。"
	case "package_unconfirmed":
		message += "至少一项映射无法确认所属软件包。"
	}
	if start, end := maintenanceNoticeWindow(policy, location, now); !start.IsZero() {
		message += "适用的计划维护窗口为 " + maintenanceNoticeTime(start) + " 至 " + maintenanceNoticeTime(end) + "。"
	}
	return message
}

func maintenanceNoticeWindow(policy SystemMaintenancePolicy, location *time.Location, at time.Time) (time.Time, time.Time) {
	start, end, err := maintenanceWindowMinutes(policy.Window)
	if err != nil || policy.Mode != "scheduled" || !policy.Enabled || location == nil {
		return time.Time{}, time.Time{}
	}
	local := at.In(location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, location)
	if end < start && local.Hour()*60+local.Minute() < end {
		day = day.AddDate(0, 0, -1)
	}
	windowStart := time.Date(day.Year(), day.Month(), day.Day(), start/60, start%60, 0, 0, location)
	if end < start {
		day = day.AddDate(0, 0, 1)
	}
	windowEnd := time.Date(day.Year(), day.Month(), day.Day(), end/60, end%60, 0, 0, location)
	if !at.Before(windowEnd) {
		windowStart = windowStart.AddDate(0, 0, 1)
		windowEnd = windowEnd.AddDate(0, 0, 1)
	}
	return windowStart, windowEnd
}

func maintenanceReminderDue(state maintenanceAlertState, r *alertRule, policy SystemMaintenancePolicy, location *time.Location, sample, now time.Time) bool {
	if !policy.Enabled || r.lastFired.IsZero() || !sample.After(r.lastFired) {
		return false
	}
	due := r.lastFired.Add(6 * time.Hour)
	if state.Reminded {
		due = r.lastFired.Add(maintenanceReminderInterval)
	} else if policy.Mode == "scheduled" {
		_, end := maintenanceNoticeWindow(policy, location, r.pendingSince)
		if end.IsZero() {
			return false
		}
		due = end.Add(30 * time.Minute)
		if !due.After(r.lastFired) {
			due = r.lastFired.Add(maintenanceReminderInterval)
		}
	}
	return !now.Before(due) && !sample.Before(due)
}

// A successful round can own the terminal notice only if its verified child
// actions account for every known affected unit or outstanding action.
func maintenanceRecoveryRound(actions []systemMaintenanceAction, state maintenanceAlertState, firstSeen time.Time) (systemMaintenanceAction, time.Time) {
	var relatedAt time.Time
	for _, round := range actions {
		if round.Kind != "round" || round.Status != "succeeded" {
			continue
		}
		units := make(map[string]bool)
		var successful []int64
		var lastAction time.Time
		for _, child := range actions {
			if child.RoundID != round.ID || child.Status != "succeeded" || child.FinishedAt == "" {
				continue
			}
			successful = append(successful, child.ID)
			if finished := maintenanceActionTime(child); finished.After(lastAction) {
				lastAction = finished
			}
			var evidence []maintenanceStoredEvidence
			if json.Unmarshal([]byte(child.LastSuccessJSON), &evidence) == nil {
				for _, item := range evidence {
					units[item.Unit] = true
				}
			}
		}
		if lastAction.IsZero() || lastAction.Before(firstSeen) || !maintenanceRecoveryMatches(state, units, successful) {
			continue
		}
		if lastAction.After(relatedAt) {
			relatedAt = lastAction
		}
		prefix := maintenanceNotificationPrefix(round)
		if !strings.HasPrefix(round.NotifiedStatus, prefix) || round.NotifiedStatus == prefix+"recovered," {
			return round, relatedAt
		}
	}
	return systemMaintenanceAction{}, relatedAt
}

func maintenanceRecoveryMatches(state maintenanceAlertState, units map[string]bool, ids []int64) bool {
	switch state.Kind {
	case "unknown":
		return len(state.Units) > 0 && !slices.ContainsFunc(state.Units, func(unit string) bool { return !units[unit] })
	case "action":
		return len(state.ActionIDs) > 0 && !slices.ContainsFunc(state.ActionIDs, func(id int64) bool { return !slices.Contains(ids, id) })
	default:
		return false
	}
}
