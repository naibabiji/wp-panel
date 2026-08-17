package executor

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

//go:embed assets/image-filesize-runner/filesize_runner.php
var imageFilesizeRunnerPHPSource []byte

// imageBatchBinaryArgs 是历史图库批量优化允许调用的二进制及其固定参数——参数是
// 硬编码集合，不接受任何来自请求的动态输入，真实的安全边界由这个固定集合、
// 目标文件路径校验和降权执行三者共同构成（不依赖 executor/commander.go 的
// allowedCommands 白名单，见 ADR-0003 决策 2）。
// jpegoptim 只剥 comment/IPTC/XMP，刻意不传 --strip-all：历史照片里可能存在
// "像素本身未转正、靠 EXIF orientation 标签显示方向"的 JPEG（2019 年前上传、
// 或从其他面板迁移来的文件），--strip-all 会把 orientation 标签连同 EXIF 一起
// 剥掉，导致这些照片批量转向且不可逆。保留 EXIF/ICC，只去掉不影响显示的部分。
var imageBatchBinaryArgs = map[string][]string{
	"jpegoptim": {"--strip-com", "--strip-iptc", "--strip-xmp"},
	"optipng":   {"-o2", "-quiet"},
}

const (
	imageBatchRuntimeRoot  = "/var/wp-panel/image-optimizer"
	imageBatchExecTimeout  = 30 * time.Second
	imageFilesizeTimeout   = 60 * time.Second
	imageFilesizeResultMax = 4 << 10
)

func binaryForMime(mime string) string {
	switch mime {
	case "image/jpeg":
		return "jpegoptim"
	case "image/png":
		return "optipng"
	default:
		return ""
	}
}

// optimizeImageFile 对站点 wp-content/uploads/ 下的一个文件做原地无损重编码。
// 结构照抄 wp_core_update_runner.go 的降权范式：root 解析 system_user → 校验
// 二进制路径和属主 → runuser 降权执行 → 返回处理前后的真实文件大小。
func optimizeImageFile(ctx context.Context, webRoot, systemUser, relativePath, mime string) (before, after int64, err error) {
	binaryName := binaryForMime(mime)
	if binaryName == "" {
		return 0, 0, fmt.Errorf("不支持的图片类型: %s", mime)
	}
	fixedArgs, ok := imageBatchBinaryArgs[binaryName]
	if !ok {
		return 0, 0, fmt.Errorf("未登记的二进制: %s", binaryName)
	}

	if os.Geteuid() != 0 {
		return 0, 0, errors.New("图片优化 runner 需要 root 权限")
	}
	if !wpInventoryUserPattern.MatchString(systemUser) {
		return 0, 0, errors.New("站点系统用户格式非法")
	}

	wwwRoot := config.AppConfig.Paths.WWWRoot
	siteRoot, err := validateInventorySitePath(wwwRoot, webRoot)
	if err != nil {
		return 0, 0, fmt.Errorf("站点目录校验失败: %w", err)
	}

	absPath, err := validateImageBatchFilePath(siteRoot, relativePath)
	if err != nil {
		return 0, 0, fmt.Errorf("文件路径校验失败: %w", err)
	}

	beforeInfo, err := os.Stat(absPath)
	if err != nil {
		return 0, 0, err
	}
	before = beforeInfo.Size()

	u, err := user.Lookup(systemUser)
	if err != nil {
		return 0, 0, fmt.Errorf("查找站点系统用户失败: %w", err)
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil || uid <= 0 || gid <= 0 || u.Username != systemUser {
		return 0, 0, errors.New("站点系统用户身份异常")
	}

	binaryPath, err := validateInventoryBinary("/usr/bin/"+binaryName, "/usr/bin", 0, 0)
	if err != nil {
		return 0, 0, fmt.Errorf("%s 二进制校验失败: %w", binaryName, err)
	}
	runuserPath, err := validateInventoryBinary(wpInventoryRunuserPath, "/usr/sbin", 0, 0)
	if err != nil {
		return 0, 0, fmt.Errorf("runuser 校验失败: %w", err)
	}

	args := append([]string{"-u", u.Username, "--", binaryPath}, fixedArgs...)
	args = append(args, absPath)

	execCtx, cancel := context.WithTimeout(ctx, imageBatchExecTimeout)
	defer cancel()
	cmd := exec.CommandContext(execCtx, runuserPath, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return before, before, fmt.Errorf("%s 执行失败: %w\n%s", binaryName, runErr, string(out))
	}

	afterInfo, err := os.Stat(absPath)
	if err != nil {
		return before, before, err
	}
	after = afterInfo.Size()
	// 安全网：如果重编码后文件反而变大，理论上 jpegoptim/optipng 不应该发生，
	// 但不假设"无损重编码一定更小"，让调用方决定要不要按失败处理。
	return before, after, nil
}

func validateImageBatchFilePath(siteRoot, relativePath string) (string, error) {
	relativePath = filepath.Clean("/" + relativePath)
	absPath := filepath.Join(siteRoot, "wp-content", "uploads", relativePath)
	uploadsRoot := filepath.Join(siteRoot, "wp-content", "uploads")

	info, err := os.Lstat(absPath)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("目标不是普通文件")
	}
	real, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", err
	}
	if !pathWithin(uploadsRoot, real, false) {
		return "", errors.New("文件路径逃逸出 uploads 目录")
	}
	return real, nil
}

