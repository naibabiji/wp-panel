package executor

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDumpDatabaseToGzipRejectsInvalidDatabaseName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.sql.gz")
	badNames := []string{
		"db; rm -rf /",
		"db name",
		"db-name",
		"db`name",
		strings.Repeat("a", 65),
	}

	for _, name := range badNames {
		if err := dumpDatabaseToGzip(name, "secret", path); err == nil {
			t.Fatalf("dumpDatabaseToGzip(%q) error = nil, want error", name)
		}
	}
}

func TestDumpDatabaseToGzipDoesNotOverwriteExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.sql.gz")
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatalf("seed backup file: %v", err)
	}

	err := dumpDatabaseToGzip("valid_db", "secret", path)
	if err == nil {
		t.Fatal("dumpDatabaseToGzip existing file error = nil, want error")
	}

	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read backup file: %v", readErr)
	}
	if string(data) != "existing" {
		t.Fatalf("backup file overwritten: %q", string(data))
	}
}

func TestValidateRestoreBackupFileAcceptsWordPressSQL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.sql")
	sql := "DROP TABLE IF EXISTS `nb_options`;\n" +
		"CREATE TABLE `nb_options` (`option_id` bigint unsigned NOT NULL AUTO_INCREMENT, PRIMARY KEY (`option_id`));\n" +
		"INSERT INTO `nb_options` (`option_id`) VALUES (1);\n"
	if err := os.WriteFile(path, []byte(sql), 0600); err != nil {
		t.Fatalf("write sql: %v", err)
	}

	if err := validateRestoreBackupFile(path); err != nil {
		t.Fatalf("validateRestoreBackupFile valid sql error = %v", err)
	}
}

func TestValidateRestoreBackupFileAcceptsWordPressGzip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.sql.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create gzip: %v", err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write([]byte("CREATE TABLE `wp_options` (`option_id` bigint unsigned NOT NULL);\nINSERT INTO `wp_options` (`option_id`) VALUES (1);\n")); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	if err := validateRestoreBackupFile(path); err != nil {
		t.Fatalf("validateRestoreBackupFile valid gzip error = %v", err)
	}
}

func TestValidateRestoreBackupFileAcceptsGenericSQL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.sql")
	sql := "CREATE TABLE `app_settings` (`id` int NOT NULL, PRIMARY KEY (`id`));\n"
	if err := os.WriteFile(path, []byte(sql), 0600); err != nil {
		t.Fatalf("write sql: %v", err)
	}

	if err := validateRestoreBackupFile(path); err != nil {
		t.Fatalf("validateRestoreBackupFile generic sql error = %v", err)
	}
}

func TestValidateRestoreBackupFileAcceptsVeryLongInsertLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.sql")
	longValue := strings.Repeat("x", 2*1024*1024)
	sql := "CREATE TABLE `app_settings` (`value` longtext);\n" +
		"INSERT INTO `app_settings` (`value`) VALUES ('" + longValue + "');\n"
	if err := os.WriteFile(path, []byte(sql), 0600); err != nil {
		t.Fatalf("write sql: %v", err)
	}

	if err := validateRestoreBackupFile(path); err != nil {
		t.Fatalf("validateRestoreBackupFile long insert error = %v", err)
	}
}

func TestValidateRestoreBackupFileRejectsDangerousSQL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.sql")
	sql := "CREATE DATABASE other_db;\nCREATE TABLE `nb_options` (`option_id` int);\nINSERT INTO `nb_options` VALUES (1);\n"
	if err := os.WriteFile(path, []byte(sql), 0600); err != nil {
		t.Fatalf("write sql: %v", err)
	}

	err := validateRestoreBackupFile(path)
	if err == nil {
		t.Fatal("validateRestoreBackupFile dangerous sql error = nil, want error")
	}
	if !strings.Contains(err.Error(), "跨数据库") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRestoreBackupFileRejectsDangerousSQLVariants(t *testing.T) {
	tests := map[string]string{
		"double_space":  "CREATE  DATABASE other_db;",
		"newline":       "CREATE\nDATABASE other_db;",
		"block_comment": "CREATE /* comment */ DATABASE other_db;",
		"use":           "USE other_db;",
		"definer":       "CREATE DEFINER=`root`@`localhost` VIEW v AS SELECT 1;",
	}

	for name, dangerous := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "backup.sql")
			sql := dangerous + "\nCREATE TABLE `app_settings` (`id` int);\n"
			if err := os.WriteFile(path, []byte(sql), 0600); err != nil {
				t.Fatalf("write sql: %v", err)
			}

			if err := validateRestoreBackupFile(path); err == nil {
				t.Fatal("validateRestoreBackupFile dangerous variant error = nil, want error")
			}
		})
	}
}

func TestValidateRestoreBackupFileRejectsSQLWithoutSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.sql")
	sql := "INSERT INTO `other_table` VALUES (1);\n"
	if err := os.WriteFile(path, []byte(sql), 0600); err != nil {
		t.Fatalf("write sql: %v", err)
	}

	err := validateRestoreBackupFile(path)
	if err == nil {
		t.Fatal("validateRestoreBackupFile schema-less sql error = nil, want error")
	}
	if !strings.Contains(err.Error(), "建表") {
		t.Fatalf("unexpected error: %v", err)
	}
}
