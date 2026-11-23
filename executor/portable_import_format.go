package executor

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const (
	PortableManifestLimit = 4 << 20
	PortableChunkSize     = 5 << 20
	portableBlockLimit    = 16 << 20
	portableBundleName    = "bundle.tar"
)

var portableSHA = regexp.MustCompile("^[0-9a-f]{64}$")
var portableID = regexp.MustCompile("^[0-9a-f]{32}$")

type PortableArtifact struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type portableManifest struct {
	Magic       string `json:"magic"`
	Version     int    `json:"format_version"`
	ID          string `json:"id"`
	Complete    bool   `json:"complete"`
	Mode        string `json:"mode"`
	CompletedAt string `json:"completed_at"`
	Source      struct {
		Home    string `json:"home"`
		SiteURL string `json:"siteurl"`
		Prefix  string `json:"prefix"`
	} `json:"source"`
	Tables []struct {
		Name string `json:"name"`
		Rows int64  `json:"rows"`
	} `json:"tables"`
	Index    PortableArtifact   `json:"index"`
	Packages []PortableArtifact `json:"packages"`
}

func parsePortableManifest(raw []byte) (portableManifest, *url.URL, error) {
	var m portableManifest
	if len(raw) > PortableManifestLimit || json.Unmarshal(raw, &m) != nil ||
		m.Magic != "wp-panel-backup-development" || m.Version != 1 || !m.Complete || !portableID.MatchString(m.ID) {
		return m, nil, errors.New("备份清单不完整或格式版本不受支持")
	}
	src, err := url.Parse(m.Source.Home)
	if err != nil || (src.Scheme != "http" && src.Scheme != "https") || !IsValidDomain(src.Host) ||
		strings.Contains("."+strings.ToLower(src.Host), ".xn--") || src.User != nil || src.ForceQuery ||
		(src.Path != "" && src.Path != "/") || src.RawQuery != "" || src.Fragment != "" ||
		m.Source.Home != m.Source.SiteURL || !IsValidWPTablePrefix(m.Source.Prefix) {
		return m, nil, errors.New("只支持标准 HTTP/HTTPS 地址、根目录和相同 home/siteurl 的 WordPress 单站")
	}
	if m.Mode != "normal" && m.Mode != "temporary" && m.Mode != "move" {
		return m, nil, errors.New("备份模式无效")
	}
	if len(m.Tables) == 0 || len(m.Packages) == 0 || m.Index.Name != "files.ndjson" || !validPortableArtifact(m.Index) {
		return m, nil, errors.New("清单缺少数据库表、索引或 ZIP 分包")
	}
	seen := make(map[string]bool, len(m.Tables))
	for _, table := range m.Tables {
		if table.Name == "" || table.Rows < 0 || seen[table.Name] {
			return m, nil, errors.New("数据库表清单无效")
		}
		seen[table.Name] = true
	}
	if !seen[m.Source.Prefix+"options"] {
		return m, nil, errors.New("缺少活动表前缀对应的 WordPress 配置表")
	}
	for i, item := range m.Packages {
		if item.Name != fmt.Sprintf("part-%05d.zip", i+1) || !validPortableArtifact(item) {
			return m, nil, errors.New("ZIP 分包清单无效")
		}
	}
	return m, src, nil
}

func validPortableArtifact(a PortableArtifact) bool {
	return a.Size > 0 && portableSHA.MatchString(a.SHA256)
}

func (m portableManifest) artifacts() []PortableArtifact {
	return append([]PortableArtifact{m.Index}, m.Packages...)
}

func portablePathValid(name string) bool {
	return name != "." && name != ".." && !strings.HasPrefix(name, "../") &&
		!strings.HasPrefix(name, "/") && !strings.ContainsAny(name, "\\\x00") && path.Clean(name) == name
}

var errPortableArtifactMismatch = errors.New("备份材料大小或校验值不匹配")

func verifyPortableArtifact(dir string, item PortableArtifact) error {
	digest, size, err := hashRegularFile(filepath.Join(dir, item.Name))
	if err != nil {
		return fmt.Errorf("读取备份材料 %s 失败：%w", item.Name, err)
	}
	if size != item.Size || digest != item.SHA256 {
		return fmt.Errorf("%w，请重新选择并补传 %s", errPortableArtifactMismatch, item.Name)
	}
	return nil
}

