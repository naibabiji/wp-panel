package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	phpCustomConfigDefaultPath = "/etc/php/8.3/fpm/conf.d/zz-wppanel-custom.ini"
	phpExtensionInstallTimeout = 10 * time.Minute
	phpCustomConfigMaxBytes    = 256 * 1024
)

var phpCustomConfigPath = phpCustomConfigDefaultPath

var phpExtensionDefinitions = []PHPExtensionDefinition{
	{ID: "imagick", Package: "php8.3-imagick", Module: "imagick", Description: "Image processing used by WordPress image workflows"},
	{ID: "soap", Package: "php8.3-soap", Module: "soap", Description: "SOAP integrations used by some WordPress services"},
	{ID: "bcmath", Package: "php8.3-bcmath", Module: "bcmath", Description: "Arbitrary precision math used by plugins and payment integrations"},
	{ID: "gmp", Package: "php8.3-gmp", Module: "gmp", Description: "GMP math used by selected integrations"},
	{ID: "memcached", Package: "php8.3-memcached", Module: "memcached", Description: "Memcached client; Redis remains the panel default recommendation"},
}

var phpPoolAdminKeys = []string{
	"memory_limit", "upload_max_filesize", "post_max_size", "max_execution_time", "max_input_time",
	"disable_functions", "open_basedir", "allow_url_fopen", "allow_url_include",
}

var extensionLinePattern = regexp.MustCompile(`(?i)^\s*(extension|zend_extension)\s*=\s*(.+?)\s*$`)

type PHPExtensionDefinition struct {
	ID          string `json:"id"`
	Package     string `json:"package"`
	Module      string `json:"module"`
	Description string `json:"description"`
}

type PHPExtensionStatus struct {
	PHPExtensionDefinition
	PackageInstalled bool   `json:"package_installed"`
	Loaded           bool   `json:"loaded"`
	Error            string `json:"error,omitempty"`
}

type PHPIniPathRisk struct {
	Directive string   `json:"directive"`
	Path      string   `json:"path"`
	Reasons   []string `json:"reasons"`
}

type PHPIniInspection struct {
	Content            string           `json:"content,omitempty"`
	Path               string           `json:"path"`
	HighRiskPaths      []PHPIniPathRisk `json:"high_risk_paths,omitempty"`
	PoolOverriddenKeys []string         `json:"pool_overridden_keys,omitempty"`
}

func PHPCustomConfigPath() string { return phpCustomConfigPath }

func ReadPHPCustomConfig() (string, error) {
	data, err := os.ReadFile(phpCustomConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func InspectPHPCustomConfig(content string) (PHPIniInspection, error) {
	if len(content) > phpCustomConfigMaxBytes {
		return PHPIniInspection{}, fmt.Errorf("custom PHP configuration exceeds %d bytes", phpCustomConfigMaxBytes)
	}
	if strings.IndexByte(content, 0) >= 0 {
		return PHPIniInspection{}, errors.New("custom PHP configuration contains NUL byte")
	}
	inspection := PHPIniInspection{Content: content, Path: phpCustomConfigPath}
	seenKeys := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		match := extensionLinePattern.FindStringSubmatch(line)
		if len(match) != 3 {
			continue
		}
		directive := strings.ToLower(match[1])
		value := strings.TrimSpace(strings.Trim(match[2], `"'`))
		value = strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
		if value == "" || !filepath.IsAbs(value) {
			continue
		}
		info, err := os.Lstat(filepath.Clean(value))
		if err != nil {
			return PHPIniInspection{}, fmt.Errorf("%s path %q is unavailable: %w", directive, value, err)
		}
		if !info.Mode().IsRegular() {
			return PHPIniInspection{}, fmt.Errorf("%s path %q is not a regular file", directive, value)
		}
		if risk := inspectPHPIniPathRisk(value); len(risk) > 0 {
			inspection.HighRiskPaths = append(inspection.HighRiskPaths, PHPIniPathRisk{Directive: directive, Path: value, Reasons: risk})
		}
	}
	for _, key := range phpPoolAdminKeys {
		if findIniValue(content, key) != "" {
			seenKeys[key] = true
		}
	}
	for _, key := range phpPoolAdminKeys {
		if seenKeys[key] {
			inspection.PoolOverriddenKeys = append(inspection.PoolOverriddenKeys, key)
		}
	}
	return inspection, nil
}

func inspectPHPIniPathRisk(path string) []string {
	reasons := []string{}
	clean := filepath.Clean(path)
	if strings.HasPrefix(clean, "/tmp/") || clean == "/tmp" {
		reasons = append(reasons, "path is under /tmp")
	}
	if strings.HasPrefix(clean, "/www/wwwroot/") || clean == "/www/wwwroot" {
		reasons = append(reasons, "path is under a website WebRoot")
	}
	for current := clean; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			break
		}
		if info.Mode().Perm()&0022 != 0 {
			reasons = append(reasons, "file or parent directory has group/other write permission")
			break
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != 0 {
			reasons = append(reasons, "file or parent directory is not root-owned")
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return uniquePHPStrings(reasons)
}

func uniquePHPStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

var (
	phpFPMConfigTest = func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, "php-fpm8.3", "-t").CombinedOutput()
	}
	phpFPMReload = func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, "systemctl", "reload", "php8.3-fpm").CombinedOutput()
	}
)

