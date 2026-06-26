package executor

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

const (
	aiMaxPromptChars      = 12000
	aiMaxLogCharsPerFile  = 4000
	aiMaxLinesPerLog      = 200
	aiMaxLogReadBytes     = 64 * 1024
	aiProviderMaxRetries  = 1
	aiMaxCodeSuspects     = 8
	aiMaxRecentPHPFiles   = 8
	aiCodeContextLines    = 3
	aiMaxCodeSnippetChars = 900
	aiMaxFollowupMessages = 10
	aiMaxFollowupChars    = 6000
)

var aiRunPHPLint = func(path string) (*ExecResult, error) {
	return Execute("php", "-l", path)
}

var aiReadWPDiagnosticOptions = ReadWPDiagnosticOptions

var aiProviderRetryDelay = 800 * time.Millisecond

var aiRunProcessList = func() ([]byte, error) {
	return exec.Command("ps", "-eo", "user:32,%cpu,%mem,comm").Output()
}

var aiHTTPProbeClient = &http.Client{
	Timeout: 5 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

var aiProbeHTTP = aiProbeHTTPURL

type AIProviderError struct {
	Type       string
	StatusCode int
	Message    string
}

func (e *AIProviderError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Type
}

type aiChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type aiChatRequest struct {
	Model       string          `json:"model"`
	Messages    []aiChatMessage `json:"messages"`
	Temperature float64         `json:"temperature"`
	Stream      bool            `json:"stream"`
}

type aiChatResponse struct {
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

type aiDiagnosticContext struct {
	DiagnosisType         string                  `json:"diagnosis_type"`
	DiagnosisLabel        string                  `json:"diagnosis_label"`
	DiagnosisProfile      map[string]interface{}  `json:"diagnosis_profile"`
	PanelContext          map[string]interface{}  `json:"panel_context"`
	SiteSummary           map[string]interface{}  `json:"site_summary"`
	LocalChecks           map[string]interface{}  `json:"local_checks"`
	RecentPanelOperations []map[string]string     `json:"recent_panel_operations"`
	Logs                  map[string]aiLogSnippet `json:"logs"`
	WPConfigSummary       map[string]interface{}  `json:"wp_config_summary"`
	DBCheck               map[string]interface{}  `json:"db_check"`
	ServiceChecks         map[string]interface{}  `json:"service_checks"`
	CurrentHTTPChecks     map[string]interface{}  `json:"current_http_checks"`
	CodeSuspects          map[string]interface{}  `json:"code_suspects"`
	PerformanceSummary    map[string]interface{}  `json:"performance_summary,omitempty"`
	Constraints           map[string]interface{}  `json:"constraints"`
	OutputSchema          map[string]interface{}  `json:"output_schema"`
	PromptNotes           []string                `json:"prompt_notes,omitempty"`
}

type aiLogSnippet struct {
	Source    string   `json:"source"`
	Status    string   `json:"status"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
	Message   string   `json:"message,omitempty"`
}

func BuildAIDiagnosticPrompt(site *models.Website, symptom string) (systemPrompt, userPrompt string, err error) {
	if site == nil {
		return "", "", fmt.Errorf("网站不存在")
	}
	if !models.IsValidAIDiagnosisSymptom(symptom) {
		return "", "", fmt.Errorf("诊断类型无效")
	}

	ctx := aiDiagnosticContext{
		DiagnosisType:    symptom,
		DiagnosisLabel:   aiDiagnosisLabel(symptom),
		DiagnosisProfile: aiDiagnosisProfile(symptom),
		PanelContext:     aiPanelContext(),
		SiteSummary: map[string]interface{}{
			"domain":                site.Domain,
			"aliases":               site.Aliases,
			"site_type":             site.SiteType,
			"status":                site.Status,
			"ssl_enabled":           site.SSLEnabled,
			"ssl_last_error":        site.SSLLastError,
			"fastcgi_cache_enabled": site.FCacheEnabled,
			"fastcgi_cache_ttl":     site.FCacheTTL,
			"monitoring_enabled":    site.MonitoringEnabled,
			"wp_debug_enabled":      site.WPDebugEnabled,
			"xmlrpc_enabled":        site.XMLRPCEnabled,
			"access_log_mode":       site.AccessLogMode,
		},
		Logs: map[string]aiLogSnippet{
			"nginx_error": aiReadLogSnippet(site.LogDir, "error.log"),
			"php_error":   aiReadLogSnippet(site.LogDir, "php-error.log"),
			"wp_security": aiReadLogSnippet(site.LogDir, "wp-security.log"),
			"access_5xx":  aiReadAccess5xxSnippet(site.LogDir),
		},
		WPConfigSummary:       aiWPConfigSummary(site),
		DBCheck:               aiDBCheck(site),
		ServiceChecks:         aiServiceChecks(site),
		CurrentHTTPChecks:     aiCurrentHTTPChecks(site),
		CodeSuspects:          aiCodeSuspects(site),
		RecentPanelOperations: aiRecentPanelOperations(site.Domain, 20),
		Constraints: map[string]interface{}{
			"phase":                       "readonly_diagnosis",
			"no_write_actions":            true,
			"no_sql_execution":            true,
			"no_shell":                    true,
			"cache_recommendation_policy": aiCacheRecommendationPolicy(site),
		},
		OutputSchema: aiOutputSchema(),
	}
	if aiIsPerformanceSymptom(symptom) {
		ctx.PerformanceSummary = aiPerformanceSummary(site)
	}
	ctx.LocalChecks = aiLocalChecks(ctx)

	userPrompt, err = aiPromptWithinBudget(&ctx)
	if err != nil {
		return "", "", err
	}

	return aiSystemPrompt(), userPrompt, nil
}

func BuildAIFollowupPrompt(site *models.Website, session *models.AISessionDetail, messages []models.AIMessage, userMessage string) (systemPrompt, userPrompt string, err error) {
	if site == nil {
		return "", "", fmt.Errorf("网站不存在")
	}
	if session == nil || session.ID <= 0 {
		return "", "", fmt.Errorf("诊断会话不存在")
	}
	userMessage = strings.TrimSpace(userMessage)
	if userMessage == "" {
		return "", "", fmt.Errorf("追问内容不能为空")
	}
	_, currentContext, err := BuildAIDiagnosticPrompt(site, session.Symptom)
	if err != nil {
		return "", "", err
	}
	ctx := map[string]interface{}{
		"mode":          "followup_diagnosis",
		"panel_context": aiPanelContext(),
		"original_session": map[string]interface{}{
			"id":            session.ID,
			"site_id":       session.SiteID,
			"symptom":       session.Symptom,
			"symptom_label": aiDiagnosisLabel(session.Symptom),
			"status":        session.Status,
			"risk_level":    session.RiskLevel,
			"summary":       session.Summary,
			"report":        session.Report,
			"raw_text":      aiTruncateRunes(session.RawText, 1800),
		},
		"recent_conversation":  aiFollowupMessagesForPrompt(messages),
		"latest_user_message":  userMessage,
		"current_site_context": json.RawMessage(currentContext),
		"constraints": map[string]interface{}{
			"phase":            "readonly_followup",
			"no_write_actions": true,
			"no_sql_execution": true,
			"no_shell":         true,
		},
		"response_rules": []string{
			"用中文直接回答用户本轮反馈，不要输出 JSON。",
			"先说明重新检查到的当前状态是否与原诊断不同。",
			"如果用户说已经完成某个操作，结合 current_site_context 判断是否有新证据；不能凭用户一句话声称已经修复。",
			"只能建议 WP Panel 中真实存在的入口；没有入口时明确说明当前没有直接入口。",
			"不要建议执行 shell 命令，不要声称你已经修改文件、数据库或服务。",
			"给出下一步 1-3 个最具体的排查或处理建议。",
		},
	}
	data, err := aiMarshalMap(ctx)
	if err != nil {
		return "", "", err
	}
	userPrompt = string(data)
	if len(userPrompt) > aiMaxFollowupChars {
		ctx["recent_conversation"] = aiFollowupMessagesForPrompt(aiLimitAIMessages(messages, 4))
		ctx["original_session"].(map[string]interface{})["raw_text"] = ""
		data, err = aiMarshalMap(ctx)
		if err != nil {
			return "", "", err
		}
		userPrompt = string(data)
	}
	return aiFollowupSystemPrompt(), userPrompt, nil
}

func aiFollowupSystemPrompt() string {
	return strings.Join([]string{
		"你是 WP Panel 的 WordPress 诊断追问助手，正在同一个 AI 诊断会话中继续排查。",
		"你必须基于 original_session、recent_conversation、latest_user_message 和 current_site_context 回答。",
		"current_site_context 是本轮重新采集的当前状态，优先级高于 original_session 中的旧结论和历史日志。",
		"不要编造 WP Panel 不存在的入口；只能使用 panel_context.known_panel_entries 中的真实入口，且避开 forbidden_panel_entries。",
		"不要建议 shell 命令，不要声称已执行修复，不要要求用户提供密码、API Key、SSL 私钥或数据库密码。",
		"用简洁中文自然回复，不要输出 JSON，不要使用 Markdown 代码块。",
	}, "\n")
}

func aiMarshalMap(ctx map[string]interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(ctx); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func aiFollowupMessagesForPrompt(messages []models.AIMessage) []map[string]interface{} {
	messages = aiLimitAIMessages(messages, aiMaxFollowupMessages)
	out := make([]map[string]interface{}, 0, len(messages))
	for _, msg := range messages {
		out = append(out, map[string]interface{}{
			"role":       msg.Role,
			"content":    aiTruncateRunes(msg.Content, 1200),
			"created_at": msg.CreatedAt,
		})
	}
	return out
}

func aiLimitAIMessages(messages []models.AIMessage, max int) []models.AIMessage {
	if max <= 0 {
		return nil
	}
	if len(messages) <= max {
		return messages
	}
	return messages[len(messages)-max:]
}

func aiPromptWithinBudget(ctx *aiDiagnosticContext) (string, error) {
	return aiPromptWithinBudgetLimit(ctx, aiMaxPromptChars)
}

func aiPromptWithinBudgetLimit(ctx *aiDiagnosticContext, limit int) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("AI 诊断上下文为空")
	}
	marshal := func() (string, error) {
		data, err := aiMarshalDiagnosticContext(*ctx)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	fits := func(prompt string) bool {
		return limit <= 0 || len(prompt) <= limit
	}
	prompt, err := marshal()
	if err != nil || fits(prompt) {
		return prompt, err
	}

	type budgetStep struct {
		note string
		run  func()
	}
	steps := []budgetStep{
		{
			note: fmt.Sprintf("Prompt 超过 %d 字符，日志片段已进一步截断。", limit),
			run:  func() { aiShrinkLogs(ctx.Logs, 1200) },
		},
		{
			note: "已压缩低风险代码片段和最近 PHP 文件列表以控制上下文长度。",
			run: func() {
				aiTrimCodeSuspectSnippets(ctx.CodeSuspects, false, 320)
				aiLimitRecentPHPFiles(ctx.CodeSuspects, 4)
			},
		},
		{
			note: "已缩减近期面板操作记录数量。",
			run:  func() { ctx.RecentPanelOperations = aiLimitStringMapSlice(ctx.RecentPanelOperations, 8) },
		},
		{
			note: "已压缩 debug.log 摘要和低优先级日志。",
			run: func() {
				aiTrimDebugLog(ctx.CodeSuspects, 30, 1200)
				aiClearLogLines(ctx.Logs, []string{"wp_security", "access_5xx"}, "因上下文预算限制未发送该日志片段")
			},
		},
		{
			note: "已进一步压缩代码疑点，只保留最高优先级证据。",
			run: func() {
				aiLimitCodeSuspects(ctx.CodeSuspects, 4)
				aiTrimCodeSuspectSnippets(ctx.CodeSuspects, true, 260)
				ctx.RecentPanelOperations = aiLimitStringMapSlice(ctx.RecentPanelOperations, 4)
			},
		},
		{
			note: "已清空全部日志正文，只保留日志状态和本地检查结果。",
			run:  func() { aiClearAllLogLines(ctx.Logs, "因上下文预算限制未发送日志正文") },
		},
		{
			note: "已移除低优先级代码列表和 debug.log 正文，只保留代码疑点摘要。",
			run: func() {
				aiLimitRecentPHPFiles(ctx.CodeSuspects, 0)
				aiTrimDebugLog(ctx.CodeSuspects, 0, 0)
				aiTrimCodeSuspectSnippets(ctx.CodeSuspects, true, 0)
			},
		},
		{
			note: "已使用精简输出 schema 和面板上下文以满足上下文预算。",
			run: func() {
				ctx.PanelContext = map[string]interface{}{
					"product_name": "WP Panel",
					"scope":        "WordPress 专用服务器管理面板；不要引用其他面板入口。",
				}
				ctx.OutputSchema = aiCompactOutputSchema()
				ctx.RecentPanelOperations = nil
			},
		},
	}

	for _, step := range steps {
		ctx.PromptNotes = append(ctx.PromptNotes, step.note)
		step.run()
		prompt, err = marshal()
		if err != nil || fits(prompt) {
			return prompt, err
		}
	}
	return prompt, nil
}

func aiMarshalDiagnosticContext(ctx aiDiagnosticContext) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(ctx); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func CallAIChat(ctx context.Context, settings *models.AISettings, systemPrompt, userPrompt string) (string, int64, error) {
	if settings == nil {
		return "", 0, &AIProviderError{Type: "bad_config", Message: "AI 设置不存在"}
	}
	endpoint, err := aiChatEndpoint(settings.BaseURL)
	if err != nil {
		return "", 0, &AIProviderError{Type: "bad_config", Message: err.Error()}
	}
	reqBody := aiChatRequest{
		Model: strings.TrimSpace(settings.Model),
		Messages: []aiChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.2,
		Stream:      false,
	}
	if reqBody.Model == "" {
		return "", 0, &AIProviderError{Type: "bad_config", Message: "模型不能为空"}
	}
	body, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", 0, &AIProviderError{Type: "bad_config", Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(settings.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(settings.APIKey))
	}

	timeout := time.Duration(settings.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	start := time.Now()
	var lastErr error
	for attempt := 0; attempt <= aiProviderMaxRetries; attempt++ {
		resp, err := client.Do(req)
		elapsed := time.Since(start).Milliseconds()
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timeout") {
				return "", elapsed, &AIProviderError{Type: "timeout", Message: "AI 服务请求超时"}
			}
			return "", elapsed, &AIProviderError{Type: "network_error", Message: "无法连接 AI 服务: " + err.Error()}
		}

		respData, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", elapsed, aiHTTPError(resp.StatusCode, respData)
		}

		content, err := aiExtractChatContent(respData)
		if err != nil {
			lastErr = err
			if aiShouldRetryProviderResponse(err) && attempt < aiProviderMaxRetries && aiSleepWithContext(ctx, aiProviderRetryDelay) == nil {
				req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
				if err != nil {
					return "", elapsed, &AIProviderError{Type: "bad_config", Message: err.Error()}
				}
				req.Header.Set("Content-Type", "application/json")
				if strings.TrimSpace(settings.APIKey) != "" {
					req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(settings.APIKey))
				}
				continue
			}
			return "", elapsed, err
		}
		if strings.TrimSpace(content) == "" {
			lastErr = &AIProviderError{Type: "empty_response", Message: "AI 服务返回空内容"}
			if attempt < aiProviderMaxRetries && aiSleepWithContext(ctx, aiProviderRetryDelay) == nil {
				req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
				if err != nil {
					return "", elapsed, &AIProviderError{Type: "bad_config", Message: err.Error()}
				}
				req.Header.Set("Content-Type", "application/json")
				if strings.TrimSpace(settings.APIKey) != "" {
					req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(settings.APIKey))
				}
				continue
			}
			return "", elapsed, lastErr
		}
		return content, elapsed, nil
	}
	return "", time.Since(start).Milliseconds(), lastErr
}

func aiShouldRetryProviderResponse(err error) bool {
	var providerErr *AIProviderError
	if errors.As(err, &providerErr) {
		return providerErr.Type == "bad_response" || providerErr.Type == "empty_response"
	}
	return false
}

func aiSleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func TestAISettings(ctx context.Context, settings *models.AISettings) (int64, string, error) {
	system := "你是 WP Panel 的 AI 连接测试助手。"
	user := `请只返回 JSON：{"ok":true}`
	content, elapsed, err := CallAIChat(ctx, settings, system, user)
	if err != nil {
		return elapsed, "", err
	}
	return elapsed, content, nil
}

func ParseAIReport(content string) (*models.AIDiagnosticReport, string, bool) {
	raw := strings.TrimSpace(content)
	// Try direct parse first (model followed instructions).
	if report, ok := aiParseReportJSON([]byte(raw)); ok {
		return report, raw, true
	}
	// Extract the outermost JSON object in case the model wrapped it in markdown fences
	// or added preamble/postamble text.
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		if report, ok := aiParseReportJSON([]byte(raw[start : end+1])); ok {
			return report, raw, true
		}
	}
	return nil, raw, false
}

func aiParseReportJSON(data []byte) (*models.AIDiagnosticReport, bool) {
	var report models.AIDiagnosticReport
	if err := json.Unmarshal(data, &report); err == nil {
		return &report, true
	}
	if report, ok := aiParseFlexibleReportJSON(data); ok {
		return report, true
	}
	return nil, false
}

func aiParseFlexibleReportJSON(data []byte) (*models.AIDiagnosticReport, bool) {
	var payload struct {
		Summary                 string            `json:"summary"`
		RiskLevel               string            `json:"risk_level"`
		LikelyCauses            []json.RawMessage `json:"likely_causes"`
		RecommendedActions      []json.RawMessage `json:"recommended_actions"`
		NeedsMoreInfo           bool              `json:"needs_more_info"`
		UserFriendlyExplanation string            `json:"user_friendly_explanation"`
		Metadata                map[string]string `json:"metadata,omitempty"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, false
	}
	report := &models.AIDiagnosticReport{
		Summary:                 strings.TrimSpace(payload.Summary),
		RiskLevel:               strings.TrimSpace(payload.RiskLevel),
		NeedsMoreInfo:           payload.NeedsMoreInfo,
		UserFriendlyExplanation: strings.TrimSpace(payload.UserFriendlyExplanation),
		Metadata:                payload.Metadata,
	}
	for _, rawCause := range payload.LikelyCauses {
		var cause struct {
			Title      string          `json:"title"`
			Confidence string          `json:"confidence"`
			Evidence   json.RawMessage `json:"evidence"`
		}
		if err := json.Unmarshal(rawCause, &cause); err != nil {
			continue
		}
		report.LikelyCauses = append(report.LikelyCauses, models.AILikelyCause{
			Title:      strings.TrimSpace(cause.Title),
			Confidence: strings.TrimSpace(cause.Confidence),
			Evidence:   aiFlexibleStringList(cause.Evidence),
		})
	}
	for _, rawAction := range payload.RecommendedActions {
		var action struct {
			Label           string          `json:"label"`
			Description     string          `json:"description"`
			Risk            string          `json:"risk"`
			ManualSteps     json.RawMessage `json:"manual_steps"`
			PanelActionHint string          `json:"panel_action_hint"`
		}
		if err := json.Unmarshal(rawAction, &action); err != nil {
			continue
		}
		report.RecommendedActions = append(report.RecommendedActions, models.AIAction{
			Label:           strings.TrimSpace(action.Label),
			Description:     strings.TrimSpace(action.Description),
			Risk:            strings.TrimSpace(action.Risk),
			ManualSteps:     aiFlexibleStringList(action.ManualSteps),
			PanelActionHint: strings.TrimSpace(action.PanelActionHint),
		})
	}
	return report, report.Summary != "" || report.UserFriendlyExplanation != "" || len(report.LikelyCauses) > 0 || len(report.RecommendedActions) > 0
}

func aiFlexibleStringList(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return []string{}
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return aiCleanStringList(list)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return aiCleanStringList([]string{text})
	}
	var generic []interface{}
	if err := json.Unmarshal(raw, &generic); err == nil {
		out := make([]string, 0, len(generic))
		for _, item := range generic {
			switch value := item.(type) {
			case string:
				out = append(out, value)
			default:
				if data, err := json.Marshal(value); err == nil {
					out = append(out, string(data))
				}
			}
		}
		return aiCleanStringList(out)
	}
	return []string{}
}

