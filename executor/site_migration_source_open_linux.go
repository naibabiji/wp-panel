//go:build linux

package executor

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type migrationSourceRoot struct {
	file *os.File
}

func openMigrationSourceRoot(root string) (*migrationSourceRoot, error) {
	file, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		_ = file.Close()
		if err == nil {
			err = errors.New("source root is not a directory")
		}
		return nil, err
	}
	return &migrationSourceRoot{file: file}, nil
}

func (r *migrationSourceRoot) Close() error {
	if r == nil || r.file == nil {
		return nil
	}
	return r.file.Close()
}

func (r *migrationSourceRoot) Open(relativePath string) (*os.File, os.FileInfo, error) {
	rel, err := normalizeMigrationRelativePath(relativePath)
	if err != nil {
		return nil, nil, err
	}
	fd, err := unix.Openat2(int(r.file.Fd()), rel, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK,
		Resolve: secureSiteResolveFlags,
	})
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), rel)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err == nil {
			err = errors.New("source shard path is not a regular file")
		}
		return nil, nil, err
	}
	return file, info, nil
}