func SavePHPCustomConfig(ctx context.Context, content string, confirmLoaderRisk bool) (PHPIniInspection, error) {
	inspection, err := InspectPHPCustomConfig(content)
	if err != nil {
		return PHPIniInspection{}, err
	}
	if len(inspection.HighRiskPaths) > 0 && !confirmLoaderRisk {
		return inspection, ErrPHPHighRiskConfirmationRequired
	}
	old, readErr := os.ReadFile(phpCustomConfigPath)
	oldExists := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return inspection, readErr
	}
	if err := writePHPConfigAtomically(phpCustomConfigPath, []byte(content), len(content) == 0); err != nil {
		return inspection, err
	}
	testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	testOut, testErr := phpFPMConfigTest(testCtx)
	cancel()
	if testErr != nil {
		_ = restorePHPConfig(phpCustomConfigPath, old, oldExists)
		return inspection, fmt.Errorf("PHP-FPM configuration test failed: %s", strings.TrimSpace(string(testOut)))
	}
	reloadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	reloadOut, reloadErr := phpFPMReload(reloadCtx)
	cancel()
	if reloadErr != nil {
		restoreErr := restorePHPConfig(phpCustomConfigPath, old, oldExists)
		if restoreErr == nil {
			recoveryCtx, recoveryCancel := context.WithTimeout(ctx, 30*time.Second)
			_, recoveryErr := phpFPMReload(recoveryCtx)
			recoveryCancel()
			restoreErr = recoveryErr
		}
		if restoreErr != nil {
			return inspection, fmt.Errorf("PHP-FPM reload failed and restore failed: reload=%s restore=%v", strings.TrimSpace(string(reloadOut)), restoreErr)
		}
		return inspection, fmt.Errorf("PHP-FPM reload failed and the previous configuration was restored: %s", strings.TrimSpace(string(reloadOut)))
	}
	_ = old
	return inspection, nil
}

var ErrPHPHighRiskConfirmationRequired = errors.New("high-risk loader confirmation required")

func writePHPConfigAtomically(path string, content []byte, removeEmpty bool) error {
	if removeEmpty {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".zz-wppanel-custom-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func restorePHPConfig(path string, old []byte, existed bool) error {
	if !existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writePHPConfigAtomically(path, old, false)
}

func PHPPoolAdminOverrideKeys() []string { return append([]string(nil), phpPoolAdminKeys...) }

func PHPFPMLoadedModules(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "php-fpm8.3", "-m").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("php-fpm module listing failed: %s", strings.TrimSpace(string(out)))
	}
	return parsePHPModuleList(string(out)), nil
}

func parsePHPModuleList(output string) []string {
	modules := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[") || seen[strings.ToLower(line)] {
			continue
		}
		seen[strings.ToLower(line)] = true
		modules = append(modules, line)
	}
	sort.Strings(modules)
	return modules
}

