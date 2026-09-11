package executor

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

const (
	FileSecurityEventIntegrityAdded       = "code_integrity_added"
	FileSecurityEventIntegrityModified    = "code_integrity_modified"
	FileSecurityEventIntegrityDeleted     = "code_integrity_deleted"
	FileSecurityEventIntegrityLink        = "code_integrity_link_changed"
	FileSecurityEventIntegrityUnavailable = "code_integrity_unavailable"
	fileIntegrityInterval                 = time.Hour
	fileIntegrityTimeout                  = 10 * time.Minute
	fileIntegrityMaxEntries               = 250000
	fileIntegrityMaxBytes                 = int64(20 << 30)
)

type fileIntegrityHeader struct {
	Kind        string `json:"kind"`
	Version     int    `json:"version"`
	SiteID      int    `json:"site_id"`
	WebRootHash string `json:"web_root_hash"`
	BaselineAt  string `json:"baseline_at"`
}

type fileIntegrityEntry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size,omitempty"`
	Digest string `json:"sha256,omitempty"`
}

type fileIntegrityDifference struct {
	EventType string
	Path      string
	Size      int64
	Signature string
}

var fileIntegrityRunMu sync.Mutex

var (
	fileIntegrityStateMu     sync.Mutex
	fileIntegrityRefreshJobs = map[int]string{}
	fileIntegrityRetryAfter  = map[int]time.Time{}
)

var errFileIntegrityIdentityMismatch = errors.New("integrity baseline identity mismatch")

func fileIntegrityRoot() (string, error) {
	if config.AppConfig == nil || strings.TrimSpace(config.AppConfig.Panel.DataDir) == "" {
		return "", errors.New("panel data directory unavailable")
	}
	root := filepath.Join(filepath.Clean(config.AppConfig.Panel.DataDir), "file-integrity")
	if !filepath.IsAbs(root) {
		return "", errors.New("file integrity directory must be absolute")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", errors.New("create file integrity directory")
	}
	if err := os.Chmod(root, 0700); err != nil {
		return "", errors.New("secure file integrity directory")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("invalid file integrity directory")
	}
	return root, nil
}

func fileIntegrityPath(siteID int) (string, error) {
	if siteID <= 0 {
		return "", errors.New("invalid site")
	}
	root, err := fileIntegrityRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, strconv.Itoa(siteID)+".jsonl"), nil
}

func integrityRootHash(root string) string {
	s := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(s[:])
}

func loadFileIntegritySite(db *sql.DB, id int) (*models.Website, error) {
	s := &models.Website{}
	err := db.QueryRow(`SELECT id,domain,status,system_user,web_root,site_type,file_lock_enabled,file_lock_mode,file_lock_apply_status FROM websites WHERE id=?`, id).
		Scan(&s.ID, &s.Domain, &s.Status, &s.SystemUser, &s.WebRoot, &s.SiteType, &s.FileLockEnabled, &s.FileLockMode, &s.FileLockApplyStatus)
	return s, err
}

