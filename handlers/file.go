package handlers

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

type FileHandler struct{}

const (
	maxArchiveEntries      = 100000
	maxPanelArchiveBytes   = int64(5 * 1024 * 1024 * 1024)
	uploadChunkSize        = int64(5 * 1024 * 1024)
	maxUploadChunks        = 20000
	uploadSessionDirPrefix = "wppanel-upload-"
	uploadSessionTTL       = 24 * time.Hour
)

type multiCloser []io.Closer

func (m multiCloser) Close() error {
	var firstErr error
	for i := len(m) - 1; i >= 0; i-- {
		if err := m[i].Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type uploadSession struct {
	Filename     string `json:"filename"`
	FileSize     int64  `json:"file_size"`
	TotalChunks  int    `json:"total_chunks"`
	SiteID       int    `json:"site_id"`
	Path         string `json:"path"`
	LastModified int64  `json:"last_modified"`
	CreatedAt    int64  `json:"created_at"`
}

type fileEntry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"mod_time"`
}

type fileTransferRequest struct {
	SiteID int `json:"site_id"`
	// DestSiteID is optional to keep existing same-site copy/move requests compatible.
	DestSiteID *int     `json:"dest_site_id"`
	SrcPath    string   `json:"src_path"`
	Names      []string `json:"names"`
	DestPath   string   `json:"dest_path"`
}

type fileTransferItem struct {
	name string
	src  string
	dest string
}

type fileTransferError struct {
	status  int
	message string
}

func (e *fileTransferError) Error() string {
	return e.message
}

func newFileTransferError(status int, format string, args ...interface{}) error {
	return &fileTransferError{status: status, message: fmt.Sprintf(format, args...)}
}

func fileTransferHTTPStatus(err error) int {
	var transferErr *fileTransferError
	if errors.As(err, &transferErr) {
		return transferErr.status
	}
	return http.StatusInternalServerError
}

const (
	defaultFilePageSize = 50
	maxFilePageSize     = 200
)

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
		if runtime.GOOS != "windows" {
			return false
		}
		base, err = filepath.Abs(filepath.Clean(basePath))
		if err != nil {
			return false
		}
	}
	target, err := resolvePathForAccess(targetPath)
	if err != nil {
		return false
	}
	base = filepath.Clean(base)
	target = filepath.Clean(target)
	if runtime.GOOS == "windows" {
		base = strings.ToLower(base)
		target = strings.ToLower(target)
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func isSamePath(basePath, targetPath string) bool {
	base, err := filepath.EvalSymlinks(filepath.Clean(basePath))
	if err != nil {
		if runtime.GOOS != "windows" {
			return false
		}
		base, err = filepath.Abs(filepath.Clean(basePath))
		if err != nil {
			return false
		}
	}
	target, err := resolvePathForAccess(targetPath)
	if err != nil {
		return false
	}
	base = filepath.Clean(base)
	target = filepath.Clean(target)
	if runtime.GOOS == "windows" {
		base = strings.ToLower(base)
		target = strings.ToLower(target)
	}
	return base == target
}

func normalizeFilePage(page, perPage int) (int, int) {
	if page < 1 {
		page = 1
	}
	if perPage <= 0 {
		perPage = defaultFilePageSize
	}
	if perPage > maxFilePageSize {
		perPage = maxFilePageSize
	}
	return page, perPage
}

func sortFileEntries(files []fileEntry, sortBy, sortDir string) {
	if sortDir != "desc" {
		sortDir = "asc"
	}
	switch sortBy {
	case "type", "size", "time":
	default:
		sortBy = "name"
	}
	dir := 1
	if sortDir == "desc" {
		dir = -1
	}
	sort.SliceStable(files, func(i, j int) bool {
		a := files[i]
		b := files[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		cmp := 0
		switch sortBy {
		case "type":
			cmp = strings.Compare(fileEntryType(a), fileEntryType(b))
		case "size":
			if a.Size < b.Size {
				cmp = -1
			} else if a.Size > b.Size {
				cmp = 1
			}
		case "time":
			cmp = strings.Compare(a.ModTime, b.ModTime)
		default:
			cmp = strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		if cmp == 0 {
			cmp = strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		return dir*cmp < 0
	})
}

func fileEntryType(f fileEntry) string {
	if f.IsDir {
		return ""
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(f.Name)), ".")
	if ext == "" {
		return f.Name
	}
	return ext
}

func paginateFileEntries(files []fileEntry, page, perPage int) ([]fileEntry, int, int) {
	page, perPage = normalizeFilePage(page, perPage)
	total := len(files)
	totalPages := (total + perPage - 1) / perPage
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * perPage
	end := start + perPage
	if end > total {
		end = total
	}
	if start >= total {
		return []fileEntry{}, page, totalPages
	}
	return files[start:end], page, totalPages
}

func cleanFileOperationName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) || strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("文件名非法")
	}
	return name, nil
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
		parent := filepath.Dir(p)
		if p == "/" || p == "." || parent == p {
			// 根目录不存在则无法验证，退回到 Clean 结果
			return cleanPath, nil
		}
	}
}

func uploadSessionRoot() string {
	if config.AppConfig != nil {
		if config.AppConfig.Panel.DataDir != "" {
			return filepath.Join(config.AppConfig.Panel.DataDir, "upload-sessions")
		}
		if config.AppConfig.Panel.BackupDir != "" {
			return filepath.Join(config.AppConfig.Panel.BackupDir, "upload-sessions")
		}
	}
	return filepath.Join(os.TempDir(), "wppanel-upload-sessions")
}

func uploadSessionDir(uploadID string) string {
	return filepath.Join(uploadSessionRoot(), uploadSessionDirPrefix+filepath.Base(uploadID))
}

func uploadSessionMetaPath(dir string) string {
	return filepath.Join(dir, "session.json")
}

func cleanupExpiredUploadSessions(root string, ttl time.Duration) {
	if root == "" || ttl <= 0 {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("扫描上传会话目录失败 root=%s: %v", root, err)
		}
		return
	}

	now := time.Now()
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), uploadSessionDirPrefix) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		lastActive := info.ModTime()
		if session, err := loadUploadSession(dir); err == nil && session.CreatedAt > 0 {
			createdAt := time.Unix(session.CreatedAt, 0)
			if createdAt.After(lastActive) {
				lastActive = createdAt
			}
		}
		if now.Sub(lastActive) <= ttl {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("清理过期上传会话失败 dir=%s: %v", dir, err)
		}
	}
}

func cleanupUploadSessions() {
	cleanupExpiredUploadSessions(uploadSessionRoot(), uploadSessionTTL)
	legacyRoot := os.TempDir()
	if legacyRoot != uploadSessionRoot() {
		cleanupExpiredUploadSessions(legacyRoot, uploadSessionTTL)
	}
}

func uploadSaveErrorMessage(action string, err error) string {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no space left on device") {
		return action + "失败：上传暂存空间不足，请清理磁盘后重试"
	}
	return action + "失败"
}

func sanitizeUploadFilename(filename string) string {
	name := filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	if name == "." || name == "/" || name == "\\" {
		return ""
	}
	return name
}

func expectedUploadChunks(fileSize int64) int {
	if fileSize == 0 {
		return 0
	}
	return int((fileSize + uploadChunkSize - 1) / uploadChunkSize)
}

func makeUploadID(s uploadSession) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s\x00%d\x00%d\x00%d",
		s.SiteID, filepath.Clean(s.Path), s.Filename, s.FileSize, s.TotalChunks, s.LastModified,
	)))
	return hex.EncodeToString(sum[:16])
}

func saveUploadSession(dir string, s uploadSession) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(uploadSessionMetaPath(dir), data, 0600)
}

func loadUploadSession(dir string) (uploadSession, error) {
	var s uploadSession
	data, err := os.ReadFile(uploadSessionMetaPath(dir))
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(data, &s)
	return s, err
}

func completedUploadChunks(dir string, totalChunks int) []int {
	completed := make([]int, 0)
	for i := 0; i < totalChunks; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("chunk-%d", i))); err == nil {
			completed = append(completed, i)
		}
	}
	return completed
}

func missingUploadChunks(dir string, totalChunks int) []int {
	missing := make([]int, 0)
	for i := 0; i < totalChunks; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("chunk-%d", i))); err != nil {
			missing = append(missing, i)
		}
	}
	return missing
}

func (h *FileHandler) List(c *gin.Context) {
	siteIDStr := c.Query("site_id")
	relPath := c.DefaultQuery("path", "/")
	hasPagingParams := c.Query("page") != "" || c.Query("per_page") != "" || c.Query("sort_by") != "" || c.Query("sort_dir") != ""
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", strconv.Itoa(defaultFilePageSize)))
	page, perPage = normalizeFilePage(page, perPage)
	sortBy := c.DefaultQuery("sort_by", "name")
	sortDir := c.DefaultQuery("sort_dir", "asc")

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
	total := len(files)
	sortFileEntries(files, sortBy, sortDir)
	pageFiles := files
	totalPages := 1
	if hasPagingParams {
		pageFiles, page, totalPages = paginateFileEntries(files, page, perPage)
	} else {
		perPage = total
		if perPage == 0 {
			perPage = defaultFilePageSize
		}
	}
	if totalPages < 1 {
		totalPages = 1
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"path":        relPath,
		"files":       pageFiles,
		"total":       total,
		"page":        page,
		"per_page":    perPage,
		"total_pages": totalPages,
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

func (h *FileHandler) UploadInit(c *gin.Context) {
	var req struct {
		Filename     string `json:"filename"`
		FileSize     int64  `json:"file_size"`
		TotalChunks  int    `json:"total_chunks"`
		SiteID       *int   `json:"site_id"`
		Path         string `json:"path"`
		LastModified int64  `json:"last_modified"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数无效"))
		return
	}
	if req.SiteID == nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("请选择网站或备份目录"))
		return
	}
	siteID := *req.SiteID
	filename := sanitizeUploadFilename(req.Filename)
	expectedChunks := expectedUploadChunks(req.FileSize)
	if filename == "" || req.FileSize < 0 || req.TotalChunks != expectedChunks || req.TotalChunks > maxUploadChunks {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数无效"))
		return
	}
	if req.Path == "" {
		req.Path = "/"
	}

	basePath, err := fileBasePath(siteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}
	destPath := filepath.Join(basePath, req.Path, filename)
	destPath = filepath.Clean(destPath)
	if !isPathWithin(basePath, destPath) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("路径越权"))
		return
	}

	cleanupUploadSessions()

	session := uploadSession{
		Filename:     filename,
		FileSize:     req.FileSize,
		TotalChunks:  req.TotalChunks,
		SiteID:       siteID,
		Path:         req.Path,
		LastModified: req.LastModified,
		CreatedAt:    time.Now().Unix(),
	}
	uploadID := makeUploadID(session)
	dir := uploadSessionDir(uploadID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Printf("创建上传会话失败 dir=%s: %v", dir, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(uploadSaveErrorMessage("创建上传会话", err)))
		return
	}
	if existing, err := loadUploadSession(dir); err == nil {
		session.CreatedAt = existing.CreatedAt
	}
	if err := saveUploadSession(dir, session); err != nil {
		log.Printf("保存上传会话失败 dir=%s: %v", dir, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(uploadSaveErrorMessage("保存上传会话", err)))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"upload_id":        uploadID,
		"completed_chunks": completedUploadChunks(dir, req.TotalChunks),
	}))
}

