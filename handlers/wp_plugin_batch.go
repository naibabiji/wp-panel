package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"
)

type WPPluginBatchHandler struct{ Service wpPluginBatchService }

type wpPluginBatchService interface {
	Create(ctx context.Context, siteID int, username string, componentKeys []string) (models.WPPluginBatch, error)
	Get(ctx context.Context, siteID int, batchID string) (models.WPPluginBatch, error)
	ListForSite(ctx context.Context, siteID int) ([]models.WPPluginBatch, error)
	Rollback(ctx context.Context, siteID int, taskID string) error
	Ignore(ctx context.Context, siteID int, taskID string) error
}

const wpPluginBatchMaxComponents = 50

func (h *WPPluginBatchHandler) Create(c *gin.Context) {
	siteID, username, ok := wpPluginUpdateIdentity(c)
	if !ok {
		return
	}
	if h == nil || h.Service == nil {
		wpPluginUpdateError(c, executor.ErrWPPluginUpdateUnavailable)
		return
	}
	var req struct {
		ComponentKeys []string `json:"component_keys"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		len(req.ComponentKeys) == 0 || len(req.ComponentKeys) > wpPluginBatchMaxComponents {
		wpPluginUpdateError(c, executor.ErrWPPluginUpdateInvalid)
		return
	}
	for i, key := range req.ComponentKeys {
		req.ComponentKeys[i] = strings.TrimSpace(key)
	}
	batch, err := h.Service.Create(c.Request.Context(), siteID, username, req.ComponentKeys)
	if err != nil {
		wpPluginUpdateError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, models.SuccessResponse(batch))
}

func (h *WPPluginBatchHandler) Get(c *gin.Context) {
	siteID, _, ok := wpPluginUpdateIdentity(c)
	if !ok {
		return
	}
	if h == nil || h.Service == nil {
		wpPluginUpdateError(c, executor.ErrWPPluginUpdateUnavailable)
		return
	}
	batch, err := h.Service.Get(c.Request.Context(), siteID, c.Param("batch_id"))
	if err != nil {
		wpPluginUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(batch))
}

func (h *WPPluginBatchHandler) List(c *gin.Context) {
	siteID, _, ok := wpPluginUpdateIdentity(c)
	if !ok {
		return
	}
	if h == nil || h.Service == nil {
		wpPluginUpdateError(c, executor.ErrWPPluginUpdateUnavailable)
		return
	}
	batches, err := h.Service.ListForSite(c.Request.Context(), siteID)
	if err != nil {
		wpPluginUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(batches))
}

func (h *WPPluginBatchHandler) Rollback(c *gin.Context) {
	siteID, _, ok := wpPluginUpdateIdentity(c)
	if !ok {
		return
	}
	if h == nil || h.Service == nil {
		wpPluginUpdateError(c, executor.ErrWPPluginUpdateUnavailable)
		return
	}
	if err := h.Service.Rollback(c.Request.Context(), siteID, c.Param("task_id")); err != nil {
		wpPluginUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"ok": true}))
}

func (h *WPPluginBatchHandler) Ignore(c *gin.Context) {
	siteID, _, ok := wpPluginUpdateIdentity(c)
	if !ok {
		return
	}
	if h == nil || h.Service == nil {
		wpPluginUpdateError(c, executor.ErrWPPluginUpdateUnavailable)
		return
	}
	if err := h.Service.Ignore(c.Request.Context(), siteID, c.Param("task_id")); err != nil {
		wpPluginUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"ok": true}))
}
