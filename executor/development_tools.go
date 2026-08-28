package executor

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	wpCLIVersion        = "2.12.0"
	wpCLIURL            = "https://github.com/wp-cli/wp-cli-bundle/releases/download/v2.12.0/wp-cli-2.12.0.phar"
	wpCLISHA512         = "be928f6b8ca1e8dfb9d2f4b75a13aa4aee0896f8a9a0a1c45cd5d2c98605e6172e6d014dda2e27f88c98befc16c040cbb2bd1bfa121510ea5cdf5f6a30fe8832"
	wpCLIInstallPath    = "/usr/local/bin/wp"
	wpCLIMaxDownload    = 64 << 20
	developmentToolWait = 10 * time.Minute
)

var developmentToolInstallLock = make(chan struct{}, 1)

var ErrWPCLIProxyDownloadFailed = errors.New("WP-CLI direct and proxy downloads failed")

type DevelopmentToolStatus struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Installed   bool   `json:"installed"`
	Version     string `json:"version"`
	Recommended bool   `json:"recommended"`
}

func DevelopmentToolsStatus(ctx context.Context) []DevelopmentToolStatus {
	wpVersion, wpInstalled := wpCLICommandVersion(ctx, wpCLIInstallPath)
	nodeVersion, nodeInstalled := commandVersion(ctx, "node", "--version")
	_, npmInstalled := commandVersion(ctx, "npm", "--version")
	return []DevelopmentToolStatus{
		{ID: "wp-cli", Name: "WP-CLI", Installed: wpInstalled, Version: wpVersion, Recommended: true},
		{ID: "nodejs", Name: "Node.js + npm", Installed: nodeInstalled && npmInstalled, Version: nodeVersion, Recommended: false},
	}
}

func wpCLICommandVersion(ctx context.Context, binary string) (string, bool) {
	return commandVersion(ctx, binary, "--allow-root", "--version")
}

func commandVersion(ctx context.Context, binary string, args ...string) (string, bool) {
	path := binary
	if !filepath.IsAbs(path) {
		var err error
		path, err = exec.LookPath(binary)
		if err != nil {
			return "", false
		}
	}
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func InstallDevelopmentTool(ctx context.Context, id string) error {
	if os.Geteuid() != 0 {
		return errors.New("development tool installation requires root")
	}
	select {
	case developmentToolInstallLock <- struct{}{}:
		defer func() { <-developmentToolInstallLock }()
	case <-ctx.Done():
		return ctx.Err()
	}
	installCtx, cancel := context.WithTimeout(ctx, developmentToolWait)
	defer cancel()
	switch id {
	case "wp-cli":
		return installWPCLI(installCtx)
	case "nodejs":
		return installNodeJS(installCtx)
	default:
		return errors.New("unsupported development tool")
	}
}

func installWPCLI(ctx context.Context) error {
	if _, ok := wpCLICommandVersion(ctx, wpCLIInstallPath); ok {
		return nil
	}
	if info, err := os.Lstat(wpCLIInstallPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("existing /usr/local/bin/wp is not a regular WP-CLI file")
		}
		return errors.New("existing /usr/local/bin/wp is not runnable; refusing to overwrite it")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(wpCLIInstallPath), ".wp-cli-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	proxy := strings.TrimSpace(readSecuritySetting("github_proxy"))
	if err := downloadWPCLIWithFallback(ctx, tmp, wpCLIURL, proxy, wpCLISHA512); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "/usr/bin/php", tmpPath, "--allow-root", "--version").CombinedOutput(); err != nil || !strings.Contains(string(out), "WP-CLI "+wpCLIVersion) {
		return errors.New("downloaded WP-CLI failed version verification")
	}
	return os.Rename(tmpPath, wpCLIInstallPath)
}

func downloadWPCLIWithFallback(ctx context.Context, dst *os.File, directURL, proxy, expectedSHA512 string) error {
	directErr := downloadWPCLI(ctx, dst, directURL, expectedSHA512)
	if directErr == nil {
		return nil
	}
	proxy = strings.TrimRight(strings.TrimSpace(proxy), "/")
	if proxy == "" {
		return fmt.Errorf("WP-CLI direct download failed: %w", directErr)
	}
	proxyErr := downloadWPCLI(ctx, dst, proxy+"/"+directURL, expectedSHA512)
	if proxyErr != nil {
		return fmt.Errorf("%w: direct: %v; proxy: %v", ErrWPCLIProxyDownloadFailed, directErr, proxyErr)
	}
	return nil
}

func downloadWPCLI(ctx context.Context, dst *os.File, sourceURL, expectedSHA512 string) error {
	parsed, err := url.Parse(sourceURL)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return errors.New("invalid WP-CLI download URL")
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := dst.Truncate(0); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return err
	}
	initialHost := parsed.Hostname()
	client := &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || (req.URL.Hostname() != initialHost && req.URL.Hostname() != "github.com" && req.URL.Hostname() != "release-assets.githubusercontent.com") {
			return errors.New("WP-CLI download redirected to an untrusted host")
		}
		if len(via) > 5 {
			return errors.New("too many WP-CLI redirects")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("WP-CLI download returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > wpCLIMaxDownload {
		return errors.New("WP-CLI download size is invalid")
	}
	hash := sha512.New()
	written, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(resp.Body, wpCLIMaxDownload+1))
	if err != nil {
		return err
	}
	if written == 0 || written > wpCLIMaxDownload {
		return errors.New("WP-CLI download size is invalid")
	}
	if hex.EncodeToString(hash.Sum(nil)) != expectedSHA512 {
		return errors.New("WP-CLI SHA-512 verification failed")
	}
	return nil
}

func installNodeJS(ctx context.Context) error {
	if _, nodeOK := commandVersion(ctx, "node", "--version"); nodeOK {
		if _, npmOK := commandVersion(ctx, "npm", "--version"); npmOK {
			return nil
		}
	}
	if out, err := exec.CommandContext(ctx, "apt-get", "update").CombinedOutput(); err != nil {
		return fmt.Errorf("APT index refresh failed: %s", conciseCommandFailure(out))
	}
	if out, err := exec.CommandContext(ctx, "apt-get", "install", "-y", "--no-install-recommends", "nodejs", "npm").CombinedOutput(); err != nil {
		return fmt.Errorf("Node.js installation failed: %s", conciseCommandFailure(out))
	}
	if _, ok := commandVersion(ctx, "node", "--version"); !ok {
		return errors.New("Node.js installation completed but node is unavailable")
	}
	if _, ok := commandVersion(ctx, "npm", "--version"); !ok {
		return errors.New("Node.js installation completed but npm is unavailable")
	}
	return nil
}

func conciseCommandFailure(output []byte) string {
	text := strings.TrimSpace(string(output))
	if text == "" {
		return "command failed"
	}
	lines := strings.Split(text, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if len(last) > 300 {
		last = last[:300]
	}
	return last
}
