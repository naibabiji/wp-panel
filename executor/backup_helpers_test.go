package executor

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// countingWriter records every underlying Write call it receives, so tests can
// assert on IPC/syscall granularity instead of just output content.
type countingWriter struct {
	buf   bytes.Buffer
	calls int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.calls++
	return c.buf.Write(p)
}

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

func TestValidateRestoreBackupFileAllowsDangerousTextInsideData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.sql")
	sql := "CREATE TABLE `posts` (`content` longtext);\n" +
		"INSERT INTO `posts` (`content`) VALUES ('CREATE DATABASE example; USE example; DEFINER=not_a_statement;');\n"
	if err := os.WriteFile(path, []byte(sql), 0600); err != nil {
		t.Fatalf("write sql: %v", err)
	}

	if err := validateRestoreBackupFile(path); err != nil {
		t.Fatalf("validateRestoreBackupFile data text error = %v", err)
	}
}

func TestValidateRestoreBackupFileAllowsCommonDatabaseDumpStatements(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.sql")
	sql := "CREATE DATABASE other_db;\nCREATE TABLE `nb_options` (`option_id` int);\nINSERT INTO `nb_options` VALUES (1);\n"
	if err := os.WriteFile(path, []byte(sql), 0600); err != nil {
		t.Fatalf("write sql: %v", err)
	}

	err := validateRestoreBackupFile(path)
	if err != nil {
		t.Fatalf("validateRestoreBackupFile common dump error = %v", err)
	}
}

func TestWriteSanitizedRestoreSQLSkipsDangerousStatements(t *testing.T) {
	tests := map[string]string{
		"double_space":  "CREATE  DATABASE other_db;",
		"newline":       "CREATE\nDATABASE other_db;",
		"block_comment": "CREATE /* comment */ DATABASE other_db;",
		"use":           "USE other_db;",
		"definer":       "CREATE DEFINER=`root`@`localhost` VIEW v AS SELECT 1;",
	}

	for name, dangerous := range tests {
		t.Run(name, func(t *testing.T) {
			sql := dangerous + "\nCREATE TABLE `app_settings` (`id` int);\n"
			var out bytes.Buffer
			if err := writeSanitizedRestoreSQL(&out, strings.NewReader(sql)); err != nil {
				t.Fatalf("writeSanitizedRestoreSQL error = %v", err)
			}
			got := out.String()
			if strings.Contains(strings.ToUpper(got), "CREATE DATABASE") || strings.Contains(strings.ToUpper(got), "DROP DATABASE") || strings.Contains(strings.ToUpper(got), "USE OTHER_DB") || strings.Contains(strings.ToUpper(got), "DEFINER=") {
				t.Fatalf("sanitized SQL still contains dangerous statement: %q", got)
			}
			if !strings.Contains(got, "CREATE TABLE") {
				t.Fatalf("sanitized SQL missing safe statement: %q", got)
			}
		})
	}
}

