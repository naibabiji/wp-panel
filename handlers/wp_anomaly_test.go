package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/middleware"
)

func TestWPAnomalyHandlerScopeAndCSRF(t *testing.T) {
	setupCacheHelperTestDB(t)
	h := &WPAnomalyHandler{Monitor: executor.NewWPAnomalyMonitor(database.GetDB(), nil)}
	r := gin.New()
	r.Use(middleware.SessionRequired())
	r.Use(middleware.CSRF())
	session := middleware.GlobalSessionStore.Create("test-panel-owner")
	t.Cleanup(func() { middleware.GlobalSessionStore.Delete(session.Token) })
	r.GET("/websites/:id/anomaly-monitor", h.Handle)
	r.PUT("/websites/:id/anomaly-monitor", h.Handle)
	r.POST("/websites/:id/anomaly-monitor/check", h.Handle)
	call := func(method, path, body string, csrf bool) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "wp_session", Value: session.Token})
		if csrf {
			req.Header.Set("X-CSRF-Token", "fixture")
			req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "fixture"})
		}
		r.ServeHTTP(w, req)
		return w
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/websites/1/anomaly-monitor", nil))
	if w.Code == 200 {
		t.Fatal("unauthenticated access allowed")
	}
	w = call("GET", "/websites/1/anomaly-monitor", "", false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"threshold":5`) || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := database.GetDB().Exec(`INSERT INTO site_wp_anomaly_state(site_id,admins) VALUES(1,?) ON CONFLICT(site_id) DO UPDATE SET admins=excluded.admins`, `[{"id":1,"login":"owner","roles":["administrator"],"email_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","display_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","credential_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}]`); err != nil {
		t.Fatal(err)
	}
	w = call("GET", "/websites/1/anomaly-monitor", "", false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"login":"owner"`) || strings.Contains(w.Body.String(), "_hash") {
		t.Fatal("sensitive anomaly baseline exposed", w.Code, w.Body.String())
	}
	if w = call("PUT", "/websites/1/anomaly-monitor", `{"enabled":false,"threshold":10}`, false); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w = call("PUT", "/websites/1/anomaly-monitor", `{"enabled":false,"threshold":10}`, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, body := range []string{`{"threshold":0}`, `{"threshold":2,"site_id":2}`, `{"threshold":3.5}`} {
		if w = call("PUT", "/websites/1/anomaly-monitor", body, true); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w = call("POST", "/websites/1/anomaly-monitor/check", "", true); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w = call("GET", "/websites/999/anomaly-monitor", "", false); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if _, err := database.GetDB().Exec(`UPDATE websites SET site_type='php' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if w = call("GET", "/websites/1/anomaly-monitor", "", false); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
