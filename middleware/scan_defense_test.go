package middleware

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"
)

func TestScanDefenseAllowsCommonProbePathWithoutBan(t *testing.T) {
	db := newScanDefenseTestDB(t)
	router := newScanDefenseTestRouter(t, db)

	rec := performScanDefenseRequest(router, http.MethodGet, "/favicon.ico", "curl/8.0", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if count := scanDefenseBanCount(t, db); count != 0 {
		t.Fatalf("ban count = %d, want 0", count)
	}
}

func TestScanDefenseAllowsBasicAuthHeaderWithoutBan(t *testing.T) {
	db := newScanDefenseTestDB(t)
	router := newScanDefenseTestRouter(t, db)

	rec := performScanDefenseRequest(router, http.MethodGet, "/not-the-panel-prefix", "", "Basic dXNlcjpwYXNz")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if count := scanDefenseBanCount(t, db); count != 0 {
		t.Fatalf("ban count = %d, want 0", count)
	}
}

func TestScanDefenseBansNonBrowserProbeAndRecordsRequestSummary(t *testing.T) {
	db := newScanDefenseTestDB(t)
	router := newScanDefenseTestRouter(t, db)

	rec := performScanDefenseRequest(router, http.MethodGet, "/wp-login.php", "curl/8.0 scanner", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	var reason, jail string
	if err := db.QueryRow(`SELECT reason, source_jail FROM firewall_bans LIMIT 1`).Scan(&reason, &jail); err != nil {
		t.Fatalf("query ban: %v", err)
	}
	if jail != "panel_scan" {
		t.Fatalf("source_jail = %q, want panel_scan", jail)
	}
	for _, want := range []string{"高危扫描: 非浏览器特征探测面板端口", "path=/wp-login.php", "ua=curl/8.0 scanner"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason %q missing %q", reason, want)
		}
	}
}

func TestBanScanIPIgnoresNonPublicAddress(t *testing.T) {
	db := newScanDefenseTestDB(t)
	banScanIP(db, "127.0.0.1", "spoofed", 720)
	if count := scanDefenseBanCount(t, db); count != 0 {
		t.Fatalf("non-public ban count = %d, want 0", count)
	}
}

func newScanDefenseTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE firewall_bans (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ip_address TEXT NOT NULL,
		ban_level INTEGER NOT NULL DEFAULT 2,
		reason TEXT NOT NULL,
		source_jail TEXT NOT NULL,
		banned_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME,
		unbanned_at DATETIME,
		ban_count INTEGER NOT NULL DEFAULT 1
	)`); err != nil {
		t.Fatalf("create firewall_bans: %v", err)
	}
	return db
}

func newScanDefenseTestRouter(t *testing.T, db *sql.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	oldAddPersistBan := scanDefenseAddPersistBan
	scanDefenseAddPersistBan = func(string) {}
	t.Cleanup(func() { scanDefenseAddPersistBan = oldAddPersistBan })

	router := gin.New()
	router.Use(ScanDefense(db, "secret"))
	router.GET("/favicon.ico", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.GET("/secret", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	router.NoRoute(func(c *gin.Context) {
		c.Status(http.StatusNotFound)
	})
	return router
}

func performScanDefenseRequest(router http.Handler, method, path, userAgent, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "203.0.113.10:12345"
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func scanDefenseBanCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM firewall_bans`).Scan(&count); err != nil {
		t.Fatalf("count bans: %v", err)
	}
	return count
}