func aiCleanStringList(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	if out == nil {
		return []string{}
	}
	return out
}

func aiExtractChatContent(data []byte) (string, error) {
	var parsed aiChatResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		if content, ok := aiExtractSSEContent(data); ok && strings.TrimSpace(content) != "" {
			return strings.TrimSpace(content), nil
		}
		preview := aiResponsePreview(data, 240)
		if preview != "" {
			return "", &AIProviderError{Type: "bad_response", Message: "AI 服务返回的不是有效 JSON，响应片段：" + preview}
		}
		return "", &AIProviderError{Type: "bad_response", Message: "AI 服务返回的不是有效 JSON"}
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", &AIProviderError{Type: "provider_error", Message: parsed.Error.Message}
	}
	if len(parsed.Choices) > 0 {
		content, err := aiContentToText(parsed.Choices[0].Message.Content)
		if err == nil && strings.TrimSpace(content) != "" {
			return strings.TrimSpace(content), nil
		}
	}

	content, ok := aiExtractFallbackContent(data)
	if ok && strings.TrimSpace(content) != "" {
		return strings.TrimSpace(content), nil
	}
	return "", &AIProviderError{Type: "bad_response", Message: "AI 服务响应中未找到可用文本内容"}
}

func aiExtractSSEContent(data []byte) (string, bool) {
	raw := strings.TrimSpace(string(data))
	if !strings.Contains(raw, "data:") {
		return "", false
	}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		content, err := aiExtractChatChunkContent([]byte(payload))
		if err == nil && strings.TrimSpace(content) != "" {
			out = append(out, content)
		}
	}
	if len(out) == 0 {
		return "", true
	}
	return strings.Join(out, ""), true
}