func refreshWPCodeIntegrityBaselineOwned(siteID int) error {
	db := database.GetDB()
	if db == nil {
		return errors.New("database unavailable")
	}
	site, err := loadFileIntegritySite(db, siteID)
	if err != nil {
		return err
	}
	if !fileIntegrityEligible(site) {
		return errors.New("file lock is not ready")
	}
	m := DefaultMaintenanceManager()
	_, state, _, err := m.load(site.ID)
	if err != nil || state.Window != nil || m.isUncertain(site.ID) {
		return ErrMaintenanceBusy
	}
	if err := m.conflicts(site.ID); err != nil {
		return err
	}
	if err := VerifySiteFileLockMode(site, site.FileLockMode); err != nil {
		return errors.New("file lock verification failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), fileIntegrityTimeout)
	defer cancel()
	if err := writeFileIntegrityBaseline(ctx, site); err != nil {
		return err
	}
	return resolveIntegrityUnavailable(db, site.ID)
}

func refreshWPCodeIntegrityBaselineBestEffort(siteID int, operation string) {
	if db := database.GetDB(); db != nil {
		if site, err := loadFileIntegritySite(db, siteID); err != nil || !fileIntegrityEligible(site) {
			return
		}
	}
	fileIntegrityStateMu.Lock()
	if _, queued := fileIntegrityRefreshJobs[siteID]; !queued {
		fileIntegrityRefreshJobs[siteID] = operation
	}
	fileIntegrityStateMu.Unlock()
}

func recordIntegrityRefreshFailure(siteID int, operation string, err error) {
	log.Printf("%s后刷新代码完整性基线失败 site=%d: %v", operation, siteID, err)
	if db := database.GetDB(); db != nil {
		if site, loadErr := loadFileIntegritySite(db, siteID); loadErr == nil && fileIntegrityEligible(site) {
			_ = recordIntegrityUnavailable(db, site, err)
		}
	}
}

func RefreshWPCodeIntegrityBaselineBestEffort(siteID int, operation string) {
	refreshWPCodeIntegrityBaselineBestEffort(siteID, operation)
}

func writeFileIntegrityBaseline(ctx context.Context, site *models.Website) error {
	target, err := fileIntegrityPath(site.ID)
	if err != nil {
		return err
	}
	tmpName, err := createFileIntegritySnapshot(ctx, site, filepath.Dir(target))
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	if err := os.Rename(tmpName, target); err != nil {
		return errors.New("publish integrity baseline")
	}
	keep = true
	if d, err := os.Open(filepath.Dir(target)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func createFileIntegritySnapshot(ctx context.Context, site *models.Website, dir string) (string, error) {
	tmp, err := os.CreateTemp(dir, "."+strconv.Itoa(site.ID)+"-*.tmp")
	if err != nil {
		return "", errors.New("create integrity baseline")
	}
	name := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return "", errors.New("secure integrity baseline")
	}
	w := bufio.NewWriterSize(tmp, 128*1024)
	h := fileIntegrityHeader{Kind: "header", Version: 1, SiteID: site.ID, WebRootHash: integrityRootHash(site.WebRoot), BaselineAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeIntegrityLine(w, h); err != nil {
		return "", err
	}
	if err := walkFileIntegrityEntries(ctx, site.WebRoot, func(e fileIntegrityEntry) error { return writeIntegrityLine(w, e) }); err != nil {
		return "", err
	}
	if err := w.Flush(); err != nil {
		return "", errors.New("flush integrity baseline")
	}
	if err := tmp.Sync(); err != nil {
		return "", errors.New("sync integrity baseline")
	}
	if err := tmp.Close(); err != nil {
		return "", errors.New("close integrity baseline")
	}
	ok = true
	return name, nil
}

func writeIntegrityLine(w io.Writer, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return errors.New("encode integrity baseline")
	}
	b = append(b, '\n')
	if _, err := w.Write(b); err != nil {
		return errors.New("write integrity baseline")
	}
	return nil
}

func walkFileIntegrityEntries(ctx context.Context, webRoot string, emit func(fileIntegrityEntry) error) error {
	root, err := safeSiteWebRoot(webRoot)
	if err != nil {
		return errors.New("invalid site root")
	}
	started := time.Now()
	count := 0
	var total int64
	return filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("read integrity path")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(started) > fileIntegrityTimeout {
			return errors.New("integrity scan time limit exceeded")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return errors.New("resolve integrity path")
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		include, descend := fileIntegrityScope(rel, d.IsDir())
		if d.IsDir() && !descend {
			if include {
				count++
				if count > fileIntegrityMaxEntries {
					return errors.New("integrity scan file limit exceeded")
				}
				if err := emit(fileIntegrityEntry{Path: rel, Kind: "dir"}); err != nil {
					return err
				}
			}
			return filepath.SkipDir
		}
		if !include {
			return nil
		}
		count++
		if count > fileIntegrityMaxEntries {
			return errors.New("integrity scan file limit exceeded")
		}
		info, err := d.Info()
		if err != nil {
			return errors.New("stat integrity path")
		}
		e := fileIntegrityEntry{Path: rel}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return errors.New("read integrity link")
			}
			s := sha256.Sum256([]byte(target))
			e.Kind, e.Digest = "symlink", hex.EncodeToString(s[:])
		case info.IsDir():
			e.Kind = "dir"
		case info.Mode().IsRegular():
			e.Kind, e.Size = "file", info.Size()
			if info.Size() < 0 || total > fileIntegrityMaxBytes-info.Size() {
				return errors.New("integrity scan byte limit exceeded")
			}
			total += info.Size()
			f, err := os.Open(path)
			if err != nil {
				return errors.New("open integrity file")
			}
			h := sha256.New()
			_, copyErr := io.Copy(h, f)
			closeErr := f.Close()
			if copyErr != nil || closeErr != nil {
				return errors.New("hash integrity file")
			}
			e.Digest = hex.EncodeToString(h.Sum(nil))
		default:
			e.Kind = "other"
		}
		return emit(e)
	})
}

