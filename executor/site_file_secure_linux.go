package executor

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const secureSiteResolveFlags = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS

type secureSiteFileMetadata struct {
	mode os.FileMode
	uid  int
	gid  int
	dev  uint64
	ino  uint64
}

func validateSecureSiteParent(stat *unix.Stat_t) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("网站上级目录不安全")
	}
	// A root-owned parent may inherit 0775 from the installer's umask. GID 0
	// remains a trusted administrative group; allowing its write bit does not
	// give a per-site user permission to replace WebRoot. Other groups and all
	// world-writable parents remain untrusted, including sticky directories.
	if stat.Mode&0002 != 0 || (stat.Mode&0020 != 0 && stat.Gid != 0) {
		return errors.New("网站上级目录不安全")
	}
	return nil
}

func openSecureSiteRoot(webRoot string) (int, *unix.Stat_t, error) {
	webRoot = filepath.Clean(strings.TrimSpace(webRoot))
	if webRoot == "" || webRoot == "." || webRoot == string(filepath.Separator) || !filepath.IsAbs(webRoot) {
		return -1, nil, errors.New("网站目录不安全")
	}
	parent := filepath.Dir(webRoot)
	base := filepath.Base(webRoot)
	parentFD, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, nil, err
	}
	defer unix.Close(parentFD)
	var parentStat unix.Stat_t
	if err := unix.Fstat(parentFD, &parentStat); err != nil {
		return -1, nil, err
	}
	if err := validateSecureSiteParent(&parentStat); err != nil {
		return -1, nil, err
	}
	fd, err := unix.Openat2(parentFD, base, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: secureSiteResolveFlags,
	})
	if err != nil {
		return -1, nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return -1, nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		unix.Close(fd)
		return -1, nil, errors.New("网站目录不是普通目录")
	}
	return fd, &stat, nil
}

func validateSecureSiteRelativePath(name string) (string, error) {
	name = filepath.Clean(strings.TrimSpace(name))
	if name == "" || name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return "", errors.New("站点文件路径不安全")
	}
	return name, nil
}

func openSecureSiteFile(rootFD int, name string, flags uint64) (int, error) {
	return unix.Openat2(rootFD, name, &unix.OpenHow{
		Flags:   flags | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK,
		Resolve: secureSiteResolveFlags,
	})
}

func validateSecureSiteFile(fd int, rootStat *unix.Stat_t) (secureSiteFileMetadata, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return secureSiteFileMetadata{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return secureSiteFileMetadata{}, errors.New("站点文件不是普通文件")
	}
	if stat.Nlink != 1 {
		return secureSiteFileMetadata{}, errors.New("站点文件存在硬链接")
	}
	// Unlocked sites own their WebRoot and files. A healthy file-locked site
	// instead uses root:<site group> for both WebRoot and wp-config.php.
	if stat.Uid != rootStat.Uid || stat.Gid != rootStat.Gid {
		return secureSiteFileMetadata{}, errors.New("站点文件属主与网站目录不一致")
	}
	return secureSiteFileMetadata{
		mode: os.FileMode(stat.Mode & 0777),
		uid:  int(stat.Uid),
		gid:  int(stat.Gid),
		dev:  uint64(stat.Dev),
		ino:  stat.Ino,
	}, nil
}

func readSecureSiteFile(webRoot, name string) ([]byte, secureSiteFileMetadata, error) {
	name, err := validateSecureSiteRelativePath(name)
	if err != nil {
		return nil, secureSiteFileMetadata{}, err
	}
	rootFD, rootStat, err := openSecureSiteRoot(webRoot)
	if err != nil {
		return nil, secureSiteFileMetadata{}, err
	}
	defer unix.Close(rootFD)
	fd, err := openSecureSiteFile(rootFD, name, unix.O_RDONLY)
	if err != nil {
		return nil, secureSiteFileMetadata{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	metadata, err := validateSecureSiteFile(fd, rootStat)
	if err != nil {
		return nil, secureSiteFileMetadata{}, err
	}
	data, err := io.ReadAll(file)
	return data, metadata, err
}

func secureSiteTempName(name string) (string, error) {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(name), ".wp-panel-"+filepath.Base(name)+"-"+hex.EncodeToString(token[:])), nil
}

func writeSecureSiteFile(webRoot, name string, data []byte, createMode os.FileMode) error {
	name, err := validateSecureSiteRelativePath(name)
	if err != nil {
		return err
	}
	rootFD, rootStat, err := openSecureSiteRoot(webRoot)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)

	metadata := secureSiteFileMetadata{mode: createMode.Perm(), uid: int(rootStat.Uid), gid: int(rootStat.Gid)}
	existing := false
	fd, openErr := openSecureSiteFile(rootFD, name, unix.O_RDONLY)
	if openErr == nil {
		existing = true
		metadata, err = validateSecureSiteFile(fd, rootStat)
		unix.Close(fd)
		if err != nil {
			return err
		}
	} else if !errors.Is(openErr, syscall.ENOENT) {
		return openErr
	}

	tmpName, err := secureSiteTempName(name)
	if err != nil {
		return err
	}
	tmpFD, err := openSecureSiteFile(rootFD, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return err
	}
	tmpOpen := true
	defer func() {
		if tmpOpen {
			unix.Close(tmpFD)
		}
		_ = unix.Unlinkat(rootFD, tmpName, 0)
	}()
	if err := unix.Fchmod(tmpFD, uint32(metadata.mode.Perm())); err != nil {
		return err
	}
	if err := unix.Fchown(tmpFD, metadata.uid, metadata.gid); err != nil {
		return err
	}
	tmpFile := os.NewFile(uintptr(tmpFD), tmpName)
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		tmpOpen = false
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		tmpOpen = false
		return err
	}
	if err := tmpFile.Close(); err != nil {
		tmpOpen = false
		return err
	}
	tmpOpen = false

	if existing {
		currentFD, err := openSecureSiteFile(rootFD, name, unix.O_RDONLY)
		if err != nil {
			return err
		}
		current, validateErr := validateSecureSiteFile(currentFD, rootStat)
		unix.Close(currentFD)
		if validateErr != nil {
			return validateErr
		}
		if current.dev != metadata.dev || current.ino != metadata.ino {
			return errors.New("站点文件在写入期间发生变化")
		}
	}
	if err := unix.Renameat(rootFD, tmpName, rootFD, name); err != nil {
		return fmt.Errorf("原子替换站点文件失败: %w", err)
	}
	if err := unix.Fsync(rootFD); err != nil {
		return fmt.Errorf("同步站点目录失败: %w", err)
	}
	return nil
}

func readWPConfigSecure(webRoot string) ([]byte, error) {
	data, _, err := readSecureSiteFile(webRoot, "wp-config.php")
	return data, err
}

func writeWPConfigSecure(webRoot string, data []byte) error {
	return writeSecureSiteFile(webRoot, "wp-config.php", data, 0600)
}

func setWPConfigPermissionsSecure(webRoot string, mode os.FileMode) error {
	rootFD, rootStat, err := openSecureSiteRoot(webRoot)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	fd, err := openSecureSiteFile(rootFD, "wp-config.php", unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if _, err := validateSecureSiteFile(fd, rootStat); err != nil {
		return err
	}
	if err := unix.Fchown(fd, int(rootStat.Uid), int(rootStat.Gid)); err != nil {
		return err
	}
	return unix.Fchmod(fd, uint32(mode.Perm()))
}
