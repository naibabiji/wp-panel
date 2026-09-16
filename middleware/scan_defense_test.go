package middleware

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestScanDefenseBansTenthDistinctBrowserLikeNotFoundPath(t *testing.T) {
	db := newScanDefenseTestDB(t)
	router := newScanDefenseTestRouter(t, db)

	for i := 0; i < browserProbeThreshold; i++ {
		rec := performScanDefenseRequest(router, http.MethodGet, fmt.Sprintf("/missing-%d", i), "Mozilla/5.0", "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("request %d status=%d, want %d", i+1, rec.Code, http.StatusNotFound)
		}
		wantBans := 0
		if i+1 == browserProbeThreshold {
			wantBans = 1
		}
		if count := scanDefenseBanCount(t, db); count != wantBans {
			t.Fatalf("request %d ban count=%d, want %d", i+1, count, wantBans)
		}
	}

	var duration int
	if err := db.QueryRow(`SELECT duration_seconds FROM firewall_ban_history LIMIT 1`).Scan(&duration); err != nil {
		t.Fatal(err)
	}
	if duration != int(browserProbeBan/time.Second) {
		t.Fatalf("duration=%d, want %d", duration, int(browserProbeBan/time.Second))
	}
}

func TestScanDefenseCountsDistinctBrowserLikeNotFoundPaths(t *testing.T) {
	db := newScanDefenseTestDB(t)
	router := newScanDefenseTestRouter(t, db)

	for i := 0; i < browserProbeThreshold+5; i++ {
		rec := performScanDefenseRequest(router, http.MethodGet, "/same-missing-path", "Mozilla/5.0", "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("request %d status=%d, want %d", i+1, rec.Code, http.StatusNotFound)
		}
	}
	if count := scanDefenseBanCount(t, db); count != 0 {
		t.Fatalf("ban count=%d, want 0", count)
	}
}

func TestScanDefenseCountsBasicAuthNotFoundPaths(t *testing.T) {
	db := newScanDefenseTestDB(t)
	router := newScanDefenseTestRouter(t, db)

	for i := 0; i < browserProbeThreshold; i++ {
		performScanDefenseRequest(router, http.MethodGet, fmt.Sprintf("/basic-missing-%d", i), "", "Basic dXNlcjpwYXNz")
	}
	if count := scanDefenseBanCount(t, db); count != 1 {
		t.Fatalf("ban count=%d, want 1", count)
	}
}

func TestScanDefenseAllowsOnlyExactSiteMigrationMachinePosts(t *testing.T) {
	db := newScanDefenseTestDB(t)
	router := newScanDefenseTestRouter(t, db)

	for _, path := range []string{
		"/api/site-migration/v1/pair/redeem",
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
		"/api/site-migration/v1/source/delete-task",
	} {
		rec := performScanDefenseRequest(router, http.MethodPost, path, "Go-http-client/1.1", "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("POST %s status=%d, want downstream %d", path, rec.Code, http.StatusUnauthorized)
		}
	}
	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/api/site-migration/v1/preflight"},
		{http.MethodPost, "/api/site-migration/v1/preflight/extra"},
		{http.MethodPost, "/api/site-migration/v1/unknown"},
	} {
		rec := performScanDefenseRequest(router, request.method, request.path, "Go-http-client/1.1", "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s status=%d, want %d", request.method, request.path, rec.Code, http.StatusForbidden)
		}
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
	var historyJail string
	var duration int
	if err := db.QueryRow(`SELECT source_jail,duration_seconds FROM firewall_ban_history LIMIT 1`).Scan(&historyJail, &duration); err != nil {
		t.Fatalf("query scan history: %v", err)
	}
	if historyJail != "panel_scan" || duration != 720*60*60 {
		t.Fatalf("scan history jail=%q duration=%d", historyJail, duration)
	}
}

func TestBanScanIPIgnoresNonPublicAddress(t *testing.T) {
	db := newScanDefenseTestDB(t)
	banScanIP(db, "127.0.0.1", "spoofed", 720)
	if count := scanDefenseBanCount(t, db); count != 0 {
		t.Fatalf("non-public ban count = %d, want 0", count)
	}
}

func TestBanScanIPDoesNotLetExpiredReceiptBlockNewBan(t *testing.T) {
	db := newScanDefenseTestDB(t)
	oldAddPersistBan := scanDefenseAddPersistBan
	scanDefenseAddPersistBan = func(string) error { return nil }
	t.Cleanup(func() { scanDefenseAddPersistBan = oldAddPersistBan })

	ip := "203.0.113.20"
	if _, err := db.Exec(`INSERT INTO firewall_bans
		(ip_address,ban_level,reason,source_jail,expires_at,ban_count)
		VALUES (?,4,'expired','panel_scan',datetime('now','-1 minute'),1)`, ip); err != nil {
		t.Fatal(err)
	}
	banScanIP(db, ip, "new scan", 720)

	var active int
	if err := db.QueryRow(`SELECT COUNT(*) FROM firewall_bans WHERE ip_address=?
		AND unbanned_at IS NULL AND expires_at > datetime('now')`, ip).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("fresh scan ban count = %d, want 1", active)
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
	if _, err := db.Exec(`CREATE TABLE firewall_ban_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ip_address TEXT NOT NULL,
		ban_level INTEGER NOT NULL,
		reason TEXT NOT NULL,
		source_jail TEXT NOT NULL,
		banned_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME,
		ban_count INTEGER NOT NULL DEFAULT 1,
		is_manual INTEGER NOT NULL DEFAULT 0,
		duration_seconds INTEGER
	)`); err != nil {
		t.Fatalf("create firewall_ban_history: %v", err)
	}
	return db
}

func newScanDefenseTestRouter(t *testing.T, db *sql.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	oldAddPersistBan := scanDefenseAddPersistBan
	scanDefenseAddPersistBan = func(string) error { return nil }
	t.Cleanup(func() { scanDefenseAddPersistBan = oldAddPersistBan })

	router := gin.New()
	router.Use(ScanDefense(db, "secret"))
	router.GET("/favicon.ico", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.GET("/secret", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	for _, path := range []string{
		"/api/site-migration/v1/pair/redeem",
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
		"/api/site-migration/v1/source/delete-task",
	} {
		router.POST(path, func(c *gin.Context) { c.Status(http.StatusUnauthorized) })
	}
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
