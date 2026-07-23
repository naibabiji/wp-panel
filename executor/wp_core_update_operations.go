package executor

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

type wpCoreUpdateRunner interface {
	Update(context.Context, wpCoreUpdateExecution) error
	CheckLoad(context.Context, wpCoreUpdateExecution, string) error
}

type wpCoreDatabaseRestorer func(context.Context, string, string) error
type wpCoreFilesRestorer func(context.Context, string, string, string) error

type wpCoreChecksumSet struct {
	Version   string
	Locale    string
	Checksums map[string]string
}

type wpCoreChecksumFetcher func(context.Context, string, string) (wpCoreChecksumSet, error)
type wpCoreHomeProber func(context.Context, string) error
type wpCoreSpaceChecker func(context.Context, wpCoreUpdateExecution, ZIPInspection) error

type wpCoreSystemOperations struct {
	runner       wpCoreUpdateRunner
	restoreDB    wpCoreDatabaseRestorer
	restoreFiles wpCoreFilesRestorer
	fetch        wpCoreChecksumFetcher
	probe        wpCoreHomeProber
	space        wpCoreSpaceChecker
	mu           sync.Mutex
	prepared     map[string]wpCorePreparedHealth
}

type wpCorePreparedHealth struct {
	target wpCoreChecksumSet
	source wpCoreChecksumSet
}

func newWPCoreSystemOperations(runner wpCoreUpdateRunner, restoreDB wpCoreDatabaseRestorer, restoreFiles wpCoreFilesRestorer, fetch wpCoreChecksumFetcher, probe wpCoreHomeProber, space wpCoreSpaceChecker) (*wpCoreSystemOperations, error) {
	if runner == nil || restoreDB == nil || restoreFiles == nil || fetch == nil || probe == nil || space == nil {
		return nil, errors.New("invalid core update operations")
	}
	return &wpCoreSystemOperations{runner: runner, restoreDB: restoreDB, restoreFiles: restoreFiles, fetch: fetch, probe: probe, space: space, prepared: map[string]wpCorePreparedHealth{}}, nil
}

func newDefaultWPCoreSystemOperations(wwwRoot string) (*wpCoreSystemOperations, error) {
	runner, err := newDefaultWPCorePHPRunner(wwwRoot)
	if err != nil {
		return nil, err
	}
	return newWPCoreSystemOperations(runner, defaultWPCoreDatabaseRestorer, defaultWPCoreFilesRestorer, defaultWPCoreChecksumFetcher(nil), defaultWPCoreHomeProber(nil), defaultWPCoreSpaceChecker)
}

func (o *wpCoreSystemOperations) Prepare(ctx context.Context, execution wpCoreUpdateExecution) error {
	if execution.Task.VerificationLevel != "official_verified" {
		return errors.New("core update package is not officially verified")
	}
	report, err := ValidateWordPressPackage(ctx, execution.PackagePath)
	if err != nil || report.Version != execution.Task.TargetVersion {
		return errors.New("core update package identity mismatch")
	}
	target, err := o.fetch(ctx, execution.Task.TargetVersion, report.Locale)
	if err != nil {
		return errors.New("target checksums unavailable")
	}
	if err := validateWPCoreChecksumSet(target, execution.Task.TargetVersion, report.Locale); err != nil {
		return err
	}
	if err := verifyWPCorePackageChecksums(execution.PackagePath, target); err != nil {
		return err
	}
	if err := o.space(ctx, execution, report.Inspection); err != nil {
		return err
	}
	sourceLocale, err := readInstalledWordPressIdentity(execution.WebRoot)
	if err != nil {
		return err
	}
	if sourceLocale.Version != execution.Task.CurrentVersion {
		return errors.New("installed WordPress version changed")
	}
	source, err := o.fetch(ctx, execution.Task.CurrentVersion, sourceLocale.Locale)
	if err != nil {
		return errors.New("source checksums unavailable")
	}
	if err := validateWPCoreChecksumSet(source, execution.Task.CurrentVersion, sourceLocale.Locale); err != nil {
		return err
	}
	o.mu.Lock()
	o.prepared[execution.Task.ID] = wpCorePreparedHealth{target: target, source: source}
	o.mu.Unlock()
	return nil
}

func (o *wpCoreSystemOperations) Unlock(_ context.Context, execution wpCoreUpdateExecution) error {
	if wpConfigHasUserFileModsLock(execution.WebRoot) {
		return errors.New("user file modifications lock is enabled")
	}
	if !execution.FileLockActive {
		return nil
	}
	return ApplySiteUnlockedPermissions(coreExecutionWebsite(execution))
}

func (o *wpCoreSystemOperations) ApplyCoreUpdate(ctx context.Context, execution wpCoreUpdateExecution) error {
	return o.runner.Update(ctx, execution)
}

func (o *wpCoreSystemOperations) CheckTargetHealth(ctx context.Context, execution wpCoreUpdateExecution) error {
	prepared, err := o.preparedHealth(execution.Task.ID)
	if err != nil {
		return err
	}
	if err := checkWPCoreFilesystem(execution.WebRoot, prepared.target); err != nil {
		return err
	}
	if err := o.runner.CheckLoad(ctx, execution, execution.Task.TargetVersion); err != nil {
		return err
	}
	return o.probe(ctx, execution.Domain)
}

