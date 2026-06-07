package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyDBBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "panel_valid.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE sample (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec("INSERT INTO sample (name) VALUES (?)", "ok"); err != nil {
		t.Fatalf("insert sample: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	if err := VerifyDBBackup(dbPath); err != nil {
		t.Fatalf("VerifyDBBackup(valid) error = %v", err)
	}
}

func TestVerifyDBBackupRejectsInvalidSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel_bad.db")
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0600); err != nil {
		t.Fatalf("write invalid db: %v", err)
	}

	if err := VerifyDBBackup(path); err == nil {
		t.Fatal("VerifyDBBackup(invalid) error = nil, want error")
	}
}

func TestRestoreDBBackupPathValidatesFilename(t *testing.T) {
	dir := t.TempDir()
	validName := "panel_20260107_023000.db"
	if err := os.WriteFile(filepath.Join(dir, validName), []byte("x"), 0600); err != nil {
		t.Fatalf("write valid backup: %v", err)
	}

	tests := []struct {
		name    string
		wantErr bool
	}{
		{name: validName, wantErr: false},
		{name: "panel_missing.db", wantErr: true},
		{name: "../" + validName, wantErr: true},
		{name: `..\` + validName, wantErr: true},
		{name: "evil.db", wantErr: true},
		{name: "panel_20260107_023000.txt", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := RestoreDBBackupPath(dir, tt.name)
			if (err != nil) != tt.wantErr {
				t.Fatalf("RestoreDBBackupPath(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
		})
	}
}
