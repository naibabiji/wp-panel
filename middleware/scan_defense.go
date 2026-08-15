package middleware

import (
	"database/sql"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/executor"

	"github.com/gin-gonic/gin"
)

var browserUAs = []string{
	"Mozilla", "Chrome", "Safari", "Firefox", "Edge", "Opera",
	"MSIE", "Trident", "Edg", "OPR", "Brave", "Vivaldi",
}

var scanDefenseAddPersistBan = executor.AddPersistBan

var ensureNftablesOnce sync.Once

func ensureNftables() {
	ensureNftablesOnce.Do(func() {
		executor.EnsurePersistNftables()
	})
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

func isCommonProbePath(path string) bool {
	if path == "/" || path == "/favicon.ico" {
		return true
	}
	if path == "/apple-touch-icon.png" || path == "/apple-touch-icon-precomposed.png" {
		return true
	}
	if strings.HasPrefix(path, "/apple-touch-icon") && strings.HasSuffix(path, ".png") {
		return true
	}
	return strings.HasPrefix(path, "/.well-known/")
}

// Requests carrying a Basic Auth header are likely from uptime monitors or
// reverse-proxy health checks, not port scanners. Panel access still requires
// valid credentials and a valid session, so allowing them through does not
// grant any privileges.
func hasBasicAuthHeader(c *gin.Context) bool {
	auth := strings.TrimSpace(c.GetHeader("Authorization"))
	return strings.HasPrefix(strings.ToLower(auth), "basic ")
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

func scanReason(c *gin.Context) string {
	path := strings.TrimSpace(c.Request.URL.Path)
	ua := strings.TrimSpace(strings.Join(strings.Fields(c.GetHeader("User-Agent")), " "))
	if path == "" {
		path = "-"
	}
	path = truncateRunes(path, 120)
	if ua == "" {
		ua = "-"
	}
	ua = truncateRunes(ua, 160)
	return "高危扫描: 非浏览器特征探测面板端口 (path=" + path + ", ua=" + ua + ")"
}

func banScanIP(db *sql.DB, ip string, reason string, hours int) {
	if !executor.IsPublicIPAddress(ip) {
		log.Printf("扫描封禁已忽略非公网 IP %s", ip)
		return
	}
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

	scanDefenseAddPersistBan(ip)

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

		if isCommonProbePath(path) || hasBasicAuthHeader(c) {
			c.Next()
			return
		}

		if !isBrowserLike(c) {
			banScanIP(db, c.ClientIP(), scanReason(c), 720)
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		c.Next()
	}
}
