//go:build linux

package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const maintenanceMaxProcesses = 16384
const maintenanceMaxPaths = 512

var maintenanceMapEscape = regexp.MustCompile(`\\[0-7]{3}`)
var maintenancePackageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*(?::[a-z0-9-]+)?$`)
var maintenanceSystemPath = regexp.MustCompile(`^/usr/[A-Za-z0-9_./+@:-]+$`)
var errMaintenanceProcessChanged = errors.New("maintenance process exited or changed")

func maintenanceProcessGone(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, unix.ESRCH)
}

type maintenanceMapEntry struct {
	path    string
	old     maintenanceFileIdentity
	deleted bool
}

func readMaintenanceProcFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = errors.New("maintenance proc file exceeds limit")
	}
	return b, err
}

func maintenanceProcessStart(dir string) (uint64, error) {
	b, err := readMaintenanceProcFile(filepath.Join(dir, "stat"), 8192)
	if err != nil {
		return 0, err
	}
	// comm can itself contain spaces and ')'. Fields following its last ')'
	// start with state (field 3), so starttime (field 22) is index 19.
	end := bytes.LastIndexByte(b, ')')
	if end < 0 {
		return 0, errors.New("invalid process identity")
	}
	fields := strings.Fields(string(b[end+1:]))
	if len(fields) < 20 {
		return 0, errors.New("truncated process identity")
	}
	return strconv.ParseUint(fields[19], 10, 64)
}

func maintenanceProcessUnit(dir string) (string, error) {
	b, err := readMaintenanceProcFile(filepath.Join(dir, "cgroup"), 65536)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "0::/") {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(line, "0::"), "/")
		// Descendants belong to the user manager, not to same-named
		// system units. Preserve this boundary for reboot classification.
		for _, part := range parts {
			if strings.HasPrefix(part, "user@") && strings.HasSuffix(part, ".service") {
				return part, nil
			}
		}
		for i := len(parts) - 1; i >= 0; i-- {
			if strings.HasSuffix(parts[i], ".service") || strings.HasSuffix(parts[i], ".scope") {
				return parts[i], nil
			}
		}
		return "unmapped", nil
	}
	return "", errors.New("unsupported process cgroup layout")
}

func parseMaintenanceMapping(line string) (maintenanceMapEntry, bool, error) {
	var entry maintenanceMapEntry
	fields := strings.Fields(line)
	if len(fields) < 6 || !strings.HasPrefix(fields[5], "/") {
		return entry, false, nil
	}
	path := maintenanceMapEscape.ReplaceAllStringFunc(fields[5], func(s string) string {
		n, _ := strconv.ParseUint(s[1:], 8, 8)
		return string([]byte{byte(n)})
	})
	if !strings.HasPrefix(path, "/usr/") || strings.HasPrefix(path, "/usr/local/") {
		return entry, false, nil
	}
	base := filepath.Base(path)
	if !strings.Contains(fields[1], "x") && !strings.HasSuffix(base, ".so") && !strings.Contains(base, ".so.") {
		return entry, false, nil
	}
	if len(path) > 4096 || !maintenanceSystemPath.MatchString(path) || filepath.Clean(path) != path ||
		(len(fields) != 6 && (len(fields) != 7 || fields[6] != "(deleted)")) {
		return entry, false, errors.New("unsupported system mapping path")
	}
	device := strings.Split(fields[3], ":")
	if len(device) != 2 {
		return entry, false, errors.New("invalid mapped device")
	}
	major, err1 := strconv.ParseUint(device[0], 16, 32)
	minor, err2 := strconv.ParseUint(device[1], 16, 32)
	inode, err3 := strconv.ParseUint(fields[4], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || inode == 0 {
		return entry, false, errors.New("invalid mapped file identity")
	}
	return maintenanceMapEntry{path, maintenanceFileIdentity{unix.Mkdev(uint32(major), uint32(minor)), inode}, len(fields) == 7}, true, nil
}

func maintenanceRootFileIdentity(root int, path string) (maintenanceFileIdentity, error) {
	fd, err := unix.Openat2(root, path, &unix.OpenHow{
		Flags:   uint64(unix.O_PATH | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return maintenanceFileIdentity{}, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return maintenanceFileIdentity{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return maintenanceFileIdentity{}, errors.New("mapped path is not a regular file")
	}
	return maintenanceFileIdentity{uint64(st.Dev), st.Ino}, nil
}

type maintenanceProcessObservation struct {
	unit        string
	evidence    []maintenanceRuntimeEvidence
	unknown     bool
	unsupported int
	calibrated  map[uint64]bool
	reason      string
}

func (p *maintenanceProcessObservation) markUnknown(reason string) {
	p.unknown = true
	if p.reason == "" {
		p.reason = reason
	}
}

func scanMaintenanceProcess(ctx context.Context, dir string, pid, hostRoot int, readStart func(string) (uint64, error)) (maintenanceProcessObservation, error) {
	var result maintenanceProcessObservation
	start, err := readStart(dir)
	if err != nil {
		return result, err
	}
	result.unit, err = maintenanceProcessUnit(dir)
	if err != nil {
		return result, err
	}
	root, err := unix.Open(filepath.Join(dir, "root"), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return result, errMaintenanceProcessChanged
		}
		if os.IsNotExist(err) {
			if _, identityErr := readStart(dir); maintenanceProcessGone(identityErr) {
				return result, errMaintenanceProcessChanged
			}
			// Kernel threads have no root and an empty maps file. A live
			// userspace process with mappings but no readable root is unknown.
			if b, mapsErr := readMaintenanceProcFile(filepath.Join(dir, "maps"), 8<<20); mapsErr == nil && len(bytes.TrimSpace(b)) == 0 {
				return result, errMaintenanceProcessChanged
			}
			return result, errors.New("live process root unavailable")
		}
		return result, err
	}
	defer unix.Close(root)
	maps, err := readMaintenanceProcFile(filepath.Join(dir, "maps"), 8<<20)
	if err != nil {
		return result, err
	}
	calibrated := map[uint64]bool{}
	result.calibrated = calibrated
	seen := map[string]bool{}
	for _, line := range strings.Split(string(maps), "\n") {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		entry, use, err := parseMaintenanceMapping(line)
		if err != nil {
			result.markUnknown("mapping_parse_failed")
			continue
		}
		if !use {
			continue
		}
		key := fmt.Sprintf("%s/%d/%d", entry.path, entry.old.Device, entry.old.Inode)
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(seen) > 4096 {
			return result, errors.New("process mapping count exceeds limit")
		}
		current, e1 := maintenanceRootFileIdentity(root, entry.path)
		host, e2 := maintenanceRootFileIdentity(hostRoot, entry.path)
		if e1 != nil || e2 != nil {
			// Self-chrooting daemons can lose access to normal mappings loaded
			// before chroot. An unchanged host identity proves this file was
			// not replaced; skip only this unusable calibration sample. Deleted
			// mappings and changed/unreadable host files remain unknown.
			if !entry.deleted && os.IsNotExist(e1) && e2 == nil && host == entry.old {
				continue
			}
			reason := "mapping_unreadable"
			if os.IsNotExist(e1) || os.IsNotExist(e2) {
				reason = "mapping_path_missing"
			}
			result.markUnknown(reason)
			continue
		}
		if current != host {
			if entry.deleted {
				result.unsupported++
			}
			continue
		}
		if !entry.deleted {
			if current == entry.old {
				calibrated[current.Device] = true
			} else {
				result.markUnknown("normal_identity_changed")
			}
			continue
		}
		if current.Device != entry.old.Device || current.Inode == entry.old.Inode {
			result.markUnknown("replacement_identity_unknown")
			continue
		}
		result.evidence = append(result.evidence, maintenanceRuntimeEvidence{
			Unit: result.unit, PID: pid, Start: start, Path: entry.path, Old: entry.old, Current: current,
		})
	}
	end, err := readStart(dir)
	if maintenanceProcessGone(err) || (err == nil && end != start) {
		return maintenanceProcessObservation{}, errMaintenanceProcessChanged
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

// Only scans metadata; caller must own the package-manager observation gate.
// This is not wired to any scheduler until the action/recovery layer is reviewed.
func scanSystemMaintenanceRuntime(ctx context.Context) (maintenanceRuntimeSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return scanMaintenanceRuntimeAt(ctx, "/proc", maintenancePackageOwners)
}

func scanMaintenanceRuntimeAt(ctx context.Context, procDir string, owners func(context.Context, []string) (map[string]string, error)) (maintenanceRuntimeSnapshot, error) {
	result := maintenanceRuntimeSnapshot{At: time.Now().UTC(), UnknownUnits: map[string]bool{}}
	hostRoot, err := unix.Open(filepath.Join(procDir, "1", "root"), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return result, err
	}
	defer unix.Close(hostRoot)
	f, err := os.Open(procDir)
	if err != nil {
		return result, err
	}
	entries, err := f.ReadDir(maintenanceMaxProcesses + 1)
	f.Close()
	if err != nil || len(entries) > maintenanceMaxProcesses {
		return result, errors.New("cannot enumerate bounded process snapshot")
	}
	sort.Slice(entries, func(i, j int) bool {
		a, _ := strconv.Atoi(entries[i].Name())
		b, _ := strconv.Atoi(entries[j].Name())
		return a < b
	})
	collector := newMaintenanceEvidenceCollector(&result)
	calibrated := map[uint64]bool{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid < 1 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		p, err := scanMaintenanceProcess(ctx, filepath.Join(procDir, entry.Name()), pid, hostRoot, maintenanceProcessStart)
		if maintenanceProcessGone(err) || errors.Is(err, errMaintenanceProcessChanged) {
			continue
		}
		if err != nil || p.unknown {
			unit := p.unit
			if unit == "" {
				unit = "*"
			}
			reason := p.reason
			if err != nil {
				reason = "process_unreadable"
			}
			collector.unknown(unit, reason)
		}
		if err != nil {
			continue
		}
		result.Unsupported += p.unsupported
		if p.unsupported > 0 {
			collector.unknown(p.unit, "non_host_view")
		}
		for device := range p.calibrated {
			calibrated[device] = true
		}
		for _, e := range p.evidence {
			collector.add(e)
		}
	}
	for _, e := range collector.evidence() {
		if !calibrated[e.Old.Device] {
			collector.unknown(e.Unit, "filesystem_uncalibrated")
		}
	}
	result.Evidence = collector.evidence()
	paths := map[string]bool{}
	for _, e := range result.Evidence {
		paths[e.Path] = true
	}
	if len(paths) == 0 {
		return result, ctx.Err()
	}
	var names []string
	for path := range paths {
		names = append(names, path)
	}
	sort.Strings(names)
	packages := map[string]string{}
	for start := 0; start < len(names); start += maintenanceMaxPaths {
		end := start + maintenanceMaxPaths
		if end > len(names) {
			end = len(names)
		}
		batch, err := owners(ctx, names[start:end])
		if err != nil {
			return result, err
		}
		for path, owner := range batch {
			packages[path] = owner
		}
	}
	kept := result.Evidence[:0]
	for _, e := range result.Evidence {
		owner, ok := packages[e.Path]
		if !ok || !maintenancePackageName.MatchString(owner) {
			collector.unknown(e.Unit, "package_unconfirmed")
			continue
		}
		e.Package = owner
		kept = append(kept, e)
	}
	result.Evidence = kept
	// Ownership is checked after collecting representatives. A later unowned
	// path invalidates its unit, including earlier paths in the same unit.
	kept = result.Evidence[:0]
	for _, e := range result.Evidence {
		if !result.UnknownUnits[e.Unit] {
			kept = append(kept, e)
		}
	}
	result.Evidence = kept
	return result, ctx.Err()
}

type maintenanceOwnershipOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *maintenanceOwnershipOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		b.overflow = true
		return 0, errors.New("package ownership output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func maintenancePackageOwners(ctx context.Context, paths []string) (map[string]string, error) {
	if len(paths) > maintenanceMaxPaths {
		return nil, errors.New("too many ownership paths")
	}
	for _, path := range paths {
		if !maintenanceSystemPath.MatchString(path) || filepath.Clean(path) != path {
			return nil, errors.New("invalid ownership path")
		}
	}
	cmd := exec.CommandContext(ctx, "dpkg-query", append([]string{"-S", "--"}, paths...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var output maintenanceOwnershipOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	err := cmd.Run()
	if output.overflow {
		return nil, errors.New("package ownership output exceeds limit")
	}
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1 && ctx.Err() == nil) {
		return nil, fmt.Errorf("package ownership query failed: %w", err)
	}
	wanted := map[string]bool{}
	for _, path := range paths {
		wanted[path] = true
	}
	result := map[string]string{}
	for _, line := range strings.Split(output.String(), "\n") {
		cut := strings.LastIndex(line, ": ")
		if cut < 0 {
			continue
		}
		name, path := line[:cut], line[cut+2:]
		if wanted[path] && maintenancePackageName.MatchString(name) {
			if previous := result[path]; previous != "" && previous != name {
				return nil, errors.New("ambiguous package ownership")
			}
			result[path] = name
		}
	}
	return result, nil
}
