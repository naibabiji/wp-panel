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

type WPCoreUpdateHandler struct{ Service wpCoreUpdateService }

type wpCoreUpdateService interface {
	Preview(context.Context, int, string) (models.WPCoreUpdatePreview, error)
	Confirm(context.Context, int, string, string, string) (models.WPCoreUpdateTask, error)
	Task(context.Context, int, string) (models.WPCoreUpdateTask, error)
}

func (h *WPCoreUpdateHandler) Preview(c *gin.Context) {
	siteID, username, ok := wpCoreUpdateIdentity(c)
	if !ok {
		return
	}
	if h == nil || h.Service == nil {
		wpCoreUpdateError(c, executor.ErrWPCoreUpdateUnavailable)
		return
	}
	preview, err := h.Service.Preview(c.Request.Context(), siteID, username)
	if err != nil {
		wpCoreUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(preview))
}

func (h *WPCoreUpdateHandler) Confirm(c *gin.Context) {
	siteID, username, ok := wpCoreUpdateIdentity(c)
	if !ok {
		return
	}
	if h == nil || h.Service == nil {
		wpCoreUpdateError(c, executor.ErrWPCoreUpdateUnavailable)
		return
	}
	var req struct {
		ConfirmationToken string `json:"confirmation_token"`
		TargetVersion     string `json:"target_version"`
		Confirm           bool   `json:"confirm"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !req.Confirm || strings.TrimSpace(req.ConfirmationToken) == "" || strings.TrimSpace(req.TargetVersion) == "" {
		wpCoreUpdateError(c, executor.ErrWPCoreUpdateInvalid)
		return
	}
	task, err := h.Service.Confirm(c.Request.Context(), siteID, username, req.ConfirmationToken, req.TargetVersion)
	if err != nil {
		wpCoreUpdateError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, models.SuccessResponse(task))
}

func (h *WPCoreUpdateHandler) Task(c *gin.Context) {
	siteID, _, ok := wpCoreUpdateIdentity(c)
	if !ok {
		return
	}
	if h == nil || h.Service == nil {
		wpCoreUpdateError(c, executor.ErrWPCoreUpdateUnavailable)
		return
	}
	task, err := h.Service.Task(c.Request.Context(), siteID, c.Param("task_id"))
	if err != nil {
		wpCoreUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(task))
}

func wpCoreUpdateIdentity(c *gin.Context) (int, string, bool) {
	siteID, err := strconv.Atoi(c.Param("id"))
	username, _ := c.Get("session_username")
	user, _ := username.(string)
	if err != nil || siteID <= 0 || user == "" {
		wpCoreUpdateError(c, executor.ErrWPCoreUpdateInvalid)
		return 0, "", false
	}
	return siteID, user, true
}

func wpCoreUpdateError(c *gin.Context, err error) {
	status, key := http.StatusInternalServerError, "wp_core_update.internal_error"
	switch {
	case errors.Is(err, executor.ErrWPCoreUpdateInvalid):
		status, key = http.StatusBadRequest, "wp_core_update.invalid_request"
	case errors.Is(err, executor.ErrWPCoreUpdateNotFound):
		status, key = http.StatusNotFound, "wp_core_update.not_found"
	case errors.Is(err, executor.ErrWPCoreUpdateConflict):
		status, key = http.StatusConflict, "wp_core_update.conflict"
	case errors.Is(err, executor.ErrWPCoreUpdateBusy):
		status, key = http.StatusTooManyRequests, "wp_core_update.busy"
	case errors.Is(err, executor.ErrWPCoreUpdateUnavailable):
		status, key = http.StatusServiceUnavailable, "wp_core_update.unavailable"
	}
	c.JSON(status, models.ErrorResponse(i18n.TE(c.Request, key)))
}
