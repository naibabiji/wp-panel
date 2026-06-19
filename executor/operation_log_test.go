package executor

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
	_ "modernc.org/sqlite"
)

func TestPruneOperationLogsKeepsNewestRows(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE operation_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		operation TEXT,
		target TEXT,
		status TEXT,
		message TEXT
	)`); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= operationLogKeepRows+2; i++ {
		if _, err := db.Exec("INSERT INTO operation_logs (operation, target, status, message) VALUES ('op', 'target', 'ok', ?)", fmt.Sprintf("msg-%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	pruneOperationLogs(db)

	var count, minID, maxID int
	if err := db.QueryRow("SELECT COUNT(*), MIN(id), MAX(id) FROM operation_logs").Scan(&count, &minID, &maxID); err != nil {
		t.Fatal(err)
	}
	if count != operationLogKeepRows {
		t.Fatalf("count = %d, want %d", count, operationLogKeepRows)
	}
	if minID != 3 || maxID != operationLogKeepRows+2 {
		t.Fatalf("kept id range = %d..%d, want 3..%d", minID, maxID, operationLogKeepRows+2)
	}
}

func TestLogOpRecordsBackupTargets(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE operation_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		operation TEXT,
		target TEXT,
		status TEXT,
		message TEXT
	)`); err != nil {
		t.Fatal(err)
	}

	oldDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = oldDB })

	site := &models.Website{ID: 10, Domain: "example.com"}
	logOp(&Task{Type: TaskCreateBackup, Payload: &CreateBackupPayload{Site: site}}, TaskResult{Success: true, Message: "ok"})
	logOp(&Task{Type: TaskRestoreBackup, Payload: &RestoreBackupPayload{Site: site}}, TaskResult{Success: false, Message: "failed"})

	rows, err := db.Query(`SELECT operation, target, status FROM operation_logs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	got := []string{}
	for rows.Next() {
		var operation, target, status string
		if err := rows.Scan(&operation, &target, &status); err != nil {
			t.Fatal(err)
		}
		got = append(got, operation+"|"+target+"|"+status)
	}
	want := []string{
		string(TaskCreateBackup) + "|example.com|success",
		string(TaskRestoreBackup) + "|example.com|failed",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("operation logs = %v, want %v", got, want)
	}
}
