package executor

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/naibabiji/wp-panel/config"
)

// imageBatchCandidate 是一个待处理的历史图片文件。
type imageBatchCandidate struct {
	RelativePath string // 相对 wp-content/uploads/ 的路径，用 / 分隔
	AbsPath      string
	Mime         string
	Size         int64
	ModUnix      int64
}

// scanSiteUploadsForImages 扫描站点 wp-content/uploads/ 目录，只返回真正的
// JPEG/PNG 普通文件。用 Lstat 识别并拒绝符号链接，参照
// wp_inventory_runner.go 里 validateInventorySitePath 的做法——降权执行已经把
// 影响限制在该站点系统用户可写的范围内，但如果站点被入侵、uploads 目录下被人
// 放了指向 wp-config.php 等敏感文件的符号链接，不做这层校验的话批量任务可能被
// 诱导对着这些文件原地"重编码"。
func scanSiteUploadsForImages(webRoot string) ([]imageBatchCandidate, error) {
	wwwRoot := config.AppConfig.Paths.WWWRoot
	siteRoot, err := validateInventorySitePath(wwwRoot, webRoot)
	if err != nil {
		return nil, err
	}
	uploadsRoot := filepath.Join(siteRoot, "wp-content", "uploads")
	realUploadsRoot, err := filepath.EvalSymlinks(uploadsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !pathWithin(siteRoot, realUploadsRoot, true) {
		return nil, os.ErrInvalid
	}

	var candidates []imageBatchCandidate
	err = filepath.WalkDir(realUploadsRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // 单个条目读取失败就跳过，不中断整棵树的扫描
		}
		if d.IsDir() {
			// 目录本身如果是符号链接，不进去遍历（避免逃出 uploads 树）。
			if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
				return filepath.SkipDir
			}
			return nil
		}

		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		var mime string
		switch ext {
		case ".jpg", ".jpeg":
			mime = "image/jpeg"
		case ".png":
			mime = "image/png"
		default:
			return nil
		}

		real, err := filepath.EvalSymlinks(path)
		if err != nil || !pathWithin(realUploadsRoot, real, false) {
			return nil
		}

		// 扩展名只是路由到 jpegoptim/optipng 的依据，不是"这是合法图片"的证明——
		// 伪装成 .jpg 的任意文件会被交给二进制反复尝试、计为 failed。读文件头
		// 校验 magic bytes，不合法的直接跳过，不进入候选清单。
		if !hasImageMagicBytes(real, mime) {
			return nil
		}

		rel, err := filepath.Rel(realUploadsRoot, real)
		if err != nil {
			return nil
		}
		candidates = append(candidates, imageBatchCandidate{
			RelativePath: filepath.ToSlash(rel),
			AbsPath:      real,
			Mime:         mime,
			Size:         info.Size(),
			ModUnix:      info.ModTime().Unix(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

var (
	jpegMagic = []byte{0xFF, 0xD8, 0xFF}
	pngMagic  = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
)

func hasImageMagicBytes(path, mime string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	header := make([]byte, 8)
	n, _ := io.ReadFull(f, header)
	switch mime {
	case "image/jpeg":
		return n >= len(jpegMagic) && bytes.Equal(header[:len(jpegMagic)], jpegMagic)
	case "image/png":
		return n >= len(pngMagic) && bytes.Equal(header[:len(pngMagic)], pngMagic)
	default:
		return false
	}
}
