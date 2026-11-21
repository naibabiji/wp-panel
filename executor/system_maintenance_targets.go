package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

type maintenanceTarget struct {
	Kind     string                      `json:"kind"`
	Target   string                      `json:"target"`
	Evidence []maintenanceStoredEvidence `json:"evidence"`
}

var maintenanceServices = map[string]bool{
	"nginx.service": true, "php8.3-fpm.service": true, "mariadb.service": true,
	"redis-server.service": true, "fail2ban.service": true, "rsyslog.service": true, "cron.service": true,
}

var maintenanceCommand = func(ctx context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return runSystemPackageUpdateCommand(ctx, name, args...)
}
var maintenanceScan = scanSystemMaintenanceRuntime
var maintenanceRebootTarget = collectMaintenanceRebootTarget
var maintenanceUnitMainPID = maintenanceMainPID

func maintenanceMainPID(ctx context.Context, unit string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", "--value", "--property=MainPID", unit).Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// Kernel and marker target names contain the actual target, rather than an
// unchanging generic key that would permanently consume an empty-file budget.
func collectMaintenanceRebootTarget(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	running, err := systemPackageRunningKernel()
	if err != nil {
		return "", err
	}
	installed, err := systemPackageInstalledKernels()
	if err != nil {
		return "", err
	}
	flavour, err := kernelReleaseFlavour(running)
	if err != nil {
		return "", err
	}
	best := running
	matched := false
	for _, candidate := range installed {
		f, e := kernelReleaseFlavour(candidate)
		if e != nil || f != flavour {
			continue
		}
		matched = true
		greater, e := systemPackageKernelGreater(candidate, best)
		if e != nil {
			return "", e
		}
		if greater {
			best = candidate
		}
	}
	if !matched {
		return "", errors.New("running kernel flavour not installed")
	}
	target := ""
	if best != running {
		target = "kernel/" + best
	}
	marker, err := systemPackageRebootMarker()
	if err != nil {
		return "", err
	}
	if marker {
		data, err := os.ReadFile("/run/reboot-required.pkgs")
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if len(data) > 65536 {
			return "", errors.New("reboot marker exceeds limit")
		}
		pkgs := strings.Fields(string(data))
		sort.Strings(pkgs)
		versions := []string{"reboot-required"}
		seen := map[string]bool{}
		for _, pkg := range pkgs {
			if seen[pkg] {
				continue
			}
			seen[pkg] = true
			if !maintenanceMarkerPackageName.MatchString(pkg) {
				return "", errors.New("invalid marker package")
			}
			out, err := exec.CommandContext(ctx, "dpkg-query", "-W", "-f=${Package}=${Version}", "--", pkg).Output()
			if err != nil {
				return "", err
			}
			versions = append(versions, string(out))
		}
		if target != "" {
			target += "/"
		}
		target += "marker/" + maintenanceMarkerDigest(versions)
	}
	return target, nil
}

func maintenanceMarkerDigest(versions []string) string {
	set := map[string]bool{}
	for _, v := range versions {
		set[strings.TrimSpace(v)] = true
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	digest := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(digest[:])
}

func classifyMaintenanceTargets(ctx context.Context, snapshot maintenanceRuntimeSnapshot) ([]maintenanceTarget, error) {
	if snapshot.UnknownUnits["*"] {
		return nil, errors.New("runtime snapshot globally unknown")
	}
	byUnit := map[string][]maintenanceRuntimeEvidence{}
	var reboot []maintenanceRuntimeEvidence
	cronPID := 0
	for _, e := range snapshot.Evidence {
		if snapshot.UnknownUnits[e.Unit] {
			continue
		}
		if e.Unit == "cron.service" && cronPID == 0 {
			var err error
			cronPID, err = maintenanceUnitMainPID(ctx, e.Unit)
			if err != nil {
				return nil, err
			}
		}
		if maintenanceServices[e.Unit] && (e.Unit != "cron.service" || e.PID == cronPID) {
			byUnit[e.Unit] = append(byUnit[e.Unit], e)
		} else {
			reboot = append(reboot, e)
		}
	}
	var targets []maintenanceTarget
	for unit, evidence := range byUnit {
		targets = append(targets, maintenanceTarget{"service", unit, compactMaintenanceEvidence(evidence)})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Target < targets[j].Target })
	key, err := maintenanceRebootTarget(ctx)
	if err != nil {
		// An unknown kernel/marker target is not a reason to discard reliable
		// evidence for independently managed services or replaced host files.
		if snapshot.UnknownUnits != nil {
			snapshot.UnknownUnits["kernel_or_marker"] = true
		}
		key = ""
	}
	if key == "" && len(reboot) > 0 {
		key = "runtime"
	}
	if key != "" {
		targets = append(targets, maintenanceTarget{"reboot", key, compactMaintenanceEvidence(reboot)})
	}
	return targets, nil
}

func maintenanceTargetStillPresent(t maintenanceTarget, snapshot maintenanceRuntimeSnapshot) (bool, error) {
	if snapshot.UnknownUnits["*"] {
		return false, errors.New("runtime unknown")
	}
	keys := map[string]bool{}
	cronPID := 0
	if t.Kind == "service" && t.Target == "cron.service" {
		var err error
		cronPID, err = maintenanceUnitMainPID(context.Background(), t.Target)
		if err != nil {
			return false, err
		}
	}
	for _, e := range snapshot.Evidence {
		if t.Kind == "service" && (e.Unit != t.Target || (t.Target == "cron.service" && e.PID != cronPID)) {
			continue
		}
		keys[e.fileKey()] = true
	}
	for _, e := range t.Evidence {
		if snapshot.UnknownUnits[e.Unit] {
			return false, fmt.Errorf("unit unknown: %s", e.Unit)
		}
		if keys[(maintenanceRuntimeEvidence{Path: e.Path, Old: e.Old}).fileKey()] {
			return true, nil
		}
	}
	return false, nil
}

func maintenanceServiceHasOldMappings(ctx context.Context, unit string, snapshot maintenanceRuntimeSnapshot) (bool, error) {
	if snapshot.UnknownUnits["*"] || snapshot.UnknownUnits[unit] {
		return false, errors.New("service runtime unknown")
	}
	main := 0
	if unit == "cron.service" {
		var err error
		main, err = maintenanceUnitMainPID(ctx, unit)
		if err != nil {
			return false, err
		}
	}
	for _, e := range snapshot.Evidence {
		if e.Unit == unit && (unit != "cron.service" || e.PID == main) {
			return true, nil
		}
	}
	return false, nil
}

func maintenanceRebootReached(ctx context.Context, target string) (bool, error) {
	parts := strings.Split(target, "/")
	if parts[0] == "kernel" {
		if len(parts) < 2 {
			return false, errors.New("invalid kernel target")
		}
		running, err := systemPackageRunningKernel()
		if err != nil {
			return false, err
		}
		greater, err := systemPackageKernelGreater(parts[1], running)
		if err != nil || greater {
			return false, err
		}
	}
	if strings.Contains(target, "marker/") {
		marker, err := systemPackageRebootMarker()
		if err != nil || marker {
			return false, err
		}
	}
	return true, ctx.Err()
}

func maintenanceServiceReady(ctx context.Context, cfg *config.Config, unit string) error {
	if err := maintenanceCommand(ctx, "systemctl", "is-active", "--quiet", unit); err != nil {
		return err
	}
	switch unit {
	case "mariadb.service":
		return MariaDBReady(ctx, cfg)
	case "redis-server.service":
		return RedisReady(ctx)
	case "fail2ban.service":
		return maintenanceCommand(ctx, "fail2ban-client", "ping")
	case "nginx.service":
		return maintenanceNginxReady(ctx)
	case "php8.3-fpm.service":
		return maintenanceFPMReady(ctx, cfg)
	}
	return nil
}
