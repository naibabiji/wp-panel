package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"
)

type WPThemeUpdateHandler struct{ Service wpThemeUpdateService }

type wpThemeUpdateService interface {
	Preview(context.Context, int, string, string) (models.WPThemeUpdatePreview, error)
	Confirm(context.Context, int, string, string, string, string, string, string) (models.WPThemeUpdateTask, error)
	Task(context.Context, int, string) (models.WPThemeUpdateTask, error)
	LatestTask(context.Context, int, string) (models.WPThemeUpdateTask, error)
}

func (h *WPThemeUpdateHandler) Preview(c *gin.Context) {
	siteID, username, ok := wpThemeUpdateIdentity(c)
	if !ok {
		return
	}
	if h == nil || h.Service == nil {
		wpThemeUpdateError(c, executor.ErrWPThemeUpdateUnavailable)
		return
	}
	preview, err := h.Service.Preview(c.Request.Context(), siteID, username, strings.TrimSpace(c.Query("component_key")))
	if err != nil {
		wpThemeUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(preview))
}

func (h *WPThemeUpdateHandler) Confirm(c *gin.Context) {
	siteID, username, ok := wpThemeUpdateIdentity(c)
	if !ok {
		return
	}
	if h == nil || h.Service == nil {
		wpThemeUpdateError(c, executor.ErrWPThemeUpdateUnavailable)
		return
	}
	var req struct {
		ComponentKey       string `json:"component_key"`
		ConfirmationToken  string `json:"confirmation_token"`
		RiskToken          string `json:"risk_token"`
		TargetVersion      string `json:"target_version"`
		Confirm            bool   `json:"confirm"`
		DatabaseBackupMode string `json:"database_backup_mode"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !req.Confirm ||
		strings.TrimSpace(req.ComponentKey) == "" || strings.TrimSpace(req.ConfirmationToken) == "" ||
		strings.TrimSpace(req.TargetVersion) == "" {
		wpThemeUpdateError(c, executor.ErrWPThemeUpdateInvalid)
		return
	}
	task, err := h.Service.Confirm(c.Request.Context(), siteID, username, req.ComponentKey, req.ConfirmationToken, req.RiskToken, req.TargetVersion, req.DatabaseBackupMode)
	if err != nil {
		wpThemeUpdateError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, models.SuccessResponse(task))
}

func (h *WPThemeUpdateHandler) Task(c *gin.Context) {
	siteID, _, ok := wpThemeUpdateIdentity(c)
	if !ok {
		return
	}
	if h == nil || h.Service == nil {
		wpThemeUpdateError(c, executor.ErrWPThemeUpdateUnavailable)
		return
	}
	task, err := h.Service.Task(c.Request.Context(), siteID, c.Param("task_id"))
	if err != nil {
		wpThemeUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(task))
}

func (h *WPThemeUpdateHandler) LatestTask(c *gin.Context) {
	siteID, _, ok := wpThemeUpdateIdentity(c)
	if !ok {
		return
	}
	if h == nil || h.Service == nil {
		wpThemeUpdateError(c, executor.ErrWPThemeUpdateUnavailable)
		return
	}
	task, err := h.Service.LatestTask(c.Request.Context(), siteID, strings.TrimSpace(c.Query("component_key")))
	if err != nil {
		wpThemeUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(task))
}

func wpThemeUpdateIdentity(c *gin.Context) (int, string, bool) {
	siteID, err := strconv.Atoi(c.Param("id"))
	username, _ := c.Get("session_username")
	user, _ := username.(string)
	if err != nil || siteID <= 0 || user == "" {
		wpThemeUpdateError(c, executor.ErrWPThemeUpdateInvalid)
		return 0, "", false
	}
	return siteID, user, true
}

func wpThemeUpdateError(c *gin.Context, err error) {
	status, key := http.StatusInternalServerError, "wp_theme_update.internal_error"
	switch {
	case errors.Is(err, executor.ErrWPThemeUpdateInvalid):
		status, key = http.StatusBadRequest, "wp_theme_update.invalid_request"
	case errors.Is(err, executor.ErrWPThemeUpdateNotFound):
		status, key = http.StatusNotFound, "wp_theme_update.not_found"
	case errors.Is(err, executor.ErrWPThemeUpdateConflict):
		status, key = http.StatusConflict, "wp_theme_update.conflict"
	case errors.Is(err, executor.ErrWPThemeUpdateSiteBusy):
		status, key = http.StatusConflict, "wp_theme_update.site_busy_restore"
	case errors.Is(err, executor.ErrWPThemeUpdateBusy):
		status, key = http.StatusTooManyRequests, "wp_theme_update.busy"
	case errors.Is(err, executor.ErrWPThemeUpdateNotInRepository):
		status, key = http.StatusConflict, "wp_theme_update.not_in_repository"
	case errors.Is(err, executor.ErrWPThemeUpdateLicenseInvalid):
		status, key = http.StatusConflict, "wp_theme_update.license_invalid"
	case errors.Is(err, executor.ErrWPThemeUpdateLicenseProtocolUnsupported):
		status, key = http.StatusConflict, "wp_theme_update.license_protocol_unsupported"
	case errors.Is(err, executor.ErrWPThemeUpdateUnavailable):
		status, key = http.StatusServiceUnavailable, "wp_theme_update.unavailable"
	}
	c.JSON(status, models.ErrorResponse(i18n.TE(c.Request, key)))
}
