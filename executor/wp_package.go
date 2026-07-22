package executor

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	wordpressLatestURL  = "https://wordpress.org/latest.zip"
	wpUploadMaxBytes    = int64(100 << 20)
	wpDownloadMaxBytes  = int64(200 << 20)
	wpValidationTimeout = 30 * time.Second
)

type WPPackageReport struct {
	Inspection   ZIPInspection
	PackageType  ZIPPackageType
	RootPrefix   string
	Version      string
	Locale       string
	Verification string
}

type WPPackageService struct {
	target string
	client *http.Client
	mu     sync.Mutex
}

func NewWPPackageService(target string, client *http.Client) (*WPPackageService, error) {
	if target == "" || !filepath.IsAbs(target) || filepath.Base(target) == "." {
		return nil, archiveError("package_publish_failed", nil)
	}
	if client == nil {
		client = defaultWPPackageHTTPClient()
	} else {
		transport := client.Transport
		if transport == nil {
			transport = http.DefaultTransport
		}
		client = restrictedWPPackageHTTPClient(transport)
	}
	return &WPPackageService{target: filepath.Clean(target), client: client}, nil
}

func defaultWPPackageHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&netDialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 15 * time.Second
	return restrictedWPPackageHTTPClient(transport)
}

func restrictedWPPackageHTTPClient(transport http.RoundTripper) *http.Client {
	client := &http.Client{Transport: transport, Timeout: 45 * time.Second}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !allowedWordPressURL(req.URL) {
			return errors.New("redirect rejected")
		}
		return nil
	}
	return client
}

// netDialer is an alias kept local so the production client policy is visible here.
type netDialer = net.Dialer

func (s *WPPackageService) PublishUpload(ctx context.Context, src io.Reader, declaredSize int64) (WPPackageReport, error) {
	if declaredSize <= 0 {
		return WPPackageReport{}, archiveError("archive_invalid_zip", nil)
	}
	if declaredSize > wpUploadMaxBytes {
		return WPPackageReport{}, archiveError("archive_upload_too_large", nil)
	}
	return s.publish(ctx, io.LimitReader(src, wpUploadMaxBytes+1), wpUploadMaxBytes)
}

func (s *WPPackageService) DownloadLatest(ctx context.Context) (WPPackageReport, error) {
	return s.download(ctx, wordpressLatestURL)
}

func (s *WPPackageService) download(ctx context.Context, rawURL string) (WPPackageReport, error) {
	if !s.mu.TryLock() {
		return WPPackageReport{}, archiveError("package_busy", nil)
	}
	defer s.mu.Unlock()

	u, err := url.Parse(rawURL)
	if err != nil || !allowedWordPressURL(u) {
		return WPPackageReport{}, archiveError("package_download_failed", nil)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return WPPackageReport{}, archiveError("package_download_failed", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return WPPackageReport{}, archiveError("package_download_timeout", err)
		}
		return WPPackageReport{}, archiveError("package_download_failed", err)
	}
	defer resp.Body.Close()
	if resp.Request == nil || !allowedWordPressURL(resp.Request.URL) || resp.StatusCode != http.StatusOK {
		return WPPackageReport{}, archiveError("package_download_failed", nil)
	}
	return s.publishLocked(ctx, io.LimitReader(resp.Body, wpDownloadMaxBytes+1), wpDownloadMaxBytes)
}

func (s *WPPackageService) publish(ctx context.Context, src io.Reader, maxBytes int64) (WPPackageReport, error) {
	if !s.mu.TryLock() {
		return WPPackageReport{}, archiveError("package_busy", nil)
	}
	defer s.mu.Unlock()
	return s.publishLocked(ctx, src, maxBytes)
}

func (s *WPPackageService) publishLocked(ctx context.Context, src io.Reader, maxBytes int64) (report WPPackageReport, retErr error) {
	dir := filepath.Dir(s.target)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return report, archiveError("package_publish_failed", err)
	}
	staged, err := os.CreateTemp(dir, ".wordpress-package-*.tmp")
	if err != nil {
		return report, archiveError("package_publish_failed", err)
	}
	stagedName := staged.Name()
	defer func() {
		staged.Close()
		if retErr != nil {
			os.Remove(stagedName)
		}
	}()
	if err := staged.Chmod(0600); err != nil {
		return report, archiveError("package_publish_failed", err)
	}
	written, err := io.Copy(staged, src)
	if err != nil {
		return report, archiveError("package_publish_failed", err)
	}
	if written == 0 {
		return report, archiveError("archive_invalid_zip", nil)
	}
	if written > maxBytes {
		return report, archiveError("archive_too_large", nil)
	}
	if err := staged.Sync(); err != nil {
		return report, archiveError("package_publish_failed", err)
	}
	if err := staged.Close(); err != nil {
		return report, archiveError("package_publish_failed", err)
	}

	validationCtx, cancel := context.WithTimeout(ctx, wpValidationTimeout)
	defer cancel()
	report, err = ValidateWordPressPackage(validationCtx, stagedName)
	if err != nil {
		return report, err
	}
	sha, size, err := fileSHA256(stagedName)
	if err != nil || sha != report.Inspection.SHA256 || size != report.Inspection.ArchiveBytes {
		return report, archiveError("package_publish_failed", err)
	}
	if err := os.Chmod(stagedName, 0644); err != nil {
		return report, archiveError("package_publish_failed", err)
	}
	if err := os.Rename(stagedName, s.target); err != nil {
		return report, archiveError("package_publish_failed", err)
	}
	// rename 已经原子发布；若后续目录 fsync 失败，无法在不破坏原子语义的
	// 前提下可靠恢复旧 inode。此时返回失败以表示持久化保证未完成，但目标
	// 在当前文件系统视图中可能已经是通过完整校验的新包。
	parent, err := os.Open(dir)
	if err != nil {
		return report, archiveError("package_publish_failed", err)
	}
	err = parent.Sync()
	parent.Close()
	if err != nil {
		return report, archiveError("package_publish_failed", err)
	}
	return report, nil
}

