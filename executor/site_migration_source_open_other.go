//go:build !linux

package executor

import "os"

type migrationSourceRoot struct {
	root string
}

func openMigrationSourceRoot(root string) (*migrationSourceRoot, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, err
	}
	return &migrationSourceRoot{root: root}, nil
}

func (r *migrationSourceRoot) Close() error { return nil }

func (r *migrationSourceRoot) Open(relativePath string) (*os.File, os.FileInfo, error) {
	return openMigrationSourceFile(r.root, relativePath)
}