func (o *wpCoreSystemOperations) SetMaintenance(_ context.Context, execution wpCoreUpdateExecution, enabled bool) error {
	return setWPUpdateMaintenance(execution.WebRoot, enabled)
}

func setWPUpdateMaintenance(webRoot string, enabled bool) error {
	root, err := os.OpenRoot(webRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	const name = ".maintenance"
	if info, statErr := root.Lstat(name); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("invalid WordPress maintenance file")
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if enabled {
		body := []byte("<?php $upgrading = " + strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10) + ";\n")
		f, err := root.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(body)
		syncErr := f.Sync()
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	}
	if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (o *wpCoreSystemOperations) RestoreDatabase(ctx context.Context, execution wpCoreUpdateExecution) error {
	return o.restoreDB(ctx, execution.DatabaseName, execution.DatabaseBackup)
}

func (o *wpCoreSystemOperations) RestoreCoreFiles(ctx context.Context, execution wpCoreUpdateExecution) error {
	return o.restoreFiles(ctx, execution.WebRoot, execution.CoreBackup, execution.Task.ID)
}

func (o *wpCoreSystemOperations) CheckRollbackHealth(ctx context.Context, execution wpCoreUpdateExecution) error {
	prepared, err := o.preparedHealth(execution.Task.ID)
	if err != nil {
		return err
	}
	if err := checkWPCoreFilesystem(execution.WebRoot, prepared.source); err != nil {
		return err
	}
	if err := o.runner.CheckLoad(ctx, execution, execution.Task.CurrentVersion); err != nil {
		return err
	}
	return o.probe(ctx, execution.Domain)
}

func (o *wpCoreSystemOperations) RestoreFileLock(_ context.Context, execution wpCoreUpdateExecution) error {
	var err error
	if !execution.FileLockActive {
		err = nil
	} else {
		err = ApplySiteFileLockMode(coreExecutionWebsite(execution), execution.FileLockMode)
	}
	if err == nil {
		o.mu.Lock()
		delete(o.prepared, execution.Task.ID)
		o.mu.Unlock()
	}
	return err
}

func (o *wpCoreSystemOperations) preparedHealth(taskID string) (wpCorePreparedHealth, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	prepared, ok := o.prepared[taskID]
	if !ok {
		return wpCorePreparedHealth{}, errors.New("core health plan is not prepared")
	}
	return prepared, nil
}

func coreExecutionWebsite(execution wpCoreUpdateExecution) *models.Website {
	return &models.Website{ID: execution.Task.SiteID, Domain: execution.Domain, SystemUser: execution.SystemUser, WebRoot: execution.WebRoot, DBName: execution.DatabaseName, SiteType: "wordpress", Status: models.StatusActive, FileLockEnabled: execution.FileLockActive, FileLockMode: execution.FileLockMode}
}

type wpInstalledIdentity struct{ Version, Locale string }

func readInstalledWordPressIdentity(webRoot string) (wpInstalledIdentity, error) {
	root, err := os.OpenRoot(webRoot)
	if err != nil {
		return wpInstalledIdentity{}, err
	}
	defer root.Close()
	name := filepath.Join("wp-includes", "version.php")
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return wpInstalledIdentity{}, errors.New("invalid WordPress version file")
	}
	data, err := root.ReadFile(name)
	if err != nil {
		return wpInstalledIdentity{}, err
	}
	version, locale, err := parseWordPressVersionFile(string(data))
	if err != nil {
		return wpInstalledIdentity{}, err
	}
	return wpInstalledIdentity{version, locale}, nil
}

