package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"
)

const logAnalysisKeepPerSite = 20

type LogAnalysisHandler struct{}

func (h *LogAnalysisHandler) Start(c *gin.Context) {
	var req models.LogAnalysisRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.SiteID <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return
	}
	now := time.Now()
	if req.EndAt.IsZero() {
		req.EndAt = now
	}
	if req.StartAt.IsZero() || !req.StartAt.Before(req.EndAt) || req.EndAt.Sub(req.StartAt) > 7*24*time.Hour || req.EndAt.After(now.Add(5*time.Minute)) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "log_analysis.invalid_range")))
		return
	}
	site := getWebsiteByID(req.SiteID)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "website.not_found")))
		return
	}

	var settings *models.AISettings
	if req.UseAI {
		var err error
		settings, err = loadAISettings()
		if err != nil || !settings.Enabled || strings.TrimSpace(settings.APIKey) == "" {
			c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "log_analysis.ai_not_ready")))
			return
		}
	}

	db := database.GetDB()
	_, _ = db.Exec(`UPDATE log_analysis_jobs SET status=?, error_message=?, updated_at=CURRENT_TIMESTAMP
		WHERE status=? AND updated_at <= datetime('now','-30 minutes')`, models.LogAnalysisFailed, "analysis interrupted", models.LogAnalysisRunning)
	var runningID int
	if err := db.QueryRow(`SELECT id FROM log_analysis_jobs WHERE site_id=? AND status IN (?,?) ORDER BY id DESC LIMIT 1`,
		site.ID, models.LogAnalysisPending, models.LogAnalysisRunning).Scan(&runningID); err == nil {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"id": runningID, "status": models.LogAnalysisRunning}))
		return
	}

	result, err := db.Exec(`INSERT INTO log_analysis_jobs(site_id,status,start_at,end_at,use_ai) VALUES(?,?,?,?,?)`,
		site.ID, models.LogAnalysisPending, req.StartAt, req.EndAt, req.UseAI)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "log_analysis.create_failed")))
		return
	}
	jobID, _ := result.LastInsertId()
	siteCopy := *site
	lang := i18n.LangFromRequest(c.Request)
	executor.GoSafe(func() {
		runLogAnalysis(int(jobID), &siteCopy, req.StartAt, req.EndAt, settings, lang)
	})
	c.JSON(http.StatusAccepted, models.SuccessResponse(gin.H{"id": jobID, "status": models.LogAnalysisPending}))
}

func (h *LogAnalysisHandler) Get(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return
	}
	job, err := loadLogAnalysisJob(id)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "log_analysis.not_found")))
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "log_analysis.load_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(job))
}

func (h *LogAnalysisHandler) Details(c *gin.Context) {
	job, site, ok := loadLogAnalysisDetailContext(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	detail, err := executor.AnalyzeWebsiteLogDetails(site, job.StartAt, job.EndAt, database.GetDB(), c.Query("kind"), c.Query("value"), page, pageSize)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "log_analysis.invalid_detail")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(detail))
}

func (h *LogAnalysisHandler) AnalyzeDetails(c *gin.Context) {
	job, site, ok := loadLogAnalysisDetailContext(c)
	if !ok {
		return
	}
	var req models.LogAnalysisDetailAIRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return
	}
	settings, err := loadAISettings()
	if err != nil || !settings.Enabled || strings.TrimSpace(settings.APIKey) == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "log_analysis.ai_not_ready")))
		return
	}
	detail, err := executor.AnalyzeWebsiteLogDetails(site, job.StartAt, job.EndAt, database.GetDB(), req.Kind, req.Value, 1, 100)
	if err != nil || detail.Total == 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "log_analysis.invalid_detail")))
		return
	}
	systemPrompt, userPrompt, err := executor.BuildLogAnalysisDetailPrompt(detail)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "log_analysis.invalid_detail")))
		return
	}
	timeout := time.Duration(settings.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()
	analysis, _, err := executor.CallAIChat(ctx, settings, systemPrompt, userPrompt)
	if err != nil {
		c.JSON(http.StatusBadGateway, models.ErrorResponse(i18n.TE(c.Request, "log_analysis.detail_ai_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"analysis": analysis}))
}

func loadLogAnalysisDetailContext(c *gin.Context) (*models.LogAnalysisJob, *models.Website, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return nil, nil, false
	}
	job, err := loadLogAnalysisJob(id)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "log_analysis.not_found")))
		return nil, nil, false
	}
	if err != nil || job.Status != models.LogAnalysisCompleted {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "log_analysis.load_failed")))
		return nil, nil, false
	}
	site := getWebsiteByID(job.SiteID)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "website.not_found")))
		return nil, nil, false
	}
	return job, site, true
}

