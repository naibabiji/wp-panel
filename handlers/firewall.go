package handlers

import (
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

type FirewallHandler struct{}

const (
	firewallBanHistoryLimit         = 300
	firewallBanHistoryExpandedLimit = 1000
)

func (h *FirewallHandler) ListBans(c *gin.Context) {
	db := database.GetDB()
	isHistory := c.Query("history") == "1"

	if !isHistory {
		enforcement := executor.SyncFail2banBansAndReadEnforcement()
		executor.CleanExpiredBans()
		h.listCurrentBans(c, db, enforcement)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	perPage := 30
	search := strings.TrimSpace(c.Query("search"))
	level := strings.TrimSpace(c.Query("level"))
	source := strings.TrimSpace(c.Query("source"))

	var where string
	var args []interface{}

	if isHistory {
		historyLimit := firewallBanHistoryLimit
		if c.Query("expanded") == "1" {
			historyLimit = firewallBanHistoryExpandedLimit
		}
		where = "id IN (SELECT id FROM firewall_ban_history ORDER BY banned_at DESC, id DESC LIMIT ?)"
		args = append(args, historyLimit)
	} else {
		where = "unbanned_at IS NULL AND (expires_at IS NULL OR expires_at > datetime('now'))"
	}

	if search != "" {
		where += " AND ip_address LIKE ?"
		args = append(args, "%"+search+"%")
	}
	if level != "" {
		levelValue, err := strconv.Atoi(level)
		if err != nil || levelValue < 1 || levelValue > 5 {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的封禁等级"))
			return
		}
		where += " AND ban_level = ?"
		args = append(args, levelValue)
	}
	if source != "" {
		if isAllowedBanSourceFilter(source) {
			where += " AND source_jail = ?"
			args = append(args, source)
		} else {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的封禁来源"))
			return
		}
	}

	var total int
	countArgs := make([]interface{}, len(args))
	copy(countArgs, args)
	table := "firewall_bans"
	if isHistory {
		table = "firewall_ban_history"
	}
	db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE "+where, countArgs...).Scan(&total)

	offset := (page - 1) * perPage
	unbannedColumn := "unbanned_at"
	if isHistory {
		unbannedColumn = "NULL"
	}
	query := `SELECT id, ip_address, ban_level, reason, source_jail, banned_at, expires_at, ` + unbannedColumn + `, ban_count, is_manual
			 FROM ` + table + ` WHERE ` + where + ` ORDER BY banned_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, perPage, offset)

	rows, err := db.Query(query, args...)
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

	totalPages := (total + perPage - 1) / perPage
	if totalPages == 0 {
		totalPages = 1
	}

	hasMore := false
	if isHistory && c.Query("expanded") != "1" {
		_ = db.QueryRow(`SELECT EXISTS(
			SELECT 1 FROM firewall_ban_history ORDER BY banned_at DESC,id DESC LIMIT 1 OFFSET ?
		)`, firewallBanHistoryLimit).Scan(&hasMore)
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"data":        bans,
		"total":       total,
		"page":        page,
		"per_page":    perPage,
		"total_pages": totalPages,
		"has_more":    hasMore,
	}))
}

func (h *FirewallHandler) listCurrentBans(c *gin.Context, db *sql.DB, enforcement executor.CurrentBanEnforcement) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	search := strings.TrimSpace(c.Query("search"))
	level := strings.TrimSpace(c.Query("level"))
	source := strings.TrimSpace(c.Query("source"))
	levelValue := 0
	if level != "" {
		var err error
		levelValue, err = strconv.Atoi(level)
		if err != nil || levelValue < 1 || levelValue > 5 {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的封禁等级"))
			return
		}
	}
	if source != "" && !isAllowedBanSourceFilter(source) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的封禁来源"))
		return
	}

	dbBans, err := loadCurrentBanReceipts(db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询失败"))
		return
	}
	current, anomalies := buildCurrentBanView(dbBans, enforcement)
	current = filterCurrentBanView(current, search, levelValue, source)
	anomalies = filterCurrentBanView(anomalies, search, levelValue, source)
	sortCurrentBanView(current)
	sortCurrentBanView(anomalies)

	const perPage = 30
	total := len(current)
	start := (page - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	totalPages := (total + perPage - 1) / perPage
	if totalPages == 0 {
		totalPages = 1
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"data": current[start:end], "anomalies": anomalies, "read_status": enforcement.Status,
		"total": total, "page": page, "per_page": perPage, "total_pages": totalPages,
	}))
}

func loadCurrentBanReceipts(db *sql.DB) ([]models.FirewallBan, error) {
	rows, err := db.Query(`SELECT id,ip_address,ban_level,reason,source_jail,banned_at,expires_at,unbanned_at,ban_count,is_manual
		FROM firewall_bans WHERE unbanned_at IS NULL AND (expires_at IS NULL OR expires_at > datetime('now'))
		ORDER BY banned_at DESC,id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bans []models.FirewallBan
	for rows.Next() {
		var b models.FirewallBan
		var manual int
		if err := rows.Scan(&b.ID, &b.IPAddress, &b.BanLevel, &b.Reason, &b.SourceJail, &b.BannedAt, &b.ExpiresAt, &b.UnbannedAt, &b.BanCount, &manual); err != nil {
			return nil, err
		}
		b.IsManual = manual == 1
		bans = append(bans, b)
	}
	return bans, rows.Err()
}

type currentBanKey struct{ ip, source string }

func buildCurrentBanView(receipts []models.FirewallBan, enforcement executor.CurrentBanEnforcement) ([]models.CurrentFirewallBan, []models.CurrentFirewallBan) {
	metadata := make(map[currentBanKey]models.FirewallBan)
	byIP := make(map[string][]models.FirewallBan)
	for _, receipt := range receipts {
		key := currentBanKey{receipt.IPAddress, receipt.SourceJail}
		if _, exists := metadata[key]; !exists {
			metadata[key] = receipt
		}
		byIP[receipt.IPAddress] = append(byIP[receipt.IPAddress], receipt)
	}
	rows := make(map[currentBanKey]models.CurrentFirewallBan)
	add := func(ip, source string, receipt *models.FirewallBan) {
		key := currentBanKey{ip, source}
		if _, exists := rows[key]; exists {
			return
		}
		rows[key] = currentBanFromReceipt(ip, source, receipt, true, "enforced")
	}
	for key := range enforcement.Fail2ban {
		receipt, ok := metadata[currentBanKey{key.IP, key.Source}]
		if ok {
			add(key.IP, key.Source, &receipt)
		} else {
			add(key.IP, key.Source, nil)
		}
	}
	for ip := range enforcement.Persist {
		matched := false
		for _, receipt := range byIP[ip] {
			if receipt.SourceJail == "panel" || receipt.SourceJail == "panel_scan" || receipt.SourceJail == "manual" {
				matched = true
				add(ip, receipt.SourceJail, &receipt)
			}
		}
		if !matched {
			add(ip, "nftables", nil)
		}
	}
	for ip := range enforcement.Nginx {
		matched := false
		for _, jail := range []string{"wppanel", "wppanel-404", "wppanel-login"} {
			if !enforcement.Fail2ban[executor.CurrentBanKey{IP: ip, Source: jail}] {
				continue
			}
			matched = true
			receipt, ok := metadata[currentBanKey{ip, jail}]
			if ok {
				add(ip, jail, &receipt)
			} else {
				add(ip, jail, nil)
			}
		}
		if !matched {
			for _, receipt := range byIP[ip] {
				if receipt.SourceJail == "wppanel" || receipt.SourceJail == "wppanel-404" || receipt.SourceJail == "wppanel-login" || receipt.SourceJail == "manual" {
					matched = true
					add(ip, receipt.SourceJail, &receipt)
				}
			}
		}
		if !matched {
			add(ip, "nginx", nil)
		}
	}

	current := make([]models.CurrentFirewallBan, 0, len(rows))
	for _, row := range rows {
		current = append(current, row)
	}
	var anomalies []models.CurrentFirewallBan
	for _, receipt := range receipts {
		key := currentBanKey{receipt.IPAddress, receipt.SourceJail}
		if _, enforced := rows[key]; enforced {
			continue
		}
		verification := "missing"
		switch receipt.SourceJail {
		case "wppanel", "wppanel-404", "wppanel-login", "wppanel-sshd":
			if !enforcement.Status.Fail2ban[receipt.SourceJail] {
				verification = "unverified"
			}
		case "panel", "panel_scan", "manual":
			if !enforcement.Status.Nftables {
				verification = "unverified"
			}
		}
		anomalies = append(anomalies, currentBanFromReceipt(receipt.IPAddress, receipt.SourceJail, &receipt, false, verification))
	}
	return current, anomalies
}

func currentBanFromReceipt(ip, source string, receipt *models.FirewallBan, enforced bool, verification string) models.CurrentFirewallBan {
	row := models.CurrentFirewallBan{IPAddress: ip, SourceJail: source, Enforced: enforced, Verification: verification}
	if receipt == nil {
		return row
	}
	level, reason, count := receipt.BanLevel, receipt.Reason, receipt.BanCount
	row.ID, row.BanLevel, row.Reason, row.BannedAt, row.ExpiresAt, row.BanCount, row.IsManual = receipt.ID, &level, &reason, &receipt.BannedAt, receipt.ExpiresAt, &count, receipt.IsManual
	return row
}

func filterCurrentBanView(rows []models.CurrentFirewallBan, search string, level int, source string) []models.CurrentFirewallBan {
	filtered := rows[:0]
	for _, row := range rows {
		if search != "" && !strings.Contains(row.IPAddress, search) {
			continue
		}
		if level != 0 && (row.BanLevel == nil || int(*row.BanLevel) != level) {
			continue
		}
		if source != "" && row.SourceJail != source {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func sortCurrentBanView(rows []models.CurrentFirewallBan) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].BannedAt == nil {
			return false
		}
		if rows[j].BannedAt == nil {
			return true
		}
		return rows[i].BannedAt.After(*rows[j].BannedAt)
	})
}

func isAllowedBanSourceFilter(source string) bool {
	switch source {
	case "wppanel", "wppanel-404", "wppanel-login", "wppanel-sshd", "panel", "panel_scan", "manual", "nftables", "nginx":
		return true
	default:
		return false
	}
}

func (h *FirewallHandler) WPSecurityReport(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "30"))
	items, err := executor.BuildWPSecurityReport(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("读取 WordPress 安全日志失败"))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"items": items,
		"total": len(items),
	}))
}

