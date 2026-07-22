package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type ZIPExtractRequest struct {
	ArchivePath    string
	StagingParent  string
	ExpectedSHA256 string
	Policy         ZIPPolicy
}

type ZIPExtraction struct {
	StagingPath string
	Inspection  ZIPInspection
}

type zipExtractOps struct {
	mkdirTemp func(string, string) (string, error)
	openRoot  func(string) (zipExtractRoot, error)
	chmod     func(string, os.FileMode) error
	removeAll func(string) error
	copy      func(context.Context, io.Writer, io.Reader) (int64, error)
}

type zipExtractRoot interface {
	Mkdir(string, os.FileMode) error
	Lstat(string) (os.FileInfo, error)
	Open(string) (*os.File, error)
	OpenFile(string, int, os.FileMode) (*os.File, error)
	Close() error
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func defaultZIPExtractOps() zipExtractOps {
	return zipExtractOps{
		mkdirTemp: os.MkdirTemp,
		openRoot: func(name string) (zipExtractRoot, error) {
			return os.OpenRoot(name)
		},
		chmod:     os.Chmod,
		removeAll: os.RemoveAll,
		copy:      copyWithContext,
	}
}

func ExtractZIPToNewStaging(ctx context.Context, req ZIPExtractRequest) (ZIPExtraction, error) {
	return extractZIPToNewStaging(ctx, req, defaultZIPExtractOps())
}

func extractZIPToNewStaging(ctx context.Context, req ZIPExtractRequest, ops zipExtractOps) (result ZIPExtraction, retErr error) {
	if !filepath.IsAbs(req.ArchivePath) || !filepath.IsAbs(req.StagingParent) || !sha256Pattern.MatchString(req.ExpectedSHA256) || !validZIPPolicy(req.Policy) {
		return result, archiveError("archive_extract_request_invalid", nil)
	}
	parentLstat, err := os.Lstat(req.StagingParent)
	if err != nil || !parentLstat.IsDir() || parentLstat.Mode()&os.ModeSymlink != 0 {
		return result, archiveError("archive_staging_invalid", err)
	}
	realParent, err := filepath.EvalSymlinks(req.StagingParent)
	if err != nil || !filepath.IsAbs(realParent) {
		return result, archiveError("archive_staging_invalid", err)
	}
	parentStart, err := os.Stat(realParent)
	if err != nil || !parentStart.IsDir() {
		return result, archiveError("archive_staging_invalid", err)
	}

	archive, err := os.Open(req.ArchivePath)
	if err != nil {
		return result, archiveError("archive_open_failed", err)
	}
	defer archive.Close()
	archiveStart, err := archive.Stat()
	if err != nil || !archiveStart.Mode().IsRegular() {
		return result, archiveError("archive_open_failed", err)
	}
	inspection, zr, err := inspectOpenZIP(ctx, archive, req.Policy)
	if err != nil {
		return result, err
	}
	if inspection.SHA256 != req.ExpectedSHA256 {
		return result, archiveError("archive_digest_mismatch", nil)
	}

	staging, err := ops.mkdirTemp(realParent, ".wp-extract-*")
	if err != nil {
		return result, archiveError("archive_staging_invalid", err)
	}
	cleanup := true
	defer func() {
		if !cleanup {
			return
		}
		if err := safeRemoveStaging(realParent, staging, ops.removeAll); err != nil {
			retErr = archiveError("archive_extract_cleanup_failed", errors.Join(retErr, err))
			result = ZIPExtraction{}
		}
	}()
	if err := ops.chmod(staging, 0700); err != nil {
		return result, archiveError("archive_extract_failed", err)
	}
	if err := ensureEmptyDirectory(staging); err != nil {
		return result, archiveError("archive_staging_invalid", err)
	}
	parentEnd, err := os.Stat(realParent)
	if err != nil || !os.SameFile(parentStart, parentEnd) {
		return result, archiveError("archive_staging_invalid", err)
	}
	root, err := ops.openRoot(staging)
	if err != nil {
		return result, archiveError("archive_staging_invalid", err)
	}
	rootClosed := false
	defer func() {
		if !rootClosed {
			root.Close()
		}
	}()

	var total uint64
	for _, zf := range zr.File {
		if err := ctx.Err(); err != nil {
			return result, archiveError("archive_extract_failed", err)
		}
		name, isDir, err := normalizeZIPName(zf.Name, req.Policy.RequireUTF8Names)
		if err != nil {
			return result, err
		}
		rel, err := stagingRelativePath(name)
		if err != nil {
			return result, err
		}
		mode := zf.Mode()
		if isDir {
			if !mode.IsDir() {
				return result, archiveError("archive_type_forbidden", nil)
			}
			if err := ensureRootDirectories(root, rel); err != nil {
				return result, err
			}
			continue
		}
		if !mode.IsRegular() {
			return result, archiveError("archive_type_forbidden", nil)
		}
		if err := ensureRootDirectories(root, filepath.Dir(rel)); err != nil {
			return result, err
		}
		src, err := zf.Open()
		if err != nil {
			return result, archiveError("archive_extract_failed", err)
		}
		dst, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			src.Close()
			return result, archiveError("archive_extract_failed", err)
		}
		remainingTotal := req.Policy.MaxTotalUncompressed - total
		writeLimit := req.Policy.MaxEntryUncompressed
		limitCode := "archive_entry_too_large"
		if remainingTotal < writeLimit {
			writeLimit = remainingTotal
			limitCode = "archive_total_too_large"
		}
		boundedDst := &extractLimitWriter{writer: dst, remaining: writeLimit, code: limitCode}
		written, copyErr := ops.copy(ctx, boundedDst, src)
		chmodErr := dst.Chmod(0644)
		closeDstErr := dst.Close()
		closeSrcErr := src.Close()
		if copyErr != nil && ArchiveErrorCode(copyErr) != "" {
			return result, copyErr
		}
		if copyErr != nil || chmodErr != nil || closeDstErr != nil || closeSrcErr != nil || written < 0 || uint64(written) != zf.UncompressedSize64 {
			return result, archiveError("archive_extract_failed", errors.Join(copyErr, chmodErr, closeDstErr, closeSrcErr))
		}
		if uint64(written) > req.Policy.MaxEntryUncompressed || ^uint64(0)-total < uint64(written) {
			return result, archiveError("archive_entry_too_large", nil)
		}
		total += uint64(written)
		if total > req.Policy.MaxTotalUncompressed {
			return result, archiveError("archive_total_too_large", nil)
		}
	}
	if err := root.Close(); err != nil {
		return result, archiveError("archive_extract_failed", err)
	}
	rootClosed = true

	endingSHA, err := sha256OpenFile(archive)
	archiveEnd, statErr := archive.Stat()
	if err != nil || statErr != nil || endingSHA != req.ExpectedSHA256 || !os.SameFile(archiveStart, archiveEnd) || archiveStart.Size() != archiveEnd.Size() || !archiveStart.ModTime().Equal(archiveEnd.ModTime()) {
		return result, archiveError("archive_digest_mismatch", errors.Join(err, statErr))
	}
	result = ZIPExtraction{StagingPath: staging, Inspection: inspection}
	cleanup = false
	return result, nil
}

