package handlers

import (
	"net/http"
	"strconv"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

type AlertHandler struct{}

func (h *AlertHandler) GetSettings(c *gin.Context) {
	db := database.GetDB()
	rows, err := db.Query("SELECT id, skey, svalue, description, updated_at FROM security_settings WHERE skey LIKE 'alert_%' OR skey LIKE 'smtp_%' OR skey = 'admin_email' OR skey LIKE 'webhook_%'")
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询失败"))
		return
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var id int
		var key, val, desc, updated string
		rows.Scan(&id, &key, &val, &desc, &updated)
		settings[key] = val
	}
	c.JSON(http.StatusOK, models.SuccessResponse(settings))
}

func (h *AlertHandler) SaveSettings(c *gin.Context) {
	var raw map[string]interface{}
	if err := c.ShouldBindJSON(&raw); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	db := database.GetDB()
	for key, val := range raw {
		var strVal string
		switch v := val.(type) {
		case string:
			strVal = v
		case bool:
			strVal = strconv.FormatBool(v)
		default:
			continue
		}
		db.Exec("INSERT INTO security_settings (skey, svalue, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP) ON CONFLICT(skey) DO UPDATE SET svalue = excluded.svalue, updated_at = excluded.updated_at", key, strVal)
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "已保存"}))
}

func (h *AlertHandler) TestSMTP(c *gin.Context) {
	var req struct {
		Email string `json:"email"`
	}
	c.ShouldBindJSON(&req)
	if req.Email == "" {
		cfg := executor.GetSMTPConfig()
		if cfg != nil {
			req.Email = cfg.AdminEmail
		}
	}
	if req.Email == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("请输入测试邮箱"))
		return
	}
	if err := executor.TestSMTP(req.Email); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("发送失败: "+err.Error()))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "测试邮件已发送至 " + req.Email}))
}

func (h *AlertHandler) TestWebhook(c *gin.Context) {
	var req struct {
		Channel string `json:"channel"`
		URL     string `json:"url"`
	}
	c.ShouldBindJSON(&req)
	if req.Channel == "" || req.URL == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("请填写推送渠道和 Webhook URL"))
		return
	}
	if err := executor.TestWebhook(req.Channel, req.URL); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("发送失败: "+err.Error()))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "测试消息已发送"}))
}

func (h *AlertHandler) GetLog(c *gin.Context) {
	db := database.GetDB()
	rows, err := db.Query("SELECT id, alert_type, level, message, resolved, created_at FROM alert_log ORDER BY id DESC LIMIT 30")
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询失败"))
		return
	}
	defer rows.Close()

	type logEntry struct {
		ID        int    `json:"id"`
		AlertType string `json:"alert_type"`
		Level     string `json:"level"`
		Message   string `json:"message"`
		Resolved  bool   `json:"resolved"`
		CreatedAt string `json:"created_at"`
	}
	var logs []logEntry
	for rows.Next() {
		var e logEntry
		var r int
		if rows.Scan(&e.ID, &e.AlertType, &e.Level, &e.Message, &r, &e.CreatedAt) == nil {
			e.Resolved = r == 1
			logs = append(logs, e)
		}
	}
	if logs == nil {
		logs = []logEntry{}
	}
	c.JSON(http.StatusOK, models.SuccessResponse(logs))
}
