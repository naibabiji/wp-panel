//go:build !linux

package executor

import (
	"errors"
	"os"
)

var errSecureSiteFilesUnsupported = errors.New("安全站点文件操作仅支持 Linux")

func readWPConfigSecure(string) ([]byte, error) {
	return nil, errSecureSiteFilesUnsupported
}

func writeWPConfigSecure(string, []byte) error {
	return errSecureSiteFilesUnsupported
}

func setWPConfigPermissionsSecure(string, os.FileMode) error {
	return errSecureSiteFilesUnsupported
}
