package executor

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreWPCoreDatabaseUsesBoundedArgumentsAndSanitizedDump(t *testing.T) {
	dir := t.TempDir()
	audit := filepath.Join(dir, "stdin.sql")
	mysql := filepath.Join(dir, "mysql")
	script := "#!/bin/bash\nif [[ \"$*\" == *INFORMATION_SCHEMA* ]]; then echo 'DROP TABLE IF EXISTS `wp_posts`;'; exit 0; fi\ncat > \"" + audit + "\"\n"
	if err := os.WriteFile(mysql, []byte(script), 0555); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "database.sql.gz")
	f, err := os.Create(backup)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	_, _ = gz.Write([]byte("CREATE TABLE `wp_posts` (`id` int);\nINSERT INTO `wp_posts` VALUES (1);\n"))
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := restoreWPCoreDatabase(context.Background(), mysql, "wordpress_db", "secret", backup); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"SET FOREIGN_KEY_CHECKS=0", "DROP TABLE IF EXISTS `wp_posts`", "CREATE TABLE `wp_posts`", "SET FOREIGN_KEY_CHECKS=1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("restore input missing %q: %s", want, text)
		}
	}
}

func TestDefaultWPCoreFilesRestorerRestoresOnlyCore(t *testing.T) {
	webRoot := filepath.Join(t.TempDir(), "wordpress")
	if err := os.Mkdir(webRoot, 0755); err != nil {
		t.Fatal(err)
	}
	writeWordPressCoreFixture(t, webRoot)
	backup := filepath.Join(t.TempDir(), "core.tar.gz")
	if err := archiveWordPressCore(webRoot, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "wp-admin", "admin.php"), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "wp-includes", "new.php"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.php"), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "wp-content", "plugin.php"), []byte("keep-user-content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "wp-config.php"), []byte("keep-config"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := defaultWPCoreFilesRestorer(context.Background(), webRoot, backup, "wpu_0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	assertFileText(t, filepath.Join(webRoot, "wp-admin", "admin.php"), "admin")
	if _, err := os.Stat(filepath.Join(webRoot, "wp-includes", "new.php")); !os.IsNotExist(err) {
		t.Fatalf("obsolete core file remains: %v", err)
	}
	assertFileText(t, filepath.Join(webRoot, "index.php"), "core")
	assertFileText(t, filepath.Join(webRoot, "wp-content", "plugin.php"), "keep-user-content")
	assertFileText(t, filepath.Join(webRoot, "wp-config.php"), "keep-config")
	entries, err := os.ReadDir(webRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".wp-panel-core-restore-") {
			t.Fatalf("restore directory remains: %s", entry.Name())
		}
	}
}

func TestDefaultWPCoreFilesRestorerRejectsUnexpectedArchivePath(t *testing.T) {
	webRoot := filepath.Join(t.TempDir(), "wordpress")
	if err := os.Mkdir(webRoot, 0755); err != nil {
		t.Fatal(err)
	}
	writeWordPressCoreFixture(t, webRoot)
	backup := filepath.Join(t.TempDir(), "bad.tar.gz")
	writeCoreRestoreTar(t, backup, map[string]string{"wp-admin/admin.php": "admin", "wp-config.php": "malicious"})
	if err := defaultWPCoreFilesRestorer(context.Background(), webRoot, backup, "wpu_0123456789abcdef0123456789abcdef"); err == nil {
		t.Fatal("expected archive rejection")
	}
	assertFileText(t, filepath.Join(webRoot, "wp-config.php"), "secret")
}

func TestDefaultWPCoreFilesRestorerRejectsExistingTransactionDirectory(t *testing.T) {
	webRoot := filepath.Join(t.TempDir(), "wordpress")
	if err := os.Mkdir(webRoot, 0755); err != nil {
		t.Fatal(err)
	}
	writeWordPressCoreFixture(t, webRoot)
	backup := filepath.Join(t.TempDir(), "core.tar.gz")
	if err := archiveWordPressCore(webRoot, backup); err != nil {
		t.Fatal(err)
	}
	taskID := "wpu_0123456789abcdef0123456789abcdef"
	if err := os.Mkdir(filepath.Join(webRoot, ".wp-panel-core-restore-stage-"+taskID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := defaultWPCoreFilesRestorer(context.Background(), webRoot, backup, taskID); err == nil {
		t.Fatal("expected stale transaction rejection")
	}
}

func writeCoreRestoreTar(t *testing.T, name string, files map[string]string) {
	t.Helper()
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for path, data := range files {
		h := &tar.Header{Name: path, Mode: 0644, Size: int64(len(data)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
func assertFileText(t *testing.T, name, want string) {
	t.Helper()
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s=%q want %q", name, got, want)
	}
}
