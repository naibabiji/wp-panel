package executor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

var maintenanceAlerts = maintenanceAlertMonitor{rule: alertRule{key: maintenanceAlertKey}}

type maintenanceAlertMonitor struct {
	rule          alertRule
	pendingSample time.Time
	pendingReason string
}

// This value occupies last_message for the maintenance rule only. That rule
// has no consumer of the previous display text. History still stores plain text
// and old binaries can load the existing normal/pending/firing row.
type maintenanceAlertState struct {
	Kind           string                               `json:"kind"`
	Units          []string                             `json:"units,omitempty"`
	ActionIDs      []int64                              `json:"actions,omitempty"`
	Reminded       bool                                 `json:"reminded,omitempty"`
	WindowEnd      time.Time                            `json:"window_end,omitempty"`
	MailTarget     string                               `json:"mail_target,omitempty"`
	WebhookTarget  string                               `json:"webhook_target,omitempty"`
	MailPending    string                               `json:"mail_pending,omitempty"`
	WebhookPending string                               `json:"webhook_pending,omitempty"`
	RetryAfter     time.Time                            `json:"retry_after,omitempty"`
	Recovery       bool                                 `json:"recovery,omitempty"`
	Title          string                               `json:"title,omitempty"`
	Detail         string                               `json:"detail,omitempty"`
	Problems       map[string]maintenanceUnknownProblem `json:"problems"`
}

type maintenanceUnknownProblem struct {
	First, Last, WindowEnd time.Time
	Confirmed, Reported    bool
}