func aiExtractChatChunkContent(data []byte) (string, error) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content json.RawMessage `json:"content"`
			} `json:"delta"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return "", err
	}
	if len(chunk.Choices) == 0 {
		return "", fmt.Errorf("missing choices")
	}
	if content, err := aiContentToText(chunk.Choices[0].Delta.Content); err == nil && strings.TrimSpace(content) != "" {
		return content, nil
	}
	return aiContentToText(chunk.Choices[0].Message.Content)
}

func aiContentToText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}

	var parts []map[string]interface{}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var out []string
		for _, part := range parts {
			if value, ok := part["text"].(string); ok && strings.TrimSpace(value) != "" {
				out = append(out, value)
				continue
			}
			if value, ok := part["content"].(string); ok && strings.TrimSpace(value) != "" {
				out = append(out, value)
			}
		}
		return strings.Join(out, "\n"), nil
	}
	return "", fmt.Errorf("unsupported content")
}

func aiExtractFallbackContent(data []byte) (string, bool) {
	var payload map[string]interface{}
	if json.Unmarshal(data, &payload) != nil {
		return "", false
	}
	if text, ok := payload["output_text"].(string); ok {
		return text, true
	}
	if text, ok := payload["content"].(string); ok {
		return text, true
	}
	if parts, ok := payload["content"].([]interface{}); ok {
		var out []string
		for _, item := range parts {
			part, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if text, ok := part["text"].(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return strings.Join(out, "\n"), len(out) > 0
	}
	return "", false
}

func aiResponsePreview(data []byte, maxRunes int) string {
	preview := strings.TrimSpace(string(data))
	if preview == "" {
		return ""
	}
	preview = strings.Join(strings.Fields(preview), " ")
	runes := []rune(preview)
	if maxRunes > 0 && len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return preview
}

func aiSystemPrompt() string {
	return strings.Join([]string{
		"你是 WP Panel 的 WordPress 站点级诊断助手。你只能分析输入中的站点摘要、日志摘要和检查结果。",
		"你必须以 WP Panel 的实际产品能力作答，不能引用或编造宝塔面板、BT Panel、aaPanel、1Panel、cPanel、Plesk 等其他面板的菜单、按钮、路径或操作方式。",
		"如果建议管理员在面板中操作，只能使用 user prompt 中 panel_context.known_panel_entries 列出的 WP Panel 入口；如果没有对应入口，请明确说明“WP Panel 当前没有直接入口，需要通过文件管理或人工处理”。",
		"不要编造“仪表盘 -> 监控设置”“开启站点资源监控”等不存在的入口。WP Panel 的仪表盘只展示资源图表和站点资源排行；网站详情的“网站监控”只做 HTTP 可用性监控，不是性能资源采集开关。",
		"service_checks 只表示站点配置文件、网站目录和日志目录是否存在，不代表 Nginx、PHP-FPM 或 MariaDB 服务正在运行；不要把它表述为“服务未宕机”。如果需要确认服务状态，可以建议管理员到“软件管理”页查看服务状态。",
		"current_http_checks 是本次诊断即时发起的 HTTP 探测，优先级高于历史 access_5xx 日志。若 current_http_checks.home 和 wp_admin 均不是 5xx，不要声称“当前网站 500”；只能说历史日志曾出现 5xx，当前探测未复现。",
		"code_suspects 是面板只读扫描当前启用主题、启用插件和少量高价值文件得到的证据。若 code_suspects 中存在 high 且有文件行号，应优先作为可能原因；不要要求用户再手动查同一处证据。",
		"code_suspects 中 context=conditional_block 的 die/wp_die/exit 表示位于条件代码块内，通常只作为低优先级线索；除非日志或请求条件能直接对应，不要把它写成主要原因。",
		"当 diagnosis_profile.profile=performance 时，优先分析 performance_summary 中的服务器负载、站点 PHP-FPM 资源占用、WP Panel FastCGI 缓存状态、活跃插件结构和缓存插件冲突；不要把性能问题默认当成 500 或服务宕机。",
		"如果 site_summary.fastcgi_cache_enabled=true，不能建议安装或启用 WordPress 页面缓存插件，例如 WP Super Cache、W3 Total Cache、WP Fastest Cache、Cache Enabler、WP Rocket 页面缓存等功能；应建议验证 FastCGI 缓存命中、清理机制、绕过规则，并排查对象缓存、主题插件、数据库查询、图片资源和外部请求。",
		"recent_panel_operations 是面板操作审计线索，不是故障原因结论。只有操作类型、时间和日志证据能直接对应时，才可作为可能原因；不要把 CDN 真实 IP、SSL、备份等无直接证据的近期操作表述为原因。",
		"不要声称已经修改服务器。不要建议任意 shell 命令。不要输出需要 root 权限的操作。",
		"不要要求用户提供密码、API Key、SSL 私钥或面板数据库。",
		"对每个结论给出证据，不确定时降低置信度。",
		"请用中文返回 JSON 对象，字段必须包含 summary、risk_level、likely_causes、recommended_actions、needs_more_info、user_friendly_explanation。",
		"不要包含 Markdown 代码块，不要输出 JSON 以外的文字。",
	}, "\n")
}

func aiPanelContext() map[string]interface{} {
	return map[string]interface{}{
		"product_name": "WP Panel",
		"scope":        "WordPress 专用服务器管理面板，不是宝塔面板、1Panel、cPanel 或通用 Linux 面板。",
		"answer_rules": []string{
			"推荐操作必须使用 WP Panel 的真实页面和按钮名称。",
			"不要写“登录宝塔面板”“进入宝塔网站设置”“软件商店”等其他面板文案。",
			"不要写“仪表盘 -> 监控设置”“开启站点资源监控”；WP Panel 没有这个入口。",
			"网站详情的“网站监控”只用于定期检测网站 HTTP 可用性和告警，不是服务器资源监控开关。",
			"不知道 WP Panel 是否有某个入口时，不要猜测菜单路径。",
			"不要建议执行 shell 命令；Phase 1 只提供诊断和人工修复建议。",
		},
		"forbidden_panel_entries": []string{
			"仪表盘 -> 监控设置",
			"WP Panel 仪表盘 -> 监控设置",
			"开启站点资源监控",
			"开启资源监控",
		},
		"known_panel_entries": []map[string]string{
			{
				"page":        "仪表盘",
				"entry":       "仪表盘",
				"description": "查看服务器 CPU、内存、负载趋势和站点资源排行；这里只能查看，不能开启资源监控。",
			},
			{
				"page":        "网站管理",
				"entry":       "网站管理 -> 对应站点 -> 详情",
				"description": "查看网站基本信息、数据库、SSL、Nginx 自定义配置、WordPress 优化和网站日志。",
			},
			{
				"page":        "AI 诊断",
				"entry":       "AI 诊断 -> 选择网站和问题类型 -> 开始诊断",
				"description": "发起站点级只读诊断，查看结构化结果和历史诊断记录。",
			},
			{
				"page":        "网站详情",
				"entry":       "网站详情 -> 基本信息 -> 文件管理",
				"description": "进入该站点的网站根目录，查看或编辑 wp-config.php、主题、插件等站点文件。",
			},
			{
				"page":        "网站详情",
				"entry":       "网站详情 -> 数据库 -> 同步数据库信息",
				"description": "用面板记录的数据库名和用户名同步 wp-config.php 中的 DB_NAME、DB_USER，并可同步表前缀。",
			},
			{
				"page":        "网站详情",
				"entry":       "网站详情 -> 数据库 -> 修改密码",
				"description": "修改该站点数据库用户密码；WordPress 站点需要同步 wp-config.php。",
			},
			{
				"page":        "网站详情",
				"entry":       "网站详情 -> 数据库 -> 修改站点URL",
				"description": "修改 WordPress 数据库中的 siteurl 和 home。",
			},
			{
				"page":        "网站详情",
				"entry":       "网站详情 -> 网站日志",
				"description": "查看访问日志、错误日志和 WordPress 安全日志。",
			},
			{
				"page":        "网站详情",
				"entry":       "网站详情 -> 网站监控",
				"description": "启用或关闭该站点的 HTTP 可用性定时检测和异常告警；不是资源监控或性能数据采集开关。",
			},
			{
				"page":        "网站详情",
				"entry":       "网站详情 -> Nginx 自定义配置",
				"description": "编辑 pre.conf 和 .conf 自定义片段，并由面板校验后生效。",
			},
			{
				"page":        "网站详情",
				"entry":       "网站详情 -> WordPress优化",
				"description": "管理 WP_DEBUG、XML-RPC、FastCGI 缓存等站点优化项。",
			},
			{
				"page":        "文件管理",
				"entry":       "文件管理 -> 选择站点",
				"description": "浏览、上传、编辑、删除站点根目录内文件；不要跨站点操作。",
			},
			{
				"page":        "软件管理",
				"entry":       "软件管理",
				"description": "查看 Nginx、PHP-FPM、MariaDB、Redis 等基础服务状态；诊断报告只能建议管理员查看，不要声称已经完成运行状态检查。",
			},
		},
	}
}

func aiDiagnosisLabel(symptom string) string {
	switch symptom {
	case models.AIDiagnosisSite500:
		return "网站 500 / 白屏"
	case models.AIDiagnosisWPAdminDown:
		return "后台打不开"
	case models.AIDiagnosisSSLFailure:
		return "SSL 失败"
	case models.AIDiagnosisDBConnection:
		return "数据库连接问题"
	case models.AIDiagnosisCacheIssue:
		return "缓存异常"
	case models.AIDiagnosisPerformance:
		return "网站速度慢"
	default:
		return symptom
	}
}

func aiDiagnosisProfile(symptom string) map[string]interface{} {
	if aiIsPerformanceSymptom(symptom) {
		return map[string]interface{}{
			"profile": "performance",
			"focus": []string{
				"服务器整体 CPU、内存和 load 是否异常",
				"当前站点或同机其他站点的 PHP-FPM 资源占用",
				"WP Panel 的 Nginx FastCGI 缓存是否开启和是否可能未命中",
				"WordPress 缓存插件、优化插件、页面构建器或重型插件是否导致冲突或开销过高",
			},
			"answer_rules": []string{
				"先区分服务器资源瓶颈、同机站点资源争抢、当前站点自身优化问题，再给建议。",
				"没有性能数据时，可以建议用户到“仪表盘”查看已有资源图表，或到“网站详情 -> 网站日志”查看日志；不要建议开启不存在的资源监控设置。",
				"缓存异常重点分析 FastCGI 缓存、WordPress 缓存插件和多层缓存冲突；网站速度慢重点分析资源、插件、主题和缓存命中。",
				"如果 fastcgi_cache_enabled=true，不要再建议安装或启用 WordPress 页面缓存插件；如果已存在页面缓存插件，只能建议检查是否与 FastCGI 缓存重复并按需关闭其页面缓存功能。",
			},
		}
	}
	return map[string]interface{}{
		"profile": "availability",
		"focus": []string{
			"请求是否返回 500、502、503、504 或后台不可访问",
			"wp-config.php、数据库连接、PHP fatal、主题插件代码和 Nginx/PHP 日志证据",
			"SSL 或数据库连接问题是否直接导致站点不可用",
		},
	}
}

func aiIsPerformanceSymptom(symptom string) bool {
	return symptom == models.AIDiagnosisCacheIssue || symptom == models.AIDiagnosisPerformance
}

func aiCacheRecommendationPolicy(site *models.Website) map[string]interface{} {
	if site != nil && site.FCacheEnabled {
		return map[string]interface{}{
			"fastcgi_cache_enabled": true,
			"rule":                  "WP Panel FastCGI 缓存已开启时，不要建议安装或启用 WordPress 页面缓存插件。",
			"avoid_recommending": []string{
				"WP Super Cache 页面缓存",
				"W3 Total Cache 页面缓存",
				"WP Fastest Cache 页面缓存",
				"Cache Enabler 页面缓存",
				"WP Rocket 页面缓存",
				"任何额外的 WordPress 全页/静态 HTML 页面缓存",
			},
			"prefer_recommending": []string{
				"验证 X-FastCGI-Cache 是否命中",
				"检查 FastCGI 缓存 TTL、清理机制和绕过规则",
				"检查登录态、后台、购物车、结账页等动态路径是否绕过缓存",
				"排查对象缓存、慢查询、重型插件、主题代码、图片资源、CDN 和外部 HTTP 请求",
			},
		}
	}
	return map[string]interface{}{
		"fastcgi_cache_enabled": false,
		"rule":                  "WP Panel FastCGI 缓存未开启时，可优先建议使用 WP Panel 的 FastCGI 缓存；不要默认要求同时叠加多个页面缓存插件。",
	}
}

func aiOutputSchema() map[string]interface{} {
	return map[string]interface{}{
		"summary":    "string",
		"risk_level": "low|medium|high",
		"likely_causes": []map[string]interface{}{{
			"title":      "string",
			"confidence": "low|medium|high",
			"evidence":   []string{"string，必须是字符串数组，即使只有一条证据也要用数组"},
		}},
		"recommended_actions": []map[string]interface{}{{
			"label":             "string",
			"description":       "string",
			"risk":              "low|medium|high",
			"manual_steps":      []string{"string，必须是字符串数组，即使只有一步也要用数组"},
			"panel_action_hint": "string，必须是 WP Panel 真实入口；如果没有入口则为空字符串",
		}},
		"needs_more_info":           false,
		"user_friendly_explanation": "string",
	}
}

func aiCompactOutputSchema() map[string]interface{} {
	return map[string]interface{}{
		"summary":                   "string",
		"risk_level":                "low|medium|high",
		"likely_causes":             []string{"title, confidence, evidence[]"},
		"recommended_actions":       []string{"label, description, risk, manual_steps[], panel_action_hint"},
		"needs_more_info":           false,
		"user_friendly_explanation": "string",
	}
}

func aiReadLogSnippet(logDir, filename string) aiLogSnippet {
	path := filepath.Join(logDir, filename)
	if _, err := os.Stat(path); err != nil {
		return aiLogSnippet{Source: filename, Status: "not_found", Message: "日志不可读或不存在"}
	}
	if !aiPathWithin(logDir, path) {
		return aiLogSnippet{Source: filename, Status: "forbidden", Message: "日志路径越界"}
	}
	lines, truncated, err := aiTailInterestingLines(path, aiMaxLinesPerLog, aiMaxLogCharsPerFile, false)
	if err != nil {
		return aiLogSnippet{Source: filename, Status: "not_found", Message: "日志不可读或不存在"}
	}
	return aiLogSnippet{Source: filename, Status: "ok", Lines: lines, Truncated: truncated}
}

func aiReadAccess5xxSnippet(logDir string) aiLogSnippet {
	path := filepath.Join(logDir, "access.log")
	if _, err := os.Stat(path); err != nil {
		return aiLogSnippet{Source: "access.log", Status: "not_found", Message: "访问日志不可读或不存在"}
	}
	if !aiPathWithin(logDir, path) {
		return aiLogSnippet{Source: "access.log", Status: "forbidden", Message: "日志路径越界"}
	}
	lines, truncated, err := aiTailInterestingLines(path, aiMaxLinesPerLog, aiMaxLogCharsPerFile, true)
	if err != nil {
		return aiLogSnippet{Source: "access.log", Status: "not_found", Message: "访问日志不可读或不存在"}
	}
	return aiLogSnippet{Source: "access.log", Status: "ok", Lines: lines, Truncated: truncated}
}

func aiTailInterestingLines(path string, maxLines, maxChars int, only5xx bool) ([]string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return nil, false, fmt.Errorf("invalid log file")
	}
	size := info.Size()
	readSize := int64(aiMaxLogReadBytes)
	if size < readSize {
		readSize = size
	}
	buf := make([]byte, readSize)
	if readSize > 0 {
		if _, err := f.ReadAt(buf, size-readSize); err != nil && err != io.EOF {
			return nil, false, err
		}
	}
	rawLines := strings.Split(strings.ReplaceAll(string(buf), "\r\n", "\n"), "\n")
	keywords := []string{"Fatal error", "Parse error", "Allowed memory size", "Call to undefined", "Class not found", "permission denied", "Primary script unknown", "database", "Connection refused", "upstream", " 500 ", " 502 ", " 503 ", " 504 "}
	selectedIndexes := map[int]bool{}
	seen := map[string]bool{}
	allowLine := func(line string) bool {
		if !only5xx {
			return true
		}
		return strings.Contains(line, " 500 ") || strings.Contains(line, " 502 ") || strings.Contains(line, " 503 ") || strings.Contains(line, " 504 ")
	}
	add := func(index int, line string) {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			return
		}
		if !allowLine(line) {
			return
		}
		seen[line] = true
		selectedIndexes[index] = true
	}
	for index, line := range rawLines {
		for _, kw := range keywords {
			if strings.Contains(line, kw) {
				add(index, line)
				break
			}
		}
	}
	for i := len(rawLines) - 1; i >= 0 && len(selectedIndexes) < maxLines; i-- {
		add(i, rawLines[i])
	}
	var selected []string
	for i, line := range rawLines {
		if selectedIndexes[i] {
			selected = append(selected, strings.TrimSpace(line))
		}
	}
	if len(selected) > maxLines {
		selected = selected[len(selected)-maxLines:]
	}
	// Cap from the tail to preserve the most recent lines.
	total := 0
	capStart := len(selected)
	for i := len(selected) - 1; i >= 0; i-- {
		if total+len(selected[i]) > maxChars {
			break
		}
		total += len(selected[i])
		capStart = i
	}
	return selected[capStart:], size > readSize || capStart > 0, nil
}

func aiPathWithin(basePath, targetPath string) bool {
	return isPathWithinRoot(basePath, targetPath)
}

func aiWPConfigSummary(site *models.Website) map[string]interface{} {
	result := map[string]interface{}{
		"checked": false,
		"exists":  false,
	}
	if site == nil || site.SiteType != "wordpress" {
		result["message"] = "非 WordPress 站点，未检查 wp-config.php"
		return result
	}
	path := filepath.Join(site.WebRoot, "wp-config.php")
	if !aiPathWithin(site.WebRoot, path) {
		result["message"] = "wp-config.php 路径越界"
		return result
	}
	data, err := os.ReadFile(path)
	if err != nil {
		result["message"] = "wp-config.php 不存在或不可读"
		return result
	}
	text := string(data)
	result["checked"] = true
	result["exists"] = true
	result["php_syntax_check"] = aiWPConfigSyntaxCheck(site, path)
	dbName := aiExtractWPConstant(text, "DB_NAME")
	dbUser := aiExtractWPConstant(text, "DB_USER")
	dbHost := aiExtractWPConstant(text, "DB_HOST")
	result["db_name_matches_panel"] = dbName == site.DBName
	result["db_user_matches_panel"] = dbUser == site.DBUser
	result["db_host"] = dbHost
	if prefix, err := ReadWPTablePrefix(site.WebRoot); err == nil {
		result["table_prefix"] = prefix
	} else {
		result["table_prefix_error"] = err.Error()
	}
	result["wp_debug_enabled"] = regexp.MustCompile(`(?i)define\(\s*['"]WP_DEBUG['"]\s*,\s*true\s*\)`).MatchString(text)
	result["contains_db_password"] = "redacted"
	result["contains_auth_salts"] = "redacted"
	return result
}

func aiWPConfigSyntaxCheck(site *models.Website, path string) map[string]interface{} {
	result := map[string]interface{}{
		"checked": false,
		"ok":      false,
	}
	if site == nil || site.SiteType != "wordpress" {
		result["message"] = "非 WordPress 站点，未检查 wp-config.php 语法"
		return result
	}
	if !aiPathWithin(site.WebRoot, path) {
		result["message"] = "wp-config.php 路径越界"
		return result
	}
	if _, err := os.Stat(path); err != nil {
		result["message"] = "wp-config.php 不存在或不可读"
		return result
	}

	lintResult, err := aiRunPHPLint(path)
	output := aiSanitizeWPConfigLintOutput(aiLintOutput(lintResult), path)
	if err != nil {
		if output == "" {
			result["message"] = "php -l 检查不可用: " + err.Error()
			return result
		}
		result["checked"] = true
		result["message"] = "wp-config.php PHP 语法检查失败"
		result["output"] = output
		return result
	}

	result["checked"] = true
	result["ok"] = true
	result["message"] = "wp-config.php PHP 语法检查通过"
	if output != "" {
		result["output"] = output
	}
	return result
}

func aiLintOutput(result *ExecResult) string {
	if result == nil {
		return ""
	}
	parts := []string{}
	if strings.TrimSpace(result.Stdout) != "" {
		parts = append(parts, strings.TrimSpace(result.Stdout))
	}
	if strings.TrimSpace(result.Stderr) != "" {
		parts = append(parts, strings.TrimSpace(result.Stderr))
	}
	return strings.Join(parts, "\n")
}

func aiSanitizeWPConfigLintOutput(output, path string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	output = strings.ReplaceAll(output, path, "wp-config.php")
	output = strings.ReplaceAll(output, filepath.Base(path), "wp-config.php")
	return aiTruncateRunes(output, 1000)
}

func aiSanitizeFileOutput(output, webRoot, path string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	output = strings.ReplaceAll(output, path, aiRelPath(webRoot, path))
	output = strings.ReplaceAll(output, webRoot, "<site_root>")
	return aiTruncateRunes(output, 1000)
}

func aiTruncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "..."
}