func fileIntegrityScope(rel string, isDir bool) (bool, bool) {
	p := strings.Split(rel, "/")
	if len(p) == 1 {
		if !isDir {
			return true, false
		}
		if p[0] == "wp-admin" || p[0] == "wp-includes" || p[0] == "wp-content" {
			return true, true
		}
		return true, false
	}
	if p[0] == "wp-admin" || p[0] == "wp-includes" {
		return true, true
	}
	if p[0] != "wp-content" {
		return false, false
	}
	if len(p) == 2 {
		if !isDir {
			return true, false
		}
		if p[1] == "plugins" || p[1] == "themes" || p[1] == "mu-plugins" {
			return true, true
		}
		return true, false
	}
	return p[1] == "plugins" || p[1] == "themes" || p[1] == "mu-plugins", true
}

// readFileIntegrityManifest and compareFileIntegrity are retained as a bounded
// map-based test oracle for the production streaming comparison.
func readFileIntegrityManifest(path string, site *models.Website) (map[string]fileIntegrityEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 2*1024*1024)
	if !s.Scan() {
		return nil, errors.New("integrity baseline header unavailable")
	}
	var h fileIntegrityHeader
	if json.Unmarshal(s.Bytes(), &h) != nil || h.Kind != "header" || h.Version != 1 || h.SiteID != site.ID || h.WebRootHash != integrityRootHash(site.WebRoot) {
		return nil, errors.New("integrity baseline identity mismatch")
	}
	result := map[string]fileIntegrityEntry{}
	for s.Scan() {
		var e fileIntegrityEntry
		if json.Unmarshal(s.Bytes(), &e) != nil || e.Path == "" {
			return nil, errors.New("invalid integrity baseline entry")
		}
		if _, ok := result[e.Path]; ok {
			return nil, errors.New("duplicate integrity baseline entry")
		}
		result[e.Path] = e
	}
	if s.Err() != nil {
		return nil, errors.New("read integrity baseline")
	}
	return result, nil
}

func compareFileIntegrity(ctx context.Context, site *models.Website, baseline map[string]fileIntegrityEntry) ([]fileIntegrityDifference, error) {
	current := make(map[string]fileIntegrityEntry, len(baseline))
	if err := walkFileIntegrityEntries(ctx, site.WebRoot, func(e fileIntegrityEntry) error { current[e.Path] = e; return nil }); err != nil {
		return nil, err
	}
	diffs := []fileIntegrityDifference{}
	for path, after := range current {
		before, ok := baseline[path]
		if !ok {
			diffs = append(diffs, makeIntegrityDifference(FileSecurityEventIntegrityAdded, path, fileIntegrityEntry{}, after))
		} else if before != after && (before.Kind == "symlink" || after.Kind == "symlink") {
			diffs = append(diffs, makeIntegrityDifference(FileSecurityEventIntegrityLink, path, before, after))
		} else if before != after {
			diffs = append(diffs, makeIntegrityDifference(FileSecurityEventIntegrityModified, path, before, after))
		}
	}
	for path, before := range baseline {
		if _, ok := current[path]; !ok {
			diffs = append(diffs, makeIntegrityDifference(FileSecurityEventIntegrityDeleted, path, before, fileIntegrityEntry{}))
		}
	}
	sort.Slice(diffs, func(i, j int) bool {
		if diffs[i].Path == diffs[j].Path {
			return diffs[i].EventType < diffs[j].EventType
		}
		return diffs[i].Path < diffs[j].Path
	})
	return diffs, nil
}