func (h *FileHandler) UploadChunk(c *gin.Context) {
	uploadID := c.PostForm("upload_id")
	chunkIdxStr := c.PostForm("chunk_index")
	if uploadID == "" || chunkIdxStr == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数无效"))
		return
	}

	chunkIdx, err := strconv.Atoi(chunkIdxStr)
	if err != nil || chunkIdx < 0 || chunkIdx >= maxUploadChunks {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("分片索引无效"))
		return
	}

	dir := uploadSessionDir(uploadID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, models.ErrorResponse("上传会话不存在"))
		return
	}
	session, err := loadUploadSession(dir)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("上传会话无效"))
		return
	}
	if chunkIdx >= session.TotalChunks {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("分片索引无效"))
		return
	}

	file, err := c.FormFile("chunk")
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("请选择文件"))
		return
	}
	expectedSize := uploadChunkSize
	if chunkIdx == session.TotalChunks-1 {
		expectedSize = session.FileSize - int64(chunkIdx)*uploadChunkSize
	}
	if file.Size != expectedSize {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("分片大小无效"))
		return
	}

	chunkPath := filepath.Join(dir, fmt.Sprintf("chunk-%d", chunkIdx))
	tmpPath := chunkPath + ".tmp"
	if err := c.SaveUploadedFile(file, tmpPath); err != nil {
		log.Printf("保存上传分片失败 path=%s: %v", tmpPath, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(uploadSaveErrorMessage("保存分片", err)))
		return
	}
	os.Remove(chunkPath)
	if err := os.Rename(tmpPath, chunkPath); err != nil {
		os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存分片失败"))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"chunk_index": chunkIdx}))
}