func aiExtractWPConstant(content, name string) string {
	pattern := fmt.Sprintf(`define\(\s*['"]%s['"]\s*,\s*['"]([^'"]*)['"]\s*\)`, regexp.QuoteMeta(name))
	m := regexp.MustCompile(pattern).FindStringSubmatch(content)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

func aiDBCheck(site *models.Website) map[string]interface{} {
	result := map[string]interface{}{"checked": false}
	if site == nil || site.SiteType != "wordpress" {
		result["message"] = "非 WordPress 站点，未检查数据库"
		return result
	}
	if config.AppConfig == nil {
		result["message"] = "面板配置未初始化"
		return result
	}
	prefix, err := ReadWPTablePrefix(site.WebRoot)
	if err != nil {
		result["message"] = "未能读取表前缀: " + err.Error()
		return result
	}
	result["checked"] = true
	result["table_prefix"] = prefix
	siteURL, homeURL, err := ReadWPSiteURLs(site.DBName, prefix, config.AppConfig)
	if err != nil {
		result["ok"] = false
		result["error"] = err.Error()
		return result
	}
	result["ok"] = true
	result["siteurl"] = siteURL
	result["home"] = homeURL
	return result
}

func aiServiceChecks(site *models.Website) map[string]interface{} {
	result := map[string]interface{}{}
	if site == nil {
		return result
	}
	result["nginx_conf_exists"] = aiFileExists(site.NginxConfPath)
	result["php_pool_exists"] = aiFileExists(site.PHPPoolPath)
	result["web_root_exists"] = aiDirExists(site.WebRoot)
	result["log_dir_exists"] = aiDirExists(site.LogDir)
	return result
}

func aiCurrentHTTPChecks(site *models.Website) map[string]interface{} {
	result := map[string]interface{}{"checked": false}
	if site == nil {
		result["message"] = "网站不存在，未执行 HTTP 探测"
		return result
	}
	baseURL, err := aiSiteProbeBaseURL(site)
	if err != nil {
		result["message"] = err.Error()
		return result
	}
	result["checked"] = true
	result["base_url"] = baseURL
	result["home"] = aiProbeHTTP(aiJoinURLPath(baseURL, "/"))
	if site.SiteType == "wordpress" {
		result["wp_admin"] = aiProbeHTTP(aiJoinURLPath(baseURL, "/wp-admin/"))
	}
	result["note"] = "本字段为本次诊断即时 HTTP 探测；access_5xx 是历史日志片段，不能单独证明当前仍然 500。"
	return result
}

func aiSiteProbeBaseURL(site *models.Website) (string, error) {
	domain := strings.TrimSpace(site.Domain)
	if domain == "" {
		return "", fmt.Errorf("网站域名为空，未执行 HTTP 探测")
	}
	if strings.Contains(domain, "://") {
		parsed, err := url.Parse(domain)
		if err != nil || parsed.Host == "" {
			return "", fmt.Errorf("网站域名格式异常，未执行 HTTP 探测")
		}
		domain = parsed.Host
	}
	if strings.ContainsAny(domain, "/?#") {
		return "", fmt.Errorf("网站域名包含路径或查询参数，未执行 HTTP 探测")
	}
	scheme := "http"
	if site.SSLEnabled {
		scheme = "https"
	}
	return scheme + "://" + domain, nil
}

func aiJoinURLPath(baseURL, path string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	parsed.Path = path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func aiProbeHTTPURL(target string) map[string]interface{} {
	result := map[string]interface{}{
		"checked": false,
		"url":     target,
	}
	resp, method, err := aiDoHTTPProbe(target, http.MethodHead)
	if err == nil && resp != nil && resp.StatusCode == http.StatusMethodNotAllowed {
		_ = resp.Body.Close()
		resp, method, err = aiDoHTTPProbe(target, http.MethodGet)
	}
	if err != nil {
		result["error"] = err.Error()
		return result
	}
	if resp == nil {
		result["error"] = "HTTP 探测未返回响应"
		return result
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	result["checked"] = true
	result["method"] = method
	result["status_code"] = resp.StatusCode
	result["status"] = resp.Status
	result["status_family"] = aiHTTPStatusFamily(resp.StatusCode)
	result["is_5xx"] = resp.StatusCode >= 500 && resp.StatusCode <= 599
	result["is_currently_available_signal"] = resp.StatusCode > 0 && resp.StatusCode < 500
	if location := strings.TrimSpace(resp.Header.Get("Location")); location != "" {
		result["redirect_location"] = aiTruncateRunes(location, 200)
	}
	return result
}

func aiDoHTTPProbe(target, method string) (*http.Response, string, error) {
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		return nil, method, err
	}
	req.Header.Set("User-Agent", "WP Panel AI Diagnostics")
	req.Header.Set("Accept", "text/html,*/*;q=0.8")
	resp, err := aiHTTPProbeClient.Do(req)
	return resp, method, err
}

func aiHTTPStatusFamily(statusCode int) string {
	switch {
	case statusCode >= 100 && statusCode <= 199:
		return "1xx"
	case statusCode >= 200 && statusCode <= 299:
		return "2xx"
	case statusCode >= 300 && statusCode <= 399:
		return "3xx"
	case statusCode >= 400 && statusCode <= 499:
		return "4xx"
	case statusCode >= 500 && statusCode <= 599:
		return "5xx"
	default:
		return "unknown"
	}
}

func aiPerformanceSummary(site *models.Website) map[string]interface{} {
	result := map[string]interface{}{
		"checked": true,
		"panel_optimization": map[string]interface{}{
			"fastcgi_cache_enabled": site != nil && site.FCacheEnabled,
			"fastcgi_cache_ttl":     0,
			"wp_memory_limit":       "",
			"monitoring_enabled":    site != nil && site.MonitoringEnabled,
			"access_log_mode":       "",
		},
		"server_resource_summary": aiServerResourceSummary(),
		"site_resource_summary":   aiSiteResourceSummary(site),
		"wordpress_structure":     aiWPPerformanceStructure(site),
		"limitations": []string{
			"当前 access.log 使用 combined 格式，不包含 request_time，无法仅凭访问日志证明单个请求耗时。",
			"当前磁盘 IO 采集仍为占位值，不能把磁盘 IO 高作为已确认原因。",
		},
	}
	if site != nil {
		result["panel_optimization"] = map[string]interface{}{
			"fastcgi_cache_enabled": site.FCacheEnabled,
			"fastcgi_cache_ttl":     site.FCacheTTL,
			"wp_memory_limit":       site.WPMemoryLimit,
			"monitoring_enabled":    site.MonitoringEnabled,
			"access_log_mode":       site.AccessLogMode,
		}
	}
	return result
}

func aiServerResourceSummary() map[string]interface{} {
	cores := runtime.NumCPU()
	result := map[string]interface{}{
		"checked":   false,
		"cpu_cores": cores,
		"current": map[string]interface{}{
			"load":   aiCurrentLoadSummary(cores),
			"memory": aiCurrentMemorySummary(),
		},
	}
	db := database.GetDB()
	if db == nil {
		result["message"] = "面板数据库未初始化，无法读取历史监控指标"
		return result
	}
	result["checked"] = true
	result["latest_metric"] = aiLatestMonitoringMetric(db, cores)
	result["windows"] = map[string]interface{}{
		"15m": aiMonitoringWindow(db, "-15 minutes", cores),
		"1h":  aiMonitoringWindow(db, "-1 hour", cores),
		"24h": aiMonitoringWindow(db, "-24 hours", cores),
	}
	return result
}

func aiLatestMonitoringMetric(db *sql.DB, cores int) map[string]interface{} {
	item := map[string]interface{}{"available": false}
	var cpu, memory, load1, load5, load15 sql.NullFloat64
	var recordedAt string
	err := db.QueryRow(`SELECT cpu_percent, memory_percent, load_avg_1, load_avg_5, load_avg_15, recorded_at FROM monitoring_metrics ORDER BY recorded_at DESC LIMIT 1`).Scan(&cpu, &memory, &load1, &load5, &load15, &recordedAt)
	if err != nil {
		item["message"] = "暂无历史监控数据"
		return item
	}
	item["available"] = true
	item["recorded_at"] = recordedAt
	item["cpu_percent"] = aiRoundFloat(aiNullFloat(cpu))
	item["memory_percent"] = aiRoundFloat(aiNullFloat(memory))
	item["load_avg_1"] = aiRoundFloat(aiNullFloat(load1))
	item["load_avg_5"] = aiRoundFloat(aiNullFloat(load5))
	item["load_avg_15"] = aiRoundFloat(aiNullFloat(load15))
	item["high_load"] = aiNullFloat(load1) >= float64(cores) || aiNullFloat(load5) >= float64(cores)*0.8
	return item
}

func aiMonitoringWindow(db *sql.DB, modifier string, cores int) map[string]interface{} {
	item := map[string]interface{}{"available": false}
	var count int
	var avgCPU, maxCPU, avgMemory, maxMemory, avgLoad1, maxLoad1, avgLoad5, maxLoad5 sql.NullFloat64
	err := db.QueryRow(`SELECT COUNT(*), AVG(cpu_percent), MAX(cpu_percent), AVG(memory_percent), MAX(memory_percent), AVG(load_avg_1), MAX(load_avg_1), AVG(load_avg_5), MAX(load_avg_5)
		FROM monitoring_metrics WHERE recorded_at >= datetime('now', ?)`, modifier).Scan(&count, &avgCPU, &maxCPU, &avgMemory, &maxMemory, &avgLoad1, &maxLoad1, &avgLoad5, &maxLoad5)
	if err != nil || count == 0 {
		item["sample_count"] = count
		item["message"] = "该时间窗口暂无监控数据"
		return item
	}
	item["available"] = true
	item["sample_count"] = count
	item["avg_cpu_percent"] = aiRoundFloat(aiNullFloat(avgCPU))
	item["max_cpu_percent"] = aiRoundFloat(aiNullFloat(maxCPU))
	item["avg_memory_percent"] = aiRoundFloat(aiNullFloat(avgMemory))
	item["max_memory_percent"] = aiRoundFloat(aiNullFloat(maxMemory))
	item["avg_load_1"] = aiRoundFloat(aiNullFloat(avgLoad1))
	item["max_load_1"] = aiRoundFloat(aiNullFloat(maxLoad1))
	item["avg_load_5"] = aiRoundFloat(aiNullFloat(avgLoad5))
	item["max_load_5"] = aiRoundFloat(aiNullFloat(maxLoad5))
	item["high_resource_pressure"] = aiNullFloat(maxCPU) >= 90 || aiNullFloat(maxMemory) >= 90 || aiNullFloat(maxLoad1) >= float64(cores) || aiNullFloat(avgLoad5) >= float64(cores)*0.8
	return item
}

func aiCurrentLoadSummary(cores int) map[string]interface{} {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return map[string]interface{}{"available": false, "message": "无法读取 /proc/loadavg"}
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return map[string]interface{}{"available": false, "message": "loadavg 格式异常"}
	}
	load1, _ := strconv.ParseFloat(fields[0], 64)
	load5, _ := strconv.ParseFloat(fields[1], 64)
	load15, _ := strconv.ParseFloat(fields[2], 64)
	return map[string]interface{}{
		"available":   true,
		"load_avg_1":  aiRoundFloat(load1),
		"load_avg_5":  aiRoundFloat(load5),
		"load_avg_15": aiRoundFloat(load15),
		"high_load":   load1 >= float64(cores) || load5 >= float64(cores)*0.8,
	}
}

func aiCurrentMemorySummary() map[string]interface{} {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return map[string]interface{}{"available": false, "message": "无法读取 /proc/meminfo"}
	}
	var total, available int64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseInt(fields[1], 10, 64)
		value *= 1024
		switch fields[0] {
		case "MemTotal:":
			total = value
		case "MemAvailable:":
			available = value
		}
	}
	if total <= 0 {
		return map[string]interface{}{"available": false, "message": "内存信息格式异常"}
	}
	used := total - available
	percent := float64(used) / float64(total) * 100
	return map[string]interface{}{
		"available":          true,
		"memory_used_bytes":  used,
		"memory_total_bytes": total,
		"memory_percent":     aiRoundFloat(percent),
		"high_memory":        percent >= 90,
	}
}

