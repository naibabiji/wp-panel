package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
)

func TestCompanionPluginStatusHandler(t *testing.T) {
	setupCacheHelperTestDB(t)
	if _, err := database.GetDB().Exec(`UPDATE websites SET web_root=? WHERE id=1`, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	h := &WebsiteHandler{}
	r.GET("/websites/:id/install-plugin/status", h.InstallPluginStatus)
	check := func(path string, code int, status string) {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != code || (status != "" && !strings.Contains(w.Body.String(), `"status":"`+status+`"`)) {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	check("/websites/1/install-plugin/status", 200, "not_installed")
	if !executor.TryAcquireSiteOpLock(1, "test") {
		t.Fatal("lock")
	}
	check("/websites/1/install-plugin/status", 200, "unknown")
	executor.ReleaseSiteOpLock(1)
	check("/websites/999/install-plugin/status", 404, "")
	if _, err := database.GetDB().Exec(`UPDATE websites SET site_type='php' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	check("/websites/1/install-plugin/status", 404, "")
}
