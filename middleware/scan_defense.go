package middleware

import (
	"database/sql"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var honeypotPaths = []string{
	"/.env", "/.git", "/.git/config", "/wp-admin", "/wp-login",
	"/admin", "/phpmyadmin", "/phpMyAdmin", "/phpmyadmin4", "/pma",
	"/api", "/api/v1", "/config", "/backup", "/backups", "/dump",
	"/shell", "/cmd", "/actuator", "/health", "/info", "/debug",
	"/console", "/manager", "/jenkins", "/.DS_Store",
	"/docker-compose.yml", "/composer.json", "/package.json",
	"/robots.txt", "/sitemap.xml", "/crossdomain.xml",
	"/owa", "/ecp", "/Autodiscover", "/solr", "/geoserver",
	"/lang", "/swagger", "/swagger-ui", "/graphql",
	"/vendor/phpunit", "/.svn", "/.hg", "/.bzr",
}

var browserUAs = []string{
	"Mozilla", "Chrome", "Safari", "Firefox", "Edge", "Opera",
	"MSIE", "Trident", "Edg", "OPR", "Brave", "Vivaldi",
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

	accept := c.GetHeader("Accept")
	lang := c.GetHeader("Accept-Language")
	if accept == "" && lang == "" {
		return false
	}
	if !strings.Contains(accept, "text/html") {
		return false
	}

	return false
}

func banScanIP(db *sql.DB, ip string, reason string, hours int) {
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM firewall_bans WHERE ip_address = ? AND unbanned_at IS NULL`, ip).Scan(&count)
	if count > 0 {
		return
	}

	expires := time.Now().Add(time.Duration(hours) * time.Hour).Format("2006-01-02 15:04:05")
	_, err := db.Exec(
		`INSERT INTO firewall_bans (ip_address, ban_level, reason, source_jail, banned_at, expires_at, ban_count)
		 VALUES (?, 4, ?, 'panel_scan', datetime('now'), ?, 1)`,
		ip, reason, expires,
	)
	if err != nil {
		log.Printf("扫描封禁失败 ip=%s: %v", ip, err)
		return
	}

	log.Printf("[扫描防御] 已封禁 IP %s (理由: %s, 时长: %d小时)", ip, reason, hours)
}

func ScanDefense(db *sql.DB, randomSuffix string) gin.HandlerFunc {
	legitPrefix := "/" + randomSuffix

	return func(c *gin.Context) {
		path := c.Request.URL.Path

		if path == "/" || path == "/favicon.ico" || strings.HasPrefix(path, legitPrefix) {
			c.Next()
			return
		}

		for _, hp := range honeypotPaths {
			if strings.HasPrefix(path, hp) {
				banScanIP(db, c.ClientIP(), "高危扫描: 探测已知漏洞路径 "+path, 720)
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
		}

		if !isBrowserLike(c) {
			banScanIP(db, c.ClientIP(), "高危扫描: 非浏览器特征探测 "+path, 168)
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		c.Next()
	}
}
