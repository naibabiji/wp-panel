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

const (
	browserProbeWindow    = time.Minute
	browserProbeThreshold = 10
	browserProbeBan       = 30 * time.Minute
)

type browserProbeEntry struct {
	startedAt time.Time
	paths     map[string]struct{}
}

type browserProbeTracker struct {
	mu          sync.Mutex
	now         func() time.Time
	lastCleanup time.Time
	entries     map[string]*browserProbeEntry
}

func newBrowserProbeTracker() *browserProbeTracker {
	return &browserProbeTracker{now: time.Now, entries: make(map[string]*browserProbeEntry)}
}

func (t *browserProbeTracker) record(ip, path string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	if t.lastCleanup.IsZero() || now.Sub(t.lastCleanup) >= browserProbeWindow {
		for key, entry := range t.entries {
			if now.Sub(entry.startedAt) >= browserProbeWindow {
				delete(t.entries, key)
			}
		}
		t.lastCleanup = now
	}

	entry := t.entries[ip]
	if entry == nil || now.Sub(entry.startedAt) >= browserProbeWindow {
		entry = &browserProbeEntry{startedAt: now, paths: make(map[string]struct{})}
		t.entries[ip] = entry
	}
	entry.paths[path] = struct{}{}
	if len(entry.paths) < browserProbeThreshold {
		return false
	}
	delete(t.entries, ip)
	return true
}

func ensureNftables() {
	ensureNftablesOnce.Do(func() {
		if err := executor.EnsurePersistNftables(); err != nil {
			log.Printf("初始化扫描防御持久封禁层失败，将在封禁时重试: %v", err)
		}
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

// Site migration machine calls cannot use the panel's random browser prefix.
// Only these exact POST endpoints bypass probe classification; their handlers
// still enforce pairing tokens or peer credentials and strict body limits.
func isSiteMigrationMachineRequest(c *gin.Context) bool {
	if c.Request.Method != http.MethodPost {
		return false
	}
	switch c.Request.URL.Path {
	case "/api/site-migration/v1/pair/redeem",
		"/api/site-migration/v1/pair/challenge",
		"/api/site-migration/v1/peer/revoke",
		"/api/site-migration/v1/preflight",
		"/api/site-migration/v1/target/batches",
		"/api/site-migration/v1/target/batches/queue",
		"/api/site-migration/v1/source/manifest",
		"/api/site-migration/v1/source/chunk",
		"/api/site-migration/v1/source/file-shard",
		"/api/site-migration/v1/source/database",
		"/api/site-migration/v1/source/database-chunk",
		"/api/site-migration/v1/source/certificates",
		"/api/site-migration/v1/source/certificate-chunk",
		"/api/site-migration/v1/source/settings",
		"/api/site-migration/v1/target/status",
		"/api/site-migration/v1/target/retry",
		"/api/site-migration/v1/target/delete-task",
		"/api/site-migration/v1/source/delete-task":
		return true
	default:
		return false
	}
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
	banScanIPForDuration(db, ip, reason, time.Duration(hours)*time.Hour)
}

func banScanIPForDuration(db *sql.DB, ip string, reason string, duration time.Duration) {
	if !executor.IsPublicIPAddress(ip) {
		log.Printf("扫描封禁已忽略非公网 IP %s", ip)
		return
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM firewall_bans WHERE ip_address = ? AND unbanned_at IS NULL
		AND (expires_at IS NULL OR expires_at > datetime('now'))`, ip).Scan(&count)
	if count > 0 {
		return
	}

	expires := time.Now().UTC().Add(duration).Format("2006-01-02 15:04:05")
	tx, err := db.Begin()
	if err != nil {
		log.Printf("扫描封禁失败 ip=%s: %v", ip, err)
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec(
		`INSERT INTO firewall_bans (ip_address, ban_level, reason, source_jail, banned_at, expires_at, ban_count)
		 VALUES (?, 4, ?, 'panel_scan', datetime('now'), ?, 1)`,
		ip, reason, expires,
	)
	if err != nil {
		log.Printf("扫描封禁失败 ip=%s: %v", ip, err)
		return
	}
	if _, err := tx.Exec(`INSERT INTO firewall_ban_history
		(ip_address,ban_level,reason,source_jail,expires_at,ban_count,is_manual,duration_seconds)
		VALUES (?,4,?,'panel_scan',?,1,0,?)`, ip, reason, expires, int64(duration/time.Second)); err != nil {
		log.Printf("扫描封禁历史写入失败 ip=%s: %v", ip, err)
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("扫描封禁失败 ip=%s: %v", ip, err)
		return
	}

	if err := scanDefenseAddPersistBan(ip); err != nil {
		log.Printf("[扫描防御] IP %s 已写入数据库，但持久封禁层应用失败，将等待同步重试: %v", ip, err)
		return
	}
	log.Printf("[扫描防御] 已封禁 IP %s (理由: %s, 时长: %s)", ip, reason, duration)
}

func ScanDefense(db *sql.DB, randomSuffix string) gin.HandlerFunc {
	legitPrefix := "/" + randomSuffix
	browserProbes := newBrowserProbeTracker()

	return func(c *gin.Context) {
		path := c.Request.URL.Path

		if strings.HasPrefix(path, legitPrefix) {
			c.Next()
			return
		}

		if isCommonProbePath(path) || isSiteMigrationMachineRequest(c) {
			c.Next()
			return
		}

		if !isBrowserLike(c) && !hasBasicAuthHeader(c) {
			banScanIP(db, c.ClientIP(), scanReason(c), 720)
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		c.Next()
		if c.Writer.Status() == http.StatusNotFound && browserProbes.record(c.ClientIP(), path) {
			reason := "高频扫描: 60秒内访问10个不同的未知面板路径"
			banScanIPForDuration(db, c.ClientIP(), reason, browserProbeBan)
		}
	}
}