var errPortableTARInvalid = errors.New("整套 TAR 无效，请重新选择备份文件")

// verifiedPortableTAR bounds every read to the verified USTAR member.
func verifiedPortableTAR(input *os.File, raw []byte, m portableManifest) (map[string]*io.SectionReader, error) {
	sections := make(map[string]*io.SectionReader, len(m.Packages)+1)
	reader := tar.NewReader(input)
	expected := append([]PortableArtifact{{Name: "manifest.json", Size: int64(len(raw))}}, m.artifacts()...)
	for _, item := range expected {
		header, err := reader.Next()
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, tar.ErrHeader) {
			return nil, err
		}
		if err != nil || header.Format != tar.FormatUSTAR || header.Typeflag != tar.TypeReg ||
			header.Name != item.Name || header.Size != item.Size {
			return nil, fmt.Errorf("%w：%s 缺失或无效", errPortableTARInvalid, item.Name)
		}
		if item.Name == "manifest.json" {
			got, err := io.ReadAll(reader)
			if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, err
			}
			if !bytes.Equal(got, raw) {
				return nil, fmt.Errorf("%w：清单与所选备份不一致", errPortableTARInvalid)
			}
			continue
		}
		offset, err := input.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		digest := sha256.New()
		if _, err := io.CopyN(digest, reader, item.Size); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, fmt.Errorf("%w：%s 内容不完整", errPortableTARInvalid, item.Name)
			}
			return nil, fmt.Errorf("读取备份材料 %s 失败：%w", item.Name, err)
		}
		if hex.EncodeToString(digest.Sum(nil)) != item.SHA256 {
			return nil, fmt.Errorf("%w，请重新选择并补传 %s", errPortableArtifactMismatch, portableBundleName)
		}
		sections[item.Name] = io.NewSectionReader(input, offset, item.Size)
	}
	if _, err := reader.Next(); err != io.EOF {
		if err == nil || errors.Is(err, tar.ErrHeader) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("%w：含额外材料或尾部损坏", errPortableTARInvalid)
		}
		return nil, err
	}
	return sections, nil
}

