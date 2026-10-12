package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
)

type ManagedMailHandler struct {
	Service *executor.ManagedMailPolicyService
	Sender  managedMailSender
}

type managedMailSender interface {
	Send(context.Context, int64, executor.ManagedMailMessage) (executor.ManagedMailSendResult, error)
}

type managedMailPluginPolicy struct {
	Authorized     bool   `json:"authorized"`
	SiteConfirmed  bool   `json:"site_confirmed"`
	Active         bool   `json:"active"`
	SMTPReady      bool   `json:"smtp_ready"`
	FromName       string `json:"from_name"`
	ReplyTo        string `json:"reply_to"`
	LockFromName   bool   `json:"lock_from_name"`
	MinuteLimit    int    `json:"minute_limit"`
	HourLimit      int    `json:"hour_limit"`
	DayLimit       int    `json:"day_limit"`
	ConflictPlugin string `json:"conflict_plugin"`
	PausedReason   string `json:"paused_reason"`
}

func (h *ManagedMailHandler) service() *executor.ManagedMailPolicyService {
	if h.Service != nil {
		return h.Service
	}
	return executor.NewManagedMailPolicyService(database.GetDB())
}

func (h *ManagedMailHandler) sender() managedMailSender {
	if h.Sender != nil {
		return h.Sender
	}
	return executor.NewManagedMailSender(h.service())
}

func managedMailMessageJSON(c *gin.Context, value any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024*1024)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid_request"})
		return false
	}
	return true
}

func managedMailError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	status := http.StatusInternalServerError
	code := "managed_mail_failed"
	switch {
	case errors.Is(err, executor.ErrManagedMailValidation):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, executor.ErrManagedMailUnavailable):
		status, code = http.StatusNotFound, "managed_mail_unavailable"
	case errors.Is(err, executor.ErrManagedMailUnauthorized):
		status, code = http.StatusConflict, "panel_authorization_required"
	case errors.Is(err, executor.ErrManagedMailUnconfirmed):
		status, code = http.StatusConflict, "site_confirmation_required"
	case errors.Is(err, executor.ErrManagedMailConflict):
		status, code = http.StatusConflict, "smtp_plugin_conflict"
	case errors.Is(err, executor.ErrManagedMailPaused):
		status, code = http.StatusConflict, "managed_mail_paused"
	case errors.Is(err, executor.ErrManagedMailQuota):
		status, code = http.StatusTooManyRequests, "managed_mail_quota_exceeded"
	case errors.Is(err, executor.ErrManagedMailFromNameLock):
		status, code = http.StatusConflict, "from_name_locked"
	}
	c.JSON(status, gin.H{"success": false, "message": code})
	return true
}

func managedMailPluginView(policy executor.ManagedMailSitePolicy) managedMailPluginPolicy {
	return managedMailPluginPolicy{
		Authorized: policy.Authorized, SiteConfirmed: policy.SiteConfirmed,
		Active: policy.Active, SMTPReady: policy.SMTPReady, FromName: policy.FromName,
		ReplyTo: policy.ReplyTo, LockFromName: policy.LockFromName,
		MinuteLimit: policy.MinuteLimit, HourLimit: policy.HourLimit, DayLimit: policy.DayLimit,
		ConflictPlugin: policy.ConflictPlugin, PausedReason: policy.PausedReason,
	}
}

// PanelPolicy lets the authenticated server administrator inspect or update a
// single site's authorization, sender presentation and conservative limits.
func (h *ManagedMailHandler) PanelPolicy(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	siteID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || siteID <= 0 {
		managedMailError(c, executor.ErrManagedMailValidation)
		return
	}
	if c.Request.Method == http.MethodPut {
		var req struct {
			Authorized   bool   `json:"authorized"`
			FromName     string `json:"from_name"`
			ReplyTo      string `json:"reply_to"`
			LockFromName bool   `json:"lock_from_name"`
			MinuteLimit  int    `json:"minute_limit"`
			HourLimit    int    `json:"hour_limit"`
			DayLimit     int    `json:"day_limit"`
		}
		if !maintenanceJSON(c, &req) {
			return
		}
		policy, saveErr := h.service().SaveSitePolicy(c.Request.Context(), executor.ManagedMailSitePolicy{
			SiteID: siteID, Authorized: req.Authorized, FromName: req.FromName,
			ReplyTo: req.ReplyTo, LockFromName: req.LockFromName,
			MinuteLimit: req.MinuteLimit, HourLimit: req.HourLimit, DayLimit: req.DayLimit,
		})
		if managedMailError(c, saveErr) {
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "data": policy})
		return
	}
	policy, getErr := h.service().GetSitePolicy(c.Request.Context(), siteID)
	if managedMailError(c, getErr) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": policy})
}

// PluginStatus derives the site identity only from the loopback companion key
// and deliberately omits the SMTP host, username, password and From Email.
func (h *ManagedMailHandler) PluginStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	siteID, ok := maintenancePluginSite(c)
	if !ok {
		return
	}
	policy, err := h.service().GetSitePolicy(c.Request.Context(), int64(siteID))
	if managedMailError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": managedMailPluginView(policy)})
}

func (h *ManagedMailHandler) PluginConfirm(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	siteID, ok := maintenancePluginSite(c)
	if !ok {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !maintenanceJSON(c, &req) {
		return
	}
	policy, err := h.service().ConfirmSite(c.Request.Context(), int64(siteID), req.Enabled)
	if managedMailError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": managedMailPluginView(policy)})
}

func (h *ManagedMailHandler) PluginSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	siteID, ok := maintenancePluginSite(c)
	if !ok {
		return
	}
	var req struct {
		FromName string `json:"from_name"`
		ReplyTo  string `json:"reply_to"`
	}
	if !maintenanceJSON(c, &req) {
		return
	}
	policy, err := h.service().UpdateSiteSender(c.Request.Context(), int64(siteID), req.FromName, req.ReplyTo)
	if managedMailError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": managedMailPluginView(policy)})
}

func (h *ManagedMailHandler) PluginConflict(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	siteID, ok := maintenancePluginSite(c)
	if !ok {
		return
	}
	var req struct {
		Plugin string `json:"plugin"`
	}
	if !maintenanceJSON(c, &req) {
		return
	}
	policy, err := h.service().ReportConflict(c.Request.Context(), int64(siteID), req.Plugin)
	if managedMailError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": managedMailPluginView(policy)})
}

func (h *ManagedMailHandler) PluginSend(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	siteID, ok := maintenancePluginSite(c)
	if !ok {
		return
	}
	var message executor.ManagedMailMessage
	if !managedMailMessageJSON(c, &message) {
		return
	}
	result, err := h.sender().Send(c.Request.Context(), int64(siteID), message)
	if managedMailError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}