type aiPHPSiteResource struct {
	User      string
	SiteID    int
	Domain    string
	CPU       float64
	Mem       float64
	ProcCount int
}

func aiSiteResourceSummary(site *models.Website) map[string]interface{} {
	result := map[string]interface{}{
		"checked": false,
	}
	db := database.GetDB()
	if db == nil {
		result["message"] = "面板数据库未初始化，无法读取站点资源占用"
		return result
	}
	result["active_site_count"] = aiWebsiteCount(db, "active")
	result["total_site_count"] = aiWebsiteCount(db, "")
	resources, err := aiCollectPHPSiteResources(db)
	if err != nil {
		result["message"] = err.Error()
		return result
	}
	result["checked"] = true
	result["top_php_fpm_sites"] = aiTopSiteResources(resources, 5)
	if site != nil {
		result["current_site"] = aiFindSiteResource(resources, site.SystemUser, site.Domain)
	}
	return result
}

func aiWebsiteCount(db *sql.DB, status string) int {
	var count int
	if status == "" {
		_ = db.QueryRow("SELECT COUNT(*) FROM websites").Scan(&count)
		return count
	}
	_ = db.QueryRow("SELECT COUNT(*) FROM websites WHERE status = ?", status).Scan(&count)
	return count
}

func aiCollectPHPSiteResources(db *sql.DB) ([]aiPHPSiteResource, error) {
	out, err := aiRunProcessList()
	if err != nil {
		return nil, fmt.Errorf("读取 PHP-FPM 进程资源失败: %v", err)
	}
	agg := map[string]*aiPHPSiteResource{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		user := fields[0]
		if !strings.HasPrefix(user, "wp_") && !strings.HasPrefix(user, "php_") {
			continue
		}
		comm := fields[3]
		if !strings.HasPrefix(comm, "php-fpm") {
			continue
		}
		cpu, _ := strconv.ParseFloat(fields[1], 64)
		mem, _ := strconv.ParseFloat(fields[2], 64)
		item := agg[user]
		if item == nil {
			item = &aiPHPSiteResource{User: user}
			agg[user] = item
		}
		item.CPU += cpu
		item.Mem += mem
		item.ProcCount++
	}
	result := make([]aiPHPSiteResource, 0, len(agg))
	for _, item := range agg {
		_ = db.QueryRow("SELECT id, domain FROM websites WHERE system_user = ?", item.User).Scan(&item.SiteID, &item.Domain)
		if item.Domain == "" {
			continue
		}
		item.CPU = aiRoundFloat(item.CPU)
		item.Mem = aiRoundFloat(item.Mem)
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CPU == result[j].CPU {
			return result[i].Mem > result[j].Mem
		}
		return result[i].CPU > result[j].CPU
	})
	return result, nil
}

