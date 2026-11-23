package executor

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLRestoreWaitsForClientAfterReadingInput(t *testing.T) {
	dir := t.TempDir()
	client := filepath.Join(dir, "mysql")
	release := filepath.Join(dir, "release")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RESTORE_TEST_RELEASE", release)
	if err := os.WriteFile(client, []byte("#!/bin/sh\ncat >/dev/null\nwhile [ ! -f \"$RESTORE_TEST_RELEASE\" ]; do sleep 0.01; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })

	sql := "CREATE TABLE example (id INT);\nINSERT INTO example VALUES (1),\n(2);\n"
	var progress sqlRestoreProgress
	done := make(chan TaskResult, 1)
	go func() {
		done <- restoreSQLReader(strings.NewReader(sql), "fixture", "", int64(len(sql)), &progress)
	}()
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(time.Millisecond) {
		snapshot := progress.snapshot()
		if snapshot != nil && snapshot.Waiting {
			if snapshot.BytesRead != int64(len(sql)) || snapshot.TotalBytes != snapshot.BytesRead || snapshot.LastAdvanceAt == 0 {
				t.Fatalf("invalid final read progress: %+v", snapshot)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiting stage was never published")
		}
	}
	select {
	case result := <-done:
		t.Fatalf("reported completion before client exited: %+v", result)
	default:
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if !result.Success {
			t.Fatalf("restore failed: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("restore did not complete")
	}
}

func TestRestoreBackupSQLSizeUsesFullGzipValidation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "backup.sql.gz")
	out, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, sql := range []string{"CREATE TABLE example (id INT);\n", "INSERT INTO example VALUES (1), (2);\n"} {
		gz := gzip.NewWriter(out)
		if _, err := gz.Write([]byte(sql)); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		total += int64(len(sql))
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	size, err := restoreBackupSQLSize(file)
	if err != nil || size != total {
		t.Fatalf("SQL size = %d, want %d (error %v)", size, total, err)
	}
}

func TestMySQLRestoreErrorReportsPacketLimitWithoutExposingSQL(t *testing.T) {
	message := mysqlRestoreErrorMessage("ERROR 1153 (08S01) at line 2: rejected data: private-value")
	if !strings.Contains(message, "1153") || !strings.Contains(message, "max_allowed_packet") || strings.Contains(message, "private-value") {
		t.Fatalf("unexpected diagnostic: %q", message)
	}
}

func TestClearDatabaseTablesKeepsClientWarningsOutOfSQL(t *testing.T) {
	portableTestService(t)
	dir := t.TempDir()
	client := filepath.Join(dir, "mysql")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RESTORE_TEST_CAPTURE", filepath.Join(dir, "captured.sql"))
	script := `#!/bin/sh
if [ "$3" = "-B" ]; then
	printf 'deprecated client name\n' >&2
	printf 'DROP TABLE IF EXISTS fixture;\n'
else
	cat > "$RESTORE_TEST_CAPTURE"
fi
`
	if err := os.WriteFile(client, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ClearDatabaseTables(1, "fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	sql, err := os.ReadFile(os.Getenv("RESTORE_TEST_CAPTURE"))
	if err != nil || !strings.Contains(string(sql), "DROP TABLE IF EXISTS fixture;") || strings.Contains(string(sql), "deprecated") {
		t.Fatalf("unexpected cleanup SQL: %q (error %v)", sql, err)
	}
}
