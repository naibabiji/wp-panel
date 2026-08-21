package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

const (
	wpPackageAutoCheckInterval   = 7 * 24 * time.Hour
	wpPackageAutoCheckStartDelay = 2 * time.Minute
	wpPackageVersionCheckTimeout = 30 * time.Second
)

var wpPackageAutoCheckStarted sync.Once

// StartWPPackageAutoUpdateScheduler periodically checks wordpress.org for a
// WordPress release newer than the panel's locally cached install package
// (the one managed on the settings page) and, if found, downloads it through
// the same validated publish path as the manual "在线下载" button. Enabled by
// default; can be turned off on the settings page.
//
// A failed check (or a failed download) never touches the existing local
// package — WPPackageService only replaces it after the new file passes
// structure/SHA256 validation — so a bad network on this schedule cannot
// clobber a package an admin uploaded manually as a fallback. Failures are
// simply recorded and retried on the next 7-day tick, not retried in a tight
// loop.
func StartWPPackageAutoUpdateScheduler(cfg *config.Config) {
	wpPackageAutoCheckStarted.Do(func() {
		go func() {
			time.Sleep(wpPackageAutoCheckStartDelay)
			runWPPackageAutoCheckOnce(cfg)
			ticker := time.NewTicker(wpPackageAutoCheckInterval)
			defer ticker.Stop()
			for range ticker.C {
				runWPPackageAutoCheckOnce(cfg)
			}
		}()
	})
}

func wpPackageAutoCheckEnabled() bool {
	// Unset means "not configured yet", which defaults to enabled; only an
	// explicit "false" (saved by the settings page toggle) turns it off.
	return readSecuritySetting("wp_package_auto_check_enabled") != "false"
}

// wpPackageVersionFetcher reports the current latest stable WordPress
// version. It is a function type (rather than a hardcoded call) so tests can
// substitute a canned result without hitting the network — the same
// dependency-injection shape used for wpCoreOfferFetcher.
type wpPackageVersionFetcher func(ctx context.Context) (string, error)

func runWPPackageAutoCheckOnce(cfg *config.Config) {
	if cfg == nil || cfg.Paths.WordPressPackage == "" {
		return
	}
	svc, err := SharedWPPackageService(cfg)
	if err != nil {
		recordWPPackageCheckFailure("", "安装包服务不可用: "+err.Error())
		return
	}
	runWPPackageAutoCheck(cfg, svc, func(ctx context.Context) (string, error) {
		return fetchLatestStableWordPressVersion(ctx, nil)
	})
}

// runWPPackageAutoCheck contains the decision logic: skip when disabled,
// skip when the local package is already current, otherwise download
// through svc (which only replaces the local file after full validation).
// It takes svc and fetchVersion as parameters, not globals, so it can be
// exercised in tests without touching the network or the process-wide
// WPPackageService singleton.
func runWPPackageAutoCheck(cfg *config.Config, svc *WPPackageService, fetchVersion wpPackageVersionFetcher) {
	if !wpPackageAutoCheckEnabled() {
		return
	}

	checkCtx, cancel := context.WithTimeout(context.Background(), wpPackageVersionCheckTimeout)
	remoteVersion, err := fetchVersion(checkCtx)
	cancel()
	setSecuritySetting("wp_package_last_check_at", time.Now().Format(time.RFC3339))
	if err != nil {
		recordWPPackageCheckFailure(remoteVersion, "获取 WordPress 最新版本号失败: "+err.Error())
		return
	}
	setSecuritySetting("wp_package_last_remote_version", remoteVersion)

	localVersion, _, ok := LocalPackageInfo(context.Background(), cfg.Paths.WordPressPackage)
	if ok && CompareVersions(remoteVersion, localVersion) <= 0 {
		setSecuritySetting("wp_package_last_check_status", "up_to_date")
		setSecuritySetting("wp_package_last_check_error", "")
		return
	}

	downloadCtx, downloadCancel := context.WithTimeout(context.Background(), wpPackageDownloadHTTPTimeout)
	report, err := svc.DownloadLatestExpecting(downloadCtx, remoteVersion)
	downloadCancel()
	if err != nil {
		recordWPPackageCheckFailure(remoteVersion, "自动下载失败，本地已有安装包保持不变: "+ArchiveErrorCode(err))
		return
	}

	setSecuritySetting("wp_package_last_check_status", "updated")
	setSecuritySetting("wp_package_last_check_error", "")
	log.Printf("WordPress package auto-updated: version=%s", report.Version)
	recordOperationLog("wp_package_auto_check", report.Version, "success", "已自动检测并更新本地 WordPress 安装包")
}

func recordWPPackageCheckFailure(remoteVersion, message string) {
	setSecuritySetting("wp_package_last_check_status", "failed")
	setSecuritySetting("wp_package_last_check_error", message)
	log.Printf("WordPress package auto-check failed: %s", message)
	recordOperationLog("wp_package_auto_check", remoteVersion, "failed", message)
}

var errWPVersionCheckUnavailable = errors.New("wordpress.org 版本检测接口不可用")

// fetchLatestStableWordPressVersion asks wordpress.org's stable-check API for
// the current latest stable release. Unlike the per-site core-update offer
// fetcher (defaultWPCoreOfferFetcher), this does not need an installed
// site's current version/PHP/MySQL — it's a generic "what's newest" lookup
// used only to decide whether the panel's own install-package cache is
// stale. A nil client uses the default restricted HTTP client; tests pass
// one with a fake transport.
func fetchLatestStableWordPressVersion(ctx context.Context, client *http.Client) (string, error) {
	if client == nil {
		client = defaultWPPackageHTTPClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.wordpress.org/core/stable-check/1.0/", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request == nil || resp.Request.URL.Hostname() != "api.wordpress.org" {
		return "", errWPVersionCheckUnavailable
	}
	var versions map[string]string
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&versions); err != nil {
		return "", err
	}
	for version, status := range versions {
		if status == "latest" {
			return version, nil
		}
	}
	return "", errWPVersionCheckUnavailable
}
