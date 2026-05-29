package handlers

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUploadSessionIDIsStableForResume(t *testing.T) {
	session := uploadSession{
		Filename:     "backup.zip",
		FileSize:     uploadChunkSize + 1,
		TotalChunks:  2,
		SiteID:       7,
		Path:         "/wp-content",
		LastModified: 1770000000000,
		CreatedAt:    1,
	}

	resumed := session
	resumed.CreatedAt = 2
	if makeUploadID(session) != makeUploadID(resumed) {
		t.Fatal("upload ID should be stable for the same file upload")
	}

	otherPath := session
	otherPath.Path = "/uploads"
	if makeUploadID(session) == makeUploadID(otherPath) {
		t.Fatal("upload ID should include the target path")
	}
}

func TestUploadChunkTrackingIgnoresMetadataAndFindsMissingChunks(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"session.json", "chunk-0", "chunk-2", "chunk-2.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	completed := completedUploadChunks(dir, 3)
	if !reflect.DeepEqual(completed, []int{0, 2}) {
		t.Fatalf("completed chunks = %v, want [0 2]", completed)
	}

	missing := missingUploadChunks(dir, 3)
	if !reflect.DeepEqual(missing, []int{1}) {
		t.Fatalf("missing chunks = %v, want [1]", missing)
	}
}

func TestExpectedUploadChunksAllowsEmptyFiles(t *testing.T) {
	tests := []struct {
		size int64
		want int
	}{
		{size: 0, want: 0},
		{size: 1, want: 1},
		{size: uploadChunkSize, want: 1},
		{size: uploadChunkSize + 1, want: 2},
	}

	for _, tt := range tests {
		if got := expectedUploadChunks(tt.size); got != tt.want {
			t.Fatalf("expectedUploadChunks(%d) = %d, want %d", tt.size, got, tt.want)
		}
	}
}

func TestArchiveFormatSupportsCommonWebsitePackages(t *testing.T) {
	tests := map[string]string{
		"site.zip":        "zip",
		"site.tar":        "tar",
		"site.tar.gz":     "tar.gz",
		"site.tgz":        "tar.gz",
		"site.tar.bz2":    "tar.bz2",
		"site.tbz2":       "tar.bz2",
		"database.sql":    "",
		"database.sql.gz": "",
	}

	for name, want := range tests {
		if got := archiveFormat(name); got != want {
			t.Fatalf("archiveFormat(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestExtractTarGzArchive(t *testing.T) {
	base := t.TempDir()
	archivePath := filepath.Join(base, "site.tar.gz")
	writeTarGz(t, archivePath, map[string]string{
		"wp-content/uploads/readme.txt": "ok",
	})

	conflicts, err := checkTarArchive(archivePath, "tar.gz", base, base, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", conflicts)
	}

	if err := extractTarArchive(archivePath, "tar.gz", base, base); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(base, "wp-content", "uploads", "readme.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok" {
		t.Fatalf("extracted content = %q, want ok", string(data))
	}

	conflicts, err = checkTarArchive(archivePath, "tar.gz", base, base, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(conflicts, []string{"wp-content/uploads/readme.txt"}) {
		t.Fatalf("conflicts = %v, want extracted file", conflicts)
	}
}

func TestTarArchiveRejectsPathTraversal(t *testing.T) {
	base := t.TempDir()
	archivePath := filepath.Join(base, "site.tar.gz")
	writeTarGz(t, archivePath, map[string]string{
		"../escape.txt": "bad",
	})

	if _, err := checkTarArchive(archivePath, "tar.gz", base, base, false); err == nil {
		t.Fatal("expected path traversal archive to be rejected")
	}
}

func writeTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	gz := gzip.NewWriter(file)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
}
