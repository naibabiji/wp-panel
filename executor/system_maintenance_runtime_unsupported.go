//go:build !linux

package executor

import (
	"context"
	"database/sql"
	"errors"
	"github.com/naibabiji/wp-panel/config"
	"os"
)

func scanSystemMaintenanceRuntime(context.Context) (maintenanceRuntimeSnapshot, error) {
	return maintenanceRuntimeSnapshot{}, errors.New("system maintenance runtime inspection requires Linux")
}

func maintenanceDPKGLocksBusy() (bool, error) { return false, errors.New("maintenance requires Linux") }
func maintenancePackageBusy(*config.Config) (bool, error) {
	return false, errors.New("maintenance requires Linux")
}
func acquireMaintenanceGate(*config.Config, *sql.DB) (func(), string, error) {
	return nil, "unsupported", errors.New("maintenance requires Linux")
}

func tryMaintenanceFileLock(string) (*os.File, error) {
	return nil, errors.New("maintenance requires Linux")
}

func acquireMaintenanceLifecycleFile(*config.Config) (*os.File, error) {
	return nil, errors.New("maintenance requires Linux")
}
func acquireMaintenanceGateForUpdate(*config.Config, *sql.DB, string) (func(), string, error) {
	return nil, "unsupported", errors.New("maintenance requires Linux")
}
