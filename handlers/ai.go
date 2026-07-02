package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

// aiDiagnosisMu prevents concurrent diagnoses for the same site within one process.
var aiDiagnosisMu sync.Map

const (
	aiSessionKeepLimit          = 20
	aiMessageKeepLimit          = 40
	aiFollowupContextLimit      = 12
	aiMessageMaxChars           = 4000
	aiProviderMaxTimeoutSeconds = 180
)

type AIHandler struct{}

func (h *AIHandler) GetSettings(c *gin.Context) {
	settings, err := loadAISettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_settings.load_failed")))
		return
	}
	settings.APIKey = ""
	settings.APIKeyMasked = maskAIKey(settings.APIKeyMasked)
	c.JSON(http.StatusOK, models.SuccessResponse(settings))
}

func (h *AIHandler) SaveSettings(c *gin.Context) {
	var req models.AISettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return
	}
	settings, err := normalizeAISettingsRequest(req, true, i18n.LangFromRequest(c.Request))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(err.Error()))
		return
	}
	if err := saveAISettings(settings); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_settings.save_failed")))
		return
	}
	settings.APIKey = ""
	settings.APIKeyMasked = maskAIKey(settings.APIKeyMasked)
	c.JSON(http.StatusOK, models.SuccessResponse(settings))
}

func (h *AIHandler) Test(c *gin.Context) {
	var req models.AITestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return
	}
	settings, err := loadAISettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_settings.load_failed")))
		return
	}
	if strings.TrimSpace(req.Provider) != "" || strings.TrimSpace(req.BaseURL) != "" || strings.TrimSpace(req.Model) != "" || strings.TrimSpace(req.APIKey) != "" || req.TimeoutSeconds > 0 {
		tmp := models.AISettingsRequest{
			Enabled:        true,
			Provider:       req.Provider,
			BaseURL:        req.BaseURL,
			Model:          req.Model,
			APIKey:         req.APIKey,
			TimeoutSeconds: req.TimeoutSeconds,
		}
			normalized, err := normalizeAISettingsRequest(tmp, false, i18n.LangFromRequest(c.Request))
		if err != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResponse(err.Error()))
			return
		}
		if normalized.APIKey == "" {
			normalized.APIKey = settings.APIKey
		}
		settings = normalized
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(settings.TimeoutSeconds)*time.Second)
	defer cancel()
	elapsed, msg, err := executor.TestAISettings(ctx, settings)
	if err != nil {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
			"ok":         false,
			"provider":   settings.Provider,
			"model":      settings.Model,
			"latency_ms": elapsed,
			"message":    aiUserError(i18n.LangFromRequest(c.Request), err),
		}))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"ok":         true,
		"provider":   settings.Provider,
		"model":      settings.Model,
		"latency_ms": elapsed,
		"message":    msg,
	}))
}

