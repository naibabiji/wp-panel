package handlers

import (
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"
)

type AdminerHandler struct{}

type enableAdminerRequest struct {
	DurationMinutes int    `json:"duration_minutes"`
	Password        string `json:"password"`
}

func (h *AdminerHandler) Status(c *gin.Context) {
	siteID, err := strconv.Atoi(c.Param("id"))
	if err != nil || siteID <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "database.adminer_invalid_site")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(executor.GlobalAdminer.Status(siteID)))
}

func (h *AdminerHandler) Enable(c *gin.Context) {
	siteID, err := strconv.Atoi(c.Param("id"))
	if err != nil || siteID <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "database.adminer_invalid_site")))
		return
	}
	var req enableAdminerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.request_failed")))
		return
	}
	if req.DurationMinutes != 0 && req.DurationMinutes != 15 && req.DurationMinutes != 30 && req.DurationMinutes != 60 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "database.adminer_invalid_duration")))
		return
	}

	site, err := loadAdminerWebsite(siteID)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "database.adminer_invalid_site")))
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "database.adminer_enable_failed")))
		return
	}
	password := req.Password
	if site.SiteType != "wordpress" && password == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "database.adminer_password_required")))
		return
	}
	status, err := executor.GlobalAdminer.Enable(site, password, time.Duration(req.DurationMinutes)*time.Minute, config.AppConfig)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "database.adminer_enable_failed_with_reason", i18n.P{"error": err.Error()})))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(status))
}

func (h *AdminerHandler) Disable(c *gin.Context) {
	siteID, err := strconv.Atoi(c.Param("id"))
	if err != nil || siteID <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "database.adminer_invalid_site")))
		return
	}
	executor.GlobalAdminer.Disable(siteID)
	c.JSON(http.StatusOK, models.SuccessResponse(executor.GlobalAdminer.Status(siteID)))
}

func (h *AdminerHandler) Proxy(c *gin.Context) {
	siteID, err := strconv.Atoi(c.Param("id"))
	if err != nil || siteID <= 0 {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	executor.GlobalAdminer.ServeHTTP(siteID, c.Writer, c.Request)
	c.Abort()
}

func loadAdminerWebsite(siteID int) (*models.Website, error) {
	site := &models.Website{}
	err := database.GetDB().QueryRow(`SELECT id, domain, site_type, web_root, db_name, db_user FROM websites WHERE id = ?`, siteID).Scan(
		&site.ID, &site.Domain, &site.SiteType, &site.WebRoot, &site.DBName, &site.DBUser,
	)
	return site, err
}