func (h *LogAnalysisHandler) List(c *gin.Context) {
	siteID, err := strconv.Atoi(c.Query("site_id"))
	if err != nil || siteID <= 0 || getWebsiteByID(siteID) == nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return
	}
	rows, err := database.GetDB().Query(`SELECT j.id,j.site_id,w.domain,j.status,j.start_at,j.end_at,j.use_ai,
		j.local_report_json,j.ai_analysis,j.error_message,j.created_at,j.updated_at
		FROM log_analysis_jobs j JOIN websites w ON w.id=j.site_id WHERE j.site_id=? ORDER BY j.id DESC LIMIT 10`, siteID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "log_analysis.load_failed")))
		return
	}
	defer rows.Close()
	jobs := []models.LogAnalysisJob{}
	for rows.Next() {
		job, scanErr := scanLogAnalysisJob(rows.Scan)
		if scanErr == nil {
			jobs = append(jobs, *job)
		}
	}
	c.JSON(http.StatusOK, models.SuccessResponse(jobs))
}

func runLogAnalysis(jobID int, site *models.Website, startAt, endAt time.Time, settings *models.AISettings, lang string) {
	db := database.GetDB()
	_, _ = db.Exec(`UPDATE log_analysis_jobs SET status=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, models.LogAnalysisRunning, jobID)
	report, err := executor.AnalyzeWebsiteLogs(site, startAt, endAt, db, lang)
	if err != nil {
		_, _ = db.Exec(`UPDATE log_analysis_jobs SET status=?,error_message=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, models.LogAnalysisFailed, err.Error(), jobID)
		return
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		_, _ = db.Exec(`UPDATE log_analysis_jobs SET status=?,error_message=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, models.LogAnalysisFailed, err.Error(), jobID)
		return
	}
	aiAnalysis, aiError := "", ""
	if settings != nil {
		systemPrompt, userPrompt, promptErr := executor.BuildLogAnalysisPrompt(report)
		if promptErr != nil {
			aiError = promptErr.Error()
		} else {
			timeout := time.Duration(settings.TimeoutSeconds) * time.Second
			if timeout <= 0 {
				timeout = 60 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			aiAnalysis, _, err = executor.CallAIChat(ctx, settings, systemPrompt, userPrompt)
			cancel()
			if err != nil {
				aiError = fmt.Sprintf("AI analysis failed: %v", err)
			}
		}
	}
	_, _ = db.Exec(`UPDATE log_analysis_jobs SET status=?,local_report_json=?,ai_analysis=?,error_message=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		models.LogAnalysisCompleted, string(reportJSON), aiAnalysis, aiError, jobID)
	_, _ = db.Exec(`DELETE FROM log_analysis_jobs WHERE site_id=? AND id NOT IN (
		SELECT id FROM log_analysis_jobs WHERE site_id=? ORDER BY id DESC LIMIT ?)`, site.ID, site.ID, logAnalysisKeepPerSite)
}

func loadLogAnalysisJob(id int) (*models.LogAnalysisJob, error) {
	row := database.GetDB().QueryRow(`SELECT j.id,j.site_id,w.domain,j.status,j.start_at,j.end_at,j.use_ai,
		j.local_report_json,j.ai_analysis,j.error_message,j.created_at,j.updated_at
		FROM log_analysis_jobs j JOIN websites w ON w.id=j.site_id WHERE j.id=?`, id)
	return scanLogAnalysisJob(row.Scan)
}

func scanLogAnalysisJob(scan func(...interface{}) error) (*models.LogAnalysisJob, error) {
	job := &models.LogAnalysisJob{}
	var reportJSON string
	if err := scan(&job.ID, &job.SiteID, &job.Domain, &job.Status, &job.StartAt, &job.EndAt, &job.UseAI,
		&reportJSON, &job.AIAnalysis, &job.ErrorMessage, &job.CreatedAt, &job.UpdatedAt); err != nil {
		return nil, err
	}
	if reportJSON != "" {
		var report models.LogAnalysisReport
		if json.Unmarshal([]byte(reportJSON), &report) == nil {
			job.LocalReport = &report
		}
	}
	return job, nil
}
