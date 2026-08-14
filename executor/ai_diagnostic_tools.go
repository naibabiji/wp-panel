package executor

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

type aiLogSessionSnapshot struct {
	JobID   int                       `json:"job_id"`
	Domain  string                    `json:"domain"`
	StartAt time.Time                 `json:"start_at"`
	EndAt   time.Time                 `json:"end_at"`
	Report  *models.LogAnalysisReport `json:"report"`
}

var aiStatusQuestionPattern = regexp.MustCompile(`(?i)(?:HTTP\s*)?\b([1-5][0-9]{2})\b`)
var aiPathQuestionPattern = regexp.MustCompile(`(?:^|[\s"'，。：:])(\/[^\s"'，。<>]{1,300})`)

// BuildAILogDiagnosticPrompt creates the initial response for a log-linked AI session.
func BuildAILogDiagnosticPrompt(site *models.Website, job *models.LogAnalysisJob, focusKind, focusValue string) (string, string, []models.AIToolEvent, error) {
	if site == nil || job == nil || job.LocalReport == nil || job.ID <= 0 {
		return "", "", nil, fmt.Errorf("日志分析上下文不存在")
	}
	snapshot := aiLogSessionSnapshot{JobID: job.ID, Domain: job.Domain, StartAt: job.StartAt, EndAt: job.EndAt, Report: sanitizeLogReportForAI(job.LocalReport)}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", "", nil, err
	}
	session := &models.AISessionDetail{ID: 1, SiteID: site.ID, Symptom: models.AIDiagnosisLogAnalysis, ContextType: "log_analysis", ContextID: job.ID, ContextJSON: string(data), FocusKind: focusKind, FocusValue: focusValue}
	toolContext, tools, err := collectAIDiagnosticToolContext(site, session, "")
	if err != nil {
		return "", "", nil, err
	}
	_, siteContext, err := BuildAIDiagnosticPrompt(site, models.AIDiagnosisLogAnalysis)
	if err != nil {
		return "", "", nil, err
	}
	payload := map[string]interface{}{
		"mode":                  "log_diagnosis_initial",
		"current_site_context":  aiCompactFollowupSiteContext(siteContext),
		"readonly_tool_context": toolContext,
		"output_schema":         aiOutputSchema(),
		"response_rules": []string{
			"区分日志事实、基于事实的推断和管理员建议。",
			"解释异常在总体请求中的占比，以及现有安全规则是否已经处理。",
			"不要把独立排行榜中的 IP 和页面强行关联。",
		},
	}
	prompt, err := aiMarshalMap(payload)
	if err != nil {
		return "", "", nil, err
	}
	if len(prompt) > aiMaxFollowupChars {
		payload["current_site_context"] = aiCompactFollowupSiteContext(siteContext)
		payload["readonly_tool_context"] = aiCompactFollowupToolContext(toolContext)
		prompt, err = aiMarshalMap(payload)
		if err != nil {
			return "", "", nil, err
		}
	}
	return aiSystemPrompt(), string(prompt), tools, nil
}

func collectAIDiagnosticToolContext(site *models.Website, session *models.AISessionDetail, userMessage string) (map[string]interface{}, []models.AIToolEvent, error) {
	result := map[string]interface{}{}
	events := []models.AIToolEvent{}
	result["runtime_configuration"] = aiRuntimeConfigurationSummary(site)
	events = append(events, models.AIToolEvent{ToolName: "read_site_runtime_configuration", ResultSummary: "已读取网站、PHP、Nginx和WordPress只读配置摘要"})
	result["security_configuration"] = aiSecuritySummary(site)
	events = append(events, models.AIToolEvent{ToolName: "read_security_configuration", ResultSummary: "已读取限速、Fail2ban、CDN真实IP和封禁状态摘要"})
	if session == nil || session.ContextType != "log_analysis" || strings.TrimSpace(session.ContextJSON) == "" {
		return result, events, nil
	}
	var snapshot aiLogSessionSnapshot
	if err := json.Unmarshal([]byte(session.ContextJSON), &snapshot); err != nil || snapshot.Report == nil {
		return result, events, fmt.Errorf("日志分析会话上下文损坏")
	}
	overview := snapshot
	overview.Report = sanitizeLogReportForAI(snapshot.Report)
	overview.Report.Samples = nil
	for i := range overview.Report.Findings {
		overview.Report.Findings[i].Evidence = nil
	}
	result["log_overview"] = overview
	events = append(events, models.AIToolEvent{ToolName: "read_log_analysis_overview", ResultSummary: fmt.Sprintf("已读取日志任务 #%d 的完整汇总（%s 至 %s）", snapshot.JobID, snapshot.StartAt.Format("2006-01-02 15:04"), snapshot.EndAt.Format("2006-01-02 15:04"))})

	kind, value := selectLogDiagnosticFocus(session.FocusKind, session.FocusValue, userMessage)
	if kind == "" || value == "" {
		return result, events, nil
	}
	detail, err := AnalyzeWebsiteLogDetails(site, snapshot.StartAt, snapshot.EndAt, database.GetDB(), kind, value, 1, 100)
	if err != nil {
		result["focused_log_detail"] = map[string]interface{}{"kind": kind, "value": sanitizeLogAnalysisAIText(value), "message": "当前无法读取这组日志明细"}
		events = append(events, models.AIToolEvent{ToolName: "read_log_detail_" + kind, ResultSummary: fmt.Sprintf("尝试读取 %s=%s 的日志明细失败或日志已轮转", kind, sanitizeLogAnalysisAIText(value))})
		return result, events, nil
	}
	if detail.Total == 0 {
		result["focused_log_detail"] = map[string]interface{}{"kind": kind, "value": sanitizeLogAnalysisAIText(value), "message": "当前保留日志中没有匹配记录"}
		events = append(events, models.AIToolEvent{ToolName: "read_log_detail_" + kind, ResultSummary: fmt.Sprintf("已查询 %s=%s，当前保留日志中没有匹配记录", kind, sanitizeLogAnalysisAIText(value))})
		return result, events, nil
	}
	sanitizeLogDetailForAI(detail)
	result["focused_log_detail"] = detail
	events = append(events, models.AIToolEvent{ToolName: "read_log_detail_" + kind, ResultSummary: fmt.Sprintf("已分析 %s=%s，共 %d 条匹配日志，发送最多30条脱敏样本", kind, sanitizeLogAnalysisAIText(value), detail.Total)})
	return result, events, nil
}

