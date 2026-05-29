package handlers

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

type FileHandler struct{}

const (
	maxZipEntries        = 10000
	maxZipSingleFileSize = int64(512 * 1024 * 1024)
	maxZipTotalSize      = int64(2 * 1024 * 1024 * 1024)
)

type fileEntry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"mod_time"`
}

func fileBasePath(siteID int) (string, error) {
	if siteID == 0 {
		return "/www/server/panel/backups", nil
	}
	site := getWebsiteByID(siteID)
	if site == nil {
		return "", fmt.Errorf("网站不存在")
	}
	return site.WebRoot, nil
}

func isPathWithin(basePath, targetPath string) bool {
	base, err := filepath.EvalSymlinks(filepath.Clean(basePath))
	if err != nil {
		return false
	}
	target, err := resolvePathForAccess(targetPath)
	if err != nil {
		return false
	}
	base = filepath.Clean(base)
	target = filepath.Clean(target)
	if target == base {
		return true
	}
	return strings.HasPrefix(target, base+string(filepath.Separator))
}

func resolvePathForAccess(path string) (string, error) {
	cleanPath := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(cleanPath); err == nil {
		return resolved, nil
	}
	// 向上逐级查找第一个存在的目录
	for p := filepath.Dir(cleanPath); ; p = filepath.Dir(p) {
		resolved, err := filepath.EvalSymlinks(p)
		if err == nil {
			rel, relErr := filepath.Rel(p, cleanPath)
			if relErr != nil {
				return "", relErr
			}
			return filepath.Join(resolved, rel), nil
		}
		if p == "/" || p == "." {
			// 根目录不存在则无法验证，退回到 Clean 结果
			return cleanPath, nil
		}
	}
}

func (h *FileHandler) List(c *gin.Context) {
	siteIDStr := c.Query("site_id")
	relPath := c.DefaultQuery("path", "/")

	siteID, err := strconv.Atoi(siteIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	basePath, err := fileBasePath(siteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	fullPath := filepath.Join(basePath, relPath)
	fullPath = filepath.Clean(fullPath)
	if !isPathWithin(basePath, fullPath) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("路径越权"))
		return
	}

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		log.Printf("读取目录失败 path=%s: %v", fullPath, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("读取目录失败"))
		return
	}

	var files []fileEntry
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{
			Name:    e.Name(),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	if files == nil {
		files = []fileEntry{}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"path":  relPath,
		"files": files,
	}))
}

func (h *FileHandler) Upload(c *gin.Context) {
	siteIDStr := c.Query("site_id")
	relPath := c.DefaultQuery("path", "/")

	siteID, err := strconv.Atoi(siteIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	basePath, err := fileBasePath(siteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("请选择文件"))
		return
	}

	destPath := filepath.Join(basePath, relPath, filepath.Base(file.Filename))
	destPath = filepath.Clean(destPath)
	if !isPathWithin(basePath, destPath) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("路径越权"))
		return
	}

	if err := c.SaveUploadedFile(file, destPath); err != nil {
		log.Printf("文件上传失败 path=%s: %v", destPath, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("上传失败"))
		return
	}
	os.Chmod(destPath, 0644)

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "文件上传成功"}))
}

func (h *FileHandler) Download(c *gin.Context) {
	siteIDStr := c.Query("site_id")
	relPath := c.Query("path")

	siteID, err := strconv.Atoi(siteIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	basePath, err := fileBasePath(siteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	fullPath := filepath.Join(basePath, relPath)
	fullPath = filepath.Clean(fullPath)
	if !isPathWithin(basePath, fullPath) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("路径越权"))
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		c.JSON(http.StatusNotFound, models.ErrorResponse("文件不存在"))
		return
	}

	c.FileAttachment(fullPath, filepath.Base(fullPath))
}

func (h *FileHandler) Delete(c *gin.Context) {
	siteIDStr := c.Query("site_id")
	relPath := c.Query("path")

	siteID, err := strconv.Atoi(siteIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	basePath, err := fileBasePath(siteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	fullPath := filepath.Join(basePath, relPath)
	fullPath = filepath.Clean(fullPath)
	if !isPathWithin(basePath, fullPath) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("路径越权"))
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("路径不存在"))
		return
	}

	if info.IsDir() {
		if err := os.RemoveAll(fullPath); err != nil {
			log.Printf("删除失败 path=%s: %v", fullPath, err)
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("删除失败"))
			return
		}
	} else {
		if err := os.Remove(fullPath); err != nil {
			log.Printf("删除失败 path=%s: %v", fullPath, err)
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("删除失败"))
			return
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "删除成功"}))
}

func (h *FileHandler) Rename(c *gin.Context) {
	var req struct {
		SiteID  int    `json:"site_id"`
		OldPath string `json:"old_path"`
		NewName string `json:"new_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	basePath, err := fileBasePath(req.SiteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	oldFull := filepath.Join(basePath, req.OldPath)
	newFull := filepath.Join(filepath.Dir(oldFull), req.NewName)

	if !isPathWithin(basePath, oldFull) ||
		!isPathWithin(basePath, newFull) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("路径越权"))
		return
	}

	if err := os.Rename(oldFull, newFull); err != nil {
		log.Printf("重命名失败 old=%s new=%s: %v", oldFull, newFull, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("重命名失败"))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "重命名成功"}))
}

