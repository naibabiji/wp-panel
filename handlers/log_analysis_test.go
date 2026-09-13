package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
	_ "modernc.org/sqlite"
)

func TestLogAnalysisDetailsAcceptsOnlyKnownTrafficCategories(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldDB := database.DB
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		database.DB = oldDB
	})
	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)
	line := fmt.Sprintf(`192.0.2.10 - - [%s] "GET /blocked HTTP/1.1" 444 0 "-" "Mozilla/5.0"`, now.Format("02/Jan/2006:15:04:05 -0700"))
	if err := os.WriteFile(filepath.Join(dir, "access.log"), []byte(line+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`INSERT INTO websites
		(id,name,domain,aliases,status,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path,site_type)
		VALUES (1,'example','example.com','','active','wp_example','/www/wwwroot/example',?,'db','dbuser','/tmp/pool','/tmp/nginx','wordpress')`, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`INSERT INTO log_analysis_jobs
		(id,site_id,status,start_at,end_at,use_ai,local_report_json) VALUES (1,1,?,?,?,?,?)`, models.LogAnalysisCompleted, now.Add(-time.Hour), now.Add(time.Hour), false, `{}`); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.GET("/api/log-analysis/:id/details", (&LogAnalysisHandler{}).Details)
	request := func(value string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/log-analysis/1/details?kind=category&value="+value, nil)
		router.ServeHTTP(recorder, req)
		return recorder
	}
	if recorder := request("security_rejected"); recorder.Code != http.StatusOK || !containsJSONNumber(recorder.Body.Bytes(), "total", 1) {
		t.Fatalf("known category status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := request("invented"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid category status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func containsJSONNumber(data []byte, key string, want float64) bool {
	var response struct {
		Data map[string]interface{} `json:"data"`
	}
	if json.Unmarshal(data, &response) != nil {
		return false
	}
	got, ok := response.Data[key].(float64)
	return ok && got == want
}

func TestAcquireLogAnalysisStartSerializesBySite(t *testing.T) {
	releaseFirst, ok := acquireLogAnalysisStart(101)
	if !ok {
		t.Fatal("first start lock was not acquired")
	}
	if _, ok := acquireLogAnalysisStart(101); ok {
		t.Fatal("same site acquired a second start lock")
	}
	releaseOther, ok := acquireLogAnalysisStart(202)
	if !ok {
		t.Fatal("different site should acquire its own start lock")
	}
	releaseOther()
	releaseFirst()

	releaseAgain, ok := acquireLogAnalysisStart(101)
	if !ok {
		t.Fatal("site lock was not released")
	}
	releaseAgain()
}

func TestRecoverStaleLogAnalysisJobsIncludesPending(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE log_analysis_jobs(id INTEGER PRIMARY KEY,status TEXT,error_message TEXT,updated_at DATETIME);
		INSERT INTO log_analysis_jobs VALUES
			(1,'pending','',datetime('now','-31 minutes')),
			(2,'running','',datetime('now','-31 minutes')),
			(3,'pending','',CURRENT_TIMESTAMP),
			(4,'completed','',datetime('now','-31 minutes'))`); err != nil {
		t.Fatal(err)
	}
	if err := recoverStaleLogAnalysisJobs(db); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int]string{1: models.LogAnalysisFailed, 2: models.LogAnalysisFailed, 3: models.LogAnalysisPending, 4: models.LogAnalysisCompleted} {
		var got string
		if err := db.QueryRow(`SELECT status FROM log_analysis_jobs WHERE id=?`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("job %d status=%q err=%v, want %q", id, got, err, want)
		}
	}
}
