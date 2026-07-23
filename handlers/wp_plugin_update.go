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

type WPPluginUpdateHandler struct{ Service wpPluginUpdateService }

type wpPluginUpdateService interface {
	Preview(context.Context, int, string, string) (models.WPPluginUpdatePreview, error)
	Confirm(context.Context, int, string, string, string, string, string) (models.WPPluginUpdateTask, error)
	Task(context.Context, int, string) (models.WPPluginUpdateTask, error)
	LatestTask(context.Context, int, string) (models.WPPluginUpdateTask, error)
}

func (h *WPPluginUpdateHandler) Preview(c *gin.Context) {
	siteID, username, ok := wpPluginUpdateIdentity(c)
	if !ok {
		return
	}
	componentKey := strings.TrimSpace(c.Query("component_key"))
	if h == nil || h.Service == nil {
		wpPluginUpdateError(c, executor.ErrWPPluginUpdateUnavailable)
		return
	}
	preview, err := h.Service.Preview(c.Request.Context(), siteID, username, componentKey)
	if err != nil {
		wpPluginUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(preview))
}

func (h *WPPluginUpdateHandler) Confirm(c *gin.Context) {
	siteID, username, ok := wpPluginUpdateIdentity(c)
	if !ok {
		return
	}
	if h == nil || h.Service == nil {
		wpPluginUpdateError(c, executor.ErrWPPluginUpdateUnavailable)
		return
	}
	var req struct {
		ComponentKey       string `json:"component_key"`
		ConfirmationToken  string `json:"confirmation_token"`
		TargetVersion      string `json:"target_version"`
		Confirm            bool   `json:"confirm"`
		DatabaseBackupMode string `json:"database_backup_mode"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !req.Confirm ||
		strings.TrimSpace(req.ComponentKey) == "" || strings.TrimSpace(req.ConfirmationToken) == "" ||
		strings.TrimSpace(req.TargetVersion) == "" {
		wpPluginUpdateError(c, executor.ErrWPPluginUpdateInvalid)
		return
	}
	task, err := h.Service.Confirm(c.Request.Context(), siteID, username, req.ComponentKey, req.ConfirmationToken, req.TargetVersion, req.DatabaseBackupMode)
	if err != nil {
		wpPluginUpdateError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, models.SuccessResponse(task))
}

func (h *WPPluginUpdateHandler) Task(c *gin.Context) {
	siteID, _, ok := wpPluginUpdateIdentity(c)
	if !ok {
		return
	}
	if h == nil || h.Service == nil {
		wpPluginUpdateError(c, executor.ErrWPPluginUpdateUnavailable)
		return
	}
	task, err := h.Service.Task(c.Request.Context(), siteID, c.Param("task_id"))
	if err != nil {
		wpPluginUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(task))
}

func (h *WPPluginUpdateHandler) LatestTask(c *gin.Context) {
	siteID, _, ok := wpPluginUpdateIdentity(c)
	if !ok {
		return
	}
	if h == nil || h.Service == nil {
		wpPluginUpdateError(c, executor.ErrWPPluginUpdateUnavailable)
		return
	}
	task, err := h.Service.LatestTask(c.Request.Context(), siteID, strings.TrimSpace(c.Query("component_key")))
	if err != nil {
		wpPluginUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(task))
}

func wpPluginUpdateIdentity(c *gin.Context) (int, string, bool) {
	siteID, err := strconv.Atoi(c.Param("id"))
	username, _ := c.Get("session_username")
	user, _ := username.(string)
	if err != nil || siteID <= 0 || user == "" {
		wpPluginUpdateError(c, executor.ErrWPPluginUpdateInvalid)
		return 0, "", false
	}
	return siteID, user, true
}

func wpPluginUpdateError(c *gin.Context, err error) {
	status, key := http.StatusInternalServerError, "wp_plugin_update.internal_error"
	switch {
	case errors.Is(err, executor.ErrWPPluginUpdateInvalid):
		status, key = http.StatusBadRequest, "wp_plugin_update.invalid_request"
	case errors.Is(err, executor.ErrWPPluginUpdateNotFound):
		status, key = http.StatusNotFound, "wp_plugin_update.not_found"
	case errors.Is(err, executor.ErrWPPluginUpdateConflict):
		status, key = http.StatusConflict, "wp_plugin_update.conflict"
	case errors.Is(err, executor.ErrWPPluginUpdateSiteBusy):
		status, key = http.StatusConflict, "wp_plugin_update.site_busy_restore"
	case errors.Is(err, executor.ErrWPPluginUpdateBusy):
		status, key = http.StatusTooManyRequests, "wp_plugin_update.busy"
	case errors.Is(err, executor.ErrWPPluginUpdateNotInRepository):
		status, key = http.StatusConflict, "wp_plugin_update.not_in_repository"
	case errors.Is(err, executor.ErrWPPluginUpdateUnavailable):
		status, key = http.StatusServiceUnavailable, "wp_plugin_update.unavailable"
	}
	c.JSON(status, models.ErrorResponse(i18n.TE(c.Request, key)))
}