func selectLogDiagnosticFocus(defaultKind, defaultValue, question string) (string, string) {
	question = strings.TrimSpace(question)
	if ip := net.ParseIP(question); ip != nil {
		return "ip", ip.String()
	}
	if candidate := logAnalysisIPv4Pattern.FindString(question); net.ParseIP(candidate) != nil {
		return "ip", candidate
	}
	if match := aiStatusQuestionPattern.FindStringSubmatch(question); len(match) == 2 {
		return "status", match[1]
	}
	if match := aiPathQuestionPattern.FindStringSubmatch(question); len(match) == 2 {
		return "path", strings.TrimRight(match[1], "?!.；;")
	}
	lower := strings.ToLower(question)
	for _, bot := range []string{"googlebot", "bingbot", "baiduspider", "sogou", "yandexbot", "ahrefsbot", "semrushbot", "duckduckbot", "other bot"} {
		if strings.Contains(lower, bot) {
			verification := "unverified"
			if bot == "googlebot" || bot == "bingbot" {
				verification = "verified"
			}
			if strings.Contains(lower, "假冒") || strings.Contains(lower, "伪造") || strings.Contains(lower, "fake") {
				verification = "fake"
			} else if strings.Contains(lower, "无法验证") || strings.Contains(lower, "unknown") {
				verification = "unknown"
			}
			return "bot", canonicalBotName(bot) + ":" + verification
		}
	}
	if defaultKind == "status" || defaultKind == "path" || defaultKind == "bot" || defaultKind == "ip" {
		return defaultKind, strings.TrimSpace(defaultValue)
	}
	return "", ""
}

func canonicalBotName(value string) string {
	switch value {
	case "googlebot":
		return "Googlebot"
	case "bingbot":
		return "Bingbot"
	case "baiduspider":
		return "Baiduspider"
	case "sogou":
		return "Sogou"
	case "yandexbot":
		return "YandexBot"
	case "ahrefsbot":
		return "AhrefsBot"
	case "semrushbot":
		return "SemrushBot"
	case "duckduckbot":
		return "DuckDuckBot"
	default:
		return "Other bot"
	}
}

func sanitizeLogReportForAI(report *models.LogAnalysisReport) *models.LogAnalysisReport {
	if report == nil {
		return nil
	}
	copyReport := *report
	copyReport.TopIPs = append([]models.LogAnalysisCount(nil), report.TopIPs...)
	for i := range copyReport.TopIPs {
		copyReport.TopIPs[i].Name = maskIP(copyReport.TopIPs[i].Name)
	}
	copyReport.Samples = append([]string(nil), report.Samples...)
	for i := range copyReport.Samples {
		copyReport.Samples[i] = sanitizeLogAnalysisAIText(copyReport.Samples[i])
	}
	copyReport.Findings = append([]models.LogAnalysisFinding(nil), report.Findings...)
	for i := range copyReport.Findings {
		copyReport.Findings[i].Evidence = append([]string(nil), report.Findings[i].Evidence...)
		for j := range copyReport.Findings[i].Evidence {
			copyReport.Findings[i].Evidence[j] = sanitizeLogAnalysisAIText(copyReport.Findings[i].Evidence[j])
		}
	}
	return &copyReport
}