func (h *FileHandler) UploadComplete(c *gin.Context) {
	var req struct {
		UploadID string `json:"upload_id"`
		SiteID   *int   `json:"site_id"`
		Path     string `json:"path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.UploadID == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数无效"))
		return
	}
	if req.SiteID == nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("请选择网站或备份目录"))
		return
	}

	uploadID := filepath.Base(req.UploadID)
	dir := uploadSessionDir(uploadID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, models.ErrorResponse("上传会话不存在"))
		return
	}
	session, err := loadUploadSession(dir)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("上传会话无效"))
		return
	}
	if *req.SiteID != session.SiteID || filepath.Clean(req.Path) != filepath.Clean(session.Path) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("上传会话不匹配"))
		return
	}

	basePath, err := fileBasePath(session.SiteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	destPath := filepath.Join(basePath, session.Path, session.Filename)
	destPath = filepath.Clean(destPath)
	if !isPathWithin(basePath, destPath) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("路径越权"))
		return
	}

	if missing := missingUploadChunks(dir, session.TotalChunks); len(missing) > 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(fmt.Sprintf("分片 %d 缺失，请重新上传", missing[0])))
		return
	}

	tmpDestPath := destPath + ".uploading-" + uploadID
	dst, err := os.OpenFile(tmpDestPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(uploadSaveErrorMessage("创建文件", err)))
		return
	}
	copyOK := false
	defer func() {
		dst.Close()
		if !copyOK {
			os.Remove(tmpDestPath)
		}
	}()

	for i := 0; i < session.TotalChunks; i++ {
		chunkPath := filepath.Join(dir, fmt.Sprintf("chunk-%d", i))
		src, err := os.Open(chunkPath)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse(fmt.Sprintf("分片 %d 缺失，请重新上传", i)))
			return
		}
		if _, err := io.Copy(dst, src); err != nil {
			src.Close()
			log.Printf("合并上传分片失败 dest=%s chunk=%s: %v", tmpDestPath, chunkPath, err)
			c.JSON(http.StatusInternalServerError, models.ErrorResponse(uploadSaveErrorMessage("合并分片", err)))
			return
		}
		src.Close()
	}
	if err := dst.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存文件失败"))
		return
	}

	if err := os.Chmod(tmpDestPath, 0644); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("设置文件权限失败"))
		return
	}
	if err := os.Rename(tmpDestPath, destPath); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存文件失败"))
		return
	}
	copyOK = true
	os.RemoveAll(dir)
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "上传完成"}))
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
	if isSamePath(basePath, fullPath) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("不能删除根目录"))
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

func archiveFormat(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return "zip"
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "tar.gz"
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"):
		return "tar.bz2"
	case strings.HasSuffix(lower, ".tar"):
		return "tar"
	default:
		return ""
	}
}

func supportedArchiveMessage() string {
	return "仅支持解压 .zip / .tar / .tar.gz / .tgz / .tar.bz2 / .tbz2 文件"
}

func shellQuoteForDisplay(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func archiveSSHExtractCommand(archivePath, format string) string {
	dir := filepath.ToSlash(filepath.Dir(archivePath))
	name := filepath.ToSlash(filepath.Base(archivePath))
	switch format {
	case "zip":
		return fmt.Sprintf("cd %s && unzip -o %s", shellQuoteForDisplay(dir), shellQuoteForDisplay(name))
	case "tar":
		return fmt.Sprintf("cd %s && tar xvf %s", shellQuoteForDisplay(dir), shellQuoteForDisplay(name))
	case "tar.gz":
		return fmt.Sprintf("cd %s && tar zxvf %s", shellQuoteForDisplay(dir), shellQuoteForDisplay(name))
	case "tar.bz2":
		return fmt.Sprintf("cd %s && tar jxvf %s", shellQuoteForDisplay(dir), shellQuoteForDisplay(name))
	default:
		return ""
	}
}

func oversizedArchiveMessage(archivePath, format string) string {
	cmd := archiveSSHExtractCommand(archivePath, format)
	if cmd == "" {
		return "文件超大，面板解压可能不稳定，建议从 SSH 端执行解压命令。"
	}
	return "文件超大，面板解压可能不稳定，建议从 SSH 端执行解压命令：\n" + cmd
}

func openTarReader(path, format string) (*tar.Reader, io.Closer, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}

	switch format {
	case "tar":
		return tar.NewReader(file), multiCloser{file}, nil
	case "tar.gz":
		gz, err := gzip.NewReader(file)
		if err != nil {
			file.Close()
			return nil, nil, err
		}
		return tar.NewReader(gz), multiCloser{file, gz}, nil
	case "tar.bz2":
		return tar.NewReader(bzip2.NewReader(file)), multiCloser{file}, nil
	default:
		file.Close()
		return nil, nil, fmt.Errorf("unsupported archive format")
	}
}

func tarTargetForHeader(basePath, destDir string, hdr *tar.Header) (string, bool, error) {
	switch hdr.Typeflag {
	case tar.TypeDir, tar.TypeReg, tar.TypeRegA:
	case tar.TypeXHeader, tar.TypeXGlobalHeader, tar.TypeGNULongName, tar.TypeGNULongLink:
		return "", true, nil
	default:
		return "", false, fmt.Errorf("压缩包包含不支持的条目: %s", hdr.Name)
	}

	if hdr.Name == "" {
		return "", true, nil
	}

	target := filepath.Join(destDir, filepath.FromSlash(hdr.Name))
	target = filepath.Clean(target)
	if !isPathWithin(basePath, target) {
		return "", false, fmt.Errorf("压缩包包含非法路径: %s", hdr.Name)
	}
	return target, false, nil
}

func checkTarArchive(archivePath, format, basePath, destDir string, overwrite bool) ([]string, error) {
	tr, closer, err := openTarReader(archivePath, format)
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var conflicts []string
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		count++
		if count > maxArchiveEntries {
			return nil, fmt.Errorf("压缩包文件数量过多")
		}

		target, skip, err := tarTargetForHeader(basePath, destDir, hdr)
		if err != nil {
			return nil, err
		}
		if skip || hdr.Typeflag == tar.TypeDir || overwrite {
			continue
		}
		if _, err := os.Stat(target); err == nil {
			conflicts = append(conflicts, hdr.Name)
		}
	}
	return conflicts, nil
}

func extractTarArchive(archivePath, format, basePath, destDir string) error {
	tr, closer, err := openTarReader(archivePath, format)
	if err != nil {
		return err
	}
	defer closer.Close()

	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		count++
		if count > maxArchiveEntries {
			return fmt.Errorf("压缩包文件数量过多")
		}

		target, skip, err := tarTargetForHeader(basePath, destDir, hdr)
		if err != nil {
			return err
		}
		if skip {
			continue
		}

		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("创建目录失败: %s", hdr.Name)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return fmt.Errorf("创建目录失败: %s", hdr.Name)
		}
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return fmt.Errorf("创建文件失败: %s", hdr.Name)
		}
		_, copyErr := io.Copy(dst, tr)
		closeErr := dst.Close()
		if copyErr != nil {
			os.Remove(target)
			return fmt.Errorf("写入文件失败: %s", hdr.Name)
		}
		if closeErr != nil {
			os.Remove(target)
			return fmt.Errorf("保存文件失败: %s", hdr.Name)
		}
	}
	return nil
}

func zipTargetForFile(basePath, destDir string, f *zip.File) (string, bool, error) {
	if f.Name == "" {
		return "", true, nil
	}
	info := f.FileInfo()
	if !info.IsDir() && info.Mode().Type() != 0 {
		return "", false, fmt.Errorf("压缩包包含不支持的条目: %s", f.Name)
	}
	target := filepath.Join(destDir, filepath.FromSlash(f.Name))
	target = filepath.Clean(target)
	if !isPathWithin(basePath, target) {
		return "", false, fmt.Errorf("压缩包包含非法路径: %s", f.Name)
	}
	return target, false, nil
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

	format := archiveFormat(fullPath)
	if format == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(supportedArchiveMessage()))
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("压缩文件不存在"))
		return
	}
	if info.Size() > maxPanelArchiveBytes {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(oversizedArchiveMessage(fullPath, format)))
		return
	}

	destDir := filepath.Dir(fullPath)
	overwrite := c.Query("overwrite") == "1"

	if format != "zip" {
		conflicts, err := checkTarArchive(fullPath, format, basePath, destDir, overwrite)
		if err != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResponse(err.Error()))
			return
		}
		if len(conflicts) > 0 {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "以下文件已存在，确认覆盖？", "conflicts": conflicts})
			return
		}
		if err := extractTarArchive(fullPath, format, basePath, destDir); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse(err.Error()))
			return
		}
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "解压完成"}))
		return
	}

	r, err := zip.OpenReader(fullPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("打开压缩文件失败"))
		return
	}
	defer r.Close()

	var conflicts []string
	if len(r.File) > maxArchiveEntries {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("压缩包文件数量过多"))
		return
	}
	for _, f := range r.File {
		target, skip, err := zipTargetForFile(basePath, destDir, f)
		if err != nil {
			c.JSON(http.StatusForbidden, models.ErrorResponse(err.Error()))
			return
		}
		if skip {
			continue
		}
		if !f.FileInfo().IsDir() && !overwrite {
			if _, err := os.Stat(target); err == nil {
				conflicts = append(conflicts, f.Name)
			}
		}
	}
	if len(conflicts) > 0 {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "以下文件已存在，确认覆盖？", "conflicts": conflicts})
		return
	}

	for _, f := range r.File {
		target, skip, err := zipTargetForFile(basePath, destDir, f)
		if err != nil {
			c.JSON(http.StatusForbidden, models.ErrorResponse(err.Error()))
			return
		}
		if skip {
			continue
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
		_, copyErr := io.Copy(dst, src)
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
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "解压完成"}))
}

func resolveTransferRequest(req fileTransferRequest) (int, string, string, string, string, []fileTransferItem, error) {
	destSiteID := req.SiteID
	if req.DestSiteID != nil {
		destSiteID = *req.DestSiteID
	}
	if req.SiteID != destSiteID && (req.SiteID == 0 || destSiteID == 0) {
		return 0, "", "", "", "", nil, newFileTransferError(http.StatusBadRequest, "跨站复制/剪切仅支持网站目录")
	}

	srcBase, err := fileBasePath(req.SiteID)
	if err != nil {
		return 0, "", "", "", "", nil, newFileTransferError(http.StatusNotFound, "源网站不存在")
	}
	destBase, err := fileBasePath(destSiteID)
	if err != nil {
		return 0, "", "", "", "", nil, newFileTransferError(http.StatusNotFound, "目标网站不存在")
	}

	srcDir := filepath.Clean(filepath.Join(srcBase, req.SrcPath))
	destDir := filepath.Clean(filepath.Join(destBase, req.DestPath))
	if !isPathWithin(srcBase, srcDir) || !isPathWithin(destBase, destDir) {
		return 0, "", "", "", "", nil, newFileTransferError(http.StatusForbidden, "路径越权")
	}

	items := make([]fileTransferItem, 0, len(req.Names))
	for _, name := range req.Names {
		cleanName, err := cleanFileOperationName(name)
		if err != nil {
			return 0, "", "", "", "", nil, newFileTransferError(http.StatusBadRequest, "%s", err.Error())
		}
		src := filepath.Clean(filepath.Join(srcDir, cleanName))
		dest := filepath.Clean(filepath.Join(destDir, cleanName))
		if !isPathWithin(srcBase, src) || !isPathWithin(destBase, dest) {
			return 0, "", "", "", "", nil, newFileTransferError(http.StatusForbidden, "路径越权")
		}
		if isSamePath(src, dest) {
			return 0, "", "", "", "", nil, newFileTransferError(http.StatusBadRequest, "目标已存在: %s", cleanName)
		}
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				return 0, "", "", "", "", nil, newFileTransferError(http.StatusNotFound, "源文件不存在: %s", cleanName)
			}
			return 0, "", "", "", "", nil, err
		}
		if _, err := os.Stat(dest); err == nil {
			return 0, "", "", "", "", nil, newFileTransferError(http.StatusBadRequest, "目标已存在: %s", cleanName)
		} else if !os.IsNotExist(err) {
			return 0, "", "", "", "", nil, err
		}
		items = append(items, fileTransferItem{name: cleanName, src: src, dest: dest})
	}
	return destSiteID, srcBase, destBase, srcDir, destDir, items, nil
}

func chownTransferredPath(destSiteID int, dest string) error {
	if destSiteID == 0 {
		return nil
	}
	site := getWebsiteByID(destSiteID)
	if site == nil {
		return fmt.Errorf("目标网站不存在")
	}
	return executor.ChownSitePath(dest, site.WebRoot, site.SystemUser)
}

func removeFileOrDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

func cleanupTransferredItems(items []fileTransferItem) []string {
	failed := []string{}
	for _, item := range items {
		if err := removeFileOrDir(item.dest); err != nil && !os.IsNotExist(err) {
			log.Printf("跨站操作清理目标失败 dest=%s: %v", item.dest, err)
			failed = append(failed, item.name)
		}
	}
	return failed
}

func joinItemNames(items []string) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) > 5 {
		return strings.Join(items[:5], "、") + fmt.Sprintf(" 等 %d 个项目", len(items))
	}
	return strings.Join(items, "、")
}

func (h *FileHandler) Move(c *gin.Context) {
	var req fileTransferRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Names) == 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	destSiteID, srcBase, destBase, _, _, items, err := resolveTransferRequest(req)
	if err != nil {
		c.JSON(fileTransferHTTPStatus(err), models.ErrorResponse(err.Error()))
		return
	}

	if req.SiteID == destSiteID {
		for _, item := range items {
			if err := os.Rename(item.src, item.dest); err != nil {
				log.Printf("移动失败 src=%s dest=%s: %v", item.src, item.dest, err)
				c.JSON(http.StatusInternalServerError, models.ErrorResponse("移动失败"))
				return
			}
		}
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": fmt.Sprintf("已移动 %d 个项目", len(req.Names))}))
		return
	}

	copied := []fileTransferItem{}
	for _, item := range items {
		if err := copyFileOrDir(srcBase, destBase, item.src, item.dest); err != nil {
			cleanupFailed := cleanupTransferredItems(copied)
			log.Printf("跨站移动复制失败 src=%s dest=%s: %v", item.src, item.dest, err)
			msg := "移动失败"
			if len(cleanupFailed) > 0 {
				msg += "，且目标清理失败: " + joinItemNames(cleanupFailed)
			}
			c.JSON(http.StatusInternalServerError, models.ErrorResponse(msg))
			return
		}
		copied = append(copied, item)
		if err := chownTransferredPath(destSiteID, item.dest); err != nil {
			cleanupFailed := cleanupTransferredItems(copied)
			log.Printf("跨站移动权限修复失败 dest=%s: %v", item.dest, err)
			msg := "目标权限修复失败"
			if len(cleanupFailed) > 0 {
				msg += "，且目标清理失败: " + joinItemNames(cleanupFailed)
			}
			c.JSON(http.StatusInternalServerError, models.ErrorResponse(msg))
			return
		}
	}

	deleteFailed := []string{}
	for _, item := range items {
		if err := removeFileOrDir(item.src); err != nil {
			log.Printf("跨站移动删除源失败 src=%s: %v", item.src, err)
			deleteFailed = append(deleteFailed, item.name)
		}
	}
	if len(deleteFailed) > 0 {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("移动未完全完成：目标站点已有文件副本，但源站点文件未能删除: "+joinItemNames(deleteFailed)))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": fmt.Sprintf("已移动 %d 个项目", len(req.Names))}))
}

func (h *FileHandler) Copy(c *gin.Context) {
	var req fileTransferRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Names) == 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	destSiteID, srcBase, destBase, _, _, items, err := resolveTransferRequest(req)
	if err != nil {
		c.JSON(fileTransferHTTPStatus(err), models.ErrorResponse(err.Error()))
		return
	}

	for _, item := range items {
		if err := copyFileOrDir(srcBase, destBase, item.src, item.dest); err != nil {
			log.Printf("复制失败 src=%s dest=%s: %v", item.src, item.dest, err)
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("复制失败"))
			return
		}
		if req.SiteID != destSiteID {
			if err := chownTransferredPath(destSiteID, item.dest); err != nil {
				if cleanupErr := removeFileOrDir(item.dest); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
					log.Printf("跨站复制清理目标失败 dest=%s: %v", item.dest, cleanupErr)
				}
				log.Printf("跨站复制权限修复失败 dest=%s: %v", item.dest, err)
				c.JSON(http.StatusInternalServerError, models.ErrorResponse("目标权限修复失败"))
				return
			}
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": fmt.Sprintf("已复制 %d 个项目", len(req.Names))}))
}

func copyFileOrDir(srcBase, destBase, src, dest string) error {
	if !isPathWithin(srcBase, src) || !isPathWithin(destBase, dest) {
		return fmt.Errorf("path outside base")
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if isSamePath(src, dest) || isPathWithin(src, dest) {
			return fmt.Errorf("cannot copy directory into itself")
		}
		if err := os.MkdirAll(dest, info.Mode().Perm()); err != nil {
			return err
		}
		if err := os.Chmod(dest, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyFileOrDir(srcBase, destBase, filepath.Join(src, e.Name()), filepath.Join(dest, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	copyOK := false
	defer func() {
		out.Close()
		if !copyOK {
			os.Remove(dest)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chmod(dest, info.Mode().Perm()); err != nil {
		return err
	}
	copyOK = true
	return nil
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