func (h *FileHandler) Permissions(c *gin.Context) {
	siteIDStr := c.Query("site_id")
	relPath := c.Query("path")

	siteID, err := strconv.Atoi(siteIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	basePath, err := fileBasePath(siteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	fullPath := filepath.Join(basePath, relPath)
	fullPath = filepath.Clean(fullPath)
	if !isPathWithin(basePath, fullPath) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("路径越权"))
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("路径不存在"))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"path":        relPath,
		"permissions": info.Mode().String(),
		"size":        info.Size(),
		"mod_time":    info.ModTime().Format("2006-01-02 15:04:05"),
		"is_dir":      info.IsDir(),
	}))
}

func (h *FileHandler) BatchCompress(c *gin.Context) {
	var req struct {
		SiteID      int      `json:"site_id"`
		Path        string   `json:"path"`
		Names       []string `json:"names"`
		ArchiveName string   `json:"archive_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Names) == 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("请选择要压缩的文件或目录"))
		return
	}

	basePath, err := fileBasePath(req.SiteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	workPath := filepath.Join(basePath, req.Path)
	workPath = filepath.Clean(workPath)
	if !isPathWithin(basePath, workPath) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("路径越权"))
		return
	}

	archiveName := strings.TrimSpace(req.ArchiveName)
	if archiveName == "" {
		archiveName = fmt.Sprintf("archive_%s.zip", time.Now().Format("20060102_150405"))
	}
	if !strings.HasSuffix(strings.ToLower(archiveName), ".zip") {
		archiveName += ".zip"
	}

	zipPath := filepath.Join(workPath, archiveName)
	if !isPathWithin(basePath, zipPath) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("压缩文件名非法"))
		return
	}
	zipFile, err := os.Create(zipPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("创建压缩文件失败"))
		return
	}
	defer zipFile.Close()

	w := zip.NewWriter(zipFile)
	defer w.Close()

	for _, name := range req.Names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		fullPath := filepath.Join(workPath, filepath.Clean(name))
		if !isPathWithin(basePath, fullPath) {
			continue
		}
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		if info.IsDir() {
			filepath.Walk(fullPath, func(path string, fi os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if !isPathWithin(basePath, path) {
					return nil
				}
				rel, _ := filepath.Rel(basePath, path)
				rel = filepath.ToSlash(rel)
				header, err := zip.FileInfoHeader(fi)
				if err != nil {
					return nil
				}
				header.Name = rel
				header.Method = zip.Deflate
				if fi.IsDir() {
					header.Name += "/"
					w.CreateHeader(header)
					return nil
				}
				writer, err := w.CreateHeader(header)
				if err != nil {
					return nil
				}
				f, err := os.Open(path)
				if err != nil {
					return nil
				}
				defer f.Close()
				io.Copy(writer, f)
				return nil
			})
		} else {
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				continue
			}
			header.Name = info.Name()
			header.Method = zip.Deflate
			writer, err := w.CreateHeader(header)
			if err != nil {
				continue
			}
			f, err := os.Open(fullPath)
			if err != nil {
				continue
			}
			defer f.Close()
			io.Copy(writer, f)
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": fmt.Sprintf("已压缩为 %s", archiveName)}))
}

func (h *FileHandler) Compress(c *gin.Context) {
	siteIDStr := c.Query("site_id")
	relPath := c.Query("path")

	siteID, err := strconv.Atoi(siteIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	basePath, err := fileBasePath(siteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	fullPath := filepath.Join(basePath, relPath)
	fullPath = filepath.Clean(fullPath)
	if !isPathWithin(basePath, fullPath) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("路径越权"))
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("路径不存在"))
		return
	}

	zipName := info.Name() + ".zip"
	zipPath := filepath.Join(filepath.Dir(fullPath), zipName)
	zipFile, err := os.Create(zipPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("创建压缩文件失败"))
		return
	}
	defer zipFile.Close()

	w := zip.NewWriter(zipFile)
	defer w.Close()

	baseDir := filepath.Dir(fullPath)

	if info.IsDir() {
		filepath.Walk(fullPath, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if !isPathWithin(basePath, path) {
				return nil
			}
			rel, _ := filepath.Rel(baseDir, path)
			rel = filepath.ToSlash(rel)

			header, err := zip.FileInfoHeader(fi)
			if err != nil {
				return nil
			}
			header.Name = rel
			header.Method = zip.Deflate

			if fi.IsDir() {
				header.Name += "/"
				w.CreateHeader(header)
				return nil
			}

			writer, err := w.CreateHeader(header)
			if err != nil {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()
			io.Copy(writer, f)
			return nil
		})
	} else {
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("压缩失败"))
			return
		}
		header.Name = info.Name()
		header.Method = zip.Deflate

		writer, err := w.CreateHeader(header)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("压缩失败"))
			return
		}
		f, err := os.Open(fullPath)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("压缩失败"))
			return
		}
		defer f.Close()
		io.Copy(writer, f)
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": fmt.Sprintf("已压缩为 %s", zipName)}))
}

func (h *FileHandler) Decompress(c *gin.Context) {
	siteIDStr := c.Query("site_id")
	relPath := c.Query("path")

	siteID, err := strconv.Atoi(siteIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	basePath, err := fileBasePath(siteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	fullPath := filepath.Join(basePath, relPath)
	fullPath = filepath.Clean(fullPath)
	if !isPathWithin(basePath, fullPath) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("路径越权"))
		return
	}

	if !strings.HasSuffix(strings.ToLower(fullPath), ".zip") {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("仅支持解压 .zip 文件"))
		return
	}

	r, err := zip.OpenReader(fullPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("打开压缩文件失败"))
		return
	}
	defer r.Close()

	destDir := filepath.Dir(fullPath)
	overwrite := c.Query("overwrite") == "1"
	var conflicts []string
	if len(r.File) > maxZipEntries {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("压缩包文件数量过多"))
		return
	}
	var declaredTotal uint64
	var actualTotal int64
	for _, f := range r.File {
		target := filepath.Join(destDir, filepath.FromSlash(f.Name))
		target = filepath.Clean(target)
		if !isPathWithin(basePath, target) {
			c.JSON(http.StatusForbidden, models.ErrorResponse("压缩包包含非法路径: "+f.Name))
			return
		}
		if !f.FileInfo().IsDir() {
			if f.UncompressedSize64 > uint64(maxZipSingleFileSize) {
				c.JSON(http.StatusBadRequest, models.ErrorResponse("压缩包内单个文件过大: "+f.Name))
				return
			}
			if declaredTotal > uint64(maxZipTotalSize)-f.UncompressedSize64 {
				c.JSON(http.StatusBadRequest, models.ErrorResponse("压缩包解压后总体积过大"))
				return
			}
			declaredTotal += f.UncompressedSize64
			if !overwrite {
				if _, err := os.Stat(target); err == nil {
					conflicts = append(conflicts, f.Name)
				}
			}
		}
	}
	if len(conflicts) > 0 {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "以下文件已存在，确认覆盖？", "conflicts": conflicts})
		return
	}

	for _, f := range r.File {
		target := filepath.Join(destDir, filepath.FromSlash(f.Name))
		target = filepath.Clean(target)
		if !isPathWithin(basePath, target) {
			c.JSON(http.StatusForbidden, models.ErrorResponse("压缩包包含非法路径: "+f.Name))
			return
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				c.JSON(http.StatusInternalServerError, models.ErrorResponse("创建目录失败: "+f.Name))
				return
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("创建目录失败: "+f.Name))
			return
		}
		src, err := f.Open()
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("读取压缩包文件失败: "+f.Name))
			return
		}
		dst, err := os.Create(target)
		if err != nil {
			src.Close()
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("创建文件失败: "+f.Name))
			return
		}
		written, copyErr := io.Copy(dst, io.LimitReader(src, maxZipSingleFileSize+1))
		src.Close()
		closeErr := dst.Close()
		if copyErr != nil {
			os.Remove(target)
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("写入文件失败: "+f.Name))
			return
		}
		if closeErr != nil {
			os.Remove(target)
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存文件失败: "+f.Name))
			return
		}
		if written > maxZipSingleFileSize {
			os.Remove(target)
			c.JSON(http.StatusBadRequest, models.ErrorResponse("压缩包内单个文件过大: "+f.Name))
			return
		}
		actualTotal += written
		if actualTotal > maxZipTotalSize {
			os.Remove(target)
			c.JSON(http.StatusBadRequest, models.ErrorResponse("压缩包解压后总体积过大"))
			return
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "解压完成"}))
}

func (h *FileHandler) Move(c *gin.Context) {
	var req struct {
		SiteID   int      `json:"site_id"`
		SrcPath  string   `json:"src_path"`
		Names    []string `json:"names"`
		DestPath string   `json:"dest_path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Names) == 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	basePath, err := fileBasePath(req.SiteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	srcDir := filepath.Join(basePath, req.SrcPath)
	destDir := filepath.Join(basePath, req.DestPath)
	srcDir = filepath.Clean(srcDir)
	destDir = filepath.Clean(destDir)

	if !isPathWithin(basePath, srcDir) || !isPathWithin(basePath, destDir) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("路径越权"))
		return
	}

	for _, name := range req.Names {
		src := filepath.Join(srcDir, filepath.Clean(name))
		dest := filepath.Join(destDir, filepath.Clean(name))
		if !isPathWithin(basePath, src) || !isPathWithin(basePath, dest) {
			continue
		}
		os.Rename(src, dest)
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": fmt.Sprintf("已移动 %d 个项目", len(req.Names))}))
}