func aiTopSiteResources(resources []aiPHPSiteResource, limit int) []map[string]interface{} {
	if limit <= 0 || len(resources) == 0 {
		return []map[string]interface{}{}
	}
	if len(resources) < limit {
		limit = len(resources)
	}
	out := make([]map[string]interface{}, 0, limit)
	for _, item := range resources[:limit] {
		out = append(out, aiSiteResourceMap(item))
	}
	return out
}

func aiFindSiteResource(resources []aiPHPSiteResource, systemUser, domain string) map[string]interface{} {
	for _, item := range resources {
		if item.User == systemUser || (domain != "" && item.Domain == domain) {
			found := aiSiteResourceMap(item)
			found["found"] = true
			return found
		}
	}
	return map[string]interface{}{
		"found":   false,
		"domain":  domain,
		"message": "当前未发现该站点的活跃 php-fpm 进程；可能刚好无请求，或进程采样时未命中。",
	}
}

func aiSiteResourceMap(item aiPHPSiteResource) map[string]interface{} {
	return map[string]interface{}{
		"site_id":     item.SiteID,
		"domain":      item.Domain,
		"cpu_percent": item.CPU,
		"mem_percent": item.Mem,
		"proc_count":  item.ProcCount,
	}
}

func aiWPPerformanceStructure(site *models.Website) map[string]interface{} {
	result := map[string]interface{}{"checked": false}
	if site == nil || site.SiteType != "wordpress" {
		result["message"] = "非 WordPress 站点，未检查插件结构"
		return result
	}
	prefix, err := ReadWPTablePrefix(site.WebRoot)
	if err != nil {
		result["message"] = "未能读取表前缀: " + err.Error()
		return result
	}
	if config.AppConfig == nil {
		result["message"] = "面板配置未初始化"
		return result
	}
	opts, err := aiReadWPDiagnosticOptions(site.DBName, prefix, config.AppConfig)
	if err != nil {
		result["message"] = err.Error()
		return result
	}
	activePlugins := aiParseActivePlugins(opts["active_plugins"])
	pageCachePlugins := aiClassifyPlugins(activePlugins, aiPageCachePluginPatterns())
	objectCachePlugins := aiClassifyPlugins(activePlugins, aiObjectCachePluginPatterns())
	assetOptimizationPlugins := aiClassifyPlugins(activePlugins, aiAssetOptimizationPluginPatterns())
	builderPlugins := aiClassifyPlugins(activePlugins, aiBuilderPluginPatterns())
	heavyPlugins := aiClassifyPlugins(activePlugins, aiHeavyPluginPatterns())
	result["checked"] = true
	result["active_theme"] = map[string]string{
		"template":   strings.TrimSpace(opts["template"]),
		"stylesheet": strings.TrimSpace(opts["stylesheet"]),
	}
	result["active_plugin_count"] = len(activePlugins)
	result["page_cache_plugins"] = pageCachePlugins
	result["object_cache_plugins"] = objectCachePlugins
	result["asset_optimization_plugins"] = assetOptimizationPlugins
	result["builder_plugins"] = builderPlugins
	result["heavy_plugins"] = heavyPlugins
	result["multiple_page_cache_plugins"] = len(pageCachePlugins) > 1
	result["potential_fastcgi_page_cache_overlap"] = site.FCacheEnabled && len(pageCachePlugins) > 0
	result["asset_optimization_overlap"] = len(assetOptimizationPlugins) > 1
	result["many_active_plugins"] = len(activePlugins) >= 25
	return result
}

func aiClassifyPlugins(activePlugins []string, patterns map[string]string) []map[string]string {
	var result []map[string]string
	for _, plugin := range activePlugins {
		normalized := strings.ToLower(filepath.ToSlash(plugin))
		for needle, label := range patterns {
			if strings.Contains(normalized, needle) {
				result = append(result, map[string]string{
					"plugin": plugin,
					"label":  label,
				})
				break
			}
		}
	}
	if result == nil {
		return []map[string]string{}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i]["plugin"] < result[j]["plugin"]
	})
	return result
}

func aiPageCachePluginPatterns() map[string]string {
	return map[string]string{
		"litespeed-cache/":  "LiteSpeed Cache",
		"wp-rocket/":        "WP Rocket",
		"w3-total-cache/":   "W3 Total Cache",
		"wp-super-cache/":   "WP Super Cache",
		"cache-enabler/":    "Cache Enabler",
		"wp-fastest-cache/": "WP Fastest Cache",
		"breeze/":           "Breeze",
		"sg-cachepress/":    "SiteGround Optimizer",
	}
}

func aiObjectCachePluginPatterns() map[string]string {
	return map[string]string{
		"redis-cache/":        "Redis Object Cache",
		"memcached/":          "Memcached",
		"object-cache-pro/":   "Object Cache Pro",
		"wp-redis/":           "WP Redis",
		"wp-redis-cache/":     "WP Redis Cache",
		"redis-object-cache/": "Redis Object Cache",
	}
}

func aiAssetOptimizationPluginPatterns() map[string]string {
	return map[string]string{
		"autoptimize/":          "Autoptimize",
		"wp-rocket/":            "WP Rocket",
		"litespeed-cache/":      "LiteSpeed Cache",
		"w3-total-cache/":       "W3 Total Cache",
		"perfmatters/":          "Perfmatters",
		"asset-cleanup/":        "Asset CleanUp",
		"wp-optimize/":          "WP-Optimize",
		"fast-velocity-minify/": "Fast Velocity Minify",
	}
}

func aiBuilderPluginPatterns() map[string]string {
	return map[string]string{
		"elementor/":         "Elementor",
		"elementor-pro/":     "Elementor Pro",
		"js_composer/":       "WPBakery Page Builder",
		"oxygen/":            "Oxygen Builder",
		"bricks/":            "Bricks",
		"bb-plugin/":         "Beaver Builder",
		"divi-builder/":      "Divi Builder",
		"siteorigin-panels/": "SiteOrigin Page Builder",
	}
}