func (h *FirewallHandler) ListFileSecurityEvents(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	events, err := executor.ListFileSecurityEvents(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("读取文件安全事件失败"))
		return
	}
	summary, err := executor.GetFileSecuritySummary()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("读取文件安全事件摘要失败"))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"summary": summary,
		"events":  events,
		"total":   len(events),
	}))
}

func (h *FirewallHandler) RefreshFileSecurityEvents(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if clearMode, err := strconv.ParseBool(c.Query("clear")); err == nil && clearMode {
		if err := executor.ClearFileSecurityEvents(); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("清理文件安全事件失败"))
			return
		}
	}
	summary, err := executor.RefreshFileSecurityEvents()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("刷新文件安全事件失败"))
		return
	}
	events, err := executor.ListFileSecurityEvents(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("读取文件安全事件失败"))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"summary": summary,
		"events":  events,
		"total":   len(events),
	}))
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

	if jail == "wppanel" || jail == "wppanel-404" || jail == "wppanel-login" || jail == "wppanel-sshd" {
		if _, err := executor.Execute("fail2ban-client", "set", jail, "unbanip", ip); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("Fail2ban 解封失败"))
			return
		}
	}

	if _, err := db.Exec("UPDATE firewall_bans SET unbanned_at = datetime('now') WHERE id = ?", id); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("解封失败"))
		return
	}

	executor.GoSafe(func() {
		if jail == "wppanel" || jail == "wppanel-404" || jail == "wppanel-login" || jail == "manual" {
			_ = executor.MaybeRemoveNginxBan(ip)
		}
		_ = executor.MaybeRemovePersistBan(ip)
	})

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
	var ip, jail string
	err = db.QueryRow("SELECT ip_address, source_jail FROM firewall_bans WHERE id = ?", id).Scan(&ip, &jail)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("封禁记录不存在"))
		return
	}

	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("永久封禁失败"))
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE firewall_bans SET ban_level = 5, expires_at = NULL, is_manual = 1 WHERE id = ?`, id); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("永久封禁失败"))
		return
	}
	if _, err := tx.Exec(`INSERT INTO firewall_ban_history
		(ip_address,ban_level,reason,source_jail,ban_count,is_manual,duration_seconds,expires_at)
		VALUES (?,5,'管理员永久封禁','manual',1,1,NULL,NULL)`, ip); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("永久封禁历史写入失败"))
		return
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("永久封禁失败"))
		return
	}

	executor.GoSafe(func() { executor.AddPersistBan(ip) })
	if jail == "wppanel" || jail == "wppanel-404" || jail == "wppanel-login" || jail == "manual" {
		executor.GoSafe(func() { executor.AddNginxBan(ip) })
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "IP " + ip + " 已加入永久黑名单"}))
}
