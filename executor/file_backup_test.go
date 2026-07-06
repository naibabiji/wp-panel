package executor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func TestCleanOldBackupsRemovesRotatedFileBackupsRows(t *testing.T) {
	openTestDB(t)
	insertMinimalWebsite(t, "rotate.example.com")

	dir := t.TempDir()
	names := []string{
		"file_full_20260101_000000.tar.gz",
		"file_full_20260102_000000.tar.gz",
		"file_full_20260103_000000.tar.gz",
	}
	base := time.Now().Add(-time.Hour)
	for i, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		mt := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
		if _, err := database.GetDB().Exec(`INSERT INTO file_backups (site_id, filename, file_size, mode) VALUES (1, ?, 1, 'full')`, name); err != nil {
			t.Fatalf("insert file_backups %s: %v", name, err)
		}
	}

	cleanOldBackups(dir, 2, 1)

	if _, err := os.Stat(filepath.Join(dir, names[0])); !os.IsNotExist(err) {
		t.Fatalf("oldest backup file should have been removed from disk, stat err = %v", err)
	}
	var stillHasOldest int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM file_backups WHERE filename = ?`, names[0]).Scan(&stillHasOldest); err != nil {
		t.Fatalf("query file_backups: %v", err)
	}
	if stillHasOldest != 0 {
		t.Fatal("file_backups row for rotated-out backup should have been deleted alongside the file")
	}

	for _, name := range names[1:] {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("kept backup file missing on disk: %s: %v", name, err)
		}
		var count int
		if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM file_backups WHERE filename = ?`, name).Scan(&count); err != nil {
			t.Fatalf("query file_backups: %v", err)
		}
		if count != 1 {
			t.Fatalf("file_backups row for kept backup %s missing", name)
		}
	}
}

func TestRecordFileBackupInsertsRow(t *testing.T) {
	openTestDB(t)
	insertMinimalWebsite(t, "record-ok.example.com")

	recordFileBackup(1, "file_full_20260101_000000.tar.gz", 123, "full", "record-ok.example.com")

	var size int64
	var mode string
	if err := database.GetDB().QueryRow(`SELECT file_size, mode FROM file_backups WHERE site_id = 1 AND filename = ?`,
		"file_full_20260101_000000.tar.gz").Scan(&size, &mode); err != nil {
		t.Fatalf("query file_backups: %v", err)
	}
	if size != 123 || mode != "full" {
		t.Fatalf("file_backups row = (%d, %q), want (123, \"full\")", size, mode)
	}
}

func TestRecordFileBackupLogsWithoutPanicOnInsertFailure(t *testing.T) {
	openTestDB(t)
	insertMinimalWebsite(t, "record-fail.example.com")
	if _, err := database.GetDB().Exec(`DROP TABLE file_backups`); err != nil {
		t.Fatalf("drop file_backups: %v", err)
	}

	// file_backups 写入失败不应 panic，也不应影响调用方（已生成的备份文件保留）；
	// 失败情况通过日志可见，而不是静默吞掉。
	recordFileBackup(1, "file_full_20260101_000000.tar.gz", 123, "full", "record-fail.example.com")
}