type portableRecord struct {
	Kind    string `json:"kind"`
	Target  string `json:"target"`
	Path    string `json:"path"`
	Package string `json:"package"`
	Member  string `json:"member"`
	Offset  int64  `json:"offset"`
	Length  int64  `json:"length"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

type portableObject struct {
	kind, path string
	file       *os.File
	hash       hash.Hash
	size       int64
}

func openPortableObject(root *os.Root, stage, kind, name string) (*portableObject, error) {
	target := name
	if kind == "sql" || name == "wp-config.php" {
		target = "source-config.php"
		if kind == "sql" {
			target = "database.sql"
		}
		f, err := os.OpenFile(filepath.Join(stage, target), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		return &portableObject{kind: kind, path: name, file: f, hash: sha256.New()}, err
	}
	f, err := root.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	return &portableObject{kind: kind, path: name, file: f, hash: sha256.New()}, err
}

type portableZIPReader struct {
	open     func(string) (*zip.Reader, io.Closer, error)
	packages []PortableArtifact
	index    int
	archive  *zip.Reader
	closer   io.Closer
	member   int
	payload  int64
}

func (z *portableZIPReader) close() {
	if z.closer != nil {
		z.closer.Close()
		z.closer = nil
	}
	z.archive = nil
}

func (z *portableZIPReader) block(record portableRecord) (*zip.File, error) {
	if z.archive != nil && z.member == len(z.archive.File) {
		z.close()
		z.index++
	}
	if z.index >= len(z.packages) || record.Package != z.packages[z.index].Name {
		return nil, errors.New("索引中的 ZIP 分包顺序或成员数量不符")
	}
	if z.archive == nil {
		archive, closer, err := z.open(record.Package)
		if err != nil {
			return nil, fmt.Errorf("ZIP 分包 %s 无法打开", record.Package)
		}
		z.archive, z.closer, z.member, z.payload = archive, closer, 0, 0
	}
	if len(z.archive.File) == 0 || len(z.archive.File) > 60000 {
		return nil, fmt.Errorf("ZIP 分包 %s 的数据块数量无效", record.Package)
	}
	member := z.archive.File[z.member]
	z.member++
	z.payload += record.Length
	if z.payload > 1024<<20 {
		return nil, errors.New("ZIP 分包原始载荷超出 1 GiB")
	}
	if member.Name != record.Member || member.Name != fmt.Sprintf("data/%05d", z.member) ||
		!member.Mode().IsRegular() || member.UncompressedSize64 != uint64(record.Length) {
		return nil, fmt.Errorf("ZIP 分包 %s 中的数据块与索引不符", record.Package)
	}
	return member, nil
}

func copyPortableBlock(obj *portableObject, member *zip.File, record portableRecord) error {
	stream, err := member.Open()
	if err != nil {
		return err
	}
	defer stream.Close()
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(obj.file, obj.hash, digest), io.LimitReader(stream, record.Length+1))
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		return fmt.Errorf("解压网站文件或数据库暂存时磁盘空间不足：%w", err)
	}
	if err != nil || n != record.Length || hex.EncodeToString(digest.Sum(nil)) != record.SHA256 {
		return fmt.Errorf("ZIP 分包 %s 中的数据块校验失败", record.Package)
	}
	obj.size += n
	return nil
}

func assemblePortableBackup(index io.Reader, openZIP func(string) (*zip.Reader, io.Closer, error), tree, stage string, m portableManifest) error {
	if err := os.Mkdir(tree, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(tree)
	if err != nil {
		return err
	}
	defer root.Close()
	zips := portableZIPReader{open: openZIP, packages: m.Packages}
	defer zips.close()
	var obj *portableObject
	defer func() {
		if obj != nil && obj.file != nil {
			obj.file.Close()
		}
	}()
	scanner := bufio.NewScanner(index)
	scanner.Buffer(make([]byte, 8192), 256<<10)
	sqlFound := false
	for scanner.Scan() {
		var record portableRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil || !portablePathValid(record.Path) {
			return errors.New("文件索引记录或路径无效")
		}
		switch record.Kind {
		case "directory", "excluded":
			if obj != nil {
				return errors.New("文件数据块缺少结束记录")
			}
			if record.Kind == "directory" {
				if err := root.Mkdir(record.Path, 0755); err != nil {
					return err
				}
			}
			continue
		case "block", "file", "sql":
		default:
			return errors.New("不支持的文件索引记录")
		}
		kind := record.Kind
		if kind == "block" {
			kind = record.Target
		}
		if (kind != "file" && kind != "sql") ||
			(kind == "sql" && (sqlFound || record.Path != "database.sql")) ||
			!portableSHA.MatchString(record.SHA256) {
			return errors.New("数据块目标或校验摘要无效")
		}
		if kind == "file" && (record.Path == "wp-content/db.php" || record.Path == "wp-content/object-cache.php" ||
			record.Path == "wp-content/advanced-cache.php" || record.Path == ".maintenance") {
			return errors.New("备份包含不支持的源数据库、缓存或维护加载文件")
		}
		if obj == nil {
			obj, err = openPortableObject(root, stage, kind, record.Path)
			if err != nil {
				return err
			}
		}
		if obj.kind != kind || obj.path != record.Path {
			return errors.New("数据块与当前文件不匹配")
		}
		if record.Kind == "block" {
			if record.Offset != obj.size || record.Length <= 0 || record.Length > portableBlockLimit {
				return errors.New("数据块偏移或大小无效")
			}
			member, err := zips.block(record)
			if err != nil {
				return err
			}
			if err := copyPortableBlock(obj, member, record); err != nil {
				return err
			}
			continue
		}
		if record.Size != obj.size || hex.EncodeToString(obj.hash.Sum(nil)) != record.SHA256 {
			return fmt.Errorf("恢复材料 %s 完整校验失败", record.Path)
		}
		if err := obj.file.Close(); err != nil {
			return err
		}
		if kind == "sql" {
			sqlFound = obj.size > 0
		}
		obj = nil
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if obj != nil || !sqlFound || zips.archive == nil || zips.index != len(zips.packages)-1 ||
		zips.member != len(zips.archive.File) {
		return errors.New("索引未完整引用数据库及全部 ZIP 数据块")
	}
	for _, name := range []string{"wp-load.php", "wp-settings.php", "index.php"} {
		if info, err := root.Stat(name); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("缺少 WordPress 根目录文件 %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(stage, "source-config.php")); err != nil {
		return errors.New("缺少源 wp-config.php 参考文件")
	}
	return nil
}
