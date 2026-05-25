package handlers

import (
	"net/http"
	"strconv"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

type FirewallHandler struct{}

func (h *FirewallHandler) ListBans(c *gin.Context) {
	executor.SyncFail2banBans()
	executor.CleanExpiredBans()
	db := database.GetDB()

	query := `SELECT id, ip_address, ban_level, reason, source_jail, banned_at, expires_at, unbanned_at, ban_count, is_manual
	 FROM firewall_bans WHERE unbanned_at IS NULL AND (expires_at IS NULL OR expires_at > datetime('now')) ORDER BY banned_at DESC`
	if c.Query("history") == "1" {
		query = `SELECT id, ip_address, ban_level, reason, source_jail, banned_at, expires_at, unbanned_at, ban_count, is_manual
		 FROM firewall_bans ORDER BY banned_at DESC LIMIT 30`
	}

	rows, err := db.Query(query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询失败"))
		return
	}
	defer rows.Close()

	var bans []models.FirewallBan
	for rows.Next() {
		var b models.FirewallBan
		var isManual int
		if err := rows.Scan(&b.ID, &b.IPAddress, &b.BanLevel, &b.Reason, &b.SourceJail,
			&b.BannedAt, &b.ExpiresAt, &b.UnbannedAt, &b.BanCount, &isManual); err != nil {
			continue
		}
		b.IsManual = isManual == 1
		bans = append(bans, b)
	}
	if bans == nil {
		bans = []models.FirewallBan{}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(bans))
}

func (h *FirewallHandler) Unban(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的记录ID"))
		return
	}

	db := database.GetDB()
	var ip, jail string
	err = db.QueryRow("SELECT ip_address, source_jail FROM firewall_bans WHERE id = ? AND unbanned_at IS NULL", id).Scan(&ip, &jail)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("封禁记录不存在或已解封"))
		return
	}

	if _, err := db.Exec("UPDATE firewall_bans SET unbanned_at = datetime('now') WHERE id = ?", id); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("解封失败"))
		return
	}

	go func() {
		switch jail {
		case "wppanel", "wppanel-404", "wppanel-sshd":
			executor.Execute("fail2ban-client", "set", jail, "unbanip", ip)
		}
		executor.RemovePersistBan(ip)
	}()

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "IP " + ip + " 已解除封禁"}))
}

func (h *FirewallHandler) ManualBan(c *gin.Context) {
	var req struct {
		IP       string `json:"ip"`
		Duration int    `json:"duration"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IP == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("请输入有效的IP地址"))
		return
	}

	payload := &executor.ManualBanPayload{IP: req.IP, Duration: req.Duration}
	task := executor.GlobalQueue.Enqueue(executor.TaskManualBan, payload)
	result := <-task.ResultCh

	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": result.Message}))
	} else {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
	}
}

func (h *FirewallHandler) PermanentBan(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的记录ID"))
		return
	}

	db := database.GetDB()
	var ip string
	err = db.QueryRow("SELECT ip_address FROM firewall_bans WHERE id = ?", id).Scan(&ip)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("封禁记录不存在"))
		return
	}

	if _, err := db.Exec(
		`UPDATE firewall_bans SET ban_level = 5, expires_at = NULL, is_manual = 1 WHERE id = ?`, id,
	); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("永久封禁失败"))
		return
	}

	go executor.AddPersistBan(ip)

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "IP " + ip + " 已加入永久黑名单"}))
}
