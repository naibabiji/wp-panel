//go:build !linux

package executor

import (
	"fmt"
	"os"
)

func fileOwnerIDs(os.FileInfo) (int, int, error) {
	return 0, 0, fmt.Errorf("file lock ownership verification requires Linux")
}
