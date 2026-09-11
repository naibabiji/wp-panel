package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"
)

type WPAnomalyHandler struct{ Monitor *executor.WPAnomalyMonitor }

func (h *WPAnomalyHandler) Handle(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(400, models.ErrorResponse("anomaly_invalid"))
		return
	}
	if c.Request.Method == http.MethodPut {
		var request struct {
			Enabled   bool `json:"enabled"`
			Threshold int  `json:"threshold"`
		}
		if !maintenanceJSON(c, &request) {
			return
		}
		err = h.Monitor.Configure(id, request.Enabled, request.Threshold)
		if err == nil && request.Enabled {
			_, err = h.Monitor.Check(c.Request.Context(), id)
		}
	} else if c.Request.Method == http.MethodPost {
		_, err = h.Monitor.Check(c.Request.Context(), id)
	}
	if err == nil {
		var state executor.WPAnomalyState
		state, err = h.Monitor.Status(id)
		if err == nil {
			c.JSON(200, models.SuccessResponse(state))
			return
		}
	}
	code, status := "sample_failed", http.StatusInternalServerError
	switch {
	case errors.Is(err, sql.ErrNoRows):
		code, status = "anomaly_invalid", 404
	case errors.Is(err, executor.ErrWPAnomalyInvalid):
		code, status = "anomaly_invalid", 400
	case errors.Is(err, executor.ErrWPAnomalyBusy):
		code, status = "anomaly_busy", 409
	case errors.Is(err, executor.ErrWPAnomalyDisabled):
		code, status = "anomaly_disabled", 409
	}
	c.JSON(status, models.ErrorResponse(code))
}