func compareFileIntegrityManifestFiles(baselinePath, currentPath string, site *models.Website) ([]fileIntegrityDifference, error) {
	open := func(path string) (*os.File, *bufio.Scanner, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, err
		}
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 64*1024), 2*1024*1024)
		if !s.Scan() {
			f.Close()
			return nil, nil, errors.New("integrity baseline header unavailable")
		}
		var h fileIntegrityHeader
		if json.Unmarshal(s.Bytes(), &h) != nil || h.Kind != "header" || h.Version != 1 {
			f.Close()
			return nil, nil, errors.New("invalid integrity baseline header")
		}
		if h.SiteID != site.ID || h.WebRootHash != integrityRootHash(site.WebRoot) {
			f.Close()
			return nil, nil, errFileIntegrityIdentityMismatch
		}
		return f, s, nil
	}
	oldFile, oldScan, err := open(baselinePath)
	if err != nil {
		return nil, err
	}
	defer oldFile.Close()
	newFile, newScan, err := open(currentPath)
	if err != nil {
		return nil, err
	}
	defer newFile.Close()
	next := func(s *bufio.Scanner) (fileIntegrityEntry, bool, error) {
		if !s.Scan() {
			if s.Err() != nil {
				return fileIntegrityEntry{}, false, errors.New("read integrity baseline")
			}
			return fileIntegrityEntry{}, false, nil
		}
		var e fileIntegrityEntry
		if json.Unmarshal(s.Bytes(), &e) != nil || e.Path == "" {
			return e, false, errors.New("invalid integrity baseline entry")
		}
		return e, true, nil
	}
	before, hasBefore, err := next(oldScan)
	if err != nil {
		return nil, err
	}
	after, hasAfter, err := next(newScan)
	if err != nil {
		return nil, err
	}
	diffs := []fileIntegrityDifference{}
	for hasBefore || hasAfter {
		switch {
		case !hasBefore || (hasAfter && compareFileIntegrityPath(after.Path, before.Path) < 0):
			diffs = append(diffs, makeIntegrityDifference(FileSecurityEventIntegrityAdded, after.Path, fileIntegrityEntry{}, after))
			after, hasAfter, err = next(newScan)
		case !hasAfter || compareFileIntegrityPath(before.Path, after.Path) < 0:
			diffs = append(diffs, makeIntegrityDifference(FileSecurityEventIntegrityDeleted, before.Path, before, fileIntegrityEntry{}))
			before, hasBefore, err = next(oldScan)
		default:
			if before != after {
				kind := FileSecurityEventIntegrityModified
				if before.Kind == "symlink" || after.Kind == "symlink" {
					kind = FileSecurityEventIntegrityLink
				}
				diffs = append(diffs, makeIntegrityDifference(kind, before.Path, before, after))
			}
			before, hasBefore, err = next(oldScan)
			if err == nil {
				after, hasAfter, err = next(newScan)
			}
		}
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(diffs, func(i, j int) bool {
		if diffs[i].Path == diffs[j].Path {
			return diffs[i].EventType < diffs[j].EventType
		}
		return diffs[i].Path < diffs[j].Path
	})
	return diffs, nil
}

func compareFileIntegrityPath(a, b string) int {
	aParts, bParts := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		if aParts[i] < bParts[i] {
			return -1
		}
		if aParts[i] > bParts[i] {
			return 1
		}
	}
	if len(aParts) < len(bParts) {
		return -1
	}
	if len(aParts) > len(bParts) {
		return 1
	}
	return 0
}

func makeIntegrityDifference(kind, path string, before, after fileIntegrityEntry) fileIntegrityDifference {
	s := sha256.Sum256([]byte(kind + "\x00" + path + "\x00" + before.Kind + "\x00" + before.Digest + "\x00" + after.Kind + "\x00" + after.Digest))
	return fileIntegrityDifference{EventType: kind, Path: path, Size: after.Size, Signature: hex.EncodeToString(s[:8])}
}

func fileIntegrityEligible(s *models.Website) bool {
	return s != nil && s.SiteType == "wordpress" && s.Status == models.StatusActive && s.FileLockEnabled && s.FileLockApplyStatus == FileLockApplyStatusReady
}

