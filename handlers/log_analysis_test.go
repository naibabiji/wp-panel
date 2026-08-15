package handlers

import (
	"database/sql"
	"testing"

	"github.com/naibabiji/wp-panel/models"
	_ "modernc.org/sqlite"
)

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
