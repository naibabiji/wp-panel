package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"
)

// ImageOptimizerHandler 是网站详情页"图片优化"卡片对应的历史图库批量优化接口：
// 启动任务 / 查询进度 / 停止任务。真正的扫描、降权执行、进度记录都在
// executor 的 image_batch_* 里，这里只做参数校验和状态转译。
type ImageOptimizerHandler struct{}

func (h *ImageOptimizerHandler) Start(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return
	}
	if site := getWebsiteByID(id); site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "image_optimizer.not_found")))
		return
	}

	jobID, err := executor.StartImageOptimizationJob(id)
	if err != nil {
		if !executor.ImageBatchBinariesReady() {
			c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "image_optimizer.not_ready")))
			return
		}
		// 建表时的唯一索引（同一站点只允许一个 queued/running 任务）拒绝了本次
		// 插入，视为"已有任务在跑"，不是系统错误。
		if status, statusErr := executor.GetImageOptimizationJobStatus(id); statusErr == nil && status != nil &&
			(status.Status == "queued" || status.Status == "running") {
			c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"id": status.ID, "status": status.Status}))
			return
		}
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "image_optimizer.create_failed")))
		return
	}
	c.JSON(http.StatusAccepted, models.SuccessResponse(gin.H{"id": jobID, "status": "queued"}))
}

func (h *ImageOptimizerHandler) Status(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return
	}
	status, err := executor.GetImageOptimizationJobStatus(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "image_optimizer.load_failed")))
		return
	}
	if status == nil {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"status": "none"}))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(status))
}

func (h *ImageOptimizerHandler) Stop(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return
	}
	if err := executor.StopImageOptimizationJob(id); err != nil {
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "image_optimizer.no_active_job")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"status": "stopping"}))
}
