package executor

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

const (
	wpPanelFileLockBegin = "// BEGIN WP Panel File Lock"
	wpPanelFileLockEnd   = "// END WP Panel File Lock"

	FileLockModeLegacy   = "legacy"
	FileLockModeStandard = "standard"
	FileLockModeStrict   = "strict"

	FileLockApplyStatusReady    = "ready"
	FileLockApplyStatusApplying = "applying"
	FileLockApplyStatusFailed   = "failed"
)

var (
	disallowFileModsPattern      = regexp.MustCompile(`(?im)^\s*define\s*\(\s*['"]DISALLOW_FILE_MODS['"]\s*,\s*[^)]+\)\s*;\s*$`)
	disallowFileModsFalsePattern = regexp.MustCompile(`(?im)^\s*define\s*\(\s*['"]DISALLOW_FILE_MODS['"]\s*,\s*false\s*\)\s*;\s*$`)
	disallowFileModsTruePattern  = regexp.MustCompile(`(?im)^\s*define\s*\(\s*['"]DISALLOW_FILE_MODS['"]\s*,\s*true\s*\)\s*;\s*$`)
	fsMethodPattern              = regexp.MustCompile(`(?im)^\s*define\s*\(\s*['"]FS_METHOD['"]\s*,\s*[^)]+\)\s*;\s*$`)
)

var wpFileLockCodeDirs = map[string]struct{}{
	"mu-plugins": {},
	"plugins":    {},
	"themes":     {},
}

var wpFileLockLockedContentDirs = map[string]struct{}{
	"upgrade":             {},
	"upgrade-temp-backup": {},
}

var wpFileLockWritableContentDirs = map[string]map[string]struct{}{
	FileLockModeStandard: {
		"uploads": {},
		"cache":   {},
		"wflogs":  {},
	},
	FileLockModeStrict: {
		"uploads": {},
	},
}

var wpFileLockConfigNames = map[string]struct{}{
	".user.ini":         {},
	"php.ini":           {},
	"wordfence-waf.php": {},
	"wp-config.php":     {},
}

var wpFileLockKnownContentPHPFiles = map[string]struct{}{
	"advanced-cache.php": {},
	"db.php":             {},
	"index.php":          {},
	"object-cache.php":   {},
	"sunrise.php":        {},
}

type FileLockPreview struct {
	Mode            string   `json:"mode"`
	WritableDirs    []string `json:"writable_dirs"`
	ReadOnlyDirs    []string `json:"read_only_dirs"`
	ExecutableFiles []string `json:"executable_files"`
	SymlinkPaths    []string `json:"symlink_paths"`
	SensitiveFiles  []string `json:"sensitive_files"`
	Truncated       bool     `json:"truncated"`
}

func siteOwner(systemUser string) string {
	return systemUser + ":" + systemUser
}

func ensureSitePrimaryGroup(systemUser string) error {
	systemUser = strings.TrimSpace(systemUser)
	if systemUser == "" {
		return fmt.Errorf("system user is empty")
	}

	if _, err := executeCommand("getent", "group", systemUser); err != nil {
		if _, err := executeCommand("groupadd", "-r", systemUser); err != nil {
			if _, checkErr := executeCommand("getent", "group", systemUser); checkErr != nil {
				return fmt.Errorf("create site group %s: %w", systemUser, err)
			}
		}
	}

	if _, err := executeCommand("usermod", "-g", systemUser, systemUser); err != nil {
		return fmt.Errorf("set primary group for %s: %w", systemUser, err)
	}
	return nil
}