func (m *maintenanceAlertMonitor) run(ctx context.Context, db *sql.DB) error {
	retryDirtyAlertRuntimeState(&m.rule)
	if !isRuleEnabled(maintenanceAlertKey) {
		disableAlertRule(&m.rule)
		if m.rule.lastAlertMsg != "" {
			m.rule.lastAlertMsg = ""
			if err := persistAlertRuntimeState(&m.rule); err != nil {
				return err
			}
		}
		m.pendingSample = time.Time{}
		return cancelPendingMaintenanceNotices(ctx, db)
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
	var retry maintenanceAlertState
	if json.Unmarshal([]byte(m.rule.lastAlertMsg), &retry) == nil &&
		(m.rule.firing || retry.Recovery) && !retry.RetryAfter.IsZero() && !now.Before(retry.RetryAfter) {
		m.send(&retry, retry.Title, retry.Detail, !retry.Recovery, false, now)
		if retry.Recovery && retry.RetryAfter.IsZero() {
			m.rule.lastAlertMsg = ""
			if err := persistAlertRuntimeState(&m.rule); err != nil {
				return err
			}
		} else if err := m.saveState(retry); err != nil {
			return err
		}
	}
	if err := notifyMaintenanceActions(ctx, db, actions, policy, awaitingRound); err != nil {
		return err
	}
	var state maintenanceAlertState
	if m.rule.firing && json.Unmarshal([]byte(m.rule.lastAlertMsg), &state) == nil && state.Kind == "action" {
		oldMail, oldWebhook := state.MailTarget, state.WebhookTarget
		for _, action := range actions {
			if !slices.Contains(state.ActionIDs, action.ID) {
				continue
			}
			if smtp := GetSMTPConfig(); alertSMTPConfigured(smtp) {
				target := notificationTarget(strings.TrimSpace(smtp.AdminEmail))
				if strings.Contains(action.NotifiedStatus, "smtp-target-"+target+",") {
					state.MailTarget = target
				}
			}
			if webhook := GetWebhookConfig(); webhookConfigured(webhook) {
				target := notificationTarget(webhook.Channel + "\x00" + strings.TrimSpace(webhook.URL))
				if strings.Contains(action.NotifiedStatus, "webhook-target-"+target+",") {
					state.WebhookTarget = target
				}
			}
		}
		if state.MailTarget != oldMail || state.WebhookTarget != oldWebhook {
			return m.saveState(state)
		}
	}
	return nil
}

func (m *maintenanceAlertMonitor) evaluate(db *sql.DB, actions []systemMaintenanceAction, inspection SystemMaintenanceInspection, policy SystemMaintenancePolicy, location *time.Location, now time.Time, readErr error) (int64, error) {
	r := &m.rule
	var state maintenanceAlertState
	if r.firing || !r.pendingSince.IsZero() {
		// A pre-upgrade row has plain text. Its firing status and time remain
		// authoritative, but the old text cannot identify an action or unit.
		_ = json.Unmarshal([]byte(r.lastAlertMsg), &state)
	}
	packageStatus := ReadSystemPackageUpdateStatus(config.AppConfig)
	actionIDs, actionPending := maintenanceActiveActions(actions, packageStatus)
	kind, detail := maintenanceInspectionNotice(inspection, readErr)
	if kind == "" && inspection.checkFailed && len(actionIDs) == 0 {
		kind, detail = "check_failed", "自动维护检查未能完成，运行状态无法确认。"
	}
	if kind != "" {
		// Independent actionable states take precedence over routine sampling.
		if state.Kind != kind {
			r.pendingSince = now
			return 0, m.raise(now, maintenanceAlertState{Kind: kind}, "自动维护需要人工关注", detail)
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
	deadline := maintenanceUnknownDeadline(policy, location, r.pendingSince)
	observing := state.Kind == "unknown" || (!r.firing && !r.pendingSince.IsZero())
	if observing && !state.Reminded && policy.Enabled && !r.pendingSince.IsZero() &&
		(inspection.Status == "unknown" || inspection.sampledAt.IsZero()) &&
		!now.Before(deadline) && inspection.sampledAt.Before(deadline) {
		state.Kind, state.Reminded = "unknown", true
		if state.Problems == nil && !inspection.sampledAt.IsZero() {
			state.Problems = maintenanceBaselineProblems(inspection)
		}
		return 0, m.raise(now, state, "自动维护复查长期未完成", "面板一直未取得新的有效运行检查结果。请在设置的系统更新卡片复制诊断并联系管理员。")
	}
	if state.Kind == "unknown" && policy.Enabled &&
		(inspection.Status == "unknown" || inspection.sampledAt.IsZero()) {
		if units := expireUnverifiedMaintenanceProblems(&state, inspection, policy, location, now); len(units) != 0 {
			detail := "以下新增问题长期未取得有效复查结果：" + strings.Join(units, "、") + "。请复制诊断并联系管理员。"
			return 0, m.raise(now, state, "自动维护复查长期未完成", detail)
		}
	}
	if readErr == nil && inspection.Status == "unknown" && !inspection.sampledAt.IsZero() {
		// Adopt an existing alert without a second first notification on upgrade.
		if r.firing && state.Kind == "" {
			r.pendingSince = r.lastFired
			return 0, m.saveState(maintenanceAlertState{Kind: "unknown", Units: maintenanceKnownUnknownUnits(inspection)})
		}
		windowEnd := state.WindowEnd
		if policy.Mode == "scheduled" && windowEnd.IsZero() {
			start, end := maintenanceNoticeWindow(policy, location, inspection.sampledAt)
			if !start.IsZero() && !inspection.sampledAt.Before(start) && inspection.sampledAt.Before(end) {
				windowEnd = end
			}
		}
		if state.Kind != "unknown" {
			if m.pendingSample.IsZero() && !r.firing && !r.pendingSince.IsZero() {
				m.pendingSample, m.pendingReason = r.pendingSince, state.Kind
			}
			if m.pendingSample.IsZero() || m.pendingReason != inspection.unknownReason {
				m.pendingSample, m.pendingReason = inspection.sampledAt, inspection.unknownReason
				if !r.firing {
					r.pendingSince = inspection.sampledAt
				}
				return 0, m.saveState(maintenanceAlertState{Kind: inspection.unknownReason, WindowEnd: windowEnd})
			}
			if !inspection.sampledAt.After(m.pendingSample) {
				return 0, nil
			}
			r.pendingSince = m.pendingSample
			state = maintenanceAlertState{Kind: "unknown", Units: maintenanceKnownUnknownUnits(inspection), WindowEnd: windowEnd, Problems: maintenanceBaselineProblems(inspection)}
			detail := maintenanceUnknownNotice(inspection, packageStatus, policy, location, now)
			if detailedNotifications() {
				return 0, m.raise(now, state, "自动维护待确认", detail)
			}
			logAlertEvent(maintenanceAlertKey, "info", detail+" 暂时无需操作，面板将继续复查。", "")
			return 0, m.raise(now, state, "", "")
		}
		windowChanged := !state.WindowEnd.Equal(windowEnd)
		state.WindowEnd = windowEnd
		if !state.Reminded && maintenanceReminderDue(state, r, policy, location, inspection.sampledAt, now) {
			state.Reminded = true
			state.Units = maintenanceKnownUnknownUnits(inspection)
			return 0, m.raise(now, state, "自动维护仍待确认", maintenanceUnknownNotice(inspection, packageStatus, policy, location, now)+" 请复制诊断并联系管理员。")
		}
		if changed, problems := trackMaintenanceUnknownProblems(&state, inspection, policy, location, now, detailedNotifications()); len(problems) != 0 {
			detail := "新的运行检查问题仍待确认，涉及 " + strings.Join(problems, "、") + "。请在设置的系统更新卡片复制诊断并联系管理员。"
			return 0, m.raise(now, state, "自动维护发现新的待确认问题", detail)
		} else if changed || windowChanged {
			return 0, m.saveState(state)
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
	if _, err := db.Exec(`UPDATE alert_log SET resolved=1 WHERE alert_type=? AND resolved=0`, maintenanceAlertKey); err != nil {
		return 0, err
	}
	r.firing, r.pendingSince, r.lastAlertMsg = false, time.Time{}, ""
	detail = "后续运行检查已确认此前维护告警解除。检查时间 " + maintenanceNoticeTime(inspection.sampledAt) + "。详情见设置的系统更新卡片。"
	logAlertEvent(maintenanceAlertKey, "info", detail, "")
	state = maintenanceAlertState{MailTarget: state.MailTarget, WebhookTarget: state.WebhookTarget, Recovery: true}
	m.send(&state, "系统自动维护告警已解除", detail, false, true, now)
	if !state.RetryAfter.IsZero() {
		return 0, m.saveState(state)
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
		detail += " 首次发现时间 " + maintenanceNoticeTime(r.pendingSince) + "。请在设置的系统更新卡片查看详情。"
		logAlert(maintenanceAlertKey, "critical", detail)
		m.send(&state, title, detail, true, true, now)
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

func notificationTarget(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (m *maintenanceAlertMonitor) send(state *maintenanceAlertState, title, detail string, firing, fresh bool, now time.Time) {
	subject := getPanelTitle() + " " + title
	pendingMail, pendingWebhook := state.MailPending, state.WebhookPending
	state.MailPending, state.WebhookPending = "", ""
	if smtp := GetSMTPConfig(); alertSMTPConfigured(smtp) {
		target := notificationTarget(strings.TrimSpace(smtp.AdminEmail))
		if (fresh && (firing || target == state.MailTarget)) || (!fresh && target == pendingMail) {
			if err := maintenanceSendMail(smtp.AdminEmail, subject, formatEmailHTML(title, detail, "", firing)); err != nil {
				reportAlertStateError("维护邮件发送失败: %v", err)
				state.MailPending = target
			} else if firing {
				state.MailTarget = target
			}
		}
	}
	if webhook := GetWebhookConfig(); webhookConfigured(webhook) {
		target := notificationTarget(webhook.Channel + "\x00" + strings.TrimSpace(webhook.URL))
		if (fresh && (firing || target == state.WebhookTarget)) || (!fresh && target == pendingWebhook) {
			if err := maintenanceSendWebhook(webhook, subject, detail); err != nil {
				reportAlertStateError("维护 Webhook 发送失败: %v", err)
				state.WebhookPending = target
			} else if firing {
				state.WebhookTarget = target
			}
		}
	}
	state.RetryAfter, state.Title, state.Detail = time.Time{}, "", ""
	if state.MailPending != "" || state.WebhookPending != "" {
		state.RetryAfter, state.Title, state.Detail = now.Add(time.Hour), title, detail
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
		if inspection.unknownReason == "mapping_path_missing" {
			message += "至少一个映射的旧文件路径已不存在，例如升级更换库文件名时会发生这种情况。"
		}
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

func maintenanceProblemKeys(inspection SystemMaintenanceInspection) map[string]string {
	keys := make(map[string]string, len(inspection.unknownProblems))
	for unit, reason := range inspection.unknownProblems {
		keys[unit+"\x00"+reason] = unit
	}
	return keys
}

func maintenanceBaselineProblems(inspection SystemMaintenanceInspection) map[string]maintenanceUnknownProblem {
	problems := make(map[string]maintenanceUnknownProblem)
	for key := range maintenanceProblemKeys(inspection) {
		// The enclosing alert owns all problems in its initial sample.
		problems[key] = maintenanceUnknownProblem{Reported: true}
	}
	return problems
}

func trackMaintenanceUnknownProblems(state *maintenanceAlertState, inspection SystemMaintenanceInspection,
	policy SystemMaintenancePolicy, location *time.Location, now time.Time, detailed bool,
) (bool, []string) {
	if state.Problems == nil {
		state.Problems = maintenanceBaselineProblems(inspection)
		return true, nil
	}
	keys := maintenanceProblemKeys(inspection)
	changed := false
	var ready []string
	for key, unit := range keys {
		problem := state.Problems[key]
		if problem.Reported || !inspection.sampledAt.After(problem.Last) {
			continue
		}
		changed = true
		problem.Last = inspection.sampledAt
		if problem.First.IsZero() {
			problem.First = problem.Last
		} else {
			problem.Confirmed = true
		}
		if policy.Mode == "scheduled" && problem.WindowEnd.IsZero() {
			start, end := maintenanceNoticeWindow(policy, location, problem.Last)
			if !start.IsZero() && !problem.Last.Before(start) && problem.Last.Before(end) {
				problem.WindowEnd = end
			}
		}
		due := maintenanceProblemDue(problem, policy, location)
		if problem.Confirmed && (detailed || (policy.Enabled && !now.Before(due) && !problem.Last.Before(due))) {
			problem.Reported = true
			ready = append(ready, unit)
		}
		state.Problems[key] = problem
	}
	for key, problem := range state.Problems {
		if _, present := keys[key]; !present && !problem.Reported && inspection.sampledAt.After(problem.Last) {
			delete(state.Problems, key)
			changed = true
		}
	}
	slices.Sort(ready)
	return changed, slices.Compact(ready)
}

func expireUnverifiedMaintenanceProblems(state *maintenanceAlertState, inspection SystemMaintenanceInspection,
	policy SystemMaintenancePolicy, location *time.Location, now time.Time,
) []string {
	var units []string
	for key, problem := range state.Problems {
		if problem.Reported || problem.First.IsZero() {
			continue
		}
		due := maintenanceUnknownDeadline(policy, location, problem.First)
		if now.Before(due) || !inspection.sampledAt.Before(due) {
			continue
		}
		problem.Reported = true
		state.Problems[key] = problem
		unit, _, _ := strings.Cut(key, "\x00")
		units = append(units, unit)
	}
	slices.Sort(units)
	return slices.Compact(units)
}

func maintenanceProblemDue(problem maintenanceUnknownProblem, policy SystemMaintenancePolicy, location *time.Location) time.Time {
	due := maintenanceUnknownDeadline(policy, location, problem.First)
	if policy.Mode == "scheduled" && !problem.WindowEnd.IsZero() && problem.WindowEnd.Add(30*time.Minute).Before(due) {
		return problem.WindowEnd.Add(30 * time.Minute)
	}
	return due
}

func maintenanceUnknownDeadline(policy SystemMaintenancePolicy, location *time.Location, first time.Time) time.Time {
	if policy.Mode == "immediate" {
		return first.Add(6 * time.Hour)
	}
	deadline := first.Add(72 * time.Hour)
	at := first
	for i := 0; i < 3; i++ {
		_, end := maintenanceNoticeWindow(policy, location, at)
		if end.IsZero() {
			return deadline
		}
		at = end
	}
	if at.Before(deadline) {
		return at
	}
	return deadline
}

func maintenanceReminderDue(state maintenanceAlertState, r *alertRule, policy SystemMaintenancePolicy, location *time.Location, sample, now time.Time) bool {
	if !policy.Enabled || r.lastFired.IsZero() || r.pendingSince.IsZero() || !sample.After(r.lastFired) {
		return false
	}
	due := maintenanceUnknownDeadline(policy, location, r.pendingSince)
	if policy.Mode == "scheduled" && !state.WindowEnd.IsZero() {
		afterObservedWindow := state.WindowEnd.Add(30 * time.Minute)
		if afterObservedWindow.Before(due) {
			due = afterObservedWindow
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
