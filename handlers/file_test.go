package handlers

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

func TestFileOperationNameRejectsTraversal(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../x", "sub/file", `sub\file`} {
		if _, err := cleanFileOperationName(name); err == nil {
			t.Fatalf("cleanFileOperationName(%q) error = nil, want error", name)
		}
	}
	if got, err := cleanFileOperationName("readme.txt"); err != nil || got != "readme.txt" {
		t.Fatalf("cleanFileOperationName safe = %q, %v", got, err)
	}
}

func TestFileEntryPaginationDefaultsToFifty(t *testing.T) {
	files := make([]fileEntry, 60)
	for i := range files {
		files[i] = fileEntry{Name: string(rune('a' + i%26))}
	}

	page, perPage := normalizeFilePage(0, 0)
	if page != 1 || perPage != defaultFilePageSize {
		t.Fatalf("normalizeFilePage defaults = (%d, %d), want (1, %d)", page, perPage, defaultFilePageSize)
	}

	pageFiles, gotPage, totalPages := paginateFileEntries(files, page, perPage)
	if gotPage != 1 {
		t.Fatalf("page = %d, want 1", gotPage)
	}
	if totalPages != 2 {
		t.Fatalf("totalPages = %d, want 2", totalPages)
	}
	if len(pageFiles) != defaultFilePageSize {
		t.Fatalf("page size = %d, want %d", len(pageFiles), defaultFilePageSize)
	}
}

func TestFileEntryPaginationClampsLastPage(t *testing.T) {
	files := make([]fileEntry, 55)
	for i := range files {
		files[i] = fileEntry{Name: string(rune('a' + i%26))}
	}

	pageFiles, page, totalPages := paginateFileEntries(files, 99, 50)
	if page != 2 {
		t.Fatalf("page = %d, want 2", page)
	}
	if totalPages != 2 {
		t.Fatalf("totalPages = %d, want 2", totalPages)
	}
	if len(pageFiles) != 5 {
		t.Fatalf("last page size = %d, want 5", len(pageFiles))
	}
}

func TestNormalizeFilePageClampsMaxPageSize(t *testing.T) {
	page, perPage := normalizeFilePage(-1, 300)
	if page != 1 || perPage != maxFilePageSize {
		t.Fatalf("normalizeFilePage = (%d, %d), want (1, %d)", page, perPage, maxFilePageSize)
	}
}

func TestFileEntryPaginationEmptyList(t *testing.T) {
	pageFiles, page, totalPages := paginateFileEntries(nil, 1, 50)
	if page != 1 || totalPages != 1 || len(pageFiles) != 0 {
		t.Fatalf("empty pagination = len %d page %d totalPages %d, want 0/1/1", len(pageFiles), page, totalPages)
	}
}

func TestFileEntryPaginationWithSmallPageSize(t *testing.T) {
	files := []fileEntry{{Name: "a"}, {Name: "b"}}
	pageFiles, page, totalPages := paginateFileEntries(files, 2, 1)
	if page != 2 || totalPages != 2 || len(pageFiles) != 1 || pageFiles[0].Name != "b" {
		t.Fatalf("pagination = %#v page %d totalPages %d, want second single item", pageFiles, page, totalPages)
	}
}

func TestSortFileEntriesKeepsDirectoriesFirst(t *testing.T) {
	files := []fileEntry{
		{Name: "z.txt", Size: 1},
		{Name: "assets", IsDir: true},
		{Name: "a.txt", Size: 2},
		{Name: "uploads", IsDir: true},
	}

	sortFileEntries(files, "name", "desc")
	got := []string{files[0].Name, files[1].Name, files[2].Name, files[3].Name}
	want := []string{"uploads", "assets", "z.txt", "a.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sorted names = %v, want %v", got, want)
	}
}