func HardenSiteSensitivePermissions(domain, webRoot, systemUser string) error {
	if err := ensureSitePrimaryGroup(systemUser); err != nil {
		return err
	}

	if webRoot != "" {
		if _, err := executeCommand("chown", "-R", siteOwner(systemUser), webRoot); err != nil {
			return err
		}
		configPath := filepath.Join(webRoot, "wp-config.php")
		if _, err := os.Stat(configPath); err == nil {
			if err := os.Chmod(configPath, 0600); err != nil {
				return err
			}
			if _, err := executeCommand("chown", siteOwner(systemUser), configPath); err != nil {
				return err
			}
		}
	}

	if domain != "" {
		secretsDir := filepath.Join("/var/wp-panel/site-secrets", domain)
		if _, err := os.Stat(secretsDir); err == nil {
			if err := os.Chmod(secretsDir, 0700); err != nil {
				return err
			}
			cfgPath := filepath.Join(secretsDir, "wp-panel-config.json")
			if _, err := os.Stat(cfgPath); err == nil {
				if err := os.Chmod(cfgPath, 0600); err != nil {
					return err
				}
			}
			if _, err := executeCommand("chown", "-R", siteOwner(systemUser), secretsDir); err != nil {
				return err
			}
		}
	}

	return nil
}

func isPathWithinRoot(rootPath, targetPath string) bool {
	cleanExistingPath := func(path string) (string, error) {
		cleanPath := filepath.Clean(path)
		resolved, err := filepath.EvalSymlinks(cleanPath)
		if err == nil {
			return resolved, nil
		}
		if runtime.GOOS == "windows" {
			return filepath.Abs(cleanPath)
		}
		return "", err
	}

	root, err := cleanExistingPath(rootPath)
	if err != nil {
		return false
	}
	target, err := cleanExistingPath(targetPath)
	if err != nil {
		return false
	}
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		target = strings.ToLower(target)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func IsWPExecutableFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".php", ".phtml", ".phar":
		return true
	default:
		return len(ext) == len(".php0") && strings.HasPrefix(ext, ".php") && ext[4] >= '0' && ext[4] <= '9'
	}
}

func NormalizeFileLockMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case FileLockModeLegacy, FileLockModeStandard, FileLockModeStrict:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid file lock mode")
	}
}

func EffectiveFileLockMode(site *models.Website) string {
	if site == nil || !site.FileLockEnabled {
		return ""
	}
	mode, err := NormalizeFileLockMode(site.FileLockMode)
	if err != nil {
		// Sites locked before modes existed keep the historical compatibility policy.
		return FileLockModeLegacy
	}
	return mode
}