func (h *AIHandler) Diagnose(c *gin.Context) {
	id, err := parseSiteID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.invalid_site_id")))
		return
	}
	var req models.AIDiagnoseRequest
	if err := c.ShouldBindJSON(&req); err != nil || !models.IsValidAIDiagnosisSymptom(req.Symptom) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.invalid_symptom")))
		return
	}

	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "website.not_found")))
		return
	}

	settings, err := loadAISettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_settings.load_failed")))
		return
	}
	if !settings.Enabled {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.not_enabled")))
		return
	}
	if strings.TrimSpace(settings.APIKey) == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.api_key_required")))
		return
	}

	// Mark sessions left in 'running' by a previous process restart as failed.
	_, _ = database.GetDB().Exec(
		`UPDATE ai_sessions SET status = ?, error_message = ?, updated_at = CURRENT_TIMESTAMP
			 WHERE site_id = ? AND status = ? AND updated_at <= datetime('now', '-10 minutes')`,
			models.AISessionFailed, i18n.TE(c.Request, "ai_diagnostics.session_interrupted"), site.ID, models.AISessionRunning,
		)

	// Prevent concurrent diagnoses for the same site within this process.
		if _, loaded := aiDiagnosisMu.LoadOrStore(site.ID, struct{}{}); loaded {
			c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
				"status":  models.AISessionRunning,
				"message": i18n.TE(c.Request, "ai_diagnostics.already_running"),
			}))
			return
		}
	defer aiDiagnosisMu.Delete(site.ID)

	// Also block if a running session exists from a different process.
		if running, ok := activeAISession(site.ID); ok {
			c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
				"session_id": running.ID,
				"status":     running.Status,
				"message":    i18n.TE(c.Request, "ai_diagnostics.already_running"),
			}))
			return
		}

	sessionID, err := createAISession(site.ID, req.Symptom)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.create_session_failed")))
		return
	}
	updateAISessionStatus(sessionID, models.AISessionRunning, "")

	systemPrompt, userPrompt, err := executor.BuildAIDiagnosticPrompt(site, req.Symptom)
	if err != nil {
		failAISession(sessionID, err.Error(), len(userPrompt), 0)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.collect_context_failed")))
		return
	}

	// Keep the diagnosis running even if the browser request is aborted. The
	// result is persisted to ai_sessions and can be loaded from history later.
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(settings.TimeoutSeconds)*time.Second)
	defer cancel()
	content, _, err := executor.CallAIChat(ctx, settings, systemPrompt, userPrompt)
	if err != nil {
			msg := aiUserError(i18n.LangFromRequest(c.Request), err)
		failAISession(sessionID, msg, len(userPrompt), 0)
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
			"session_id":    sessionID,
			"status":        models.AISessionFailed,
			"error_message": msg,
		}))
		return
	}

	report, rawText, ok := executor.ParseAIReport(content)
	reportJSON := ""
	summary := ""
	riskLevel := ""
	if ok && report != nil {
		data, _ := json.Marshal(report)
		reportJSON = string(data)
		summary = strings.TrimSpace(report.Summary)
		riskLevel = strings.TrimSpace(report.RiskLevel)
	} else {
		rawText = content
		summary = excerpt(content, 500)
	}
	if summary == "" {
		summary = i18n.TE(c.Request, "ai_diagnostics.result_ready")
	}
	if err := completeAISession(sessionID, riskLevel, summary, reportJSON, rawText, len(userPrompt), len(content)); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.save_result_failed")))
		return
	}
	pruneAISessions(site.ID, aiSessionKeepLimit)

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"session_id": sessionID,
		"status":     models.AISessionCompleted,
		"report":     report,
		"raw_text":   rawText,
	}))
}

func (h *AIHandler) ListSessions(c *gin.Context) {
	id, err := parseSiteID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.invalid_site_id")))
		return
	}
	if getWebsiteByID(id) == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "website.not_found")))
		return
	}
	rows, err := database.GetDB().Query(`SELECT id, site_id, symptom, status, risk_level, summary, error_message, created_at, updated_at
			FROM ai_sessions WHERE site_id = ? ORDER BY created_at DESC LIMIT ?`, id, aiSessionKeepLimit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.load_sessions_failed")))
		return
	}
	defer rows.Close()
	var items []models.AISessionSummary
	for rows.Next() {
		var item models.AISessionSummary
		var summary string
		if err := rows.Scan(&item.ID, &item.SiteID, &item.Symptom, &item.Status, &item.RiskLevel, &summary, &item.ErrorMessage, &item.CreatedAt, &item.UpdatedAt); err != nil {
			continue
		}
		item.SummaryExcerpt = excerpt(summary, 160)
		items = append(items, item)
	}
	if items == nil {
		items = []models.AISessionSummary{}
	}
	c.JSON(http.StatusOK, models.SuccessResponse(items))
}

func (h *AIHandler) GetSession(c *gin.Context) {
	siteID, err := parseSiteID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.invalid_site_id")))
		return
	}
	sessionID, err := parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.invalid_session_id")))
		return
	}
	detail, err := loadAISessionDetail(siteID, sessionID)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.session_not_found")))
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.load_sessions_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(detail))
}

func (h *AIHandler) ListMessages(c *gin.Context) {
	siteID, sessionID, ok := parseAIMessageScope(c)
	if !ok {
		return
	}
	if _, err := loadAISessionDetail(siteID, sessionID); err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.session_not_found")))
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.load_sessions_failed")))
		return
	}
	messages, err := listAIMessages(sessionID, aiMessageKeepLimit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.load_messages_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(messages))
}

