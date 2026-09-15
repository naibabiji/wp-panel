package handlers

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
)

func extensionRequest(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := &ExtensionHandler{}
	router.PUT("/extensions", h.Save)
	router.DELETE("/extensions/:id", h.Delete)
	router.POST("/extensions/reset", h.Reset)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestExtensionSaveRollsBackWholeRequest(t *testing.T) {
	setupBackupOverviewTestDB(t)
	db := database.GetDB()
	_, _ = db.Exec(`INSERT INTO wp_extension_config(etype,slug,name,enabled) VALUES('plugin','existing','Existing',1)`)
	_, _ = db.Exec(`CREATE TRIGGER reject_extension BEFORE INSERT ON wp_extension_config WHEN NEW.slug='reject' BEGIN SELECT RAISE(ABORT,'test'); END`)

	recorder := extensionRequest(t, http.MethodPut, "/extensions", `[{"etype":"plugin","slug":"first","name":"First","enabled":true},{"etype":"plugin","slug":"reject","name":"Reject","enabled":true}]`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM wp_extension_config WHERE slug='first'`).Scan(&count)
	if count != 0 {
		t.Fatalf("partially saved rows=%d", count)
	}
}

func TestExtensionResetRollsBackWhenDefaultsFail(t *testing.T) {
	setupBackupOverviewTestDB(t)
	db := database.GetDB()
	_, _ = db.Exec(`INSERT INTO wp_extension_config(etype,slug,name,enabled) VALUES('plugin','keep-me','Keep',1)`)
	_, _ = db.Exec(`CREATE TRIGGER reject_default BEFORE INSERT ON wp_extension_config WHEN NEW.slug='elementor' BEGIN SELECT RAISE(ABORT,'test'); END`)
	recorder := extensionRequest(t, http.MethodPost, "/extensions/reset", "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM wp_extension_config WHERE slug='keep-me'`).Scan(&count)
	if count != 1 {
		t.Fatal("original extension list was not restored")
	}
}

func TestExtensionDeleteReportsDatabaseFailure(t *testing.T) {
	setupBackupOverviewTestDB(t)
	db := database.GetDB()
	result, _ := db.Exec(`INSERT INTO wp_extension_config(etype,slug,name,enabled) VALUES('plugin','keep-me','Keep',1)`)
	id, _ := result.LastInsertId()
	_, _ = db.Exec(`CREATE TRIGGER reject_delete BEFORE DELETE ON wp_extension_config BEGIN SELECT RAISE(ABORT,'test'); END`)
	recorder := extensionRequest(t, http.MethodDelete, "/extensions/"+strconv.FormatInt(id, 10), "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
