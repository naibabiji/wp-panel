package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"
)

type WPInventoryHandler struct {
	DB *sql.DB
}

func (h *WPInventoryHandler) Summary(c *gin.Context) {
	siteID, ok := wpInventorySiteID(c)
	if !ok {
		return
	}
	service, err := executor.NewWPInventoryService(h.DB)
	if err != nil {
		wpInventoryError(c, err, "wp_inventory.query_failed")
		return
	}
	summary, err := service.Summary(c.Request.Context(), siteID)
	if err != nil {
		wpInventoryError(c, err, "wp_inventory.query_failed")
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(summary))
}

func (h *WPInventoryHandler) Refresh(c *gin.Context) {
	siteID, ok := wpInventorySiteID(c)
	if !ok {
		return
	}
	service, err := executor.NewWPInventoryService(h.DB)
	if err != nil {
		wpInventoryError(c, err, "wp_inventory.refresh_failed")
		return
	}
	result, err := service.Refresh(c.Request.Context(), siteID, time.Now().UTC())
	if err != nil {
		wpInventoryError(c, err, "wp_inventory.refresh_failed")
		return
	}
	c.JSON(http.StatusAccepted, models.SuccessResponse(result))
}

func (h *WPInventoryHandler) Task(c *gin.Context) {
	siteID, ok := wpInventorySiteID(c)
	if !ok {
		return
	}
	service, err := executor.NewWPInventoryService(h.DB)
	if err != nil {
		wpInventoryError(c, err, "wp_inventory.query_failed")
		return
	}
	task, err := service.Task(c.Request.Context(), siteID, c.Param("task_id"))
	if err != nil {
		wpInventoryError(c, err, "wp_inventory.query_failed")
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(task))
}

func (h *WPInventoryHandler) Components(c *gin.Context) {
	siteID, ok := wpInventorySiteID(c)
	if !ok {
		return
	}
	options, ok := wpInventoryListOptions(c)
	if !ok {
		return
	}
	service, err := executor.NewWPInventoryService(h.DB)
	if err != nil {
		wpInventoryError(c, err, "wp_inventory.query_failed")
		return
	}
	result, err := service.Components(c.Request.Context(), siteID, options)
	if err != nil {
		wpInventoryError(c, err, "wp_inventory.query_failed")
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(result))
}

func (h *WPInventoryHandler) Updates(c *gin.Context) {
	siteID, ok := wpInventorySiteID(c)
	if !ok {
		return
	}
	options, ok := wpInventoryListOptions(c)
	if !ok {
		return
	}
	service, err := executor.NewWPInventoryService(h.DB)
	if err != nil {
		wpInventoryError(c, err, "wp_inventory.query_failed")
		return
	}
	result, err := service.Updates(c.Request.Context(), siteID, options)
	if err != nil {
		wpInventoryError(c, err, "wp_inventory.query_failed")
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(result))
}

func wpInventorySiteID(c *gin.Context) (int, bool) {
	siteID, err := strconv.Atoi(c.Param("id"))
	if err != nil || siteID <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "wp_inventory.invalid_request")))
		return 0, false
	}
	return siteID, true
}

func wpInventoryListOptions(c *gin.Context) (executor.WPInventoryListOptions, bool) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil {
		wpInventoryError(c, executor.ErrWPInventoryInvalidRequest, "wp_inventory.query_failed")
		return executor.WPInventoryListOptions{}, false
	}
	pageSize, err := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if err != nil {
		wpInventoryError(c, executor.ErrWPInventoryInvalidRequest, "wp_inventory.query_failed")
		return executor.WPInventoryListOptions{}, false
	}
	return executor.WPInventoryListOptions{
		Page: page, PageSize: pageSize, Type: c.Query("type"), Search: c.Query("search"),
	}, true
}

func wpInventoryError(c *gin.Context, err error, fallbackKey string) {
	status := http.StatusInternalServerError
	key := fallbackKey
	switch {
	case errors.Is(err, executor.ErrWPInventoryInvalidRequest):
		status, key = http.StatusBadRequest, "wp_inventory.invalid_request"
	case errors.Is(err, executor.ErrWPInventorySiteNotFound):
		status, key = http.StatusNotFound, "website.not_found"
	case errors.Is(err, executor.ErrWPInventoryTaskNotFound):
		status, key = http.StatusNotFound, "wp_inventory.task_not_found"
	case errors.Is(err, executor.ErrWPInventoryUnsupportedSite):
		status, key = http.StatusConflict, "wp_inventory.wordpress_only"
	case errors.Is(err, executor.ErrWPInventorySiteUnavailable):
		status, key = http.StatusConflict, "wp_inventory.site_unavailable"
	}
	c.JSON(status, models.ErrorResponse(i18n.TE(c.Request, key)))
}