func scanWPCodeIntegritySite(siteID int, notify bool) error {
	db := database.GetDB()
	if db == nil {
		return errors.New("database unavailable")
	}
	site, err := loadFileIntegritySite(db, siteID)
	if err != nil || !fileIntegrityEligible(site) {
		return err
	}
	if !TryAcquireSiteOpLock(site.ID, "code_integrity") {
		return ErrMaintenanceBusy
	}
	defer ReleaseSiteOpLock(site.ID)
	m := DefaultMaintenanceManager()
	_, state, _, err := m.load(site.ID)
	if err != nil || state.Window != nil || m.isUncertain(site.ID) {
		return ErrMaintenanceBusy
	}
	if err := m.conflicts(site.ID); err != nil {
		return err
	}
	path, err := fileIntegrityPath(site.ID)
	if err != nil {
		return recordIntegrityUnavailable(db, site, err)
	}
	_, err = readFileIntegrityManifestHeader(path, site)
	if os.IsNotExist(err) {
		if err := writeFileIntegrityBaseline(context.Background(), site); err != nil {
			return recordIntegrityUnavailable(db, site, err)
		}
		return resolveIntegrityUnavailable(db, site.ID)
	}
	if errors.Is(err, errFileIntegrityIdentityMismatch) {
		if err := VerifySiteFileLockMode(site, site.FileLockMode); err != nil {
			return recordIntegrityUnavailable(db, site, errors.New("file lock verification failed"))
		}
		if err := writeFileIntegrityBaseline(context.Background(), site); err != nil {
			return recordIntegrityUnavailable(db, site, err)
		}
		return resolveIntegrityUnavailable(db, site.ID)
	}
	if err != nil {
		return recordIntegrityUnavailable(db, site, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), fileIntegrityTimeout)
	defer cancel()
	currentPath, err := createFileIntegritySnapshot(ctx, site, filepath.Dir(path))
	if err != nil {
		return recordIntegrityUnavailable(db, site, err)
	}
	defer os.Remove(currentPath)
	diffs, err := compareFileIntegrityManifestFiles(path, currentPath, site)
	if err != nil {
		return recordIntegrityUnavailable(db, site, err)
	}
	newCount, err := persistIntegrityDiffs(db, site, diffs)
	if err != nil {
		return err
	}
	if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
		return recordIntegrityUnavailable(db, site, errors.New("update integrity scan timestamp"))
	}
	_ = resolveIntegrityUnavailable(db, site.ID)
	if notify && newCount > 0 {
		msg := fmt.Sprintf("%s 在文件锁定期间发现 %d 项新的代码完整性变化。请在安全防御的文件安全页面核查。", site.Domain, newCount)
		sendResolvedAlertEvent("alert_wp_code_integrity", "WordPress 代码完整性变化", msg, "请确认是否来自授权维护；面板不会自动删除或恢复文件。")
	}
	return nil
}

func readFileIntegrityManifestHeader(path string, site *models.Website) (fileIntegrityHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return fileIntegrityHeader{}, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	if !s.Scan() {
		return fileIntegrityHeader{}, errors.New("integrity baseline header unavailable")
	}
	var h fileIntegrityHeader
	if json.Unmarshal(s.Bytes(), &h) != nil || h.Kind != "header" || h.Version != 1 {
		return h, errors.New("invalid integrity baseline header")
	}
	if h.SiteID != site.ID || h.WebRootHash != integrityRootHash(site.WebRoot) {
		return h, errFileIntegrityIdentityMismatch
	}
	return h, nil
}

func persistIntegrityDiffs(db *sql.DB, site *models.Website, diffs []fileIntegrityDifference) (int, error) {
	seen := map[string]bool{}
	fresh := 0
	for _, d := range diffs {
		path := "/" + d.Path
		seen[d.EventType+"\x00"+path] = true
		message := "锁定基线与当前文件状态不一致；变化签名=" + d.Signature
		var old string
		var resolved sql.NullString
		err := db.QueryRow(`SELECT message,resolved_at FROM file_security_events WHERE site_id=? AND event_type=? AND path=? AND ip_address='' AND request_method=''`, site.ID, d.EventType, path).Scan(&old, &resolved)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fresh, err
		}
		if errors.Is(err, sql.ErrNoRows) || old != message || resolved.Valid {
			fresh++
		}
		if err := upsertFileSecurityEvent(db, fileSecurityRecord{SiteID: site.ID, Domain: site.Domain, EventType: d.EventType, Source: "integrity", RiskLevel: "high", Path: path, FileSize: d.Size, Message: message}); err != nil {
			return fresh, err
		}
	}
	rows, err := db.Query(`SELECT event_type,path FROM file_security_events WHERE site_id=? AND source='integrity' AND event_type<>? AND resolved_at IS NULL`, site.ID, FileSecurityEventIntegrityUnavailable)
	if err != nil {
		return fresh, err
	}
	var stale [][2]string
	for rows.Next() {
		var k, p string
		if rows.Scan(&k, &p) == nil && !seen[k+"\x00"+p] {
			stale = append(stale, [2]string{k, p})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fresh, err
	}
	for _, v := range stale {
		if _, err := db.Exec(`UPDATE file_security_events SET resolved_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE site_id=? AND event_type=? AND path=? AND resolved_at IS NULL`, site.ID, v[0], v[1]); err != nil {
			return fresh, err
		}
	}
	return fresh, nil
}

