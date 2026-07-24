package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"
)

// passwordResetSiteLocks 按站点串行化密码找回保护修改，避免两个并发请求
// 交错写入 mu-plugin 文件与更新数据库导致"磁盘文件"与"数据库状态"不一致。
var passwordResetSiteLocks sync.Map // siteID(int) -> *sync.Mutex

func passwordResetSiteLock(id int) *sync.Mutex {
	v, _ := passwordResetSiteLocks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// SetPasswordResetMode 设置站点的密码找回保护模式（allow / all / admin）。
// 由面板以托管 mu-plugin 形式落地到 wp-content/mu-plugins，数据库仅记录当前模式。
func (h *WebsiteHandler) SetPasswordResetMode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.invalid_site_id")))
		return
	}
	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "website.not_found")))
		return
	}
	if site.SiteType != "wordpress" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.password_reset_wordpress_only")))
		return
	}
	if site.FileLockEnabled {
		c.JSON(http.StatusLocked, models.ErrorResponse(fileLockBlockedMessage))
		return
	}

	var req struct {
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return
	}
	mode, err := executor.ValidatePasswordResetMode(req.Mode)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.password_reset_invalid_mode")))
		return
	}
	if mode == site.PasswordResetMode {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
			"message":             i18n.TE(c.Request, "website.password_reset_saved"),
			"password_reset_mode": mode,
		}))
		return
	}

	// 同一站点的并发修改串行化：读原模式 → 写 mu-plugin → 落库 必须在同一把锁内，
	// 否则一个请求落库失败回滚时可能留下另一个请求写入的文件，造成状态不一致。
	lock := passwordResetSiteLock(id)
	lock.Lock()
	defer lock.Unlock()

	prevMode := site.PasswordResetMode
	if err := executor.ApplyWPPasswordResetMode(site.WebRoot, site.SystemUser, mode); err != nil {
		recordHandlerOperationLog("wp_password_reset", site.Domain, "failed", err.Error())
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "website.password_reset_save_failed")))
		return
	}

	if _, err := database.GetDB().Exec(
		"UPDATE websites SET password_reset_mode = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		mode, id,
	); err != nil {
		// 数据库落库失败则回滚 mu-plugin 文件，保持磁盘与状态一致。
		if rbErr := executor.ApplyWPPasswordResetMode(site.WebRoot, site.SystemUser, prevMode); rbErr != nil {
			recordHandlerOperationLog("wp_password_reset", site.Domain, "failed",
				fmt.Sprintf("save failed (%v); rollback also failed: %v", err, rbErr))
		} else {
			recordHandlerOperationLog("wp_password_reset", site.Domain, "failed", err.Error())
		}
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "website.password_reset_save_failed")))
		return
	}

	recordHandlerOperationLog("wp_password_reset", site.Domain, "success", "密码找回保护="+mode)
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"message":             i18n.TE(c.Request, "website.password_reset_saved"),
		"password_reset_mode": mode,
	}))
}
