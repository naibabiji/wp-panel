package database

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestOpenEnablesForeignKeysOnEveryConnection(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}

	ctx := context.Background()
	conns := make([]*sql.Conn, 0, 4)
	for i := 0; i < 4; i++ {
		conn, err := DB.Conn(ctx)
		if err != nil {
			t.Fatalf("DB.Conn() #%d error = %v", i+1, err)
		}
		conns = append(conns, conn)
		defer conn.Close()

		var enabled int
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); err != nil {
			t.Fatalf("query foreign_keys on connection #%d: %v", i+1, err)
		}
		if enabled != 1 {
			t.Fatalf("foreign_keys on connection #%d = %d, want 1", i+1, enabled)
		}
	}

	for i, conn := range conns {
		domain := fmt.Sprintf("foreign-key-%d.example.com", i+1)
		result, err := conn.ExecContext(ctx, `INSERT INTO websites
			(name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
			VALUES (?, ?, 'active', 'wp_fk_test', '/tmp/www', '/tmp/log', 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf')`, domain, domain)
		if err != nil {
			t.Fatalf("insert website on connection #%d: %v", i+1, err)
		}
		siteID, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("website id on connection #%d: %v", i+1, err)
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO file_security_events (site_id) VALUES (?)", siteID); err != nil {
			t.Fatalf("insert child on connection #%d: %v", i+1, err)
		}
		if _, err := conn.ExecContext(ctx, "DELETE FROM websites WHERE id = ?", siteID); err != nil {
			t.Fatalf("delete website on connection #%d: %v", i+1, err)
		}

		var children int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM file_security_events WHERE site_id = ?", siteID).Scan(&children); err != nil {
			t.Fatalf("count children on connection #%d: %v", i+1, err)
		}
		if children != 0 {
			t.Fatalf("children after cascade on connection #%d = %d, want 0", i+1, children)
		}
	}
}

func TestOpenFailureDoesNotSetGlobalDB(t *testing.T) {
	if DB != nil {
		_ = Close()
	}
	DB = nil
	t.Cleanup(func() {
		_ = Close()
		DB = nil
	})

	err := Open(filepath.Clean(t.TempDir()))
	if err == nil {
		t.Fatal("Open() error = nil, want error for directory path")
	}
	if DB != nil {
		t.Fatal("DB was set after Open() failed")
	}
}