type extractLimitWriter struct {
	writer    io.Writer
	remaining uint64
	code      string
}

func (w *extractLimitWriter) Write(p []byte) (int, error) {
	if uint64(len(p)) > w.remaining {
		return 0, archiveError(w.code, nil)
	}
	n, err := w.writer.Write(p)
	w.remaining -= uint64(n)
	return n, err
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 128*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			written, writeErr := dst.Write(buf[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func stagingRelativePath(slashName string) (string, error) {
	rel := filepath.FromSlash(slashName)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", archiveError("archive_path_invalid", nil)
	}
	return rel, nil
}

func ensureRootDirectories(root zipExtractRoot, directory string) error {
	if directory == "." || directory == "" {
		return nil
	}
	current := ""
	for _, part := range strings.Split(directory, string(os.PathSeparator)) {
		if current == "" {
			current = part
		} else {
			current = filepath.Join(current, part)
		}
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			if err := root.Mkdir(current, 0755); err != nil {
				return archiveError("archive_extract_failed", err)
			}
			info, err = root.Lstat(current)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return archiveError("archive_staging_invalid", err)
		}
		dir, err := root.Open(current)
		if err != nil {
			return archiveError("archive_staging_invalid", err)
		}
		dirInfo, statErr := dir.Stat()
		chmodErr := dir.Chmod(0755)
		closeErr := dir.Close()
		if statErr != nil || !dirInfo.IsDir() || chmodErr != nil || closeErr != nil {
			return archiveError("archive_extract_failed", errors.Join(statErr, chmodErr, closeErr))
		}
	}
	return nil
}

func validZIPPolicy(policy ZIPPolicy) bool {
	return policy.PackageType != "" && policy.MaxArchiveBytes > 0 && policy.MaxEntries > 0 && policy.MaxEntryUncompressed > 0 && policy.MaxTotalUncompressed > 0 && policy.MaxCompressionRatio > 0
}

func ensureEmptyDirectory(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("staging not empty")
	}
	return nil
}

func safeRemoveStaging(parent, staging string, removeAll func(string) error) error {
	if filepath.Dir(staging) != parent || !strings.HasPrefix(filepath.Base(staging), ".wp-extract-") || staging == parent {
		return errors.New("unsafe staging cleanup target")
	}
	return removeAll(staging)
}

func sha256OpenFile(f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