func PHPExtensionStatuses(ctx context.Context) []PHPExtensionStatus {
	loadedOutput, loadedErr := exec.CommandContext(ctx, "php-fpm8.3", "-m").CombinedOutput()
	loaded := map[string]bool{}
	for _, module := range parsePHPModuleList(string(loadedOutput)) {
		loaded[strings.ToLower(module)] = true
	}
	result := make([]PHPExtensionStatus, 0, len(phpExtensionDefinitions))
	for _, definition := range phpExtensionDefinitions {
		status := PHPExtensionStatus{PHPExtensionDefinition: definition, Loaded: loaded[strings.ToLower(definition.Module)]}
		out, err := exec.CommandContext(ctx, "dpkg-query", "-W", "-f=${Status}", definition.Package).CombinedOutput()
		status.PackageInstalled = err == nil && strings.Contains(string(out), "install ok installed")
		if loadedErr != nil {
			status.Error = strings.TrimSpace(string(loadedOutput))
		}
		result = append(result, status)
	}
	return result
}

func IsSupportedPHPExtension(id string) bool {
	_, ok := phpExtensionDefinition(id)
	return ok
}

func InstallPHPExtension(ctx context.Context, id string) error {
	definition, ok := phpExtensionDefinition(id)
	if !ok {
		return errors.New("unsupported PHP extension")
	}
	if os.Geteuid() != 0 {
		return errors.New("PHP extension installation requires root")
	}
	select {
	case developmentToolInstallLock <- struct{}{}:
		defer func() { <-developmentToolInstallLock }()
	case <-ctx.Done():
		return ctx.Err()
	}
	packageLock, err := acquireSystemPackageLock(ctx)
	if err != nil {
		return err
	}
	defer releaseSystemPackageLock(packageLock)
	installCtx, cancel := context.WithTimeout(ctx, phpExtensionInstallTimeout)
	defer cancel()
	if installed, err := phpPackageInstalled(installCtx, definition.Package); err != nil {
		return err
	} else if !installed {
		if out, err := exec.CommandContext(installCtx, "apt-get", "-y", "-o", "Dpkg::Lock::Timeout=60", "install", definition.Package).CombinedOutput(); err != nil {
			return fmt.Errorf("install %s failed: %s", definition.Package, strings.TrimSpace(string(out)))
		}
	}
	moduleOutput, moduleErr := exec.CommandContext(installCtx, "php-fpm8.3", "-m").CombinedOutput()
	if moduleErr != nil || !phpModuleLoaded(string(moduleOutput), definition.Module) {
		return fmt.Errorf("package %s is installed but PHP-FPM did not load module %s: %s", definition.Package, definition.Module, strings.TrimSpace(string(moduleOutput)))
	}
	if out, err := phpFPMConfigTest(installCtx); err != nil {
		return fmt.Errorf("PHP-FPM configuration test failed after installing %s: %s", definition.Package, strings.TrimSpace(string(out)))
	}
	if out, err := phpFPMReload(installCtx); err != nil {
		return fmt.Errorf("PHP-FPM reload failed after installing %s: %s", definition.Package, strings.TrimSpace(string(out)))
	}
	return nil
}

func phpPackageInstalled(ctx context.Context, packageName string) (bool, error) {
	out, err := exec.CommandContext(ctx, "dpkg-query", "-W", "-f=${Status}", packageName).CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	return err == nil && strings.Contains(string(out), "install ok installed"), nil
}

func phpModuleLoaded(output, module string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.EqualFold(strings.TrimSpace(line), module) {
			return true
		}
	}
	return false
}

func phpExtensionDefinition(id string) (PHPExtensionDefinition, bool) {
	for _, definition := range phpExtensionDefinitions {
		if definition.ID == id {
			return definition, true
		}
	}
	return PHPExtensionDefinition{}, false
}

func acquireSystemPackageLock(ctx context.Context) (*os.File, error) {
	lock, err := os.OpenFile(systemPackageUpdateLockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			lock.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			lock.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func releaseSystemPackageLock(lock *os.File) {
	if lock == nil {
		return
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_ = lock.Close()
}
