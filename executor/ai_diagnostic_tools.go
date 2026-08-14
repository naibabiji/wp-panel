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
func BuildAILogDiagnosticPrompt(site *models.Website, job *models.LogAnalysisJob, sessionID int, focusKind, focusValue string) (string, string, []models.AIToolEvent, error) {
	if site == nil || job == nil || job.LocalReport == nil || job.ID <= 0 {
		return "", "", nil, fmt.Errorf("日志分析上下文不存在")
	}
	snapshot := aiLogSessionSnapshot{JobID: job.ID, Domain: job.Domain, StartAt: job.StartAt, EndAt: job.EndAt, Report: sanitizeLogReportForAISession(job.LocalReport)}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", "", nil, err
	}
	session := &models.AISessionDetail{ID: sessionID, SiteID: site.ID, Symptom: models.AIDiagnosisLogAnalysis, ContextType: "log_analysis", ContextID: job.ID, ContextJSON: string(data), FocusKind: focusKind, FocusValue: focusValue}
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
		"response_rules":        logDiagnosticResponseRules(focusKind, focusValue),
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
	anonymized, err := AnonymizeAIText(sessionID, string(prompt))
	if err != nil {
		return "", "", nil, err
	}
	return aiSystemPrompt(), anonymized, tools, nil
}

func logDiagnosticResponseRules(kind, value string) []string {
	rules := []string{
		"上下文中的 IP-01、IP-02 等是面板生成的会话内稳定脱敏别名；同一别名始终代表同一地址。可以跨日志关联其行为，但不得猜测或还原真实IP，面板会在本地向管理员恢复显示。",
		"区分日志事实、基于事实的推断和管理员建议；没有证据时必须明确说明无法判断。",
		"解释当前分组在总体请求中的占比，并判断现有安全规则是否已经处理，不能把已拦截请求重复描述为未处理风险。",
		"高频 IP、高频页面等独立排行榜不能强行关联；只有 focused_log_detail.ip_path_pairs 或原始日志样本能证明具体 IP 与路径的关系。",
		"不要仅凭请求量、User-Agent 名称或身份未验证就建议封禁；限速或封禁必须有异常频率、敏感路径扫描、异常状态码或资源影响等行为证据。",
	}
	switch kind {
	case "path":
		return append(rules,
			"这是高频页面专项分析：首先解释该路径在 WordPress 中的正常用途，再结合总请求占比、唯一 IP、方法、状态码、小时分布、IP集中度和安全事件判断高频原因。",
			"对于 /wp-login.php、/wp-admin 等入口，HTTP 200 不等于正常用户；必须结合POST比例、来源集中度、401/403/444、Fail2ban记录和访问节奏区分正常登录、插件行为与撞库扫描。",
			"回答为什么访问量高、是否正常、现有防护是否已处理，以及管理员是否确实需要操作；不能看到敏感路径就直接断言攻击。")
	case "status":
		return append(rules, statusDiagnosticRules(value)...)
	case "bot":
		return append(rules,
			"这是爬虫专项分析：按 user_agents 拆分当前分组中的实际爬虫名称，并结合唯一IP、来源集中度、路径、状态码、方法和小时分布逐类判断行为。Other bot 是多个含 bot/crawler/spider/scraper 的 User-Agent 汇总，不是单一爬虫。",
			"verified 仅表示 Googlebot/Bingbot 命中面板缓存的官方IP段；fake仅表示声称为Googlebot/Bingbot但未命中对应官方段；unknown表示官方段未成功缓存；unverified表示其他爬虫只按User-Agent识别。",
			"unverified可能是真实爬虫，也可能是假冒，身份状态本身是中性的。不得将unverified写成假冒、恶意、不安全或攻击，也不得声称已完成官方身份验证。",
			"AI没有实时IP、ASN或RDNS查询能力；只能根据脱敏别名对应的日志行为判断较像正常爬虫、行为可疑或身份无法验证，不能仅凭IP断言真实身份（此处IP均为脱敏别名）。",
			"只有存在高频抓取、后台或敏感文件扫描、随机路径、异常状态码或资源压力证据时，才能建议限速；说明限速可能影响真实爬虫，不能仅因未验证建议封禁。")
	case "ip":
		return append(rules,
			"这是来源IP专项分析：结合路径、状态码、方法、小时分布、安全事件和封禁历史判断其行为，不得根据IP归属或信誉作无数据依据的猜测。",
			"当前封禁与时间段内曾封禁必须分开解释；安全事件和封禁记录受保留数量限制，0不代表历史从未发生。")
	default:
		return append(rules,
			"这是整站日志总览：分别总结正常访问、搜索引擎与其他爬虫、错误响应、安全扫描和已拦截流量，按影响和证据排序管理员最值得关注的问题。",
			"比较高频页面、状态码、小时趋势和爬虫统计，但不要把彼此独立的排行榜直接关联；需要深入判断时明确建议用户进入对应页面、状态码、IP或爬虫分组。",
			"未验证爬虫是中性统计，不得与假冒搜索引擎合并成同一风险；只有其访问行为异常或造成资源影响时才作为问题。")
	}
}