func FileLockWritableContentDirs(mode string) ([]string, error) {
	mode, err := NormalizeFileLockMode(mode)
	if err != nil {
		return nil, err
	}
	if mode == FileLockModeLegacy {
		return []string{"uploads", "cache", "languages", "wflogs"}, nil
	}
	dirs := make([]string, 0, len(wpFileLockWritableContentDirs[mode]))
	for dir := range wpFileLockWritableContentDirs[mode] {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs, nil
}

func FileLockSecurityScanContentDirs(mode, webRoot string) ([]string, error) {
	mode, err := NormalizeFileLockMode(mode)
	if err != nil {
		return nil, err
	}
	if mode == FileLockModeLegacy {
		entries, readErr := os.ReadDir(filepath.Join(webRoot, "wp-content"))
		if readErr != nil {
			return nil, readErr
		}
		dirs := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && wpFileLockContentDirWritable(mode, entry.Name()) {
				dirs = append(dirs, entry.Name())
			}
		}
		sort.Strings(dirs)
		return dirs, nil
	}
	dirs, err := FileLockWritableContentDirs(mode)
	if err != nil {
		return nil, err
	}
	// Known runtime directories remain scanned even when a mode makes them read-only.
	// A post-lock executable there signals permission drift or a privileged write.
	seen := make(map[string]struct{}, len(dirs)+4)
	for _, dir := range dirs {
		seen[dir] = struct{}{}
	}
	for _, dir := range []string{"uploads", "cache", "languages", "wflogs"} {
		if _, ok := seen[dir]; !ok {
			dirs = append(dirs, dir)
			seen[dir] = struct{}{}
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

func IsWPFileLockRuntimeWritablePath(mode, webRoot, targetPath string, isDir, allowExecutableCleanup bool) bool {
	relParts, ok := wpFileLockRelParts(webRoot, targetPath)
	if !ok || len(relParts) < 3 || relParts[0] != "wp-content" {
		return false
	}
	if !wpFileLockContentDirWritable(mode, relParts[1]) {
		return false
	}
	if wpFileLockSensitiveConfigName(relParts, targetPath) {
		return false
	}
	if !isDir && IsWPExecutableFile(targetPath) {
		return allowExecutableCleanup && len(relParts) >= 3
	}
	return true
}

func wpFileLockPermissionWritablePath(mode, webRoot, targetPath string, isDir bool) bool {
	relParts, ok := wpFileLockRelParts(webRoot, targetPath)
	if !ok || len(relParts) == 0 {
		return false
	}
	if len(relParts) == 2 && relParts[0] == "wp-content" && isDir {
		return wpFileLockContentDirWritable(mode, relParts[1])
	}
	return IsWPFileLockRuntimeWritablePath(mode, webRoot, targetPath, isDir, false)
}

func wpFileLockContentDirWritable(mode, dir string) bool {
	mode, err := NormalizeFileLockMode(mode)
	if err != nil {
		return false
	}
	dir = strings.ToLower(strings.TrimSpace(dir))
	if _, locked := wpFileLockCodeDirs[dir]; locked {
		return false
	}
	if _, locked := wpFileLockLockedContentDirs[dir]; locked {
		return false
	}
	if dir == ".htaccess" {
		return false
	}
	if _, locked := wpFileLockConfigNames[dir]; locked {
		return false
	}
	if mode == FileLockModeLegacy {
		return dir != ""
	}
	_, writable := wpFileLockWritableContentDirs[mode][dir]
	return writable
}

func wpFileLockSensitiveConfigName(relParts []string, targetPath string) bool {
	base := strings.ToLower(filepath.Base(targetPath))
	if base == ".htaccess" {
		return len(relParts) <= 2
	}
	_, locked := wpFileLockConfigNames[base]
	return locked
}

func wpFileLockRelParts(webRoot, targetPath string) ([]string, bool) {
	root := filepath.Clean(webRoot)
	target := filepath.Clean(targetPath)
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		target = strings.ToLower(target)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "." || parts[0] == "" {
		return nil, false
	}
	return parts, true
}

func PreviewSiteFileLock(site *models.Website, mode string) (FileLockPreview, error) {
	if site == nil {
		return FileLockPreview{}, fmt.Errorf("site is nil")
	}
	mode, err := NormalizeFileLockMode(mode)
	if err != nil || mode == FileLockModeLegacy {
		return FileLockPreview{}, fmt.Errorf("invalid preview mode")
	}
	webRoot, err := safeSiteWebRoot(site.WebRoot)
	if err != nil {
		return FileLockPreview{}, err
	}

	preview := FileLockPreview{Mode: mode}
	writableDirs, err := FileLockWritableContentDirs(mode)
	if err != nil {
		return FileLockPreview{}, err
	}
	for _, dir := range writableDirs {
		preview.WritableDirs = append(preview.WritableDirs, filepath.ToSlash(filepath.Join("wp-content", dir)))
	}

	contentRoot := filepath.Join(webRoot, "wp-content")
	entries, err := os.ReadDir(contentRoot)
	if err != nil {
		return FileLockPreview{}, err
	}
	const maxPreviewEntries = 20000
	const maxPreviewFindings = 200
	visited := 0
	codeRoots := make([]string, 0, len(wpFileLockCodeDirs))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			preview.SymlinkPaths = append(preview.SymlinkPaths, filepath.ToSlash(filepath.Join("wp-content", entry.Name())))
			if len(preview.ExecutableFiles)+len(preview.SymlinkPaths) >= maxPreviewFindings {
				preview.Truncated = true
				break
			}
			continue
		}
		if !entry.IsDir() {
			name := strings.ToLower(entry.Name())
			if IsWPExecutableFile(entry.Name()) {
				if _, known := wpFileLockKnownContentPHPFiles[name]; !known {
					preview.ExecutableFiles = append(preview.ExecutableFiles, filepath.ToSlash(filepath.Join("wp-content", entry.Name())))
				}
			}
			continue
		}
		name := strings.ToLower(entry.Name())
		if !wpFileLockContentDirWritable(mode, name) {
			preview.ReadOnlyDirs = append(preview.ReadOnlyDirs, filepath.ToSlash(filepath.Join("wp-content", entry.Name())))
		}
		if _, codeDir := wpFileLockCodeDirs[name]; codeDir {
			codeRoots = append(codeRoots, filepath.Join(contentRoot, entry.Name()))
			continue
		}
		root := filepath.Join(contentRoot, entry.Name())
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			visited++
			if visited > maxPreviewEntries || len(preview.ExecutableFiles)+len(preview.SymlinkPaths) >= maxPreviewFindings {
				preview.Truncated = true
				return filepath.SkipAll
			}
			if d.Type()&os.ModeSymlink != 0 {
				rel, relErr := filepath.Rel(webRoot, path)
				if relErr == nil {
					preview.SymlinkPaths = append(preview.SymlinkPaths, filepath.ToSlash(rel))
				}
				return nil
			}
			if d.IsDir() || !IsWPExecutableFile(path) {
				return nil
			}
			rel, relErr := filepath.Rel(webRoot, path)
			if relErr == nil {
				preview.ExecutableFiles = append(preview.ExecutableFiles, filepath.ToSlash(rel))
			}
			return nil
		})
		if walkErr != nil {
			return FileLockPreview{}, walkErr
		}
		if preview.Truncated {
			break
		}
	}
	if !preview.Truncated {
		for _, root := range codeRoots {
			walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return nil
				}
				visited++
				if visited > maxPreviewEntries || len(preview.ExecutableFiles)+len(preview.SymlinkPaths) >= maxPreviewFindings {
					preview.Truncated = true
					return filepath.SkipAll
				}
				if d.Type()&os.ModeSymlink == 0 {
					return nil
				}
				rel, relErr := filepath.Rel(webRoot, path)
				if relErr == nil {
					preview.SymlinkPaths = append(preview.SymlinkPaths, filepath.ToSlash(rel))
				}
				return nil
			})
			if walkErr != nil {
				return FileLockPreview{}, walkErr
			}
			if preview.Truncated {
				break
			}
		}
	}

	for _, rel := range []string{
		"wp-config.php", ".user.ini", "php.ini", "wordfence-waf.php",
		filepath.Join("wp-content", "advanced-cache.php"),
		filepath.Join("wp-content", "object-cache.php"),
		filepath.Join("wp-content", "db.php"),
		filepath.Join("wp-content", "sunrise.php"),
	} {
		path := filepath.Join(webRoot, rel)
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink == 0 {
			preview.SensitiveFiles = append(preview.SensitiveFiles, filepath.ToSlash(rel))
		}
	}
	sort.Strings(preview.ReadOnlyDirs)
	sort.Strings(preview.ExecutableFiles)
	sort.Strings(preview.SymlinkPaths)
	sort.Strings(preview.SensitiveFiles)
	return preview, nil
}

