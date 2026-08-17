package executor

import (
	"context"
	"log"
	"os/exec"
	"time"
)

const phpExifPackage = "php8.3-exif"

// jpegoptimPackage/optipngPackage 是历史图库批量优化依赖的无损压缩二进制，
// 只影响那一个功能，跟 php8.3-exif（影响新上传处理）互不混淆。
const jpegoptimPackage = "jpegoptim"
const optipngPackage = "optipng"

// EnsurePHPExifExtension 保证 php8.3-exif 扩展已安装，供配套插件的新上传图片处理
// （EXIF 方向修正）使用。新装机通过 install.sh 已经装了这个包；这个函数是给已经
// 装机的老服务器做补装，异步执行、不阻塞面板启动，装完自动重载 php8.3-fpm 让扩展
// 生效。插件侧不查询这个函数的状态——它直接在 PHP 运行时用
// is_callable('exif_read_data') 判断，装好之后下次页面加载自然可用。
func EnsurePHPExifExtension() {
	ensureAptPackage(phpExifPackage, func() {
		if err := exec.Command("systemctl", "reload", "php8.3-fpm").Run(); err != nil {
			log.Printf("[图片优化] %s 补装成功，但重载 php8.3-fpm 失败，需要人工重启使扩展生效: %v", phpExifPackage, err)
			return
		}
		log.Printf("[图片优化] 已补装 %s 并重载 php8.3-fpm", phpExifPackage)
	})
}

// EnsureImageBatchBinaries 保证历史图库批量优化依赖的 jpegoptim/optipng 已安装。
// 这两个是独立二进制，装完不需要重载任何服务，装好之后下次批量任务扫描时
// ImageBatchBinariesReady() 直接能感知到。
func EnsureImageBatchBinaries() {
	ensureAptPackage(jpegoptimPackage, nil)
	ensureAptPackage(optipngPackage, nil)
}

// ImageBatchBinariesReady 供面板 API 判断历史图库批量优化当前是否可用
// （jpegoptim 和 optipng 都已经能找到）。
func ImageBatchBinariesReady() bool {
	return binaryInPath(jpegoptimPackage) && binaryInPath(optipngPackage)
}

func ensureAptPackage(pkg string, onInstalled func()) {
	if aptPackageInstalled(pkg) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "apt-get", "install", "-y",
		"-o", "DPkg::Lock::Timeout=60",
		pkg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			log.Printf("[图片优化] 补装 %s 超时（3 分钟），后续面板重启会重试", pkg)
			return
		}
		log.Printf("[图片优化] 补装 %s 失败: %v\n%s", pkg, err, string(out))
		return
	}

	if onInstalled != nil {
		onInstalled()
		return
	}
	log.Printf("[图片优化] 已补装 %s", pkg)
}

func aptPackageInstalled(pkg string) bool {
	return exec.Command("dpkg", "-s", pkg).Run() == nil
}

func binaryInPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
