//go:build !linux

package executor

import "errors"

func serviceControlLockDirectory(string) (string, error) {
	return "", errors.New("service control coordination requires Linux")
}