func ChownSitePath(path, allowedRoot, systemUser string) error {
	path = filepath.Clean(strings.TrimSpace(path))
	allowedRoot = filepath.Clean(strings.TrimSpace(allowedRoot))
	systemUser = strings.TrimSpace(systemUser)
	if path == "" || path == "." || path == string(filepath.Separator) {
		return fmt.Errorf("path is unsafe")
	}
	if allowedRoot == "" || allowedRoot == "." || allowedRoot == string(filepath.Separator) {
		return fmt.Errorf("allowed root is unsafe")
	}
	if !isPathWithinRoot(allowedRoot, path) {
		return fmt.Errorf("path outside allowed root")
	}
	if systemUser == "" {
		return fmt.Errorf("system user is empty")
	}

	u, err := user.Lookup(systemUser)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return os.Chown(path, uid, gid)
	}
	return filepath.Walk(path, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(p, uid, gid)
	})
}

func executeSetFileLock(task *Task) TaskResult {
	payload, ok := task.Payload.(*SetFileLockPayload)
	if !ok || payload.Site == nil {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}
	site := payload.Site
	if site.SiteType != "" && site.SiteType != "wordpress" {
		return TaskResult{Success: false, Message: "只有 WordPress 站点支持文件锁定"}
	}
	if _, err := database.GetDB().Exec(
		"UPDATE websites SET file_lock_apply_status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		FileLockApplyStatusApplying, site.ID,
	); err != nil {
		return taskFailure("准备文件锁定设置失败", err)
	}

	var err error
	mode := ""
	if payload.Enabled {
		mode, err = NormalizeFileLockMode(payload.Mode)
		if err == nil {
			err = ApplySiteFileLockMode(site, mode)
		}
	} else {
		err = ApplySiteUnlockedPermissions(site)
	}
	if err != nil {
		if _, statusErr := database.GetDB().Exec(
			"UPDATE websites SET file_lock_apply_status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
			FileLockApplyStatusFailed, site.ID,
		); statusErr != nil {
			log.Printf("记录文件锁定权限应用失败状态 site=%d: %v", site.ID, statusErr)
		}
		return taskFailure("文件锁定设置失败", err)
	}

	enabled := 0
	lockEnabledAt := ""
	applyStatus := ""
	message := "文件锁定已关闭"
	if payload.Enabled {
		enabled = 1
		lockEnabledAt = formatEventTime(time.Now())
		applyStatus = FileLockApplyStatusReady
		message = "文件锁定已开启"
	} else if wpConfigHasUserFileModsLock(site.WebRoot) {
		message = "文件锁定已关闭，但 wp-config.php 中仍存在用户自定义 DISALLOW_FILE_MODS=true，WordPress 后台文件修改仍会被禁止"
	}
	if _, err := database.GetDB().Exec(
		"UPDATE websites SET file_lock_enabled = ?, file_lock_enabled_at = ?, file_lock_mode = ?, file_lock_apply_status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		enabled, lockEnabledAt, mode, applyStatus, site.ID,
	); err != nil {
		return taskFailure("保存文件锁定状态失败", err)
	}
	site.FileLockEnabled = payload.Enabled
	site.FileLockMode = mode
	site.FileLockApplyStatus = applyStatus

	return TaskResult{Success: true, Message: message, Data: map[string]interface{}{
		"file_lock_enabled":      payload.Enabled,
		"file_lock_mode":         mode,
		"file_lock_apply_status": site.FileLockApplyStatus,
	}}
}

