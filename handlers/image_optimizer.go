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

// 以下三个方法给配套插件自己的设置页用——插件不持有面板管理员会话，走的是跟
// CacheHelperHandler 其他接口一样的"仅本机回环 + 站点 API Key"认证
// （pluginSiteByDomain），不是 protected 分组的 SessionRequired()。

func (h *ImageOptimizerHandler) PluginStart(c *gin.Context) {
	var req struct {
		Domain string `json:"domain"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Domain == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	site, ok := (&CacheHelperHandler{}).pluginSiteByDomain(req.Domain, c)
	if !ok {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse("API Key 无效"))
		return
	}

	jobID, err := executor.StartImageOptimizationJob(site.ID)
	if err != nil {
		if !executor.ImageBatchBinariesReady() {
			c.JSON(http.StatusConflict, models.ErrorResponse("服务器尚未就绪，请稍后重试"))
			return
		}
		if status, statusErr := executor.GetImageOptimizationJobStatus(site.ID); statusErr == nil && status != nil &&
			(status.Status == "queued" || status.Status == "running") {
			c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"id": status.ID, "status": status.Status}))
			return
		}
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("创建任务失败"))
		return
	}
	c.JSON(http.StatusAccepted, models.SuccessResponse(gin.H{"id": jobID, "status": "queued"}))
}

func (h *ImageOptimizerHandler) PluginStatus(c *gin.Context) {
	domain := c.Query("domain")
	if domain == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	site, ok := (&CacheHelperHandler{}).pluginSiteByDomain(domain, c)
	if !ok {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse("API Key 无效"))
		return
	}

	status, err := executor.GetImageOptimizationJobStatus(site.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询失败"))
		return
	}
	if status == nil {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"status": "none"}))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(status))
}

func (h *ImageOptimizerHandler) PluginStop(c *gin.Context) {
	var req struct {
		Domain string `json:"domain"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Domain == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	site, ok := (&CacheHelperHandler{}).pluginSiteByDomain(req.Domain, c)
	if !ok {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse("API Key 无效"))
		return
	}
	if err := executor.StopImageOptimizationJob(site.ID); err != nil {
		c.JSON(http.StatusConflict, models.ErrorResponse("没有正在进行的任务"))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"status": "stopping"}))
}