func recordIntegrityUnavailable(db *sql.DB, site *models.Website, cause error) error {
	_ = upsertFileSecurityEvent(db, fileSecurityRecord{SiteID: site.ID, Domain: site.Domain, EventType: FileSecurityEventIntegrityUnavailable, Source: "integrity", RiskLevel: "medium", Path: "/", Message: "代码完整性基线或扫描不可用，已保留上一份有效基线并将在之后重试。"})
	return cause
}
func resolveIntegrityUnavailable(db *sql.DB, id int) error {
	_, err := db.Exec(`UPDATE file_security_events SET resolved_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE site_id=? AND event_type=? AND resolved_at IS NULL`, id, FileSecurityEventIntegrityUnavailable)
	return err
}

func runWPCodeIntegrityChecks() {
	if !fileIntegrityRunMu.TryLock() {
		return
	}
	defer fileIntegrityRunMu.Unlock()
	db := database.GetDB()
	if db == nil {
		return
	}
	if runQueuedWPCodeIntegrityRefresh(db) {
		return
	}
	rows, err := db.Query(`SELECT id FROM websites WHERE site_type='wordpress' AND status='active' AND file_lock_enabled=1 AND file_lock_apply_status=? ORDER BY id`, FileLockApplyStatusReady)
	if err != nil {
		return
	}
	ids := []int{}
	for rows.Next() {
		var id int
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	now := time.Now()
	for _, id := range ids {
		fileIntegrityStateMu.Lock()
		retryAfter := fileIntegrityRetryAfter[id]
		fileIntegrityStateMu.Unlock()
		if now.Before(retryAfter) {
			continue
		}
		path, err := fileIntegrityPath(id)
		if err == nil {
			if info, e := os.Lstat(path); e == nil && info.Mode().IsRegular() && now.Sub(info.ModTime()) < fileIntegrityInterval {
				continue
			}
		}
		err = scanWPCodeIntegritySite(id, true)
		fileIntegrityStateMu.Lock()
		if err != nil {
			fileIntegrityRetryAfter[id] = time.Now().Add(10 * time.Minute)
		} else {
			delete(fileIntegrityRetryAfter, id)
		}
		fileIntegrityStateMu.Unlock()
		return
	}
}

func runQueuedWPCodeIntegrityRefresh(db *sql.DB) bool {
	now := time.Now()
	fileIntegrityStateMu.Lock()
	ids := make([]int, 0, len(fileIntegrityRefreshJobs))
	for id := range fileIntegrityRefreshJobs {
		ids = append(ids, id)
	}
	fileIntegrityStateMu.Unlock()
	sort.Ints(ids)
	for _, id := range ids {
		fileIntegrityStateMu.Lock()
		operation, queued := fileIntegrityRefreshJobs[id]
		retryAfter := fileIntegrityRetryAfter[id]
		fileIntegrityStateMu.Unlock()
		if !queued || now.Before(retryAfter) {
			continue
		}
		site, err := loadFileIntegritySite(db, id)
		if err != nil || !fileIntegrityEligible(site) {
			fileIntegrityStateMu.Lock()
			delete(fileIntegrityRefreshJobs, id)
			delete(fileIntegrityRetryAfter, id)
			fileIntegrityStateMu.Unlock()
			continue
		}
		if !TryAcquireSiteOpLock(id, "code_integrity_refresh") {
			continue
		}
		err = refreshWPCodeIntegrityBaselineOwned(id)
		ReleaseSiteOpLock(id)
		fileIntegrityStateMu.Lock()
		if err == nil {
			delete(fileIntegrityRefreshJobs, id)
			delete(fileIntegrityRetryAfter, id)
		} else {
			fileIntegrityRetryAfter[id] = time.Now().Add(10 * time.Minute)
		}
		fileIntegrityStateMu.Unlock()
		if err != nil {
			recordIntegrityRefreshFailure(id, operation, err)
		}
		return true
	}
	return false
}

func RemoveWPCodeIntegrityBaseline(siteID int) error {
	fileIntegrityStateMu.Lock()
	delete(fileIntegrityRefreshJobs, siteID)
	delete(fileIntegrityRetryAfter, siteID)
	fileIntegrityStateMu.Unlock()
	path, err := fileIntegrityPath(siteID)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	err = nil
	if db := database.GetDB(); db != nil {
		_, err = db.Exec(`UPDATE file_security_events SET resolved_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE site_id=? AND source='integrity' AND resolved_at IS NULL`, siteID)
	}
	return err
}