func ApplySiteFileLock(site *models.Website) error {
	mode := EffectiveFileLockMode(site)
	if mode == "" {
		mode = FileLockModeLegacy
	}
	return ApplySiteFileLockMode(site, mode)
}

func ApplySiteFileLockMode(site *models.Website, mode string) error {
	if site == nil {
		return fmt.Errorf("site is nil")
	}
	if site.SiteType != "" && site.SiteType != "wordpress" {
		return fmt.Errorf("only WordPress sites support file lock")
	}
	mode, err := NormalizeFileLockMode(mode)
	if err != nil {
		return err
	}
	webRoot, err := safeSiteWebRoot(site.WebRoot)
	if err != nil {
		return err
	}
	systemUser := strings.TrimSpace(site.SystemUser)
	if systemUser == "" {
		return fmt.Errorf("system user is empty")
	}
	if err := ensureSitePrimaryGroup(systemUser); err != nil {
		return err
	}
	uid, gid, err := siteUserIDs(systemUser)
	if err != nil {
		return err
	}

	for _, path := range []string{
		filepath.Join(webRoot, "wp-config.php"),
		filepath.Join(webRoot, ".user.ini"),
		filepath.Join(webRoot, ".htaccess"),
		filepath.Join(webRoot, "php.ini"),
		filepath.Join(webRoot, "wordfence-waf.php"),
		filepath.Join(webRoot, "wp-admin"),
		filepath.Join(webRoot, "wp-includes"),
		filepath.Join(webRoot, "wp-content"),
		filepath.Join(webRoot, "wp-content", "plugins"),
		filepath.Join(webRoot, "wp-content", "themes"),
		filepath.Join(webRoot, "wp-content", "mu-plugins"),
	} {
		if err := rejectSymlinkPath(path); err != nil {
			return err
		}
	}
	if err := setWPFileModsLock(webRoot, true); err != nil {
		return err
	}
	writableDirs, err := FileLockWritableContentDirs(mode)
	if err != nil {
		return err
	}
	for _, dir := range writableDirs {
		if err := os.MkdirAll(filepath.Join(webRoot, "wp-content", dir), 0755); err != nil {
			return err
		}
	}

	return filepath.WalkDir(webRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if wpFileLockPermissionWritablePath(mode, webRoot, path, d.IsDir()) {
			return applyOwnerMode(path, uid, gid, modeForWritablePath(d))
		}
		mode := os.FileMode(0444)
		if d.IsDir() {
			mode = 0555
		}
		if filepath.Clean(path) == filepath.Join(webRoot, "wp-config.php") {
			mode = 0440
		}
		return applyOwnerMode(path, 0, gid, mode)
	})
}