func (h *AIHandler) SendMessage(c *gin.Context) {
	siteID, sessionID, ok := parseAIMessageScope(c)
	if !ok {
		return
	}
	var req models.AIMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.followup_required")))
		return
	}
	if len([]rune(content)) > aiMessageMaxChars {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.followup_too_long")))
		return
	}
	site := getWebsiteByID(siteID)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "website.not_found")))
		return
	}
	session, err := loadAISessionDetail(siteID, sessionID)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.session_not_found")))
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.load_sessions_failed")))
		return
	}
	if session.Status == models.AISessionRunning || session.Status == models.AISessionPending {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.followup_wait")))
		return
	}
	settings, err := loadAISettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_settings.load_failed")))
		return
	}
	if !settings.Enabled {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.not_enabled")))
		return
	}
	if strings.TrimSpace(settings.APIKey) == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.api_key_required")))
		return
	}

		if _, loaded := aiDiagnosisMu.LoadOrStore(site.ID, struct{}{}); loaded {
			c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
				"status":  models.AISessionRunning,
				"message": i18n.TE(c.Request, "ai_diagnostics.already_running_followup"),
			}))
			return
		}
	defer aiDiagnosisMu.Delete(site.ID)

	if _, err := createAIMessage(sessionID, "user", content, 0, 0, ""); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.save_followup_failed")))
		return
	}
	messages, err := listAIMessages(sessionID, aiFollowupContextLimit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.load_context_failed")))
		return
	}
	systemPrompt, userPrompt, err := executor.BuildAIFollowupPrompt(site, &session, messages, content)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.build_context_failed")))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(settings.TimeoutSeconds)*time.Second)
	defer cancel()
	reply, _, err := executor.CallAIChat(ctx, settings, systemPrompt, userPrompt)
	if err != nil {
			msg := aiUserError(i18n.LangFromRequest(c.Request), err)
		_, _ = createAIMessage(sessionID, "assistant", "", len(userPrompt), 0, msg)
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
			"status":        models.AISessionFailed,
			"error_message": msg,
		}))
		return
	}
	if _, err := createAIMessage(sessionID, "assistant", reply, len(userPrompt), len(reply), ""); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.save_reply_failed")))
		return
	}
	pruneAIMessages(sessionID, aiMessageKeepLimit)
	allMessages, err := listAIMessages(sessionID, aiMessageKeepLimit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.load_messages_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"status":   models.AISessionCompleted,
		"messages": allMessages,
	}))
}

func parseSiteID(c *gin.Context) (int, error) {
	return strconvAtoi(c.Param("id"))
}

func parseSessionID(c *gin.Context) (int, error) {
	return strconvAtoi(c.Param("session_id"))
}

func parseAIMessageScope(c *gin.Context) (siteID, sessionID int, ok bool) {
	siteID, err := parseSiteID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.invalid_site_id")))
		return 0, 0, false
	}
	sessionID, err = parseSessionID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "ai_diagnostics.invalid_session_id")))
		return 0, 0, false
	}
	return siteID, sessionID, true
}

func strconvAtoi(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("empty id")
	}
	var id int
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, errors.New("invalid id")
		}
		id = id*10 + int(ch-'0')
	}
	if id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