type imageFilesizeResult struct {
	Token     string `json:"token"`
	OK        bool   `json:"ok"`
	Updated   int    `json:"updated"`
	Total     int    `json:"total"`
	ErrorCode string `json:"error_code"`
}

// runFilesizeRewrite 把"相对路径 -> 新文件大小"的清单交给降权执行的 PHP 脚本，
// 由脚本在 WordPress 运行环境里用 update_post_meta() 完成回写。面板 Go 侧不直接
// 操作 MySQL——_wp_attachment_metadata 是 PHP 序列化格式，直接改库会破坏结构。
func runFilesizeRewrite(ctx context.Context, webRoot, systemUser string, manifest map[string]int64) (updated, total int, err error) {
	if len(manifest) == 0 {
		return 0, 0, nil
	}
	if os.Geteuid() != 0 {
		return 0, 0, errors.New("filesize runner 需要 root 权限")
	}
	if !wpInventoryUserPattern.MatchString(systemUser) {
		return 0, 0, errors.New("站点系统用户格式非法")
	}

	wwwRoot := config.AppConfig.Paths.WWWRoot
	siteRoot, err := validateInventorySitePath(wwwRoot, webRoot)
	if err != nil {
		return 0, 0, fmt.Errorf("站点目录校验失败: %w", err)
	}

	u, err := user.Lookup(systemUser)
	if err != nil {
		return 0, 0, err
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil || uid <= 0 || gid <= 0 || u.Username != systemUser {
		return 0, 0, errors.New("站点系统用户身份异常")
	}

	phpPath, err := validateInventoryBinary(wpInventoryPHPPath, "/usr/bin", 0, 0)
	if err != nil {
		return 0, 0, err
	}
	runuserPath, err := validateInventoryBinary(wpInventoryRunuserPath, "/usr/sbin", 0, 0)
	if err != nil {
		return 0, 0, err
	}

	if err := os.MkdirAll(imageBatchRuntimeRoot, 0711); err != nil {
		return 0, 0, err
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return 0, 0, err
	}
	token := hex.EncodeToString(tokenBytes)
	runtimeDir := filepath.Join(imageBatchRuntimeRoot, token)
	if filepath.Dir(runtimeDir) != filepath.Clean(imageBatchRuntimeRoot) || os.Mkdir(runtimeDir, 0710) != nil {
		return 0, 0, errors.New("创建运行目录失败")
	}
	defer func() { _ = os.RemoveAll(runtimeDir) }()
	if err := os.Chown(runtimeDir, 0, gid); err != nil {
		return 0, 0, err
	}

	manifestPath := filepath.Join(runtimeDir, "manifest.json")
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return 0, 0, err
	}
	if err := writeOwnedFile(manifestPath, manifestJSON, uid, gid); err != nil {
		return 0, 0, err
	}
	resultPath := filepath.Join(runtimeDir, "result.json")
	if err := createWPPluginRunnerResult(resultPath, uid, gid); err != nil {
		return 0, 0, err
	}

	openBase := strings.Join([]string{siteRoot, runtimeDir, "/tmp", "/usr/share/php"}, ":")
	execCtx, cancel := context.WithTimeout(ctx, imageFilesizeTimeout)
	defer cancel()
	args := []string{"-u", u.Username, "--", "/usr/bin/env", "-i",
		"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=" + u.HomeDir, "USER=" + u.Username, "LOGNAME=" + u.Username, "TMPDIR=/tmp",
		phpPath,
		"-d", "open_basedir=" + openBase,
		"-d", "disable_functions=" + sitePHPDisabledFunctions(),
		"-d", "allow_url_include=0",
		"-d", "memory_limit=256M",
		"-r", string(imageFilesizeRunnerPHPSource),
		token, siteRoot, manifestPath, resultPath,
	}
	cmd := exec.CommandContext(execCtx, runuserPath, args...)
	if runErr := cmd.Run(); runErr != nil && !errors.Is(execCtx.Err(), context.DeadlineExceeded) {
		// PHP 可能已经在退出码非 0 之前写好了结果文件，继续尝试读取。
	}

	result, err := readImageFilesizeResult(resultPath, token)
	if err != nil {
		return 0, 0, err
	}
	if !result.OK {
		return 0, 0, fmt.Errorf("filesize 回写失败: %s", result.ErrorCode)
	}
	return result.Updated, result.Total, nil
}

func writeOwnedFile(name string, data []byte, uid, gid int) error {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chown(uid, gid); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func readImageFilesizeResult(name, token string) (imageFilesizeResult, error) {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() <= 0 || info.Size() > imageFilesizeResultMax {
		return imageFilesizeResult{}, errors.New("filesize 回写结果无效")
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		return imageFilesizeResult{}, err
	}
	var result imageFilesizeResult
	if err := json.Unmarshal(raw, &result); err != nil || result.Token != token {
		return imageFilesizeResult{}, errors.New("filesize 回写结果 token 不匹配")
	}
	return result, nil
}
