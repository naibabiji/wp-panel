package executor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/naibabiji/wp-panel/config"
)

var errServiceControlBusy = errors.New("service operation busy")

// These are the guard's six services, not the automatic-maintenance whitelist.
var guardServiceNames = []string{"fail2ban", "mariadb", "nftables", "nginx", "php8.3-fpm", "redis-server"}

func guardedServiceName(unit string) (string, bool) {
	name := strings.TrimSuffix(unit, ".service")
	for _, service := range guardServiceNames {
		if name == service {
			return name, true
		}
	}
	return "", false
}

func serviceControlDataDir() string {
	if config.AppConfig == nil {
		return ""
	}
	return config.AppConfig.Panel.DataDir
}

// The caller owns all descriptors through its final state/health check. Never
// unlink these files: their existence is not an indication that a lock is held.
// An explicit directory also supports the detached runner before config loading.
func tryServiceControlLocks(dataDir string, units ...string) (func(), error) {
	names := make([]string, 0, len(units))
	for _, unit := range units {
		name, ok := guardedServiceName(unit)
		if !ok {
			return nil, fmt.Errorf("unsupported guarded service: %s", unit)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var files []*os.File
	release := func() {
		for i := len(files) - 1; i >= 0; i-- {
			files[i].Close()
		}
	}
	if len(names) == 0 {
		return release, nil
	}
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("service control data directory unavailable")
	}
	dir, err := serviceControlLockDirectory(dataDir)
	if err != nil {
		return nil, err
	}
	for i, name := range names {
		if i > 0 && names[i-1] == name {
			continue
		}
		f, err := tryMaintenanceFileLock(filepath.Join(dir, "service-control-"+name+".lock"))
		if err != nil {
			release()
			if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
				return nil, fmt.Errorf("%w: %s", errServiceControlBusy, name)
			}
			return nil, err
		}
		files = append(files, f)
	}
	return release, nil
}
