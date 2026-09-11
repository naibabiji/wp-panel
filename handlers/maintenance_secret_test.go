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

func TestMaintenanceSecretRequiresOwnerAndCSRF(t *testing.T) {
	setupCacheHelperTestDB(t)
	m := executor.NewMaintenanceManager(database.GetDB())
	if err := m.Configure(1, true, 5, "1234"); err != nil {
		t.Fatal(err)
	}
	h := &MaintenanceHandler{Manager: m}
	r := gin.New()
	r.Use(middleware.SessionRequired(), middleware.CSRF())
	r.POST("/sites/:id/password", h.Password)
	s := middleware.GlobalSessionStore.Create("owner")
	defer middleware.GlobalSessionStore.Delete(s.Token)
	for _, tc := range []struct {
		session, csrf bool
		code          int
	}{{false, false, 401}, {true, false, 403}, {true, true, 200}} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/sites/1/password", nil)
		if tc.session {
			req.AddCookie(&http.Cookie{Name: "wp_session", Value: s.Token})
		}
		if tc.csrf {
			req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "fixture"})
			req.Header.Set("X-CSRF-Token", "fixture")
		}
		r.ServeHTTP(w, req)
		if tc.code == 200 {
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"password":"1234"`) || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w.Code, w.Body.String())
			}
		} else if w.Code == 200 || strings.Contains(w.Body.String(), "1234") {
			t.Fatal("secret access without auth/CSRF")
		}
	}
}