func ValidateWordPressPackage(ctx context.Context, filename string) (WPPackageReport, error) {
	inspection, err := InspectZIP(ctx, filename, WordPressFullZIPPolicy())
	if err != nil {
		return WPPackageReport{}, err
	}
	report := WPPackageReport{Inspection: inspection, PackageType: ZIPPackageWordPressFull, RootPrefix: "wordpress/", Verification: "structure_only", Locale: "en_US"}
	needed := map[string]bool{
		"wordpress/wp-includes/version.php": false,
		"wordpress/wp-settings.php":         false,
		"wordpress/wp-load.php":             false,
		"wordpress/wp-admin/index.php":      false,
		"wordpress/wp-includes/load.php":    false,
	}
	adminFiles, includesFiles := 0, 0
	for _, name := range inspection.NormalizedNames {
		if name != "wordpress" && !strings.HasPrefix(name, "wordpress/") {
			return WPPackageReport{}, archiveError("package_structure_invalid", nil)
		}
		if _, ok := needed[name]; ok {
			needed[name] = true
		}
		if strings.HasPrefix(name, "wordpress/wp-admin/") && name != "wordpress/wp-admin" {
			adminFiles++
		}
		if strings.HasPrefix(name, "wordpress/wp-includes/") && name != "wordpress/wp-includes" {
			includesFiles++
		}
	}
	for _, found := range needed {
		if !found {
			return WPPackageReport{}, archiveError("package_structure_invalid", nil)
		}
	}
	if adminFiles == 0 || includesFiles == 0 {
		return WPPackageReport{}, archiveError("package_structure_invalid", nil)
	}

	zr, err := zip.OpenReader(filename)
	if err != nil {
		return WPPackageReport{}, archiveError("archive_invalid_zip", err)
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if zf.Name != "wordpress/wp-includes/version.php" {
			continue
		}
		if zf.UncompressedSize64 > 64<<10 {
			return WPPackageReport{}, archiveError("package_version_invalid", nil)
		}
		rc, err := zf.Open()
		if err != nil {
			return WPPackageReport{}, archiveError("package_version_invalid", err)
		}
		body, err := io.ReadAll(io.LimitReader(rc, 64<<10+1))
		rc.Close()
		if err != nil || len(body) > 64<<10 {
			return WPPackageReport{}, archiveError("package_version_invalid", err)
		}
		report.Version, report.Locale, err = parseWordPressVersionFile(string(body))
		if err != nil {
			return WPPackageReport{}, err
		}
		return report, nil
	}
	return WPPackageReport{}, archiveError("package_structure_invalid", nil)
}

var (
	versionAssignment = regexp.MustCompile(`(?m)^\s*\$wp_version\s*=\s*(?:'([0-9]+(?:\.[0-9]+){1,3}(?:[-+][A-Za-z0-9.-]+)?)'|"([0-9]+(?:\.[0-9]+){1,3}(?:[-+][A-Za-z0-9.-]+)?)")\s*;\s*$`)
	localeAssignment  = regexp.MustCompile(`(?m)^\s*\$wp_local_package\s*=\s*(?:'([A-Za-z]{2,3}(?:_[A-Za-z0-9]{2,8})?)'|"([A-Za-z]{2,3}(?:_[A-Za-z0-9]{2,8})?)")\s*;\s*$`)
	versionMention    = regexp.MustCompile(`(?m)^\s*\$wp_version\s*=`)
	localeMention     = regexp.MustCompile(`(?m)^\s*\$wp_local_package\s*=`)
)

func parseWordPressVersionFile(body string) (string, string, error) {
	versions := versionAssignment.FindAllStringSubmatch(body, -1)
	if len(versions) != 1 || len(versionMention.FindAllString(body, -1)) != 1 {
		return "", "", archiveError("package_version_invalid", nil)
	}
	locales := localeAssignment.FindAllStringSubmatch(body, -1)
	if len(localeMention.FindAllString(body, -1)) != len(locales) || len(locales) > 1 {
		return "", "", archiveError("package_version_invalid", nil)
	}
	locale := "en_US"
	if len(locales) == 1 {
		locale = firstNonEmpty(locales[0][1], locales[0][2])
	}
	return firstNonEmpty(versions[0][1], versions[0][2]), locale, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func allowedWordPressURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "wordpress.org" || host == "downloads.wordpress.org"
}

func fileSHA256(filename string) (string, int64, error) {
	f, err := os.Open(filename)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func (r WPPackageReport) String() string {
	return fmt.Sprintf("version=%s locale=%s %s", r.Version, r.Locale, r.Inspection.String())
}