func loadAISettings() (*models.AISettings, error) {
	db := database.GetDB()
	_, _ = db.Exec("INSERT OR IGNORE INTO ai_settings (id) VALUES (1)")
	var enabled int
	var settings models.AISettings
	err := db.QueryRow(`SELECT enabled, provider, base_url, model, api_key, timeout_seconds, created_at, updated_at
		FROM ai_settings WHERE id = 1`).Scan(
		&enabled, &settings.Provider, &settings.BaseURL, &settings.Model, &settings.APIKey,
		&settings.TimeoutSeconds, &settings.CreatedAt, &settings.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	settings.Enabled = enabled == 1
	settings.APIKeyMasked = settings.APIKey
	return &settings, nil
}

func normalizeAISettingsRequest(req models.AISettingsRequest, preserveExistingKey bool, lang string) (*models.AISettings, error) {
	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		provider = "deepseek"
	}
	if provider != "deepseek" && provider != "openai" && provider != "openai_compatible" {
		return nil, errors.New(i18n.T(lang, "ai_settings.invalid_provider"))
	}
	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if baseURL == "" {
		if provider == "deepseek" {
			baseURL = "https://api.deepseek.com"
		} else {
			baseURL = "https://api.openai.com/v1"
		}
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		if provider == "deepseek" {
			model = "deepseek-v4-pro"
		} else {
			model = "gpt-4.1-mini"
		}
	}
	timeout := req.TimeoutSeconds
	if timeout <= 0 {
		timeout = 60
	}
	if timeout < 15 {
		timeout = 15
	}
	if timeout > aiProviderMaxTimeoutSeconds {
		timeout = aiProviderMaxTimeoutSeconds
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if preserveExistingKey && (apiKey == "" || strings.Contains(apiKey, "...")) {
		if current, err := loadAISettings(); err == nil {
			apiKey = current.APIKey
		}
	}
	return &models.AISettings{
		Enabled:        req.Enabled,
		Provider:       provider,
		BaseURL:        baseURL,
		Model:          model,
		APIKey:         apiKey,
		APIKeyMasked:   apiKey,
		TimeoutSeconds: timeout,
	}, nil
}

func saveAISettings(settings *models.AISettings) error {
	enabled := 0
	if settings.Enabled {
		enabled = 1
	}
	res, err := database.GetDB().Exec(`UPDATE ai_settings
		SET enabled = ?, provider = ?, base_url = ?, model = ?, api_key = ?, timeout_seconds = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = 1`, enabled, settings.Provider, settings.BaseURL, settings.Model, settings.APIKey, settings.TimeoutSeconds)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := database.GetDB().Exec("INSERT OR IGNORE INTO ai_settings (id) VALUES (1)"); err != nil {
			return err
		}
		_, err = database.GetDB().Exec(`UPDATE ai_settings
			SET enabled = ?, provider = ?, base_url = ?, model = ?, api_key = ?, timeout_seconds = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = 1`, enabled, settings.Provider, settings.BaseURL, settings.Model, settings.APIKey, settings.TimeoutSeconds)
		return err
	}
	return nil
}

func createAISession(siteID int, symptom string) (int, error) {
	res, err := database.GetDB().Exec(`INSERT INTO ai_sessions (site_id, symptom, status) VALUES (?, ?, ?)`,
		siteID, symptom, models.AISessionPending)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return int(id), err
}

func loadAISessionDetail(siteID, sessionID int) (models.AISessionDetail, error) {
	var detail models.AISessionDetail
	var reportJSON string
	err := database.GetDB().QueryRow(`SELECT id, site_id, symptom, status, risk_level, summary, report_json, raw_text, error_message, prompt_chars, response_chars, created_at, updated_at
		FROM ai_sessions WHERE id = ? AND site_id = ?`, sessionID, siteID).Scan(
		&detail.ID, &detail.SiteID, &detail.Symptom, &detail.Status, &detail.RiskLevel,
		&detail.Summary, &reportJSON, &detail.RawText, &detail.ErrorMessage,
		&detail.PromptChars, &detail.ResponseChars, &detail.CreatedAt, &detail.UpdatedAt,
	)
	if err != nil {
		return detail, err
	}
	if strings.TrimSpace(reportJSON) != "" {
		var report models.AIDiagnosticReport
		if json.Unmarshal([]byte(reportJSON), &report) == nil {
			detail.Report = &report
		}
	}
	return detail, nil
}

func activeAISession(siteID int) (models.AISessionSummary, bool) {
	var item models.AISessionSummary
	err := database.GetDB().QueryRow(`SELECT id, site_id, symptom, status, risk_level, summary, error_message, created_at, updated_at
		FROM ai_sessions
		WHERE site_id = ? AND status = ? AND updated_at > datetime('now', '-10 minutes')
		ORDER BY updated_at DESC LIMIT 1`, siteID, models.AISessionRunning).Scan(
		&item.ID, &item.SiteID, &item.Symptom, &item.Status, &item.RiskLevel,
		&item.SummaryExcerpt, &item.ErrorMessage, &item.CreatedAt, &item.UpdatedAt,
	)
	return item, err == nil
}

