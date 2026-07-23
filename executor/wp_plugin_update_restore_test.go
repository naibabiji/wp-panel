package executor

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

func TestDefaultWPPluginFilesRestorerReplacesPartialDirectory(t *testing.T) {
	u, err := user.Current()
	if err != nil || u.Uid == "0" {
		t.Skip("non-root current user required")
	}
	web := t.TempDir()
	plugins := filepath.Join(web, "wp-content", "plugins")
	original := filepath.Join(plugins, "sample")
	if err := os.MkdirAll(original, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(original, "sample.php"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(original, "keep.txt"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "plugin.tar.gz")
	if err := archiveWordPressPlugin(web, "sample/sample.php", backup); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(original); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(original, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(original, "partial.php"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	execution := wpPluginUpdateExecution{Task: WPUpdateTask{ID: "wpu_0123456789abcdef0123456789abcdef", ComponentKey: "sample/sample.php"}, WebRoot: web, SystemUser: u.Username, PluginBackup: backup}
	if err := defaultWPPluginFilesRestorer(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(original, "sample.php")); string(data) != "old" {
		t.Fatalf("main=%q", data)
	}
	if _, err := os.Stat(filepath.Join(original, "partial.php")); !os.IsNotExist(err) {
		t.Fatal("partial file remained")
	}
	if data, _ := os.ReadFile(filepath.Join(original, "keep.txt")); string(data) != "keep" {
		t.Fatalf("keep=%q", data)
	}
}

func TestDefaultWPPluginFilesRestorerRejectsUnsafeArchiveEntries(t *testing.T) {
	u, err := user.Current()
	if err != nil || u.Uid == "0" {
		t.Skip("non-root current user required")
	}
	for _, tc := range []struct {
		name     string
		header   tar.Header
		contents string
	}{
		{name: "traversal", header: tar.Header{Name: "../escape.php", Mode: 0644, Typeflag: tar.TypeReg}, contents: "escape"},
		{name: "symlink", header: tar.Header{Name: "sample/link.php", Mode: 0777, Typeflag: tar.TypeSymlink, Linkname: "/tmp/escape"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			web := t.TempDir()
			plugins := filepath.Join(web, "wp-content", "plugins")
			if err := os.MkdirAll(filepath.Join(plugins, "sample"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(plugins, "sample", "sample.php"), []byte("old"), 0644); err != nil {
				t.Fatal(err)
			}
			backup := filepath.Join(t.TempDir(), "plugin.tar.gz")
			writePluginRestoreArchive(t, backup, tc.header, tc.contents)
			execution := wpPluginUpdateExecution{Task: WPUpdateTask{ID: "wpu_0123456789abcdef0123456789abcdef", ComponentKey: "sample/sample.php"}, WebRoot: web, SystemUser: u.Username, PluginBackup: backup}
			if err := defaultWPPluginFilesRestorer(context.Background(), execution); err == nil {
				t.Fatal("unsafe restore archive was accepted")
			}
			if data, err := os.ReadFile(filepath.Join(plugins, "sample", "sample.php")); err != nil || string(data) != "old" {
				t.Fatalf("original changed: data=%q err=%v", data, err)
			}
		})
	}
}

func writePluginRestoreArchive(t *testing.T, path string, header tar.Header, contents string) {
	t.Helper()
	header.Size = int64(len(contents))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&header); err != nil {
		t.Fatal(err)
	}
	if contents != "" {
		if _, err := tw.Write([]byte(contents)); err != nil {
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