func aiHeavyPluginPatterns() map[string]string {
	return map[string]string{
		"woocommerce/":             "WooCommerce",
		"wordfence/":               "Wordfence",
		"wordfence-security/":      "Wordfence Security",
		"updraftplus/":             "UpdraftPlus",
		"all-in-one-wp-migration/": "All-in-One WP Migration",
		"duplicator/":              "Duplicator",
	}
}

func aiNullFloat(value sql.NullFloat64) float64 {
	if !value.Valid {
		return 0
	}
	return value.Float64
}

func aiRoundFloat(value float64) float64 {
	return math.Round(value*10) / 10
}

func aiCodeSuspects(site *models.Website) map[string]interface{} {
	result := map[string]interface{}{
		"checked":  false,
		"suspects": []map[string]interface{}{},
	}
	if site == nil || site.SiteType != "wordpress" {
		result["message"] = "非 WordPress 站点，未扫描主题或插件代码"
		return result
	}
	if !aiDirExists(site.WebRoot) {
		result["message"] = "网站根目录不存在或不可读"
		return result
	}
	result["checked"] = true

	prefix, err := ReadWPTablePrefix(site.WebRoot)
	if err != nil {
		result["wp_options_error"] = "未能读取表前缀: " + err.Error()
	} else if config.AppConfig == nil {
		result["wp_options_error"] = "面板配置未初始化"
	} else if opts, err := aiReadWPDiagnosticOptions(site.DBName, prefix, config.AppConfig); err != nil {
		result["wp_options_error"] = err.Error()
	} else {
		templateName := strings.TrimSpace(opts["template"])
		stylesheetName := strings.TrimSpace(opts["stylesheet"])
		activePlugins := aiParseActivePlugins(opts["active_plugins"])
		result["active_theme"] = map[string]interface{}{
			"template":   templateName,
			"stylesheet": stylesheetName,
		}
		result["active_plugins"] = activePlugins
		suspects := result["suspects"].([]map[string]interface{})
		suspects = append(suspects, aiScanActiveTheme(site.WebRoot, stylesheetName)...)
		suspects = append(suspects, aiScanActivePlugins(site.WebRoot, activePlugins)...)
		aiSortCodeSuspects(suspects)
		if len(suspects) > aiMaxCodeSuspects {
			suspects = suspects[:aiMaxCodeSuspects]
			result["suspects_truncated"] = true
		}
		result["suspects"] = suspects
	}

	result["debug_log"] = aiDebugLogSummary(site.WebRoot)
	result["recent_php_files"] = aiRecentPHPFiles(site.WebRoot)
	return result
}

func aiScanActiveTheme(webRoot, stylesheetName string) []map[string]interface{} {
	if strings.TrimSpace(stylesheetName) == "" {
		return []map[string]interface{}{aiCodeSuspect("theme", "", 0, "active_theme_missing", "high", "WordPress 当前主题 stylesheet 为空", "")}
	}
	if !aiSafeThemeName(stylesheetName) {
		return []map[string]interface{}{aiCodeSuspect("theme", "", 0, "active_theme_invalid", "high", "WordPress 当前主题 stylesheet 包含不安全路径字符", "")}
	}
	themeDir := filepath.Join(webRoot, "wp-content", "themes", stylesheetName)
	relThemeDir := filepath.ToSlash(filepath.Join("wp-content", "themes", stylesheetName))
	if !aiPathWithin(webRoot, themeDir) || !aiDirExists(themeDir) {
		return []map[string]interface{}{aiCodeSuspect("theme", relThemeDir, 0, "active_theme_not_found", "high", "当前启用主题目录不存在或不可读", "")}
	}
	functionsPath := filepath.Join(themeDir, "functions.php")
	relFunctions := filepath.ToSlash(filepath.Join(relThemeDir, "functions.php"))
	if !aiFileExists(functionsPath) {
		return []map[string]interface{}{aiCodeSuspect("theme", relFunctions, 0, "functions_php_missing", "medium", "当前主题没有 functions.php；这不一定是错误，但无法从该文件扫描代码疑点", "")}
	}
	return aiScanPHPFileForSuspects(webRoot, functionsPath, "active_theme_functions")
}

func aiScanActivePlugins(webRoot string, plugins []string) []map[string]interface{} {
	var suspects []map[string]interface{}
	for _, plugin := range plugins {
		if !aiSafePluginPath(plugin) {
			suspects = append(suspects, aiCodeSuspect("plugin", plugin, 0, "active_plugin_invalid", "high", "active_plugins 中存在不安全插件路径", ""))
			continue
		}
		path := filepath.Join(webRoot, "wp-content", "plugins", filepath.FromSlash(plugin))
		rel := filepath.ToSlash(filepath.Join("wp-content", "plugins", filepath.FromSlash(plugin)))
		if !aiPathWithin(webRoot, path) || !aiFileExists(path) {
			suspects = append(suspects, aiCodeSuspect("plugin", rel, 0, "active_plugin_not_found", "high", "启用插件文件不存在或不可读", ""))
			continue
		}
		fileSuspects := aiScanPHPFileForSuspects(webRoot, path, "active_plugin_main")
		suspects = append(suspects, fileSuspects...)
	}
	aiSortCodeSuspects(suspects)
	if len(suspects) > aiMaxCodeSuspects {
		return suspects[:aiMaxCodeSuspects]
	}
	return suspects
}

func aiSortCodeSuspects(suspects []map[string]interface{}) {
	priority := func(item map[string]interface{}) int {
		switch item["severity"] {
		case "high":
			return 0
		case "medium":
			return 1
		case "low":
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(suspects, func(i, j int) bool {
		left := priority(suspects[i])
		right := priority(suspects[j])
		if left != right {
			return left < right
		}
		return aiSuspectLine(suspects[i]) < aiSuspectLine(suspects[j])
	})
}

func aiSuspectLine(item map[string]interface{}) int {
	line, ok := item["line"].(int)
	if !ok {
		return 0
	}
	return line
}

func aiScanPHPFileForSuspects(webRoot, path, scope string) []map[string]interface{} {
	if !aiPathWithin(webRoot, path) || !aiFileExists(path) {
		return nil
	}
	var suspects []map[string]interface{}
	rel := aiRelPath(webRoot, path)
	if lintResult, err := aiRunPHPLint(path); err != nil {
		output := aiSanitizeFileOutput(aiLintOutput(lintResult), webRoot, path)
		if output != "" {
			suspects = append(suspects, aiCodeSuspect(scope, rel, 0, "php_syntax_error", "high", output, ""))
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return suspects
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	braceDepth := 0
	for i, line := range lines {
		depthBeforeLine := braceDepth
		pattern, severity, reason, conditional, ok := aiSuspiciousPHPLine(line, depthBeforeLine)
		if ok {
			suspect := aiCodeSuspect(scope, rel, i+1, pattern, severity, reason, aiSnippetAround(lines, i, aiCodeContextLines))
			if conditional {
				suspect["context"] = "conditional_block"
			}
			suspects = append(suspects, suspect)
			if len(suspects) >= aiMaxCodeSuspects {
				break
			}
		}
		braceDepth += aiBraceDelta(line)
		if braceDepth < 0 {
			braceDepth = 0
		}
	}
	return suspects
}

func aiSuspiciousPHPLine(line string, braceDepth int) (pattern, severity, reason string, conditional bool, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "#") {
		return "", "", "", false, false
	}
	if regexp.MustCompile(`^\s*wp_die\s*\(`).MatchString(trimmed) {
		if braceDepth == 0 {
			return "wp_die(", "high", "文件中存在顶层 wp_die 调用，可能直接终止 WordPress 请求并造成白屏/500", false, true
		}
		return "wp_die(", "low", "代码块内存在 wp_die 调用，通常需要特定条件触发；仅作为低优先级线索", true, true
	}
	if regexp.MustCompile(`^\s*die\s*\(`).MatchString(trimmed) {
		if braceDepth == 0 {
			return "die(", "high", "文件中存在顶层 die 调用，可能直接终止 PHP 请求", false, true
		}
		return "die(", "low", "代码块内存在 die 调用，通常需要特定条件触发；仅作为低优先级线索", true, true
	}
	if regexp.MustCompile(`^\s*exit\s*(\(|;)`).MatchString(trimmed) {
		if braceDepth == 0 {
			return "exit", "high", "文件中存在顶层 exit 调用，可能直接终止 PHP 请求", false, true
		}
		return "exit", "low", "代码块内存在 exit 调用，通常需要特定条件触发；仅作为低优先级线索", true, true
	}
	checks := []struct {
		re       *regexp.Regexp
		pattern  string
		severity string
		reason   string
	}{
		{regexp.MustCompile(`trigger_error\s*\([^,\)]*,\s*E_USER_ERROR\s*\)`), "trigger_error(E_USER_ERROR)", "medium", "代码中存在 E_USER_ERROR，特定条件下可能触发 fatal error"},
		{regexp.MustCompile(`throw\s+new\s+(Error|Exception|RuntimeException)\b`), "throw new", "medium", "代码中存在抛出异常/错误，若未捕获可能导致 500"},
		{regexp.MustCompile(`\b(require|require_once|include|include_once)\s*\(?\s*['"][^'"]+['"]`), "include/require", "low", "代码中存在 include/require，若目标文件缺失可能导致错误；需结合具体路径验证"},
	}
	for _, check := range checks {
		if check.re.MatchString(trimmed) {
			return check.pattern, check.severity, check.reason, braceDepth > 0, true
		}
	}
	return "", "", "", false, false
}

func aiBraceDelta(line string) int {
	stripped := aiStripPHPLineForBraceCount(line)
	return strings.Count(stripped, "{") - strings.Count(stripped, "}")
}

func aiStripPHPLineForBraceCount(line string) string {
	var out strings.Builder
	var quote rune
	escaped := false
	for _, ch := range line {
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		out.WriteRune(ch)
	}
	stripped := out.String()
	if idx := strings.Index(stripped, "//"); idx >= 0 {
		stripped = stripped[:idx]
	}
	if idx := strings.Index(stripped, "#"); idx >= 0 {
		stripped = stripped[:idx]
	}
	return stripped
}

func aiCodeSuspect(scope, relPath string, line int, pattern, severity, reason, snippet string) map[string]interface{} {
	item := map[string]interface{}{
		"scope":    scope,
		"file":     filepath.ToSlash(relPath),
		"pattern":  pattern,
		"severity": severity,
		"reason":   reason,
	}
	if line > 0 {
		item["line"] = line
	}
	if strings.TrimSpace(snippet) != "" {
		item["snippet"] = aiTruncateRunes(snippet, aiMaxCodeSnippetChars)
	}
	return item
}

func aiSnippetAround(lines []string, index, contextLines int) string {
	start := index - contextLines
	if start < 0 {
		start = 0
	}
	end := index + contextLines + 1
	if end > len(lines) {
		end = len(lines)
	}
	var out []string
	for i := start; i < end; i++ {
		out = append(out, fmt.Sprintf("%d: %s", i+1, lines[i]))
	}
	return strings.Join(out, "\n")
}

func aiDebugLogSummary(webRoot string) map[string]interface{} {
	path := filepath.Join(webRoot, "wp-content", "debug.log")
	result := map[string]interface{}{
		"checked": true,
		"exists":  false,
		"file":    "wp-content/debug.log",
	}
	if !aiPathWithin(webRoot, path) || !aiFileExists(path) {
		result["message"] = "debug.log 不存在或不可读"
		return result
	}
	lines, truncated, err := aiTailInterestingLines(path, 80, 2500, false)
	if err != nil {
		result["message"] = "debug.log 不可读"
		return result
	}
	result["exists"] = true
	result["lines"] = lines
	result["truncated"] = truncated
	return result
}

func aiRecentPHPFiles(webRoot string) []map[string]interface{} {
	base := filepath.Join(webRoot, "wp-content")
	if !aiPathWithin(webRoot, base) || !aiDirExists(base) {
		return []map[string]interface{}{}
	}
	type fileInfo struct {
		path    string
		modTime time.Time
		size    int64
	}
	var files []fileInfo
	_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "uploads" || name == "cache" || name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) > 500 {
			return filepath.SkipAll
		}
		if !strings.EqualFold(filepath.Ext(path), ".php") || !aiPathWithin(webRoot, path) {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		files = append(files, fileInfo{path: path, modTime: info.ModTime(), size: info.Size()})
		return nil
	})
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})
	if len(files) > aiMaxRecentPHPFiles {
		files = files[:aiMaxRecentPHPFiles]
	}
	result := make([]map[string]interface{}, 0, len(files))
	for _, item := range files {
		result = append(result, map[string]interface{}{
			"file":     aiRelPath(webRoot, item.path),
			"modified": item.modTime.Format(time.RFC3339),
			"size":     item.size,
		})
	}
	return result
}