func ApplySiteUnlockedPermissions(site *models.Website) error {
	if site == nil {
		return fmt.Errorf("site is nil")
	}
	webRoot, err := safeSiteWebRoot(site.WebRoot)
	if err != nil {
		return err
	}
	systemUser := strings.TrimSpace(site.SystemUser)
	if systemUser == "" {
		return fmt.Errorf("system user is empty")
	}
	if err := setWPFileModsLock(webRoot, false); err != nil {
		return err
	}
	uid, gid, err := siteUserIDs(systemUser)
	if err != nil {
		return err
	}
	if err := filepath.WalkDir(webRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		return applyOwnerMode(path, uid, gid, modeForWritablePath(d))
	}); err != nil {
		return err
	}
	return HardenSiteSensitivePermissions(site.Domain, webRoot, systemUser)
}

func safeSiteWebRoot(webRoot string) (string, error) {
	webRoot = filepath.Clean(strings.TrimSpace(webRoot))
	if webRoot == "" || webRoot == "." || webRoot == string(filepath.Separator) {
		return "", fmt.Errorf("web root is unsafe")
	}
	resolved, err := filepath.EvalSymlinks(webRoot)
	if err != nil {
		return "", err
	}
	resolved = filepath.Clean(resolved)
	if resolved == "" || resolved == "." || resolved == string(filepath.Separator) {
		return "", fmt.Errorf("web root is unsafe")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("web root is not a directory")
	}
	return resolved, nil
}

