package executor

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func extractRequestForTest(t *testing.T) ZIPExtractRequest {
	t.Helper()
	archive := validWordPressZIP(t)
	inspection, err := InspectZIP(context.Background(), archive, WordPressFullZIPPolicy())
	if err != nil {
		t.Fatal(err)
	}
	return ZIPExtractRequest{
		ArchivePath:    archive,
		StagingParent:  t.TempDir(),
		ExpectedSHA256: inspection.SHA256,
		Policy:         WordPressFullZIPPolicy(),
	}
}

func TestExtractZIPToNewStaging(t *testing.T) {
	req := extractRequestForTest(t)
	result, err := ExtractZIPToNewStaging(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(result.StagingPath) })
	if filepath.Dir(result.StagingPath) != req.StagingParent || !strings.HasPrefix(filepath.Base(result.StagingPath), ".wp-extract-") {
		t.Fatalf("unexpected staging path %q", result.StagingPath)
	}
	versionFile := filepath.Join(result.StagingPath, "wordpress", "wp-includes", "version.php")
	body, err := os.ReadFile(versionFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "$wp_version = '7.0.2';") {
		t.Fatalf("unexpected version.php: %q", body)
	}
	info, err := os.Stat(versionFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("file mode = %o, want 644", info.Mode().Perm())
	}
}

func TestExtractZIPDigestMismatchCreatesNothing(t *testing.T) {
	req := extractRequestForTest(t)
	req.ExpectedSHA256 = strings.Repeat("0", 64)
	_, err := ExtractZIPToNewStaging(context.Background(), req)
	if code := ArchiveErrorCode(err); code != "archive_digest_mismatch" {
		t.Fatalf("code = %q, want archive_digest_mismatch", code)
	}
	entries, err := os.ReadDir(req.StagingParent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging parent contains %d entries", len(entries))
	}
}

func TestExtractZIPRejectsInvalidRequestAndSymlinkParent(t *testing.T) {
	req := extractRequestForTest(t)
	req.ArchivePath = "relative.zip"
	if _, err := ExtractZIPToNewStaging(context.Background(), req); ArchiveErrorCode(err) != "archive_extract_request_invalid" {
		t.Fatalf("relative archive code = %q", ArchiveErrorCode(err))
	}
	req = extractRequestForTest(t)
	realParent := t.TempDir()
	linkParent := filepath.Join(t.TempDir(), "parent-link")
	if err := os.Symlink(realParent, linkParent); err != nil {
		t.Fatal(err)
	}
	req.StagingParent = linkParent
	if _, err := ExtractZIPToNewStaging(context.Background(), req); ArchiveErrorCode(err) != "archive_staging_invalid" {
		t.Fatalf("symlink parent code = %q", ArchiveErrorCode(err))
	}
}

