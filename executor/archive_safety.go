package executor

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

type ZIPPackageType string

const ZIPPackageWordPressFull ZIPPackageType = "wordpress_full"

type ZIPPolicy struct {
	PackageType          ZIPPackageType
	MaxArchiveBytes      int64
	MaxEntries           int
	MaxEntryUncompressed uint64
	MaxTotalUncompressed uint64
	MaxCompressionRatio  uint64
	RequireUTF8Names     bool
}

type ZIPInspection struct {
	SHA256               string
	ArchiveBytes         int64
	EntryCount           int
	DeclaredUncompressed uint64
	VerifiedUncompressed uint64
	NormalizedNames      []string
}

type ArchiveError struct {
	Code string
	err  error
}

func (e *ArchiveError) Error() string { return e.Code }
func (e *ArchiveError) Unwrap() error { return e.err }

func archiveError(code string, err error) error { return &ArchiveError{Code: code, err: err} }

func ArchiveErrorCode(err error) string {
	var target *ArchiveError
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

func WordPressFullZIPPolicy() ZIPPolicy {
	return ZIPPolicy{
		PackageType:          ZIPPackageWordPressFull,
		MaxArchiveBytes:      200 << 20,
		MaxEntries:           20_000,
		MaxEntryUncompressed: 256 << 20,
		MaxTotalUncompressed: 1 << 30,
		MaxCompressionRatio:  200,
		RequireUTF8Names:     true,
	}
}

func InspectZIP(ctx context.Context, filename string, policy ZIPPolicy) (ZIPInspection, error) {
	var report ZIPInspection
	f, err := os.Open(filename)
	if err != nil {
		return report, archiveError("archive_open_failed", err)
	}
	defer f.Close()
	report, _, err = inspectOpenZIP(ctx, f, policy)
	return report, err
}

func inspectOpenZIP(ctx context.Context, f *os.File, policy ZIPPolicy) (ZIPInspection, *zip.Reader, error) {
	var report ZIPInspection
	info, err := f.Stat()
	if err != nil {
		return report, nil, archiveError("archive_open_failed", err)
	}
	report.ArchiveBytes = info.Size()
	if info.Size() <= 0 {
		return report, nil, archiveError("archive_invalid_zip", nil)
	}
	if info.Size() > policy.MaxArchiveBytes {
		return report, nil, archiveError("archive_too_large", nil)
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return report, nil, archiveError("archive_open_failed", err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return report, nil, archiveError("archive_open_failed", err)
	}
	report.SHA256 = hex.EncodeToString(hash.Sum(nil))

	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return report, nil, archiveError("archive_invalid_zip", err)
	}
	if len(zr.File) == 0 {
		return report, nil, archiveError("archive_invalid_zip", nil)
	}
	if len(zr.File) > policy.MaxEntries {
		return report, nil, archiveError("archive_too_many_entries", nil)
	}

	types := make(map[string]bool, len(zr.File)) // true means directory.
	usedAsParent := make(map[string]struct{}, len(zr.File))
	type byteRange struct{ start, end int64 }
	ranges := make([]byteRange, 0, len(zr.File))
	var declared uint64
	var compressed uint64
	for _, zf := range zr.File {
		if err := ctx.Err(); err != nil {
			return report, nil, archiveError("archive_validation_timeout", err)
		}
		name, isDir, err := normalizeZIPName(zf.Name, policy.RequireUTF8Names)
		if err != nil {
			return report, nil, err
		}
		if _, exists := types[name]; exists {
			return report, nil, archiveError("archive_duplicate_path", nil)
		}
		if err := validatePathConflicts(types, usedAsParent, name, isDir); err != nil {
			return report, nil, err
		}
		if zf.Flags&1 != 0 || (zf.Method != zip.Store && zf.Method != zip.Deflate) {
			return report, nil, archiveError("archive_type_forbidden", nil)
		}
		mode := zf.Mode()
		if isDir {
			if !mode.IsDir() {
				return report, nil, archiveError("archive_type_forbidden", nil)
			}
		} else if !mode.IsRegular() {
			return report, nil, archiveError("archive_type_forbidden", nil)
		}
		if zf.UncompressedSize64 > policy.MaxEntryUncompressed {
			return report, nil, archiveError("archive_entry_too_large", nil)
		}
		if math.MaxUint64-declared < zf.UncompressedSize64 {
			return report, nil, archiveError("archive_total_too_large", nil)
		}
		declared += zf.UncompressedSize64
		if declared > policy.MaxTotalUncompressed {
			return report, nil, archiveError("archive_total_too_large", nil)
		}
		if math.MaxUint64-compressed < zf.CompressedSize64 {
			return report, nil, archiveError("archive_ratio_exceeded", nil)
		}
		compressed += zf.CompressedSize64
		if exceedsRatio(zf.UncompressedSize64, zf.CompressedSize64, policy.MaxCompressionRatio) {
			return report, nil, archiveError("archive_ratio_exceeded", nil)
		}
		start, end, err := validateLocalHeader(f, info.Size(), zf)
		if err != nil {
			return report, nil, err
		}
		ranges = append(ranges, byteRange{start: start, end: end})
		types[name] = isDir
		rememberParentPaths(usedAsParent, name)
		report.NormalizedNames = append(report.NormalizedNames, name)
	}
	if exceedsRatio(declared, compressed, policy.MaxCompressionRatio) {
		return report, nil, archiveError("archive_ratio_exceeded", nil)
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })
	for i := 1; i < len(ranges); i++ {
		if ranges[i].start < ranges[i-1].end {
			return report, nil, archiveError("archive_header_mismatch", nil)
		}
	}

	var verified uint64
	buf := make([]byte, 32*1024)
	for _, zf := range zr.File {
		if strings.HasSuffix(zf.Name, "/") {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return report, nil, archiveError("archive_crc_failed", err)
		}
		var entry uint64
		for {
			if err := ctx.Err(); err != nil {
				rc.Close()
				return report, nil, archiveError("archive_validation_timeout", err)
			}
			n, readErr := rc.Read(buf)
			entry += uint64(n)
			verified += uint64(n)
			if entry > policy.MaxEntryUncompressed {
				rc.Close()
				return report, nil, archiveError("archive_entry_too_large", nil)
			}
			if verified > policy.MaxTotalUncompressed {
				rc.Close()
				return report, nil, archiveError("archive_total_too_large", nil)
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				rc.Close()
				return report, nil, archiveError("archive_crc_failed", readErr)
			}
		}
		if err := rc.Close(); err != nil || entry != zf.UncompressedSize64 {
			return report, nil, archiveError("archive_crc_failed", err)
		}
	}
	report.EntryCount = len(zr.File)
	report.DeclaredUncompressed = declared
	report.VerifiedUncompressed = verified
	return report, zr, nil
}