func aiParseActivePlugins(serialized string) []string {
	re := regexp.MustCompile(`s:\d+:"([^"]+\.php)"`)
	matches := re.FindAllStringSubmatch(serialized, -1)
	plugins := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		plugin := filepath.ToSlash(strings.TrimSpace(match[1]))
		if plugin == "" || seen[plugin] {
			continue
		}
		seen[plugin] = true
		plugins = append(plugins, plugin)
	}
	return plugins
}

func aiSafeThemeName(name string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(name) && !strings.Contains(name, "..")
}

func aiSafePluginPath(plugin string) bool {
	if plugin == "" || strings.Contains(plugin, "..") || strings.HasPrefix(plugin, "/") || strings.HasPrefix(plugin, "\\") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(plugin)))
	if clean != plugin || !strings.HasSuffix(clean, ".php") {
		return false
	}
	for _, part := range strings.Split(clean, "/") {
		if !regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(part) {
			return false
		}
	}
	return true
}

func aiRelPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.Base(path)
	}
	return filepath.ToSlash(rel)
}

func aiFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func aiDirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func aiRecentPanelOperations(domain string, limit int) []map[string]string {
	db := database.GetDB()
	if db == nil || domain == "" {
		return []map[string]string{}
	}
	rows, err := db.Query(`SELECT operation, target, status, message, created_at
		FROM operation_logs
		WHERE target = ?
		ORDER BY created_at DESC
		LIMIT ?`, domain, limit)
	if err != nil {
		return []map[string]string{}
	}
	defer rows.Close()
	var result []map[string]string
	for rows.Next() {
		var operation, target, status, message, createdAt string
		if err := rows.Scan(&operation, &target, &status, &message, &createdAt); err != nil {
			continue
		}
		result = append(result, map[string]string{
			"operation":       operation,
			"operation_label": aiOperationLabel(operation),
			"target":          target,
			"status":          status,
			"message":         message,
			"created_at":      createdAt,
		})
	}
	if result == nil {
		return []map[string]string{}
	}
	return result
}

func aiOperationLabel(operation string) string {
	switch operation {
	case "wp_optimizations":
		return "WordPress 优化设置"
	case "set_cdn_realip":
		return "CDN 真实 IP 设置"
	case "set_access_log_mode":
		return "访问日志设置"
	case "save_nginx_custom":
		return "Nginx 自定义配置"
	case "change_db_password":
		return "数据库密码修改"
	case "update_domains":
		return "域名设置"
	case "enable_ssl":
		return "启用 SSL"
	case "remove_ssl":
		return "删除 SSL"
	case "create_backup":
		return "创建备份"
	case "restore_backup":
		return "恢复备份"
	case "create_site":
		return "创建网站"
	case "delete_site":
		return "删除网站"
	case "pause_site":
		return "暂停网站"
	case "enable_site":
		return "启用网站"
	case "ssl_certificate_export":
		return "SSL 证书导出"
	default:
		return operation
	}
}

func aiLocalChecks(ctx aiDiagnosticContext) map[string]interface{} {
	all := strings.ToLower(aiJoinedLogs(ctx.Logs))
	hits := []string{}
	check := func(label, needle string) {
		if strings.Contains(all, strings.ToLower(needle)) {
			hits = append(hits, label)
		}
	}
	check("PHP Fatal error", "Fatal error")
	check("PHP Parse error", "Parse error")
	check("PHP memory exhausted", "Allowed memory size")
	check("Undefined function", "Call to undefined")
	check("Class not found", "Class not found")
	check("Permission denied", "permission denied")
	check("Nginx Primary script unknown", "Primary script unknown")
	check("Database related error", "database")
	check("Nginx upstream error", "upstream")
	return map[string]interface{}{
		"rule_hits": hits,
		"has_hits":  len(hits) > 0,
	}
}

func aiJoinedLogs(logs map[string]aiLogSnippet) string {
	var b strings.Builder
	for _, item := range logs {
		for _, line := range item.Lines {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func aiShrinkLogs(logs map[string]aiLogSnippet, maxChars int) {
	for key, item := range logs {
		if len(item.Lines) == 0 {
			continue
		}
		total := 0
		start := len(item.Lines)
		for i := len(item.Lines) - 1; i >= 0; i-- {
			if total+len(item.Lines[i]) > maxChars {
				break
			}
			total += len(item.Lines[i])
			start = i
		}
		if start > 0 {
			item.Truncated = true
			item.Lines = item.Lines[start:]
		}
		logs[key] = item
	}
}

func aiClearLogLines(logs map[string]aiLogSnippet, keys []string, message string) {
	for _, key := range keys {
		item, ok := logs[key]
		if !ok {
			continue
		}
		item.Lines = nil
		item.Truncated = true
		item.Message = message
		logs[key] = item
	}
}

func aiClearAllLogLines(logs map[string]aiLogSnippet, message string) {
	keys := make([]string, 0, len(logs))
	for key := range logs {
		keys = append(keys, key)
	}
	aiClearLogLines(logs, keys, message)
}

func aiLimitStringMapSlice(items []map[string]string, max int) []map[string]string {
	if max <= 0 {
		return nil
	}
	if len(items) <= max {
		return items
	}
	return items[:max]
}

func aiCodeSuspectItems(code map[string]interface{}) []map[string]interface{} {
	if code == nil {
		return nil
	}
	items, ok := code["suspects"].([]map[string]interface{})
	if ok {
		return items
	}
	generic, ok := code["suspects"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(generic))
	for _, item := range generic {
		if m, ok := item.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

func aiSetCodeSuspectItems(code map[string]interface{}, items []map[string]interface{}) {
	if code != nil {
		code["suspects"] = items
	}
}

func aiTrimCodeSuspectSnippets(code map[string]interface{}, includeHigh bool, maxChars int) {
	items := aiCodeSuspectItems(code)
	for _, item := range items {
		severity, _ := item["severity"].(string)
		if severity == "high" && !includeHigh {
			continue
		}
		if maxChars <= 0 {
			delete(item, "snippet")
			continue
		}
		if snippet, ok := item["snippet"].(string); ok {
			item["snippet"] = aiTruncateRunes(snippet, maxChars)
		}
	}
	aiSetCodeSuspectItems(code, items)
}

func aiLimitCodeSuspects(code map[string]interface{}, max int) {
	if max <= 0 {
		aiSetCodeSuspectItems(code, []map[string]interface{}{})
		return
	}
	items := aiCodeSuspectItems(code)
	if len(items) <= max {
		return
	}
	priority := func(item map[string]interface{}) int {
		switch item["severity"] {
		case "high":
			return 0
		case "medium":
			return 1
		case "low":
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return priority(items[i]) < priority(items[j])
	})
	aiSetCodeSuspectItems(code, items[:max])
	if code != nil {
		code["suspects_truncated"] = true
	}
}

func aiLimitRecentPHPFiles(code map[string]interface{}, max int) {
	if code == nil {
		return
	}
	if max <= 0 {
		delete(code, "recent_php_files")
		return
	}
	if files, ok := code["recent_php_files"].([]map[string]interface{}); ok && len(files) > max {
		code["recent_php_files"] = files[:max]
	}
}

func aiTrimDebugLog(code map[string]interface{}, maxLines, maxChars int) {
	if code == nil {
		return
	}
	debugLog, ok := code["debug_log"].(map[string]interface{})
	if !ok {
		return
	}
	if maxLines <= 0 || maxChars <= 0 {
		delete(debugLog, "lines")
		debugLog["truncated"] = true
		debugLog["message"] = "因上下文预算限制未发送 debug.log 正文"
		return
	}
	lines, ok := debugLog["lines"].([]string)
	if !ok {
		return
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
		debugLog["truncated"] = true
	}
	total := 0
	start := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		if total+len(lines[i]) > maxChars {
			break
		}
		total += len(lines[i])
		start = i
	}
	debugLog["lines"] = lines[start:]
	if start > 0 {
		debugLog["truncated"] = true
	}
}

func aiChatEndpoint(baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", fmt.Errorf("Base URL 不能为空")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("Base URL 格式无效")
	}
	if u.User != nil {
		return "", fmt.Errorf("Base URL 不能包含用户名或密码")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("Base URL 仅支持 http 或 https")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(u.Path, "/chat/completions") {
		return u.String(), nil
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	return u.String(), nil
}

func aiHTTPError(status int, data []byte) error {
	msg := strings.TrimSpace(string(data))
	var parsed aiChatResponse
	if json.Unmarshal(data, &parsed) == nil && parsed.Error != nil && parsed.Error.Message != "" {
		msg = parsed.Error.Message
	}
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", status)
	}
	errType := "provider_error"
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		errType = "unauthorized"
	case http.StatusTooManyRequests:
		errType = "rate_limited"
	default:
		if status >= 500 {
			errType = "provider_error"
		}
	}
	return &AIProviderError{Type: errType, StatusCode: status, Message: msg}
}