func TestSortFileEntriesBySizeAndInvalidOptions(t *testing.T) {
	files := []fileEntry{
		{Name: "b.log", Size: 20},
		{Name: "a.txt", Size: 10},
	}

	sortFileEntries(files, "size", "asc")
	if got := []string{files[0].Name, files[1].Name}; !reflect.DeepEqual(got, []string{"a.txt", "b.log"}) {
		t.Fatalf("size asc = %v", got)
	}

	sortFileEntries(files, "unknown", "invalid")
	if got := []string{files[0].Name, files[1].Name}; !reflect.DeepEqual(got, []string{"a.txt", "b.log"}) {
		t.Fatalf("fallback name asc = %v", got)
	}
}

func TestSortFileEntriesByTypeAndTime(t *testing.T) {
	files := []fileEntry{
		{Name: "b.zip", ModTime: "2026-01-02 00:00:00"},
		{Name: "a.txt", ModTime: "2026-01-01 00:00:00"},
	}

	sortFileEntries(files, "type", "asc")
	if got := []string{files[0].Name, files[1].Name}; !reflect.DeepEqual(got, []string{"a.txt", "b.zip"}) {
		t.Fatalf("type asc = %v", got)
	}

	sortFileEntries(files, "time", "desc")
	if got := []string{files[0].Name, files[1].Name}; !reflect.DeepEqual(got, []string{"b.zip", "a.txt"}) {
		t.Fatalf("time desc = %v", got)
	}
}

func TestCopyFileOrDirRejectsDirectoryIntoItself(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "wp-content")
	dest := filepath.Join(src, "copy")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := copyFileOrDir(base, base, src, dest); err == nil {
		t.Fatal("copyFileOrDir into itself error = nil, want error")
	}
}

func TestCopyFileOrDirAllowsSeparateBases(t *testing.T) {
	srcBase := t.TempDir()
	destBase := t.TempDir()
	src := filepath.Join(srcBase, "readme.txt")
	dest := filepath.Join(destBase, "readme.txt")
	if err := os.WriteFile(src, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := copyFileOrDir(srcBase, destBase, src, dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok" {
		t.Fatalf("copied content = %q, want ok", string(data))
	}
}

func TestCopyFileOrDirPreservesFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not reliably preserve Unix executable bits")
	}
	srcBase := t.TempDir()
	destBase := t.TempDir()
	src := filepath.Join(srcBase, "wp-cli")
	dest := filepath.Join(destBase, "wp-cli")
	if err := os.WriteFile(src, []byte("ok"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0755); err != nil {
		t.Fatal(err)
	}

	if err := copyFileOrDir(srcBase, destBase, src, dest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0755 {
		t.Fatalf("copied mode = %o, want 0755", got)
	}
}

func TestCopyFileOrDirRejectsDestinationOutsideBase(t *testing.T) {
	srcBase := t.TempDir()
	destBase := t.TempDir()
	outside := t.TempDir()
	src := filepath.Join(srcBase, "readme.txt")
	dest := filepath.Join(outside, "readme.txt")
	if err := os.WriteFile(src, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := copyFileOrDir(srcBase, destBase, src, dest); err == nil {
		t.Fatal("copyFileOrDir outside destination error = nil, want error")
	}
}

func TestZipTargetRejectsSpecialEntries(t *testing.T) {
	base := t.TempDir()
	header := &zip.FileHeader{Name: "link"}
	header.SetMode(os.ModeSymlink | 0777)
	f := &zip.File{FileHeader: *header}
	if _, _, err := zipTargetForFile(base, base, f); err == nil {
		t.Fatal("zipTargetForFile symlink error = nil, want error")
	}
}

func TestZipTargetRejectsPathTraversal(t *testing.T) {
	base := t.TempDir()
	f := &zip.File{FileHeader: zip.FileHeader{Name: "../escape.txt"}}
	if _, _, err := zipTargetForFile(base, base, f); err == nil {
		t.Fatal("zipTargetForFile traversal error = nil, want error")
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