func statusDiagnosticRules(status string) []string {
	base := []string{
		"这是HTTP状态码专项分析：解释该状态码的标准含义，并结合总请求占比、主要路径、来源集中度、方法、小时趋势和样本判断是正常现象、已处理的安全流量还是需要修复的问题。",
		"同一状态码可能同时包含正常和异常请求，必须按路径与行为拆分，不能只凭状态码下结论。",
	}
	switch status {
	case "444":
		return append(base, "WP Panel的HTTP 444表示请求已经被面板安全规则拒绝，是拦截结果，不是服务器错误，也不能把后续444再次计作新的攻击或建议重复封禁。")
	case "401", "403":
		return append(base, "401/403可能是正常权限控制，也可能是后台、API或敏感资源探测；结合目标路径、请求方法、安全事件和来源集中度判断。")
	case "404":
		return append(base, "404需要区分正常旧链接或缺失静态资源、搜索引擎历史抓取，与随机路径、敏感文件和漏洞扫描。")
	case "500", "502", "503", "504":
		return append(base, "5xx需要结合具体日志样本、路径、PHP/Nginx错误、慢请求和当前HTTP探测；历史出现但当前未复现时不能声称网站仍在故障。")
	case "301", "302", "303", "307", "308":
		return append(base, "重定向通常可能正常；检查是否集中于登录、HTTPS、规范网址或后台流程，并识别异常循环或非预期目标的证据。")
	case "304":
		return append(base, "304通常代表浏览器或爬虫缓存命中，不是错误；只有比例、路径或客户端行为异常时才需要进一步关注。")
	case "200", "201", "204", "206", "207":
		return append(base, "成功状态码不代表请求行为一定正常；登录页、后台、API或扫描请求也可能返回成功，需要结合路径、方法、来源和频率判断。")
	default:
		return base
	}
}

func logDiagnosticRulesForSession(session *models.AISessionDetail, userMessage string) []string {
	if session == nil || session.ContextType != "log_analysis" {
		return nil
	}
	kind, value := selectLogDiagnosticFocus(session.FocusKind, session.FocusValue, userMessage)
	return logDiagnosticResponseRules(kind, value)
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
	overview.Report = sanitizeLogReportForAISession(snapshot.Report)
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
		result["focused_log_detail"] = map[string]interface{}{"kind": kind, "value": sanitizeLogAnalysisAITextKeepingIPs(value), "message": "当前无法读取这组日志明细"}
		events = append(events, models.AIToolEvent{ToolName: "read_log_detail_" + kind, ResultSummary: fmt.Sprintf("尝试读取 %s=%s 的日志明细失败或日志已轮转", kind, sanitizeLogAnalysisAIText(value))})
		return result, events, nil
	}
	if detail.Total == 0 {
		result["focused_log_detail"] = map[string]interface{}{"kind": kind, "value": sanitizeLogAnalysisAITextKeepingIPs(value), "message": "当前保留日志中没有匹配记录"}
		events = append(events, models.AIToolEvent{ToolName: "read_log_detail_" + kind, ResultSummary: fmt.Sprintf("已查询 %s=%s，当前保留日志中没有匹配记录", kind, sanitizeLogAnalysisAIText(value))})
		return result, events, nil
	}
	sanitizeLogDetailForAISession(detail)
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
	return sanitizeLogReportForAIWithIPMode(report, false)
}