var wpOfficialMD5Pattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var wpCoreVersionPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){1,3}(?:[-+][A-Za-z0-9.-]+)?$`)

func validateWPCoreChecksumSet(set wpCoreChecksumSet, version, locale string) error {
	if set.Version != version || set.Locale != locale || len(set.Checksums) == 0 {
		return errors.New("WordPress checksum identity mismatch")
	}
	required := map[string]bool{"wp-includes/version.php": false, "wp-settings.php": false}
	for name, digest := range set.Checksums {
		if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || filepath.ToSlash(filepath.Clean(name)) != name || name == "." || strings.HasPrefix(name, "../") || !wpOfficialMD5Pattern.MatchString(digest) {
			return errors.New("invalid WordPress checksum entry")
		}
		if _, ok := required[name]; ok {
			required[name] = true
		}
	}
	for _, found := range required {
		if !found {
			return errors.New("WordPress checksums incomplete")
		}
	}
	return nil
}

func verifyWPCorePackageChecksums(packagePath string, set wpCoreChecksumSet) error {
	zr, err := zip.OpenReader(packagePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	seen := map[string]bool{}
	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		if !strings.HasPrefix(entry.Name, "wordpress/") {
			return errors.New("core package path rejected")
		}
		name := strings.TrimPrefix(entry.Name, "wordpress/")
		if name == "wp-content" || strings.HasPrefix(name, "wp-content/") {
			continue
		}
		expected, ok := set.Checksums[name]
		if !ok {
			return errors.New("core package contains unverified file")
		}
		src, err := entry.Open()
		if err != nil {
			return err
		}
		h := md5.New()
		_, copyErr := io.Copy(h, src)
		closeErr := src.Close()
		if copyErr != nil || closeErr != nil || hex.EncodeToString(h.Sum(nil)) != expected {
			return errors.New("core package checksum mismatch")
		}
		seen[name] = true
	}
	for name := range set.Checksums {
		if name == "wp-content" || strings.HasPrefix(name, "wp-content/") {
			continue
		}
		if !seen[name] {
			return errors.New("core package checksum file missing")
		}
	}
	return nil
}

func checkWPCoreFilesystem(webRoot string, set wpCoreChecksumSet) error {
	identity, err := readInstalledWordPressIdentity(webRoot)
	if err != nil || identity.Version != set.Version {
		return errors.New("WordPress version mismatch")
	}
	if _, err := os.Lstat(filepath.Join(webRoot, ".maintenance")); err == nil {
		return errors.New("WordPress maintenance mode remains")
	} else if !os.IsNotExist(err) {
		return err
	}
	if len(set.Checksums) == 0 {
		return errors.New("WordPress checksums are empty")
	}
	root, err := os.OpenRoot(webRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	for name, expected := range set.Checksums {
		name = filepath.ToSlash(filepath.Clean(name))
		if name == "wp-content" || strings.HasPrefix(name, "wp-content/") {
			continue
		}
		if filepath.Dir(name) == "." && (filepath.Ext(name) == ".txt" || filepath.Ext(name) == ".html") {
			continue
		}
		if name == "." || strings.HasPrefix(name, "../") || !wpOfficialMD5Pattern.MatchString(expected) {
			return errors.New("invalid WordPress checksum entry")
		}
		info, err := root.Lstat(filepath.FromSlash(name))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("WordPress core file invalid")
		}
		f, err := root.Open(filepath.FromSlash(name))
		if err != nil {
			return errors.New("WordPress core file missing")
		}
		h := md5.New()
		_, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil || hex.EncodeToString(h.Sum(nil)) != expected {
			return errors.New("WordPress core checksum mismatch")
		}
	}
	return nil
}

func defaultWPCoreChecksumFetcher(client *http.Client) wpCoreChecksumFetcher {
	transport := http.DefaultTransport
	if client != nil && client.Transport != nil {
		transport = client.Transport
	}
	client = &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || req.URL.Scheme != "https" || req.URL.Hostname() != "api.wordpress.org" {
			return errors.New("checksum redirect rejected")
		}
		return nil
	}}
	return func(ctx context.Context, version, locale string) (wpCoreChecksumSet, error) {
		if !wpCoreVersionPattern.MatchString(version) || !regexp.MustCompile(`^[A-Za-z]{2,3}(?:_[A-Za-z0-9]{2,8})?$`).MatchString(locale) {
			return wpCoreChecksumSet{}, errors.New("invalid checksum request")
		}
		u := "https://api.wordpress.org/core/checksums/1.0/?" + url.Values{"version": {version}, "locale": {locale}}.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return wpCoreChecksumSet{}, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return wpCoreChecksumSet{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Request == nil || resp.Request.URL.Scheme != "https" || resp.Request.URL.Hostname() != "api.wordpress.org" {
			return wpCoreChecksumSet{}, errors.New("checksum response rejected")
		}
		var body struct {
			Checksums map[string]string `json:"checksums"`
		}
		dec := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
		if err := dec.Decode(&body); err != nil || len(body.Checksums) == 0 {
			return wpCoreChecksumSet{}, errors.New("checksum response invalid")
		}
		return wpCoreChecksumSet{Version: version, Locale: locale, Checksums: body.Checksums}, nil
	}
}

func defaultWPCoreHomeProber(client *http.Client) wpCoreHomeProber {
	var transport http.RoundTripper
	if client != nil && client.Transport != nil {
		transport = client.Transport
	} else {
		localTransport := http.DefaultTransport.(*http.Transport).Clone()
		localTransport.Proxy = nil
		transport = localTransport
	}
	client = &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(ctx context.Context, domain string) error {
		if !IsValidDomain(domain) {
			return errors.New("invalid probe domain")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1/", nil)
		if err != nil {
			return err
		}
		req.Host = domain
		resp, err := client.Do(req)
		if err != nil {
			return errors.New("site health probe failed")
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if err != nil {
			return errors.New("site health response invalid")
		}
		lower := strings.ToLower(string(body))
		if resp.StatusCode >= 500 || strings.Contains(lower, "there has been a critical error") || strings.Contains(lower, "fatal error") || strings.Contains(lower, "遇到了致命错误") || strings.Contains(lower, "发生致命错误") {
			return errors.New("site health probe detected fatal response")
		}
		return nil
	}
}
