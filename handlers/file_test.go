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
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/models"
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

func TestUploadSessionDirUsesPanelDataDir(t *testing.T) {
	oldConfig := config.AppConfig
	defer func() { config.AppConfig = oldConfig }()

	dataDir := t.TempDir()
	config.AppConfig = &config.Config{Panel: config.PanelConfig{DataDir: dataDir}}

	got := uploadSessionDir("../abc123")
	want := filepath.Join(dataDir, "upload-sessions", uploadSessionDirPrefix+"abc123")
	if got != want {
		t.Fatalf("uploadSessionDir = %q, want %q", got, want)
	}
}

func TestCleanupExpiredUploadSessionsOnlyRemovesExpiredUploadDirs(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	expired := filepath.Join(root, uploadSessionDirPrefix+"expired")
	active := filepath.Join(root, uploadSessionDirPrefix+"active")
	other := filepath.Join(root, "not-upload-expired")
	for _, dir := range []string{expired, active, other} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveUploadSession(expired, uploadSession{CreatedAt: now.Add(-25 * time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(expired, now.Add(-25*time.Hour), now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := saveUploadSession(active, uploadSession{CreatedAt: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, now.Add(-25*time.Hour), now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}

	cleanupExpiredUploadSessions(root, 24*time.Hour)

	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("expired upload session still exists, err=%v", err)
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("active upload session removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("non-upload directory removed: %v", err)
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

func TestArchiveSSHExtractCommandUsesMatchingFormat(t *testing.T) {
	tests := []struct {
		path   string
		format string
		want   string
	}{
		{"/www/wwwroot/example.com/backup.zip", "zip", "cd '/www/wwwroot/example.com' && unzip -o 'backup.zip'"},
		{"/www/wwwroot/example.com/backup.tar", "tar", "cd '/www/wwwroot/example.com' && tar xvf 'backup.tar'"},
		{"/www/wwwroot/example.com/backup.tar.gz", "tar.gz", "cd '/www/wwwroot/example.com' && tar zxvf 'backup.tar.gz'"},
		{"/www/wwwroot/example.com/backup.tar.bz2", "tar.bz2", "cd '/www/wwwroot/example.com' && tar jxvf 'backup.tar.bz2'"},
	}

	for _, tt := range tests {
		if got := archiveSSHExtractCommand(tt.path, tt.format); got != tt.want {
			t.Fatalf("archiveSSHExtractCommand(%q, %q) = %q, want %q", tt.path, tt.format, got, tt.want)
		}
	}
}

func TestArchiveSSHExtractCommandQuotesSingleQuote(t *testing.T) {
	got := archiveSSHExtractCommand("/www/wwwroot/site's/backup's.tar.gz", "tar.gz")
	want := "cd '/www/wwwroot/site'\\''s' && tar zxvf 'backup'\\''s.tar.gz'"
	if got != want {
		t.Fatalf("archiveSSHExtractCommand quoted = %q, want %q", got, want)
	}
}

func TestExtractTarGzArchive(t *testing.T) {
	base := t.TempDir()
	archivePath := filepath.Join(base, "site.tar.gz")
	writeTarGz(t, archivePath, map[string]string{
		"wp-content/uploads/readme.txt": "ok",
	})

	conflicts, err := checkTarArchive(archivePath, "tar.gz", base, base, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", conflicts)
	}

	if err := extractTarArchive(archivePath, "tar.gz", base, base, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(base, "wp-content", "uploads", "readme.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok" {
		t.Fatalf("extracted content = %q, want ok", string(data))
	}

	conflicts, err = checkTarArchive(archivePath, "tar.gz", base, base, false, nil)
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

	if _, err := checkTarArchive(archivePath, "tar.gz", base, base, false, nil); err == nil {
		t.Fatal("expected path traversal archive to be rejected")
	}
}

func TestFileLockWriteGuardAllowsUploadsMediaAndBlocksCode(t *testing.T) {
	root := t.TempDir()
	uploads := filepath.Join(root, "wp-content", "uploads")
	if err := os.MkdirAll(uploads, 0755); err != nil {
		t.Fatal(err)
	}
	site := &models.Website{
		WebRoot:         root,
		SiteType:        "wordpress",
		FileLockEnabled: true,
	}

	if err := checkFileLockWrite(site, filepath.Join(uploads, "photo.jpg"), false); err != nil {
		t.Fatalf("uploads media should be allowed: %v", err)
	}
	if err := checkFileLockWrite(site, filepath.Join(uploads, "shell.php"), false); !isFileLockWriteError(err) {
		t.Fatalf("uploads PHP error = %v, want file lock rejection", err)
	}
	if err := checkFileLockWrite(site, filepath.Join(root, "wp-content", "plugins", "plugin.php"), false); !isFileLockWriteError(err) {
		t.Fatalf("code directory write error = %v, want file lock rejection", err)
	}
	if err := checkFileLockWrite(site, filepath.Join(uploads, "shell.php"), true); err != nil {
		t.Fatalf("uploads PHP deletion should be allowed: %v", err)
	}
}

func TestFileLockWriteGuardRejectsUploadsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior differs on Windows")
	}
	root := t.TempDir()
	uploads := filepath.Join(root, "wp-content", "uploads")
	if err := os.MkdirAll(uploads, 0755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(uploads, "linked")); err != nil {
		t.Fatal(err)
	}
	site := &models.Website{
		WebRoot:         root,
		SiteType:        "wordpress",
		FileLockEnabled: true,
	}

	target := filepath.Join(uploads, "linked", "photo.jpg")
	if err := checkFileLockWrite(site, target, false); !isFileLockWriteError(err) {
		t.Fatalf("symlink escape error = %v, want file lock rejection", err)
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

func TestNormalizeFileConflictPolicy(t *testing.T) {
	for _, policy := range []string{"", fileConflictPolicyError, fileConflictPolicyOverwrite, fileConflictPolicySkip} {
		if _, err := normalizeFileConflictPolicy(policy); err != nil {
			t.Fatalf("normalizeFileConflictPolicy(%q) returned error: %v", policy, err)
		}
	}
	if _, err := normalizeFileConflictPolicy("replace"); err == nil {
		t.Fatal("normalizeFileConflictPolicy invalid policy error = nil, want error")
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

func TestCopyFileOrDirOverwriteMergesDirectoryAndKeepsExtraTargetFiles(t *testing.T) {
	srcBase := t.TempDir()
	destBase := t.TempDir()
	src := filepath.Join(srcBase, "wp-content")
	dest := filepath.Join(destBase, "wp-content")

	if err := os.MkdirAll(filepath.Join(src, "themes", "twentytwenty"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "index.php"), []byte("new core"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "themes", "twentytwenty", "style.css"), []byte("theme"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dest, "uploads", "2026"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "index.php"), []byte("old core"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "uploads", "2026", "photo.jpg"), []byte("user upload"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := copyFileOrDirWithOverwrite(srcBase, destBase, src, dest, true); err != nil {
		t.Fatal(err)
	}

	index, err := os.ReadFile(filepath.Join(dest, "index.php"))
	if err != nil {
		t.Fatal(err)
	}
	if string(index) != "new core" {
		t.Fatalf("overwritten index = %q, want new core", string(index))
	}
	upload, err := os.ReadFile(filepath.Join(dest, "uploads", "2026", "photo.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(upload) != "user upload" {
		t.Fatalf("target upload = %q, want user upload", string(upload))
	}
	if _, err := os.Stat(filepath.Join(dest, "themes", "twentytwenty", "style.css")); err != nil {
		t.Fatalf("new nested file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "index.php")); err != nil {
		t.Fatalf("copy should keep source file: %v", err)
	}
}

func TestCopyFileOrDirWithoutOverwriteRejectsExistingFile(t *testing.T) {
	srcBase := t.TempDir()
	destBase := t.TempDir()
	src := filepath.Join(srcBase, "readme.txt")
	dest := filepath.Join(destBase, "readme.txt")
	if err := os.WriteFile(src, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := copyFileOrDirWithOverwrite(srcBase, destBase, src, dest, false); err == nil {
		t.Fatal("copyFileOrDirWithOverwrite without overwrite error = nil, want error")
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("destination changed to %q, want old", string(data))
	}
}

func TestCopyFileOrDirOverwriteRejectsDirectoryOntoFile(t *testing.T) {
	srcBase := t.TempDir()
	destBase := t.TempDir()
	src := filepath.Join(srcBase, "wp-content")
	dest := filepath.Join(destBase, "wp-content")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("file"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := copyFileOrDirWithOverwrite(srcBase, destBase, src, dest, true); err == nil {
		t.Fatal("copyFileOrDirWithOverwrite directory onto file error = nil, want error")
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