func TestWriteSanitizedRestoreSQLPreservesDangerousTextInsideData(t *testing.T) {
	sql := "CREATE TABLE `posts` (`content` longtext);\n" +
		"INSERT INTO `posts` (`content`) VALUES ('CREATE DATABASE example; USE example; DEFINER=not_a_statement;');\n"
	var out bytes.Buffer
	if err := writeSanitizedRestoreSQL(&out, strings.NewReader(sql)); err != nil {
		t.Fatalf("writeSanitizedRestoreSQL error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "CREATE DATABASE example") || !strings.Contains(got, "DEFINER=not_a_statement") {
		t.Fatalf("data text was not preserved: %q", got)
	}
}

func TestWriteSanitizedRestoreSQLPreservesSafeSQL(t *testing.T) {
	sql := "CREATE TABLE `posts` (`content` longtext, `title` varchar(255));\n" +
		"INSERT INTO `posts` (`content`, `title`) VALUES ('It\\'s a CREATE DATABASE example; text', 'hello; world');\n"
	var out bytes.Buffer
	if err := writeSanitizedRestoreSQL(&out, strings.NewReader(sql)); err != nil {
		t.Fatalf("writeSanitizedRestoreSQL error = %v", err)
	}
	if got := out.String(); got != sql {
		t.Fatalf("safe SQL changed:\ngot:  %q\nwant: %q", got, sql)
	}
}

func TestWriteSanitizedRestoreSQLKeepsForeignKeyTables(t *testing.T) {
	sql := "CREATE TABLE `child` (`parent_id` int, CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`));\n" +
		"CREATE TABLE `parent` (`id` int PRIMARY KEY);\n"
	var out bytes.Buffer
	if err := writeSanitizedRestoreSQL(&out, strings.NewReader(sql)); err != nil {
		t.Fatalf("writeSanitizedRestoreSQL error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "FOREIGN KEY") || !strings.Contains(got, "CREATE TABLE `parent`") {
		t.Fatalf("foreign key SQL was not preserved: %q", got)
	}
}

func TestWriteSanitizedRestoreSQLWithoutBufferingWritesPerByte(t *testing.T) {
	longValue := strings.Repeat("x", 200*1024)
	sql := "CREATE TABLE `app_settings` (`value` longtext);\n" +
		"INSERT INTO `app_settings` (`value`) VALUES ('" + longValue + "');\n"

	dst := &countingWriter{}
	if err := writeSanitizedRestoreSQL(dst, strings.NewReader(sql)); err != nil {
		t.Fatalf("writeSanitizedRestoreSQL error = %v", err)
	}
	if dst.buf.String() != sql {
		t.Fatalf("unbuffered output mismatch: got %d bytes, want %d bytes", dst.buf.Len(), len(sql))
	}
	// Documents writeSanitizedRestoreSQL's current contract: it writes
	// non-skipped bytes to dst one at a time and relies entirely on the
	// caller to buffer. This is why every restore path must go through
	// filterRestoreSQLBuffered instead of calling writeSanitizedRestoreSQL
	// directly against a real pipe. If the filter itself is later changed to
	// batch its own output, this threshold (not the requirement to buffer
	// upstream of a pipe) is what should be relaxed.
	if dst.calls < len(sql)/2 {
		t.Fatalf("expected near-per-byte Write call count without buffering, got %d calls for %d bytes", dst.calls, len(sql))
	}
}

// TestFilterRestoreSQLBufferedCoalescesWrites exercises filterRestoreSQLBuffered
// itself — the exact helper both restoreSQLReader and restoreWPCoreDatabase
// call against mysql's stdin pipe — rather than re-deriving buffering in the
// test. That way, if buffering is ever removed from the production helper,
// this test fails instead of continuing to pass against a test-local buffer.
func TestFilterRestoreSQLBufferedCoalescesWrites(t *testing.T) {
	longValue := strings.Repeat("x", 200*1024)
	sql := "CREATE TABLE `app_settings` (`value` longtext);\n" +
		"INSERT INTO `app_settings` (`value`) VALUES ('" + longValue + "');\n"

	dst := &countingWriter{}
	if err := filterRestoreSQLBuffered(dst, strings.NewReader(sql)); err != nil {
		t.Fatalf("filterRestoreSQLBuffered error = %v", err)
	}
	if dst.buf.String() != sql {
		t.Fatalf("buffered output mismatch: got %d bytes, want %d bytes", dst.buf.Len(), len(sql))
	}
	// The underlying Write call count must stay in the "number of flushed
	// chunks" range (driven by restoreSQLWriteBufferSize), not the
	// "number of bytes" range that a raw pipe write would produce.
	maxExpectedCalls := len(sql)/restoreSQLWriteBufferSize + 8
	if dst.calls > maxExpectedCalls {
		t.Fatalf("expected buffering to coalesce writes, got %d calls for %d bytes (want <= %d)", dst.calls, len(sql), maxExpectedCalls)
	}
}

// failingWriter simulates a broken destination (e.g. mysql's stdin pipe
// closing early): it accepts up to limit bytes total and then fails, so
// tests can force bufio.Writer.Flush to surface a write error.
type failingWriter struct {
	limit int
	n     int
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.n+len(p) > f.limit {
		return 0, fmt.Errorf("simulated write failure")
	}
	f.n += len(p)
	return len(p), nil
}

func TestFilterRestoreSQLBufferedPropagatesFlushError(t *testing.T) {
	sql := "CREATE TABLE `app_settings` (`id` int);\n"
	dst := &failingWriter{limit: 4}
	if err := filterRestoreSQLBuffered(dst, strings.NewReader(sql)); err == nil {
		t.Fatal("filterRestoreSQLBuffered error = nil, want flush error to propagate")
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