func sanitizeLogReportForAISession(report *models.LogAnalysisReport) *models.LogAnalysisReport {
	return sanitizeLogReportForAIWithIPMode(report, true)
}

func sanitizeLogReportForAIWithIPMode(report *models.LogAnalysisReport, keepIPs bool) *models.LogAnalysisReport {
	if report == nil {
		return nil
	}
	copyReport := *report
	copyReport.TopIPs = append([]models.LogAnalysisCount(nil), report.TopIPs...)
	for i := range copyReport.TopIPs {
		if !keepIPs {
			copyReport.TopIPs[i].Name = maskIP(copyReport.TopIPs[i].Name)
		}
	}
	copyReport.Samples = append([]string(nil), report.Samples...)
	for i := range copyReport.Samples {
		copyReport.Samples[i] = sanitizeLogAnalysisAITextByIPMode(copyReport.Samples[i], keepIPs)
	}
	copyReport.Findings = append([]models.LogAnalysisFinding(nil), report.Findings...)
	for i := range copyReport.Findings {
		copyReport.Findings[i].Evidence = append([]string(nil), report.Findings[i].Evidence...)
		for j := range copyReport.Findings[i].Evidence {
			copyReport.Findings[i].Evidence[j] = sanitizeLogAnalysisAITextByIPMode(copyReport.Findings[i].Evidence[j], keepIPs)
		}
	}
	return &copyReport
}

func sanitizeLogAnalysisAITextByIPMode(value string, keepIPs bool) string {
	if keepIPs {
		return sanitizeLogAnalysisAITextKeepingIPs(value)
	}
	return sanitizeLogAnalysisAIText(value)
}

func sanitizeLogDetailForAI(detail *models.LogAnalysisDetail) {
	sanitizeLogDetailForAIWithIPMode(detail, false)
}

func sanitizeLogDetailForAISession(detail *models.LogAnalysisDetail) {
	sanitizeLogDetailForAIWithIPMode(detail, true)
}

func sanitizeLogDetailForAIWithIPMode(detail *models.LogAnalysisDetail, keepIPs bool) {
	detail.Value = sanitizeLogAnalysisAITextByIPMode(detail.Value, keepIPs)
	for i := range detail.TopIPs {
		if !keepIPs {
			detail.TopIPs[i].IPAddress = maskIP(detail.TopIPs[i].IPAddress)
		}
	}
	for i := range detail.IPPathPairs {
		detail.IPPathPairs[i].Name = sanitizeLogAnalysisAITextByIPMode(detail.IPPathPairs[i].Name, keepIPs)
	}
	if len(detail.Lines) > 30 {
		detail.Lines = detail.Lines[:30]
	}
	for i := range detail.Lines {
		detail.Lines[i] = aiTruncateRunes(sanitizeLogAnalysisAITextByIPMode(detail.Lines[i], keepIPs), 1200)
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
	data, err := json.Marshal(aiLogSessionSnapshot{JobID: job.ID, Domain: job.Domain, StartAt: job.StartAt, EndAt: job.EndAt, Report: sanitizeLogReportForAISession(job.LocalReport)})
	return string(data), err
}

// EncodeAILogSessionSnapshot persists a locally protected reference for later follow-up questions.
func EncodeAILogSessionSnapshot(job *models.LogAnalysisJob) (string, error) {
	return encodeAILogSessionSnapshot(job)
}
