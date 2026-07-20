package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var DB *sql.DB

func Open(dbPath string) error {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create database directory: %w", err)
	}

	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-8000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("failed to open sqlite: %w", err)
	}
	initialized := false
	defer func() {
		if !initialized {
			_ = db.Close()
		}
	}()

	// WAL 模式下允许多个连接并行读取，单连接会成为全站瓶颈
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		return fmt.Errorf("sqlite ping failed: %w", err)
	}

	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("failed to verify foreign keys: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("failed to verify foreign keys: got %d, want 1", foreignKeys)
	}

	DB = db
	initialized = true
	return nil
}

func RunMigrations() error {
	for _, stmt := range migrations {
		if _, err := DB.Exec(stmt); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, stmt[:100])
		}
	}
	return nil
}

func GetDB() *sql.DB {
	return DB
}

func Close() error {
	if DB != nil {
		err := DB.Close()
		DB = nil
		return err
	}
	return nil
}
