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
	// wp_update_tasks 的 batch_id/auto_rollback 相关索引和触发器不能放进上面这份每次
	// 启动都无条件执行的语句列表：老安装的 wp_update_tasks 表是在这两个字段存在之前
	// 建的，这里执行时字段还没被 upgrades.go 的 ALTER TABLE 补上，会直接报 "no such
	// column" 崩溃退出。ensureWPUpdateBatchSchema 本身是幂等的（ALTER 前会先检查字段
	// 是否已存在，索引/触发器都是 IF NOT EXISTS），所以这里可以无条件调用：新装数据库
	// 此时表和字段都已经建好，直接把索引/触发器补上；老安装则要等 RunUpgrades() 里
	// 版本化的同名步骤先把字段加上——两边最终都会收敛到同一个正确状态。
	if err := ensureWPUpdateBatchSchema(); err != nil {
		return fmt.Errorf("migration failed: %w", err)
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