func normalizeZIPName(name string, requireUTF8 bool) (string, bool, error) {
	if name == "" {
		return "", false, archiveError("archive_path_invalid", nil)
	}
	if requireUTF8 && !utf8.ValidString(name) {
		return "", false, archiveError("archive_name_encoding_invalid", nil)
	}
	for _, r := range name {
		if r == 0 || r < 0x20 || r == 0x7f {
			return "", false, archiveError("archive_path_invalid", nil)
		}
	}
	if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "~") || strings.HasPrefix(name, "//") {
		return "", false, archiveError("archive_path_invalid", nil)
	}
	if len(name) >= 2 && name[1] == ':' {
		return "", false, archiveError("archive_path_invalid", nil)
	}
	isDir := strings.HasSuffix(name, "/")
	trimmed := strings.TrimSuffix(name, "/")
	if trimmed == "" || strings.Contains(trimmed, "//") {
		return "", false, archiveError("archive_path_invalid", nil)
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", false, archiveError("archive_path_invalid", nil)
		}
	}
	if path.Clean(trimmed) != trimmed {
		return "", false, archiveError("archive_path_invalid", nil)
	}
	return trimmed, isDir, nil
}

func validatePathConflicts(existing map[string]bool, usedAsParent map[string]struct{}, name string, isDir bool) error {
	parts := strings.Split(name, "/")
	for i := 1; i < len(parts); i++ {
		if parentIsDir, ok := existing[strings.Join(parts[:i], "/")]; ok && !parentIsDir {
			return archiveError("archive_duplicate_path", nil)
		}
	}
	if !isDir {
		if _, alreadyUsedAsParent := usedAsParent[name]; alreadyUsedAsParent {
			return archiveError("archive_duplicate_path", nil)
		}
	}
	return nil
}

func rememberParentPaths(usedAsParent map[string]struct{}, name string) {
	parts := strings.Split(name, "/")
	for i := 1; i < len(parts); i++ {
		usedAsParent[strings.Join(parts[:i], "/")] = struct{}{}
	}
}

func exceedsRatio(uncompressed, compressed, limit uint64) bool {
	if uncompressed == 0 {
		return false
	}
	if compressed == 0 {
		return true
	}
	quotient, remainder := uncompressed/compressed, uncompressed%compressed
	return quotient > limit || (quotient == limit && remainder > 0)
}

