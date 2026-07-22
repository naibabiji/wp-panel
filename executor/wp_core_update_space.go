package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const wpCoreMinimumReserve = uint64(1 << 30)

func defaultWPCoreSpaceChecker(ctx context.Context, execution wpCoreUpdateExecution, inspection ZIPInspection) error {
	if inspection.DeclaredUncompressed == 0 || !isValidMySQLIdentifier(execution.DatabaseName) {
		return errors.New("invalid core update space inputs")
	}
	targetBytes, err := wpCoreTargetBytes(execution.WebRoot)
	if err != nil {
		return errors.New("core update target size unavailable")
	}
	dbBytes, err := wpCoreDatabaseBytes(ctx, execution.DatabaseName)
	if err != nil {
		return errors.New("core update database size unavailable")
	}
	working, err := wpCoreWorkingBytes(targetBytes, dbBytes, inspection.DeclaredUncompressed)
	if err != nil {
		return err
	}
	paths := []string{execution.WebRoot, filepath.Dir(execution.PackagePath), "/var/lib/mysql", "/var"}
	seen := map[uint64]bool{}
	for _, name := range paths {
		info, err := os.Stat(name)
		if err != nil {
			return errors.New("core update filesystem unavailable")
		}
		fileStat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("core update filesystem unavailable")
		}
		var stat syscall.Statfs_t
		if err := syscall.Statfs(name, &stat); err != nil {
			return errors.New("core update filesystem unavailable")
		}
		device := uint64(fileStat.Dev)
		if seen[device] {
			continue
		}
		seen[device] = true
		total := stat.Blocks * uint64(stat.Bsize)
		available := stat.Bavail * uint64(stat.Bsize)
		if !wpCoreHasAvailableSpace(working, total, available) {
			return errors.New("core update disk space insufficient")
		}
	}
	return nil
}

func wpCoreWorkingBytes(targetBytes, databaseBytes, packageBytes uint64) (uint64, error) {
	if targetBytes > ^uint64(0)-databaseBytes || targetBytes+databaseBytes > ^uint64(0)-packageBytes {
		return 0, errors.New("core update size overflow")
	}
	sum := targetBytes + databaseBytes + packageBytes
	if sum > ^uint64(0)/3 {
		return 0, errors.New("core update size overflow")
	}
	return 3 * sum, nil
}

func wpCoreHasAvailableSpace(working, total, available uint64) bool {
	reserve := total / 20
	if reserve < wpCoreMinimumReserve {
		reserve = wpCoreMinimumReserve
	}
	return working <= ^uint64(0)-reserve && available >= working+reserve
}

func wpCoreTargetBytes(webRoot string) (uint64, error) {
	root, err := safeSiteWebRoot(webRoot)
	if err != nil {
		return 0, err
	}
	var total uint64
	paths := append([]string{"wp-admin", "wp-includes"}, wpCoreRootFiles...)
	for _, name := range paths {
		path := filepath.Join(root, name)
		err := filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("core target symlink rejected")
			}
			if info.Mode().IsRegular() {
				size := uint64(info.Size())
				if total > ^uint64(0)-size {
					return errors.New("core target size overflow")
				}
				total += size
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func wpCoreDatabaseBytes(ctx context.Context, dbName string) (uint64, error) {
	if !isValidMySQLIdentifier(dbName) {
		return 0, errors.New("invalid database name")
	}
	password := readMariaDBPassword()
	if password == "" {
		return 0, errors.New("database credentials unavailable")
	}
	mysqlPath, err := validateInventoryBinary(wpCoreMySQLPath, "/usr/bin", 0, 0)
	if err != nil {
		return 0, err
	}
	query := "SELECT COALESCE(SUM(DATA_LENGTH + INDEX_LENGTH),0) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = '" + dbName + "'"
	cmd := exec.CommandContext(ctx, mysqlPath, "-u", "root", "-B", "-N", "-e", query)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+password)
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	return value, err
}
