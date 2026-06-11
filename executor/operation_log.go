package executor

import (
	"database/sql"
	"log"

	"github.com/naibabiji/wp-panel/database"
)

const operationLogKeepRows = 300

func recordOperationLog(operation, target, status, message string) {
	db := database.GetDB()
	if db == nil {
		return
	}
	if _, err := db.Exec(
		"INSERT INTO operation_logs (operation, target, status, message) VALUES (?, ?, ?, ?)",
		operation, target, status, message,
	); err != nil {
		log.Printf("operation log insert skipped: %v", err)
		return
	}
	pruneOperationLogs(db)
}

func EnsureOperationLogRetention() {
	db := database.GetDB()
	if db == nil {
		return
	}
	pruneOperationLogs(db)
}

func pruneOperationLogs(db *sql.DB) {
	if db == nil {
		return
	}
	if _, err := db.Exec(`DELETE FROM operation_logs
		WHERE id NOT IN (
			SELECT id FROM operation_logs ORDER BY id DESC LIMIT ?
		)`, operationLogKeepRows); err != nil {
		log.Printf("operation log prune skipped: %v", err)
	}
}
