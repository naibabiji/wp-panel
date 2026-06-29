package handlers

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"
)

const (
	maxRemoteImportSize      = int64(5 * 1024 * 1024 * 1024)
	minRemoteImportFreeSpace = int64(1024 * 1024 * 1024)
	remoteImportTaskTTL      = 24 * time.Hour
)

type remoteImportTask struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	Filename   string `json:"filename"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	Error      string `json:"error,omitempty"`
	Completed  bool   `json:"completed"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

var remoteImportTasks = struct {
	sync.Mutex
	items map[string]*remoteImportTask
}{items: make(map[string]*remoteImportTask)}

func (h *FileHandler) RemoteImport(c *gin.Context) {
	var req struct {
		URL              string `json:"url"`
		Filename         string `json:"filename"`
		SiteID           *int   `json:"site_id"`
		Path             string `json:"path"`
		AllowInsecureTLS bool   `json:"allow_insecure_tls"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数无效"))
		return
	}
	if req.SiteID == nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("请选择网站或备份目录"))
		return
	}
	if req.Path == "" {
		req.Path = "/"
	}
	u, err := validateRemoteImportURL(req.URL)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(err.Error()))
		return
	}
	filename := sanitizeUploadFilename(req.Filename)
	if filename == "" {
		filename = remoteImportFilename(u)
	}
	if filename == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无法从URL识别文件名，请手动填写文件名"))
		return
	}

	basePath, err := fileBasePath(*req.SiteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}
	destPath := filepath.Clean(filepath.Join(basePath, req.Path, filename))
	if !isPathWithin(basePath, destPath) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("路径越权"))
		return
	}
	if err := checkSiteFileLockWrite(*req.SiteID, destPath, false); err != nil {
		respondFileWriteError(c, err)
		return
	}
	if info, err := os.Stat(filepath.Dir(destPath)); err != nil || !info.IsDir() {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("目标目录不存在"))
		return
	}
	if info, err := os.Stat(destPath); err == nil && info.IsDir() {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("目标路径已存在且是目录"))
		return
	}
	var siteRoot, systemUser string
	if *req.SiteID != 0 {
		site := getWebsiteByID(*req.SiteID)
		if site == nil {
			c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
			return
		}
		siteRoot = site.WebRoot
		systemUser = site.SystemUser
	}

	task := createRemoteImportTask(filename)
	go runRemoteImport(task.ID, u.String(), req.AllowInsecureTLS, destPath, siteRoot, systemUser)

	c.JSON(http.StatusOK, models.SuccessResponse(taskSnapshot(task.ID)))
}

func (h *FileHandler) RemoteImportStatus(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	task := taskSnapshot(id)
	if task == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("远程导入任务不存在"))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(task))
}

func createRemoteImportTask(filename string) *remoteImportTask {
	cleanupRemoteImportTasks()
	now := time.Now().Unix()
	task := &remoteImportTask{
		ID:        uuid.NewString(),
		Status:    "queued",
		Message:   "等待下载",
		Filename:  filename,
		Total:     -1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	remoteImportTasks.Lock()
	remoteImportTasks.items[task.ID] = task
	remoteImportTasks.Unlock()
	return task
}

func cleanupRemoteImportTasks() {
	cutoff := time.Now().Add(-remoteImportTaskTTL).Unix()
	remoteImportTasks.Lock()
	defer remoteImportTasks.Unlock()
	for id, task := range remoteImportTasks.items {
		if task.UpdatedAt < cutoff {
			delete(remoteImportTasks.items, id)
		}
	}
}

func taskSnapshot(id string) gin.H {
	remoteImportTasks.Lock()
	defer remoteImportTasks.Unlock()
	task := remoteImportTasks.items[id]
	if task == nil {
		return nil
	}
	percent := 0
	if task.Total > 0 {
		percent = int(task.Downloaded * 100 / task.Total)
		if percent > 100 {
			percent = 100
		}
	}
	return gin.H{
		"id":         task.ID,
		"status":     task.Status,
		"message":    task.Message,
		"filename":   task.Filename,
		"downloaded": task.Downloaded,
		"total":      task.Total,
		"percent":    percent,
		"error":      task.Error,
		"completed":  task.Completed,
		"created_at": task.CreatedAt,
		"updated_at": task.UpdatedAt,
	}
}

func updateRemoteImportTask(id string, update func(*remoteImportTask)) {
	remoteImportTasks.Lock()
	defer remoteImportTasks.Unlock()
	task := remoteImportTasks.items[id]
	if task == nil {
		return
	}
	update(task)
	task.UpdatedAt = time.Now().Unix()
}

func runRemoteImport(taskID, rawURL string, allowInsecureTLS bool, destPath, siteRoot, systemUser string) {
	tmpPath := destPath + ".download_tmp-" + filepath.Base(taskID)
	copyOK := false
	defer func() {
		if !copyOK {
			_ = os.Remove(tmpPath)
		}
	}()

	updateRemoteImportTask(taskID, func(t *remoteImportTask) {
		t.Status = "downloading"
		t.Message = "正在下载"
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		failRemoteImportTask(taskID, "创建下载请求失败")
		return
	}
	req.Header.Set("User-Agent", "WP-Panel-Remote-Import/1.0")

	client := remoteImportHTTPClient(allowInsecureTLS)
	resp, err := client.Do(req)
	if err != nil {
		failRemoteImportTask(taskID, "远程下载失败: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		failRemoteImportTask(taskID, "远程服务器返回状态码 "+strconv.Itoa(resp.StatusCode))
		return
	}
	total := resp.ContentLength
	updateRemoteImportTask(taskID, func(t *remoteImportTask) {
		t.Total = total
	})
	if total > maxRemoteImportSize {
		failRemoteImportTask(taskID, "远程文件超过5GB限制")
		return
	}
	if free, ok := diskAvailableBytes(filepath.Dir(destPath)); ok {
		required := maxRemoteImportSize + minRemoteImportFreeSpace
		if total >= 0 {
			required = total + minRemoteImportFreeSpace
		}
		if free < required {
			failRemoteImportTask(taskID, "目标磁盘空间不足")
			return
		}
	}

	out, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		failRemoteImportTask(taskID, uploadSaveErrorMessage("创建远程导入文件", err))
		return
	}
	defer out.Close()

	buf := make([]byte, 1024*1024)
	var downloaded int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			downloaded += int64(n)
			if downloaded > maxRemoteImportSize {
				failRemoteImportTask(taskID, "远程文件超过5GB限制")
				return
			}
			if _, err := out.Write(buf[:n]); err != nil {
				failRemoteImportTask(taskID, uploadSaveErrorMessage("保存远程文件", err))
				return
			}
			if downloaded%(16*1024*1024) < int64(n) {
				if free, ok := diskAvailableBytes(filepath.Dir(destPath)); ok && free < minRemoteImportFreeSpace {
					failRemoteImportTask(taskID, "目标磁盘剩余空间不足1GB，已停止下载")
					return
				}
			}
			updateRemoteImportTask(taskID, func(t *remoteImportTask) {
				t.Downloaded = downloaded
			})
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			failRemoteImportTask(taskID, "读取远程文件失败: "+readErr.Error())
			return
		}
	}
	if err := out.Close(); err != nil {
		failRemoteImportTask(taskID, uploadSaveErrorMessage("保存远程文件", err))
		return
	}
	if err := os.Chmod(tmpPath, 0644); err != nil {
		failRemoteImportTask(taskID, "设置文件权限失败")
		return
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		failRemoteImportTask(taskID, "保存远程文件失败")
		return
	}
	copyOK = true
	message := "远程导入完成"
	if siteRoot != "" && systemUser != "" {
		if err := executor.ChownSitePath(destPath, siteRoot, systemUser); err != nil {
			log.Printf("远程导入权限设置失败 path=%s user=%s: %v", destPath, systemUser, err)
			message = "远程导入完成，权限设置失败，请点击修复权限"
		}
	}
	updateRemoteImportTask(taskID, func(t *remoteImportTask) {
		t.Status = "success"
		t.Message = message
		t.Downloaded = downloaded
		t.Total = downloaded
		t.Completed = true
	})
}

func failRemoteImportTask(taskID, message string) {
	log.Printf("远程导入失败 task=%s: %s", taskID, message)
	updateRemoteImportTask(taskID, func(t *remoteImportTask) {
		t.Status = "failed"
		t.Message = message
		t.Error = message
		t.Completed = true
	})
}

func remoteImportHTTPClient(allowInsecureTLS bool) *http.Client {
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: allowInsecureTLS,
	}
	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
		DialContext:     validatingRemoteImportDialContext,
	}
	return &http.Client{
		Timeout:   2 * time.Hour,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("远程地址重定向次数过多")
			}
			_, err := validateRemoteImportURL(req.URL.String())
			return err
		},
	}
}

func validatingRemoteImportDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if err := validateRemoteImportHost(ctx, host); err != nil {
		return nil, err
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, address)
}

func validateRemoteImportURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("远程URL不能为空")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("远程URL格式无效")
	}
	if strings.ToLower(u.Scheme) != "https" {
		return nil, fmt.Errorf("仅支持 HTTPS 远程导入")
	}
	if u.User != nil {
		return nil, fmt.Errorf("远程URL不能包含用户名或密码")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := validateRemoteImportHost(ctx, u.Hostname()); err != nil {
		return nil, err
	}
	return u, nil
}

func validateRemoteImportHost(ctx context.Context, host string) error {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return fmt.Errorf("远程URL主机无效")
	}
	lowerHost := strings.ToLower(host)
	if lowerHost == "localhost" || strings.HasSuffix(lowerHost, ".localhost") {
		return fmt.Errorf("远程URL不能指向本机地址")
	}
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedRemoteImportIP(ip) {
			return fmt.Errorf("远程URL不能指向内网或本机地址")
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("远程URL主机解析失败")
	}
	for _, resolved := range ips {
		if isBlockedRemoteImportIP(resolved.IP) {
			return fmt.Errorf("远程URL不能解析到内网或本机地址")
		}
	}
	return nil
}

func isBlockedRemoteImportIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	blocked := []string{
		"100.64.0.0/10",
		"169.254.0.0/16",
		"0.0.0.0/8",
		"::/128",
		"fc00::/7",
		"fe80::/10",
	}
	for _, cidr := range blocked {
		prefix := netip.MustParsePrefix(cidr)
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func remoteImportFilename(u *url.URL) string {
	name := sanitizeUploadFilename(pathBaseFromURL(u))
	if name == "" || name == "." {
		return ""
	}
	return name
}

func pathBaseFromURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	escapedPath := strings.TrimSpace(u.EscapedPath())
	if escapedPath == "" || escapedPath == "/" {
		return ""
	}
	if decoded, err := url.PathUnescape(escapedPath); err == nil {
		return path.Base(decoded)
	}
	return path.Base(escapedPath)
}
