package executor

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	nginxBannedIPsPath = "/etc/nginx/conf.d/wppanel-banned-ips.conf"
	nginxBanLockPath   = "/run/wp-panel-nginx-ban.lock"
)

func AddNginxBan(ip string) error {
	return updateNginxBannedIPs(ip, true)
}

func RemoveNginxBan(ip string) error {
	return updateNginxBannedIPs(ip, false)
}

func EnsureNginxBannedIPsConfig() error {
	if _, err := os.Stat(nginxBannedIPsPath); err == nil {
		return nil
	}
	return withNginxBanLock(func() error {
		if _, err := os.Stat(nginxBannedIPsPath); err == nil {
			return nil
		}
		return writeNginxBannedIPs(map[string]bool{}, false)
	})
}

func ReplaceNginxBannedIPs(ips map[string]bool) error {
	return withNginxBanLock(func() error {
		return writeNginxBannedIPs(ips, true)
	})
}

func updateNginxBannedIPs(ip string, banned bool) error {
	ip = strings.TrimSpace(ip)
	if !isValidIPOrCIDR(ip) {
		return fmt.Errorf("invalid IP: %s", ip)
	}

	return withNginxBanLock(func() error {
		ips, err := readNginxBannedIPs()
		if err != nil {
			return err
		}
		if banned {
			ips[ip] = true
		} else {
			delete(ips, ip)
		}
		return writeNginxBannedIPs(ips, true)
	})
}

func readNginxBannedIPs() (map[string]bool, error) {
	ips := make(map[string]bool)
	data, err := os.ReadFile(nginxBannedIPsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ips, nil
		}
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "geo ") || line == "}" {
			continue
		}
		fields := strings.Fields(strings.TrimRight(line, ";"))
		if len(fields) < 2 || fields[1] != "1" {
			continue
		}
		ip := strings.TrimSpace(fields[0])
		if isValidIPOrCIDR(ip) {
			ips[ip] = true
		}
	}
	return ips, nil
}

func writeNginxBannedIPs(ips map[string]bool, reload bool) error {
	if err := os.MkdirAll(filepath.Dir(nginxBannedIPsPath), 0755); err != nil {
		return err
	}

	oldContent, oldErr := os.ReadFile(nginxBannedIPsPath)
	hadOld := oldErr == nil

	content := renderNginxBannedIPs(ips)
	if hadOld && string(oldContent) == content {
		return nil
	}

	tmpPath := nginxBannedIPsPath + ".tmp." + fmt.Sprintf("%d", time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, []byte(content), 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, nginxBannedIPsPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	if !reload {
		return nil
	}
	if out, err := executeCommand("nginx", "-t"); err != nil {
		if hadOld {
			_ = os.WriteFile(nginxBannedIPsPath, oldContent, 0644)
		} else {
			_ = os.Remove(nginxBannedIPsPath)
		}
		return fmt.Errorf("nginx -t failed: %s", out)
	}
	if out, err := executeCommand("nginx", "-s", "reload"); err != nil {
		return fmt.Errorf("nginx reload failed: %s", out)
	}
	return nil
}

func renderNginxBannedIPs(ips map[string]bool) string {
	var entries []string
	for ip, banned := range ips {
		if banned && isValidIPOrCIDR(ip) {
			entries = append(entries, ip)
		}
	}
	sort.Strings(entries)

	var b strings.Builder
	b.WriteString("# WP Panel Generated - DO NOT EDIT MANUALLY\n")
	b.WriteString("geo $wppanel_banned_ip {\n")
	b.WriteString("    default 0;\n")
	for _, ip := range entries {
		b.WriteString("    ")
		b.WriteString(ip)
		b.WriteString(" 1;\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func withNginxBanLock(fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(nginxBanLockPath), 0755); err != nil {
		return err
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(nginxBanLockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			defer os.Remove(nginxBanLockPath)
			return fn()
		}
		if !os.IsExist(err) {
			return err
		}
		if info, statErr := os.Stat(nginxBanLockPath); statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.Remove(nginxBanLockPath)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for nginx ban lock")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func isValidIPOrCIDR(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(s)
	return err == nil
}