func createAIMessage(sessionID int, role, content string, promptChars, responseChars int, errorMessage string) (int, error) {
	res, err := database.GetDB().Exec(`INSERT INTO ai_messages (session_id, role, content, prompt_chars, response_chars, error_message)
		VALUES (?, ?, ?, ?, ?, ?)`, sessionID, role, strings.TrimSpace(content), promptChars, responseChars, strings.TrimSpace(errorMessage))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return int(id), err
}

func listAIMessages(sessionID, limit int) ([]models.AIMessage, error) {
	if limit <= 0 {
		limit = aiMessageKeepLimit
	}
	rows, err := database.GetDB().Query(`SELECT id, session_id, role, content, prompt_chars, response_chars, error_message, created_at
		FROM (
			SELECT id, session_id, role, content, prompt_chars, response_chars, error_message, created_at
			FROM ai_messages
			WHERE session_id = ?
			ORDER BY created_at DESC, id DESC
			LIMIT ?
		) ORDER BY created_at ASC, id ASC`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []models.AIMessage
	for rows.Next() {
		var msg models.AIMessage
		if err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Role, &msg.Content, &msg.PromptChars, &msg.ResponseChars, &msg.ErrorMessage, &msg.CreatedAt); err != nil {
			continue
		}
		messages = append(messages, msg)
	}
	if messages == nil {
		messages = []models.AIMessage{}
	}
	return messages, rows.Err()
}

func pruneAIMessages(sessionID, keep int) {
	_, _ = database.GetDB().Exec(`DELETE FROM ai_messages
		WHERE session_id = ? AND id NOT IN (
			SELECT id FROM ai_messages WHERE session_id = ? ORDER BY created_at DESC, id DESC LIMIT ?
		)`, sessionID, sessionID, keep)
}

func updateAISessionStatus(sessionID int, status, message string) {
	_, _ = database.GetDB().Exec(`UPDATE ai_sessions SET status = ?, error_message = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, message, sessionID)
}

func failAISession(sessionID int, message string, promptChars, responseChars int) {
	_, _ = database.GetDB().Exec(`UPDATE ai_sessions
		SET status = ?, error_message = ?, prompt_chars = ?, response_chars = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, models.AISessionFailed, message, promptChars, responseChars, sessionID)
}

func completeAISession(sessionID int, riskLevel, summary, reportJSON, rawText string, promptChars, responseChars int) error {
	_, err := database.GetDB().Exec(`UPDATE ai_sessions
		SET status = ?, risk_level = ?, summary = ?, report_json = ?, raw_text = ?, prompt_chars = ?, response_chars = ?, error_message = '', updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, models.AISessionCompleted, riskLevel, summary, reportJSON, rawText, promptChars, responseChars, sessionID)
	return err
}

func pruneAISessions(siteID, keep int) {
	_, _ = database.GetDB().Exec(`DELETE FROM ai_sessions
		WHERE site_id = ? AND id NOT IN (
			SELECT id FROM ai_sessions WHERE site_id = ? ORDER BY created_at DESC LIMIT ?
		)`, siteID, siteID, keep)
}

func maskAIKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func excerpt(s string, max int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

func aiUserError(lang string, err error) string {
	var providerErr *executor.AIProviderError
	if errors.As(err, &providerErr) {
		switch providerErr.Type {
		case "unauthorized":
			return i18n.T(lang, "ai_diagnostics.user_error_unauthorized")
		case "rate_limited":
			return i18n.T(lang, "ai_diagnostics.user_error_rate_limited")
		case "timeout":
			return i18n.T(lang, "ai_diagnostics.user_error_timeout")
		case "network_error":
			return i18n.T(lang, "ai_diagnostics.user_error_network")
		case "bad_response":
			if strings.TrimSpace(providerErr.Message) != "" {
				return providerErr.Message
			}
			return i18n.T(lang, "ai_diagnostics.user_error_bad_response")
		case "empty_response":
			return i18n.T(lang, "ai_diagnostics.user_error_empty_response")
		}
	}
	if err == nil {
		return ""
	}
	return err.Error()
}
