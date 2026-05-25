package middleware

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var browserUAs = []string{
	"Mozilla", "Chrome", "Safari", "Firefox", "Edge", "Opera",
	"MSIE", "Trident", "Edg", "OPR", "Brave", "Vivaldi",
}

var nftInitialized bool

func ensureNftables() {
	if nftInitialized {
		return
	}
	exec.Command("bash", "-c", `nft add table ip wppanel_persist 2>/dev/null
nft add chain ip wppanel_persist input { type filter hook input priority -1\; } 2>/dev/null
nft add set ip wppanel_persist banned_ips { type ipv4_addr\; } 2>/dev/null
nft list chain ip wppanel_persist input 2>/dev/null | grep -q "saddr @banned_ips drop" || nft add rule ip wppanel_persist input ip saddr @banned_ips drop`).Run()
	nftInitialized = true
}

func isBrowserLike(c *gin.Context) bool {
	ua := c.GetHeader("User-Agent")
	if ua == "" {
		return false
	}
	for _, b := range browserUAs {
		if strings.Contains(ua, b) {
			return true
		}
	}
	return false
}

func banScanIP(db *sql.DB, ip string, reason string, hours int) {
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM firewall_bans WHERE ip_address = ? AND unbanned_at IS NULL`, ip).Scan(&count)
	if count > 0 {
		return
	}

	expires := time.Now().UTC().Add(time.Duration(hours) * time.Hour).Format("2006-01-02 15:04:05")
	_, err := db.Exec(
		`INSERT INTO firewall_bans (ip_address, ban_level, reason, source_jail, banned_at, expires_at, ban_count)
		 VALUES (?, 4, ?, 'panel_scan', datetime('now'), ?, 1)`,
		ip, reason, expires,
	)
	if err != nil {
		log.Printf("扫描封禁失败 ip=%s: %v", ip, err)
		return
	}

	ensureNftables()
	cmd := fmt.Sprintf("nft add element ip wppanel_persist banned_ips { %s } 2>/dev/null; true", ip)
	if err := exec.Command("bash", "-c", cmd).Run(); err != nil {
		log.Printf("nftables 封禁失败 ip=%s: %v", ip, err)
	}

	log.Printf("[扫描防御] 已封禁 IP %s (理由: %s, 时长: %d小时)", ip, reason, hours)
}

func ScanDefense(db *sql.DB, randomSuffix string) gin.HandlerFunc {
	legitPrefix := "/" + randomSuffix

	return func(c *gin.Context) {
		path := c.Request.URL.Path

		if strings.HasPrefix(path, legitPrefix) {
			c.Next()
			return
		}

		if !isBrowserLike(c) {
			banScanIP(db, c.ClientIP(), "高危扫描: 非浏览器特征探测面板端口", 720)
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		c.Next()
	}
}