func (h *FileHandler) Copy(c *gin.Context) {
	var req struct {
		SiteID   int      `json:"site_id"`
		SrcPath  string   `json:"src_path"`
		Names    []string `json:"names"`
		DestPath string   `json:"dest_path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Names) == 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	basePath, err := fileBasePath(req.SiteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	srcDir := filepath.Join(basePath, req.SrcPath)
	destDir := filepath.Join(basePath, req.DestPath)
	srcDir = filepath.Clean(srcDir)
	destDir = filepath.Clean(destDir)

	if !isPathWithin(basePath, srcDir) || !isPathWithin(basePath, destDir) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("路径越权"))
		return
	}

	for _, name := range req.Names {
		src := filepath.Join(srcDir, filepath.Clean(name))
		dest := filepath.Join(destDir, filepath.Clean(name))
		if !isPathWithin(basePath, src) || !isPathWithin(basePath, dest) {
			continue
		}
		copyFileOrDir(basePath, src, dest)
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": fmt.Sprintf("已复制 %d 个项目", len(req.Names))}))
}

func copyFileOrDir(basePath, src, dest string) error {
	if !isPathWithin(basePath, src) || !isPathWithin(basePath, dest) {
		return fmt.Errorf("path outside base")
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		os.MkdirAll(dest, info.Mode())
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			copyFileOrDir(basePath, filepath.Join(src, e.Name()), filepath.Join(dest, e.Name()))
		}
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func (h *FileHandler) CreateDir(c *gin.Context) {
	siteIDStr := c.Query("site_id")
	relPath := c.DefaultQuery("path", "/")

	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("请输入目录名"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if strings.ContainsAny(name, "/\\") {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("目录名不能包含路径分隔符"))
		return
	}

	siteID, err := strconv.Atoi(siteIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	basePath, err := fileBasePath(siteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	fullPath := filepath.Join(basePath, relPath, name)
	fullPath = filepath.Clean(fullPath)
	if !isPathWithin(basePath, fullPath) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("路径越权"))
		return
	}

	if err := os.MkdirAll(fullPath, 0755); err != nil {
		log.Printf("创建目录失败 path=%s: %v", fullPath, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("创建目录失败"))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "目录创建成功"}))
}

func (h *FileHandler) FixPermissions(c *gin.Context) {
	siteIDStr := c.Query("site_id")
	siteID, err := strconv.Atoi(siteIDStr)
	if err != nil || siteID == 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	site := getWebsiteByID(siteID)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	webRoot := site.WebRoot
	var dirCount, fileCount int
	err = filepath.Walk(webRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !isPathWithin(webRoot, path) {
			return nil
		}
		if info.IsDir() {
			os.Chmod(path, 0755)
			dirCount++
		} else {
			os.Chmod(path, 0644)
			fileCount++
		}
		return nil
	})
	if err != nil {
		log.Printf("权限修复失败 root=%s: %v", webRoot, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("权限修复失败"))
		return
	}

	if err := executor.HardenSiteSensitivePermissions(site.Domain, webRoot, site.SystemUser); err != nil {
		log.Printf("安全权限修复失败 root=%s: %v", webRoot, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("安全权限修复失败"))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"message":    fmt.Sprintf("权限修复完成，目录 %d 个，文件 %d 个", dirCount, fileCount),
		"dir_count":  dirCount,
		"file_count": fileCount,
	}))
}