func sanitizeLogDetailForAI(detail *models.LogAnalysisDetail) {
	detail.Value = sanitizeLogAnalysisAIText(detail.Value)
	for i := range detail.TopIPs {
		detail.TopIPs[i].IPAddress = maskIP(detail.TopIPs[i].IPAddress)
	}
	for i := range detail.IPPathPairs {
		detail.IPPathPairs[i].Name = sanitizeLogAnalysisAIText(detail.IPPathPairs[i].Name)
	}
	if len(detail.Lines) > 30 {
		detail.Lines = detail.Lines[:30]
	}
	for i := range detail.Lines {
		detail.Lines[i] = aiTruncateRunes(sanitizeLogAnalysisAIText(detail.Lines[i]), 1200)
	}
}

func aiRuntimeConfigurationSummary(site *models.Website) map[string]interface{} {
	result := map[string]interface{}{
		"site_type": site.SiteType, "ssl_enabled": site.SSLEnabled, "fastcgi_cache_enabled": site.FCacheEnabled,
		"fastcgi_cache_ttl": site.FCacheTTL, "wp_debug_enabled": site.WPDebugEnabled, "xmlrpc_enabled": site.XMLRPCEnabled,
		"access_log_mode": site.AccessLogMode, "log_retention_days": site.LogRetentionDays,
		"php_pool_config_present": aiFileExists(site.PHPPoolPath), "nginx_config_present": aiFileExists(site.NginxConfPath),
	}
	if base := filepath.Base(site.PHPPoolPath); base != "." {
		result["php_pool_file"] = base
	}
	result["wp_config"] = aiWPConfigSummary(site)
	result["database_check"] = aiDBCheck(site)
	result["service_checks"] = aiServiceChecks(site)
	result["current_http_checks"] = aiCurrentHTTPChecks(site)
	result["php_pool_directives"] = aiSafeConfigDirectives(site.PHPPoolPath, map[string]bool{
		"pm": true, "pm.max_children": true, "pm.start_servers": true, "pm.min_spare_servers": true,
		"pm.max_spare_servers": true, "pm.max_requests": true, "request_terminate_timeout": true,
		"php_admin_value[memory_limit]": true, "php_admin_value[max_execution_time]": true,
	})
	result["nginx_directives"] = aiSafeConfigDirectives(site.NginxConfPath, map[string]bool{
		"client_max_body_size": true, "keepalive_timeout": true, "fastcgi_read_timeout": true,
		"limit_req": true, "limit_conn": true, "real_ip_header": true, "fastcgi_cache": true,
		"fastcgi_cache_valid": true,
	})
	return result
}

func aiSafeConfigDirectives(path string, allowed map[string]bool) map[string][]string {
	result := map[string][]string{}
	if strings.TrimSpace(path) == "" {
		return result
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 256*1024 {
		return result
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		key := ""
		if index := strings.Index(line, "="); index > 0 {
			key = strings.TrimSpace(line[:index])
		} else if fields := strings.Fields(line); len(fields) > 1 {
			key = fields[0]
		}
		if !allowed[key] || len(result[key]) >= 8 {
			continue
		}
		result[key] = append(result[key], aiTruncateRunes(line, 240))
	}
	return result
}

func aiSecuritySummary(site *models.Website) map[string]interface{} {
	result := map[string]interface{}{
		"cdn_real_ip_enabled": site.CDNRealIPEnabled,
		"file_lock_enabled":   site.FileLockEnabled,
		"file_lock_mode":      site.FileLockMode,
	}
	db := database.GetDB()
	if db == nil {
		return result
	}
	for _, key := range []string{"fail2ban_maxretry", "fail2ban_findtime", "rate_limit_enabled", "rate_limit_rpm", "rate_limit_burst", "bot_limit_enabled", "bot_limit_rpm", "bot_limit_burst", "googlebot_ips_source", "googlebot_ips_last_success_at"} {
		var value string
		if db.QueryRow(`SELECT svalue FROM security_settings WHERE skey=?`, key).Scan(&value) == nil {
			result[key] = value
		}
	}
	var activeBans, retainedEvents int
	_ = db.QueryRow(`SELECT COUNT(*) FROM firewall_bans WHERE unbanned_at IS NULL AND (expires_at IS NULL OR expires_at>datetime('now'))`).Scan(&activeBans)
	_ = db.QueryRow(`SELECT COUNT(*) FROM wp_security_events WHERE site_id=?`, site.ID).Scan(&retainedEvents)
	result["active_bans_all_sites"] = activeBans
	result["retained_security_events_for_site"] = retainedEvents
	result["retention_note"] = "封禁与安全事件受面板保留数量限制，0不代表历史上从未发生"
	return result
}

func encodeAILogSessionSnapshot(job *models.LogAnalysisJob) (string, error) {
	if job == nil || job.LocalReport == nil {
		return "", fmt.Errorf("日志分析报告为空")
	}
	data, err := json.Marshal(aiLogSessionSnapshot{JobID: job.ID, Domain: job.Domain, StartAt: job.StartAt, EndAt: job.EndAt, Report: sanitizeLogReportForAI(job.LocalReport)})
	return string(data), err
}

// EncodeAILogSessionSnapshot persists a redacted, stable reference for later follow-up questions.
func EncodeAILogSessionSnapshot(job *models.LogAnalysisJob) (string, error) {
	return encodeAILogSessionSnapshot(job)
}
