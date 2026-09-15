package handlers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"
)

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
	if !executor.TryAcquireSiteOpLock(id, "wp_password_reset") {
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "maintenance.operation_unavailable")))
		return
	}
	defer executor.ReleaseSiteOpLock(id)
	site = getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "website.not_found")))
		return
	}
	if site.FileLockEnabled {
		c.JSON(http.StatusLocked, models.ErrorResponse(fileLockBlockedMessage))
		return
	}
	if locked, lockErr := executor.SiteMigrationLocked(c.Request.Context(), site.ID, site.Domain); lockErr != nil || locked {
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "maintenance.operation_unavailable")))
		return
	}

	// 加锁后从 DB 重新读取当前已提交的模式作为回滚基准。site 对象是加锁前读取的快照，
	// 可能被并发请求改写；若此处仍用旧值，本请求回滚时会把磁盘恢复到过时状态，
	// 与 DB 当前值不一致（评审意见第 2 点）。
	var prevMode string
	if err := database.GetDB().QueryRow(
		"SELECT COALESCE(password_reset_mode, 'allow') FROM websites WHERE id = ?", id,
	).Scan(&prevMode); err != nil {
		recordHandlerOperationLog("wp_password_reset", site.Domain, "failed", err.Error())
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "website.password_reset_save_failed")))
		return
	}
	if mode == prevMode {
		matches, matchErr := executor.WPPasswordResetModeMatches(site.WebRoot, mode)
		if matchErr == nil && matches {
			c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": i18n.TE(c.Request, "website.password_reset_saved"), "password_reset_mode": mode}))
			return
		}
	}

	// rollbackToPrev 把磁盘 mu-plugin 恢复到 prevMode，使磁盘状态与 DB 中已提交的模式一致。
	rollbackToPrev := func(reason string) bool {
		if rbErr := executor.ApplyWPPasswordResetMode(site.WebRoot, site.SystemUser, prevMode); rbErr != nil {
			recordHandlerOperationLog("wp_password_reset", site.Domain, "failed",
				fmt.Sprintf("%s; rollback also failed: %v", reason, rbErr))
			return false
		} else {
			recordHandlerOperationLog("wp_password_reset", site.Domain, "failed", reason)
			return true
		}
	}

	if err := executor.ApplyWPPasswordResetMode(site.WebRoot, site.SystemUser, mode); err != nil {
		// apply 失败：文件可能已写入新模式（如 chown 失败），按 prevMode 回滚磁盘，
		// 保持磁盘与 DB 一致（评审意见第 1 点）。
		if !rollbackToPrev(fmt.Sprintf("apply failed: %v", err)) {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存失败且恢复原策略也失败，请立即检查密码找回策略文件"))
			return
		}
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "website.password_reset_save_failed")))
		return
	}

	if _, err := database.GetDB().Exec(
		"UPDATE websites SET password_reset_mode = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		mode, id,
	); err != nil {
		// 数据库落库失败则回滚 mu-plugin 文件，保持磁盘与状态一致。
		if !rollbackToPrev(fmt.Sprintf("save failed: %v", err)) {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("数据库保存失败且恢复原策略也失败，请立即检查密码找回策略文件"))
			return
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