func TestExtractZIPFailureCleansOnlyStaging(t *testing.T) {
	req := extractRequestForTest(t)
	neighbor := filepath.Join(req.StagingParent, "keep.txt")
	if err := os.WriteFile(neighbor, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	ops := defaultZIPExtractOps()
	ops.copy = func(context.Context, io.Writer, io.Reader) (int64, error) {
		return 0, errors.New("injected copy failure")
	}
	_, err := extractZIPToNewStaging(context.Background(), req, ops)
	if code := ArchiveErrorCode(err); code != "archive_extract_failed" {
		t.Fatalf("code = %q, want archive_extract_failed", code)
	}
	entries, err := os.ReadDir(req.StagingParent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "keep.txt" {
		t.Fatalf("unexpected remaining entries: %+v", entries)
	}
}

func TestExtractZIPReportsCleanupFailure(t *testing.T) {
	req := extractRequestForTest(t)
	ops := defaultZIPExtractOps()
	ops.copy = func(context.Context, io.Writer, io.Reader) (int64, error) {
		return 0, errors.New("injected copy failure")
	}
	ops.removeAll = func(string) error { return errors.New("injected cleanup failure") }
	_, err := extractZIPToNewStaging(context.Background(), req, ops)
	if code := ArchiveErrorCode(err); code != "archive_extract_cleanup_failed" {
		t.Fatalf("code = %q, want archive_extract_cleanup_failed", code)
	}
}

func TestExtractZIPCancelledContextCleansStaging(t *testing.T) {
	req := extractRequestForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ExtractZIPToNewStaging(ctx, req)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	entries, readErr := os.ReadDir(req.StagingParent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("staging residue: %+v", entries)
	}
}

func TestExtractZIPDetectsArchiveMetadataDrift(t *testing.T) {
	req := extractRequestForTest(t)
	ops := defaultZIPExtractOps()
	originalCopy := ops.copy
	ops.copy = func(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
		n, err := originalCopy(ctx, dst, src)
		info, statErr := os.Stat(req.ArchivePath)
		if statErr != nil {
			return n, statErr
		}
		if timeErr := os.Chtimes(req.ArchivePath, info.ModTime(), info.ModTime().Add(time.Second)); timeErr != nil {
			return n, timeErr
		}
		return n, err
	}
	_, err := extractZIPToNewStaging(context.Background(), req, ops)
	if code := ArchiveErrorCode(err); code != "archive_digest_mismatch" {
		t.Fatalf("code = %q, want archive_digest_mismatch", code)
	}
	entries, readErr := os.ReadDir(req.StagingParent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("staging residue: %+v", entries)
	}
}

func TestExtractLimitWriterStopsBeforeOverflow(t *testing.T) {
	var dst strings.Builder
	w := &extractLimitWriter{writer: &dst, remaining: 3, code: "archive_entry_too_large"}
	if n, err := w.Write([]byte("four")); n != 0 || ArchiveErrorCode(err) != "archive_entry_too_large" {
		t.Fatalf("n=%d code=%q", n, ArchiveErrorCode(err))
	}
	if dst.Len() != 0 {
		t.Fatalf("wrote %d bytes past limit", dst.Len())
	}
}

func TestExtractZIPRootPreventsIntermediateSymlinkEscape(t *testing.T) {
	for _, test := range []struct {
		name    string
		entries []string
	}{
		{name: "directory entry", entries: []string{"wordpress/", "wordpress/evil/"}},
		{name: "file entry", entries: []string{"wordpress/", "wordpress/evil/file.txt"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := writeOrderedExtractZIP(t, test.entries)
			inspection, err := InspectZIP(context.Background(), archive, WordPressFullZIPPolicy())
			if err != nil {
				t.Fatal(err)
			}
			parent := t.TempDir()
			outside := t.TempDir()
			req := ZIPExtractRequest{ArchivePath: archive, StagingParent: parent, ExpectedSHA256: inspection.SHA256, Policy: WordPressFullZIPPolicy()}
			ops := defaultZIPExtractOps()
			ops.openRoot = func(staging string) (zipExtractRoot, error) {
				root, err := os.OpenRoot(staging)
				if err != nil {
					return nil, err
				}
				return &symlinkAttackRoot{Root: root, staging: staging, outside: outside}, nil
			}
			_, err = extractZIPToNewStaging(context.Background(), req, ops)
			if err == nil {
				t.Fatal("expected symlink escape rejection")
			}
			if _, statErr := os.Stat(filepath.Join(outside, "evil")); !os.IsNotExist(statErr) {
				t.Fatalf("outside directory was created: %v", statErr)
			}
			if _, statErr := os.Stat(filepath.Join(outside, "file.txt")); !os.IsNotExist(statErr) {
				t.Fatalf("outside file was created: %v", statErr)
			}
			entries, readErr := os.ReadDir(parent)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("staging residue: %+v", entries)
			}
		})
	}
}

type symlinkAttackRoot struct {
	*os.Root
	staging  string
	outside  string
	attacked bool
}

func (r *symlinkAttackRoot) Mkdir(name string, perm os.FileMode) error {
	if !r.attacked && name == filepath.Join("wordpress", "evil") {
		r.attacked = true
		if err := os.Remove(filepath.Join(r.staging, "wordpress")); err != nil {
			return err
		}
		if err := os.Symlink(r.outside, filepath.Join(r.staging, "wordpress")); err != nil {
			return err
		}
	}
	return r.Root.Mkdir(name, perm)
}

func writeOrderedExtractZIP(t *testing.T, names []string) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "ordered.zip")
	f, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if strings.HasSuffix(name, "/") {
			header.SetMode(os.ModeDir | 0777)
		} else {
			header.SetMode(0777)
		}
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(name, "/") {
			if _, err := w.Write([]byte("content")); err != nil {
				t.Fatal(err)
			}
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
