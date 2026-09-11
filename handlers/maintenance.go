package handlers

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
)

type MaintenanceHandler struct{ Manager *executor.MaintenanceManager }

func (h *MaintenanceHandler) Password(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.Status(400)
		return
	}
	password, err := h.manager().RevealPassword(id)
	if maintenanceResult(c, err) {
		c.JSON(200, gin.H{"success": true, "data": gin.H{"password": password}})
	}
}

func (h *MaintenanceHandler) manager() *executor.MaintenanceManager {
	if h.Manager != nil {
		return h.Manager
	}
	return executor.DefaultMaintenanceManager()
}

func maintenanceJSON(c *gin.Context, value any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if d.Decode(value) != nil {
		c.JSON(400, gin.H{"success": false, "message": "invalid_request"})
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		c.JSON(400, gin.H{"success": false, "message": "invalid_request"})
		return false
	}
	return true
}

func maintenanceResult(c *gin.Context, err error) bool {
	if err == nil {
		return true
	}
	code := "state_unknown"
	if errors.Is(err, executor.ErrMaintenanceValidation) {
		code = "verification_failed"
	}
	if errors.Is(err, executor.ErrMaintenanceFrozen) {
		code = "verification_frozen"
	}
	if errors.Is(err, executor.ErrMaintenancePasswordRequired) {
		code = "password_required"
	}
	if errors.Is(err, executor.ErrMaintenanceBusy) {
		code = "operation_unavailable"
	}
	if errors.Is(err, executor.ErrMaintenanceLockMode) {
		code = "lock_mode_required"
	}
	c.JSON(http.StatusConflict, gin.H{"success": false, "message": code})
	return false
}

func (h *MaintenanceHandler) Panel(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.Status(400)
		return
	}
	if c.Request.Method == http.MethodPut {
		var req struct {
			Enabled  bool   `json:"enabled"`
			Minutes  int    `json:"minutes"`
			Password string `json:"password"`
		}
		if !maintenanceJSON(c, &req) {
			return
		}
		err = h.manager().Configure(id, req.Enabled, req.Minutes, req.Password)
		if !maintenanceResult(c, err) {
			return
		}
	}
	if c.Request.Method == http.MethodPost {
		var req struct {
			WindowID string `json:"window_id"`
		}
		if !maintenanceJSON(c, &req) || !maintenanceResult(c, h.manager().Relock(id, req.WindowID)) {
			return
		}
	}
	state, err := h.manager().Status(id)
	if maintenanceResult(c, err) {
		c.JSON(200, gin.H{"success": true, "data": state})
	}
}

// Resolve by the authenticated key itself: neither domain nor site_id is a
// caller-controlled selector. Keys are unique identities, not WP user proofs.
func maintenancePluginSite(c *gin.Context) (int, bool) {
	if !pluginRequestHostAllowed(c) {
		c.JSON(401, gin.H{"success": false, "message": "verification_failed"})
		return 0, false
	}
	key := c.GetHeader("X-WP-Panel-Key")
	if len(key) < 32 || len(key) > 256 {
		c.JSON(401, gin.H{"success": false, "message": "verification_failed"})
		return 0, false
	}
	var id, n int
	var stored string
	err := database.GetDB().QueryRow(`SELECT MIN(id),COUNT(*),COALESCE(MIN(plugin_api_key),'') FROM websites WHERE plugin_api_key=? AND site_type='wordpress'`, key).Scan(&id, &n, &stored)
	if err != nil || n != 1 || subtle.ConstantTimeCompare([]byte(stored), []byte(key)) != 1 {
		c.JSON(401, gin.H{"success": false, "message": "verification_failed"})
		return 0, false
	}
	return id, true
}

func (h *MaintenanceHandler) Plugin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := maintenancePluginSite(c)
	if !ok {
		return
	}
	if c.Request.Method != http.MethodGet {
		var req struct {
			WindowID  string `json:"window_id"`
			RequestID string `json:"request_id"`
			Revision  int    `json:"revision"`
			Minutes   int    `json:"minutes"`
			Password  string `json:"password"`
			Actor     string `json:"actor"`
		}
		if !maintenanceJSON(c, &req) {
			return
		}
		if len(req.Password) > 72 || len(req.Actor) > 128 {
			maintenanceResult(c, executor.ErrMaintenanceValidation)
			return
		}
		input := executor.MaintenanceRequest{WindowID: req.WindowID, RequestID: req.RequestID, Revision: req.Revision, Minutes: req.Minutes, Password: req.Password, Actor: req.Actor}
		var err error
		switch c.Param("action") {
		case "unlock":
			err = h.manager().Unlock(id, input)
		case "extend":
			err = h.manager().Extend(id, input)
		case "relock":
			err = h.manager().Relock(id, req.WindowID)
		default:
			c.Status(404)
			return
		}
		if !maintenanceResult(c, err) {
			return
		}
	}
	state, err := h.manager().Status(id)
	if maintenanceResult(c, err) {
		c.JSON(200, gin.H{"success": true, "data": state})
	}
}
