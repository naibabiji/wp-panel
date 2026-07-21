package handlers

import (
	"database/sql"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"
)

type WPFleetOverviewHandler struct {
	DB *sql.DB
}

func (h *WPFleetOverviewHandler) Overview(c *gin.Context) {
	service, err := executor.NewWPFleetOverviewService(h.DB)
	if err != nil {
		log.Printf("读取 WordPress 站群概览失败: %v", err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "wp_fleet.overview_failed")))
		return
	}
	overview, err := service.Overview(c.Request.Context())
	if err != nil {
		log.Printf("读取 WordPress 站群概览失败: %v", err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "wp_fleet.overview_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(overview))
}
