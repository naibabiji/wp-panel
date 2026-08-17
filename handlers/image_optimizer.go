package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"
)

// ImageOptimizerHandler 是配套插件设置页"历史图库批量优化"小节对应的接口：
// 启动任务 / 查询进度 / 停止任务。真正的扫描、降权执行、进度记录都在
// executor 的 image_batch_* 里，这里只做参数校验和状态转译。
//
// 插件不持有面板管理员会话，走的是跟 CacheHelperHandler 其他接口一样的
// "仅本机回环 + 站点 API Key"认证（pluginSiteByDomain），不是 protected
// 分组的 SessionRequired()——面板后台本身不提供这个入口（用户反馈这个功能
// 更适合在 WordPress 后台操作，面板侧看不到实时进度同步，已移除面板入口）。
type ImageOptimizerHandler struct{}

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

	// 历史累计节省量跟当前任务是否存在无关，两个查询都要走，任一失败不影响另一个。
	lifetimeSaved, err := executor.GetImageOptimizationLifetimeSavedBytes(site.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询失败"))
		return
	}

	status, err := executor.GetImageOptimizationJobStatus(site.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询失败"))
		return
	}
	if status == nil {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"Status": "none", "LifetimeBytesSaved": lifetimeSaved}))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(struct {
		*executor.ImageOptimizationJobStatus
		LifetimeBytesSaved int64
	}{status, lifetimeSaved}))
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