func siteUserIDs(systemUser string) (int, int, error) {
	u, err := user.Lookup(systemUser)
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

func modeForWritablePath(d fs.DirEntry) os.FileMode {
	if d.IsDir() {
		return 0755
	}
	return 0644
}

func applyOwnerMode(path string, uid, gid int, mode os.FileMode) error {
	if err := os.Chown(path, uid, gid); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func rejectSymlinkPath(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must not be a symlink", path)
	}
	return nil
}

func setWPFileModsLock(webRoot string, enabled bool) error {
	configPath := filepath.Join(webRoot, "wp-config.php")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	content := string(data)
	next, err := applyWPFileModsLockBlock(content, enabled)
	if err != nil {
		return err
	}
	if next == content {
		return nil
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, []byte(next), info.Mode().Perm())
}

func wpConfigHasUserFileModsLock(webRoot string) bool {
	data, err := os.ReadFile(filepath.Join(webRoot, "wp-config.php"))
	if err != nil {
		return false
	}
	content := removeWPPanelFileLockBlock(string(data))
	return disallowFileModsTruePattern.MatchString(content)
}

func applyWPFileModsLockBlock(content string, enabled bool) (string, error) {
	content = removeWPPanelFileLockBlock(content)
	if !enabled {
		return content, nil
	}
	if disallowFileModsFalsePattern.MatchString(content) {
		return "", fmt.Errorf("wp-config.php already defines DISALLOW_FILE_MODS as false")
	}
	if disallowFileModsPattern.MatchString(content) {
		content = fsMethodPattern.ReplaceAllString(content, "")
		block := wpPanelFileLockBegin + "\n" +
			"define('FS_METHOD', 'direct');\n" +
			wpPanelFileLockEnd + "\n"
		next := insertBeforeMarker(content, block)
		if next == content {
			return "", fmt.Errorf("wp-config.php marker not found")
		}
		return next, nil
	}
	block := wpPanelFileLockBegin + "\n" +
		"define('DISALLOW_FILE_MODS', true);\n" +
		"define('FS_METHOD', 'direct');\n" +
		wpPanelFileLockEnd + "\n"
	next := insertBeforeMarker(content, block)
	if next == content {
		return "", fmt.Errorf("wp-config.php marker not found")
	}
	return next, nil
}

func removeWPPanelFileLockBlock(content string) string {
	for {
		start := strings.Index(content, wpPanelFileLockBegin)
		if start < 0 {
			return content
		}
		end := strings.Index(content[start:], wpPanelFileLockEnd)
		if end < 0 {
			return content
		}
		end += start + len(wpPanelFileLockEnd)
		if end < len(content) && content[end] == '\r' {
			end++
		}
		if end < len(content) && content[end] == '\n' {
			end++
		}
		content = content[:start] + content[end:]
	}
}

func init() {
	database.RegisterUpgrade("1.0.4", HardenSiteUnixIsolation)
}

// HardenSiteUnixIsolation 对所有已有站点执行 Unix 用户组隔离和敏感文件权限加固（升级迁移用）。
func HardenSiteUnixIsolation() error {
	db := database.GetDB()
	rows, err := db.Query("SELECT domain, web_root, system_user FROM websites")
	if err != nil {
		return fmt.Errorf("查询网站列表失败: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var domain, webRoot, systemUser string
		if err := rows.Scan(&domain, &webRoot, &systemUser); err != nil {
			log.Printf("[权限加固] 读取网站数据失败: %v", err)
			continue
		}
		if err := HardenSiteSensitivePermissions(domain, webRoot, systemUser); err != nil {
			log.Printf("[权限加固] %s: 安全权限设置失败: %v", domain, err)
		}
	}

	return rows.Err()
}

// InstallPluginPermissions 安装插件时设置插件目录和密钥目录权限。
// 与 HardenSiteSensitivePermissions 不同，此函数不 chown 整站，且所有错误静默忽略（不阻断插件安装）。
func InstallPluginPermissions(domain, systemUser, pluginDir string) {
	systemUser = strings.TrimSpace(systemUser)
	if systemUser == "" {
		return
	}

	ensureSitePrimaryGroup(systemUser)
	owner := siteOwner(systemUser)

	if pluginDir != "" {
		executeCommand("chown", "-R", owner, pluginDir)
	}

	if domain != "" {
		secretsDir := filepath.Join("/var/wp-panel/site-secrets", domain)
		if _, err := os.Stat(secretsDir); err == nil {
			os.Chmod(secretsDir, 0700)
			cfgPath := filepath.Join(secretsDir, "wp-panel-config.json")
			if _, err := os.Stat(cfgPath); err == nil {
				os.Chmod(cfgPath, 0600)
			}
			executeCommand("chown", "-R", owner, secretsDir)
		}
	}
}