func validateLocalHeader(r io.ReaderAt, archiveSize int64, zf *zip.File) (int64, int64, error) {
	offset, err := zf.DataOffset()
	if err != nil {
		return 0, 0, archiveError("archive_header_mismatch", err)
	}
	if offset < 30 || offset > archiveSize || uint64(archiveSize-offset) < zf.CompressedSize64 {
		return 0, 0, archiveError("archive_header_mismatch", nil)
	}
	windowSize := offset
	if windowSize > 30+math.MaxUint16*2 {
		windowSize = 30 + math.MaxUint16*2
	}
	window := make([]byte, windowSize)
	windowStart := offset - windowSize
	if _, err := r.ReadAt(window, windowStart); err != nil {
		return 0, 0, archiveError("archive_header_mismatch", err)
	}
	localStart := int64(-1)
	var header []byte
	for i := len(window) - 30; i >= 0; i-- {
		if binary.LittleEndian.Uint32(window[i:i+4]) != 0x04034b50 {
			continue
		}
		nameLen := int(binary.LittleEndian.Uint16(window[i+26 : i+28]))
		extraLen := int(binary.LittleEndian.Uint16(window[i+28 : i+30]))
		end := i + 30 + nameLen + extraLen
		if end == len(window) {
			if localStart != -1 {
				return 0, 0, archiveError("archive_header_mismatch", nil)
			}
			localStart = windowStart + int64(i)
			header = window[i:end]
		}
	}
	if localStart < 0 || len(header) < 30 {
		return 0, 0, archiveError("archive_header_mismatch", nil)
	}
	nameLen := int(binary.LittleEndian.Uint16(header[26:28]))
	if string(header[30:30+nameLen]) != zf.Name || binary.LittleEndian.Uint16(header[6:8]) != zf.Flags || binary.LittleEndian.Uint16(header[8:10]) != zf.Method {
		return 0, 0, archiveError("archive_header_mismatch", nil)
	}
	end := offset + int64(zf.CompressedSize64)
	if zf.Flags&0x8 == 0 {
		if binary.LittleEndian.Uint32(header[14:18]) != zf.CRC32 || binary.LittleEndian.Uint32(header[18:22]) != uint32(zf.CompressedSize64) || binary.LittleEndian.Uint32(header[22:26]) != uint32(zf.UncompressedSize64) || zf.CompressedSize64 > math.MaxUint32 || zf.UncompressedSize64 > math.MaxUint32 {
			return 0, 0, archiveError("archive_header_mismatch", nil)
		}
	} else {
		localCRC := binary.LittleEndian.Uint32(header[14:18])
		localCompressed := binary.LittleEndian.Uint32(header[18:22])
		localUncompressed := binary.LittleEndian.Uint32(header[22:26])
		if localCRC != 0 || (localCompressed != 0 && localCompressed != math.MaxUint32) || (localUncompressed != 0 && localUncompressed != math.MaxUint32) {
			return 0, 0, archiveError("archive_header_mismatch", nil)
		}
		descriptorBytes, err := validateDataDescriptor(r, archiveSize, end, zf)
		if err != nil {
			return 0, 0, err
		}
		end += int64(descriptorBytes)
	}
	return localStart, end, nil
}

func validateDataDescriptor(r io.ReaderAt, archiveSize, offset int64, zf *zip.File) (int, error) {
	zip64 := zf.CompressedSize64 > math.MaxUint32 || zf.UncompressedSize64 > math.MaxUint32
	size := 12
	if zip64 {
		size = 20
	}
	buf := make([]byte, size+4)
	available := archiveSize - offset
	if available < int64(size) {
		return 0, archiveError("archive_header_mismatch", nil)
	}
	readSize := len(buf)
	if available < int64(readSize) {
		readSize = int(available)
	}
	if _, err := r.ReadAt(buf[:readSize], offset); err != nil {
		return 0, archiveError("archive_header_mismatch", err)
	}
	start := 0
	if readSize >= 4 && binary.LittleEndian.Uint32(buf[:4]) == 0x08074b50 {
		start = 4
	}
	if readSize-start < size {
		return 0, archiveError("archive_header_mismatch", nil)
	}
	if binary.LittleEndian.Uint32(buf[start:start+4]) != zf.CRC32 {
		return 0, archiveError("archive_header_mismatch", nil)
	}
	if zip64 {
		if binary.LittleEndian.Uint64(buf[start+4:start+12]) != zf.CompressedSize64 || binary.LittleEndian.Uint64(buf[start+12:start+20]) != zf.UncompressedSize64 {
			return 0, archiveError("archive_header_mismatch", nil)
		}
	} else if binary.LittleEndian.Uint32(buf[start+4:start+8]) != uint32(zf.CompressedSize64) || binary.LittleEndian.Uint32(buf[start+8:start+12]) != uint32(zf.UncompressedSize64) {
		return 0, archiveError("archive_header_mismatch", nil)
	}
	return start + size, nil
}

func (p ZIPInspection) String() string {
	return fmt.Sprintf("entries=%d archive_bytes=%d declared_bytes=%d verified_bytes=%d", p.EntryCount, p.ArchiveBytes, p.DeclaredUncompressed, p.VerifiedUncompressed)
}
