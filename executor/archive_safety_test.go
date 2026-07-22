package executor

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestZIP(t *testing.T, entries map[string]string) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "sample.zip")
	f, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range entries {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if strings.HasSuffix(name, "/") {
			header.SetMode(os.ModeDir | 0755)
		} else {
			header.SetMode(0644)
		}
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestInspectZIPRejectsDangerousPaths(t *testing.T) {
	for _, name := range []string{"../evil", "a/../evil", "/evil", `a\evil`, "C:/evil", "a//evil", "a/./evil"} {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			filename := writeTestZIP(t, map[string]string{name: "x"})
			_, err := InspectZIP(context.Background(), filename, WordPressFullZIPPolicy())
			if code := ArchiveErrorCode(err); code != "archive_path_invalid" {
				t.Fatalf("code = %q, want archive_path_invalid", code)
			}
		})
	}
}

func TestInspectZIPRejectsPathConflicts(t *testing.T) {
	filename := writeTestZIP(t, map[string]string{"a": "file", "a/b": "child"})
	_, err := InspectZIP(context.Background(), filename, WordPressFullZIPPolicy())
	if code := ArchiveErrorCode(err); code != "archive_duplicate_path" {
		t.Fatalf("code = %q, want archive_duplicate_path", code)
	}
}

func TestValidatePathConflictsUsesParentIndex(t *testing.T) {
	usedAsParent := map[string]struct{}{}
	rememberParentPaths(usedAsParent, "a/b/c")
	if err := validatePathConflicts(map[string]bool{"a/b/c": false}, usedAsParent, "a", false); ArchiveErrorCode(err) != "archive_duplicate_path" {
		t.Fatalf("child-first code = %q", ArchiveErrorCode(err))
	}
	if err := validatePathConflicts(map[string]bool{"a": false}, map[string]struct{}{}, "a/b", false); ArchiveErrorCode(err) != "archive_duplicate_path" {
		t.Fatalf("parent-first code = %q", ArchiveErrorCode(err))
	}
}

func TestInspectZIPRejectsLocalHeaderMismatch(t *testing.T) {
	filename := writeTestZIP(t, map[string]string{"safe.txt": "content"})
	body, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(body[:4]) != 0x04034b50 {
		t.Fatal("missing local header")
	}
	body[30] = 'x'
	if err := os.WriteFile(filename, body, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = InspectZIP(context.Background(), filename, WordPressFullZIPPolicy())
	if code := ArchiveErrorCode(err); code != "archive_header_mismatch" {
		t.Fatalf("code = %q, want archive_header_mismatch", code)
	}
}

func TestInspectZIPBoundaries(t *testing.T) {
	filename := writeTestZIP(t, map[string]string{"a": strings.Repeat("a", 100)})
	policy := WordPressFullZIPPolicy()
	policy.MaxEntryUncompressed = 99
	_, err := InspectZIP(context.Background(), filename, policy)
	if code := ArchiveErrorCode(err); code != "archive_entry_too_large" {
		t.Fatalf("code = %q, want archive_entry_too_large", code)
	}
}

func TestInspectZIPRejectsSpecialFiles(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "symlink.zip")
	f, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	header := &zip.FileHeader{Name: "link", Method: zip.Store}
	header.SetMode(os.ModeSymlink | 0777)
	w, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("target")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = InspectZIP(context.Background(), filename, WordPressFullZIPPolicy())
	if code := ArchiveErrorCode(err); code != "archive_type_forbidden" {
		t.Fatalf("code = %q, want archive_type_forbidden", code)
	}
}

func TestInspectZIPRejectsEntryCountAndRatio(t *testing.T) {
	filename := writeTestZIP(t, map[string]string{"a": strings.Repeat("a", 10_000), "b": "b"})
	policy := WordPressFullZIPPolicy()
	policy.MaxEntries = 1
	if _, err := InspectZIP(context.Background(), filename, policy); ArchiveErrorCode(err) != "archive_too_many_entries" {
		t.Fatalf("entry count code = %q", ArchiveErrorCode(err))
	}
	policy = WordPressFullZIPPolicy()
	policy.MaxCompressionRatio = 2
	if _, err := InspectZIP(context.Background(), filename, policy); ArchiveErrorCode(err) != "archive_ratio_exceeded" {
		t.Fatalf("ratio code = %q", ArchiveErrorCode(err))
	}
}

func TestInspectZIPRejectsCRCFailure(t *testing.T) {
	filename := writeTestZIP(t, map[string]string{"file.txt": strings.Repeat("content", 20)})
	zr, err := zip.OpenReader(filename)
	if err != nil {
		t.Fatal(err)
	}
	offset, err := zr.File[0].DataOffset()
	zr.Close()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filename, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0xff}, offset); err != nil {
		t.Fatal(err)
	}
	f.Close()
	_, err = InspectZIP(context.Background(), filename, WordPressFullZIPPolicy())
	if code := ArchiveErrorCode(err); code != "archive_crc_failed" {
		t.Fatalf("code = %q, want archive_crc_failed", code)
	}
}

func TestInspectZIPHonorsCancelledContext(t *testing.T) {
	filename := writeTestZIP(t, map[string]string{"a": "content"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := InspectZIP(ctx, filename, WordPressFullZIPPolicy())
	if code := ArchiveErrorCode(err); code != "archive_validation_timeout" {
		t.Fatalf("code = %q, want archive_validation_timeout", code)
	}
}
