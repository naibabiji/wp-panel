package executor

import (
	"database/sql"
	"fmt"
	"testing"

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
