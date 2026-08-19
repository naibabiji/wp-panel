package executor

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

const pluginDirName = "wp-panel-optimizer"

const cacheConfPath = "/etc/nginx/conf.d/wppanel-cache.conf"

// cacheHelperPluginFS 保存面板启动时传入的插件内嵌文件系统（main.go 的
// PluginFS），供 DeployPluginToSite/PluginNeedsUpdate 在处理单个站点的 HTTP
// 请求时使用——handlers 包不能直接拿到 main 包的 embed.FS（会形成循环导入），
// EnsureCacheHelperPlugin 是启动时唯一会调用一次的入口，顺带存一份即可。
var cacheHelperPluginFS embed.FS

func EnsureFastCGICacheConfig() {
	os.MkdirAll("/var/cache/nginx/fastcgi", 0755)
	content := `# WP Panel — FastCGI 缓存
fastcgi_cache_path /var/cache/nginx/fastcgi levels=1:2 keys_zone=WP_CACHE:200m inactive=60m max_size=2g;
`
	os.WriteFile(cacheConfPath, []byte(content), 0644)
}

// EnsureCacheHelperPlugin 把面板内嵌的配套插件目录同步到本地参照副本
// （/www/server/panel/packages/wp-panel-optimizer/），仅用于版本比对，不直接服务任何站点，
// 所以不需要 AutoDeployPluginUpdates 那套原子切换，按文件内容比对同步即可。
func EnsureCacheHelperPlugin(pluginFS embed.FS) {
	cacheHelperPluginFS = pluginFS
	pkgDir := "/www/server/panel/packages"
	refDir := filepath.Join(pkgDir, pluginDirName)
	os.MkdirAll(pkgDir, 0755)

	// 清理旧版本面板遗留的单文件参照副本
	os.Remove(filepath.Join(pkgDir, pluginDirName+".php"))

	srcFiles, err := readEmbeddedPluginFiles(pluginFS)
	if err != nil || len(srcFiles) == 0 {
		return
	}

	seen := make(map[string]bool, len(srcFiles))
	for rel, data := range srcFiles {
		seen[rel] = true
		dst := filepath.Join(refDir, filepath.FromSlash(rel))
		if existing, err := os.ReadFile(dst); err == nil && bytes.Equal(existing, data) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			continue
		}
		os.WriteFile(dst, data, 0644)
	}

	// 清理参照目录里源码已经不存在的旧文件（比如本次重构删除的老单文件模块）
	filepath.WalkDir(refDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(refDir, path)
		if relErr == nil && !seen[filepath.ToSlash(rel)] {
			os.Remove(path)
		}
		return nil
	})
}

// AutoDeployPluginUpdates 扫描所有已安装配套插件的 WordPress 站点，
// 若 plugin_api_key 非空且站点上的插件目录内容落后于面板内置版本，则自动更新。
// 每次面板启动时调用，实现插件无感自动升级。
func AutoDeployPluginUpdates(pluginFS embed.FS) {
	srcFiles, version, err := embeddedPluginFilesWithVersion(pluginFS)
	if err != nil || len(srcFiles) == 0 {
		return
	}

	db := database.GetDB()
	rows, err := db.Query(`SELECT id, web_root, system_user, domain, file_lock_enabled FROM websites
		WHERE site_type = 'wordpress' AND plugin_api_key != ''`)
	if err != nil {
		return
	}
	defer rows.Close()

	var updated int
	for rows.Next() {
		var id, fileLockEnabled int
		var webRoot, systemUser, domain string
		if err := rows.Scan(&id, &webRoot, &systemUser, &domain, &fileLockEnabled); err != nil {
			continue
		}
		if fileLockEnabled == 1 {
			continue
		}

		pluginsDir := filepath.Join(webRoot, "wp-content", "plugins")
		pluginDir := filepath.Join(pluginsDir, pluginDirName)

		// 版本标记一致就直接跳过，避免多余的系统权限调用（chown）
		if pluginDeployedVersion(pluginDir) == version {
			continue
		}

		if err := deployPluginDirectory(pluginsDir, pluginDir, srcFiles); err != nil {
			log.Printf("[插件自动更新] 部署失败 site=%d: %v", id, err)
			continue
		}
		InstallPluginPermissions(domain, systemUser, pluginDir)
		updated++
	}
	if updated > 0 {
		log.Printf("[插件自动更新] 已更新 %d 个站点的配套插件", updated)
	}
}

// DeployPluginToSite 把面板内置的最新插件版本部署到单个站点，供网站详情页
// "安装配套插件"按钮使用。跟 AutoDeployPluginUpdates 复用同一套整目录原子部署
// 逻辑——早期单文件插件时代遗留的"从 /www/server/panel/packages/wp-panel-
// optimizer.php 读一份 .php 复制过去"的实现，在插件拆分成 includes/trait-*.php
// 多文件之后就已经不可用了（EnsureCacheHelperPlugin 启动时还会主动清理掉那个
// 单文件参照副本），这里改成跟自动更新完全一致的路径，不再维护第二套部署逻辑。
func DeployPluginToSite(webRoot string) error {
	srcFiles, version, err := embeddedPluginFilesWithVersion(cacheHelperPluginFS)
	if err != nil {
		return fmt.Errorf("读取内嵌插件源码失败: %w", err)
	}
	if len(srcFiles) == 0 {
		return fmt.Errorf("内嵌插件源码为空")
	}
	pluginsDir := filepath.Join(webRoot, "wp-content", "plugins")
	pluginDir := filepath.Join(pluginsDir, pluginDirName)
	if pluginDeployedVersion(pluginDir) == version {
		return nil
	}
	return deployPluginDirectory(pluginsDir, pluginDir, srcFiles)
}

// PluginNeedsUpdate 供网站详情页判断该站点已部署的插件是否落后于面板内置版本。
// installed 为 false 时 needsUpdate 无意义。
func PluginNeedsUpdate(webRoot string) (installed bool, needsUpdate bool) {
	pluginDir := filepath.Join(webRoot, "wp-content", "plugins", pluginDirName)
	if _, err := os.Stat(filepath.Join(pluginDir, pluginDirName+".php")); err != nil {
		return false, false
	}
	_, version, err := embeddedPluginFilesWithVersion(cacheHelperPluginFS)
	if err != nil {
		return true, false
	}
	return true, pluginDeployedVersion(pluginDir) != version
}

// readEmbeddedPluginFiles 把内嵌的 wp-panel-optimizer 目录展开为「相对路径 -> 文件内容」的映射。
func readEmbeddedPluginFiles(pluginFS embed.FS) (map[string][]byte, error) {
	const root = "wp-panel-optimizer"
	files := make(map[string][]byte)
	err := fs.WalkDir(pluginFS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := pluginFS.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// pluginVersionMarkerFile 是随插件一起部署到每个站点的版本标记文件，内容是
// pluginSourceVersion() 算出的 hash。不是真正的插件功能文件，纯粹用来快速判断
// "站点上部署的版本是不是面板内置的最新版本"——原来的做法是把插件所有文件挨个
// 读出来逐字节比对，还要再遍历一遍目标目录揪出多余文件，每次面板启动、每个站点
// 都要做一整套文件系统操作；换成比对一个标记字符串，逻辑和开销都小得多。
// 以 "." 开头，WordPress 的 get_plugins() 只识别插件目录下 .php 文件的头部注释，
// 不会把它当成任何东西。
const pluginVersionMarkerFile = ".wpp-version"

// embeddedPluginFilesWithVersion 读取内嵌插件源码，算出版本标记后把标记本身也
// 作为一个文件加进返回的映射里，这样调用方不管是部署整目录（deployPluginDirectory）
// 还是同步本地参照副本，标记都会跟着一起写下去，不需要额外单独处理。
func embeddedPluginFilesWithVersion(pluginFS embed.FS) (map[string][]byte, string, error) {
	srcFiles, err := readEmbeddedPluginFiles(pluginFS)
	if err != nil {
		return nil, "", err
	}
	version := pluginSourceVersion(srcFiles)
	srcFiles[pluginVersionMarkerFile] = []byte(version)
	return srcFiles, version, nil
}

// pluginSourceVersion 对内嵌插件源码整体算一个 hash：按相对路径排序后把
// "路径+内容"逐个喂进同一个 hash，结果稳定、不依赖 map 遍历顺序（Go 的 map
// 遍历顺序是随机的），源码任何一个文件变了这个值就会变。
func pluginSourceVersion(srcFiles map[string][]byte) string {
	rels := make([]string, 0, len(srcFiles))
	for rel := range srcFiles {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	h := sha256.New()
	for _, rel := range rels {
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(srcFiles[rel])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// pluginDeployedVersion 读取已部署到某个站点的插件目录里的版本标记。目录不存在、
// 标记文件缺失（例如老版本面板部署的、还没有这个标记）统一返回空字符串——调用方
// 拿它跟当前源码版本比对时，空字符串必然不相等，会触发一次部署，效果等价于以前
// "逐文件比对发现不一致"，行为不变。
func pluginDeployedVersion(pluginDir string) string {
	data, err := os.ReadFile(filepath.Join(pluginDir, pluginVersionMarkerFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// deployPluginDirectory 把 srcFiles 部署到 pluginDir。目标目录常态下已存在且非空
// （插件更新场景），Linux rename(2) 无法用单次调用原子覆盖这种目标，因此按五步走：
// ① 清理上次部署遗留的临时/备份目录残留；② 新版本整体构建在与目标同一文件系统的临时
// 目录里；③ 当前生效目录挪到备份路径；④ 新目录挪到最终路径，失败必须回滚到步骤③之前
// 的状态；⑤ 成功后删除备份目录。临时/备份目录名以 "." 开头，WordPress 的 get_plugins()
// 会跳过这类条目，不会被误当成一个插件出现在后台插件列表里。
func deployPluginDirectory(pluginsDir, pluginDir string, srcFiles map[string][]byte) error {
	recoverOrCleanupStalePluginDirs(pluginsDir, pluginDir)

	suffix := NewCacheKey()
	stagingDir := filepath.Join(pluginsDir, "."+pluginDirName+".staging-"+suffix)
	backupDir := filepath.Join(pluginsDir, "."+pluginDirName+".backup-"+suffix)

	if err := writePluginFiles(stagingDir, srcFiles); err != nil {
		os.RemoveAll(stagingDir)
		return fmt.Errorf("构建临时目录失败: %w", err)
	}

	targetExists := false
	if _, err := os.Stat(pluginDir); err == nil {
		targetExists = true
	}

	if targetExists {
		if err := os.Rename(pluginDir, backupDir); err != nil {
			os.RemoveAll(stagingDir)
			return fmt.Errorf("移走旧目录失败: %w", err)
		}
	}

	if err := os.Rename(stagingDir, pluginDir); err != nil {
		if targetExists {
			if rollbackErr := os.Rename(backupDir, pluginDir); rollbackErr != nil {
				return fmt.Errorf("部署失败且回滚失败，插件目录可能已丢失，需人工检查 %s: 回滚错误=%v 原始错误=%v", pluginDir, rollbackErr, err)
			}
		}
		os.RemoveAll(stagingDir)
		return fmt.Errorf("替换插件目录失败（已回滚到部署前状态）: %w", err)
	}

	if targetExists {
		logUnexpectedPluginFiles(backupDir, srcFiles)
		os.RemoveAll(backupDir)
	}
	return nil
}

// logUnexpectedPluginFiles 在丢弃旧插件目录（backupDir）之前记录一次源码里没有的多余
// 文件——通常是用户手动放进插件目录的调试脚本之类。整目录原子替换会把它们一并丢弃，
// 这里只做诊断日志，不做保留（保留会破坏"部署结果与内嵌源码逐文件一致"这个不变量）。
func logUnexpectedPluginFiles(backupDir string, srcFiles map[string][]byte) {
	var extra []string
	filepath.WalkDir(backupDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(backupDir, path)
		if relErr != nil {
			return nil
		}
		if _, ok := srcFiles[filepath.ToSlash(rel)]; !ok {
			extra = append(extra, filepath.ToSlash(rel))
		}
		return nil
	})
	if len(extra) > 0 {
		log.Printf("[插件部署] 发现 %d 个源码之外的文件，本次更新会一并移除: %v", len(extra), extra)
	}
}

// recoverOrCleanupStalePluginDirs 处理上一次部署可能遗留的临时/备份目录（例如面板恰好
// 在步骤③和④之间被强制重启）。如果插件目录缺失但存在备份目录，说明上次部署卡在
// "旧目录已挪走、新目录还没就位"的中间状态，直接把备份挪回来恢复站点原有插件，
// 而不是把这份还完好的内容当垃圾删掉；处理完之后再清理所有残留的临时/备份目录。
func recoverOrCleanupStalePluginDirs(pluginsDir, pluginDir string) {
	stagingPrefix := "." + pluginDirName + ".staging-"
	backupPrefix := "." + pluginDirName + ".backup-"

	if _, statErr := os.Stat(pluginDir); os.IsNotExist(statErr) {
		if entries, err := os.ReadDir(pluginsDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() || !strings.HasPrefix(entry.Name(), backupPrefix) {
					continue
				}
				backupPath := filepath.Join(pluginsDir, entry.Name())
				if renameErr := os.Rename(backupPath, pluginDir); renameErr == nil {
					log.Printf("[插件自动更新] 检测到上次部署中断，已从备份目录恢复插件: %s", pluginDir)
					break
				}
			}
		}
	}

	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && (strings.HasPrefix(entry.Name(), stagingPrefix) || strings.HasPrefix(entry.Name(), backupPrefix)) {
			os.RemoveAll(filepath.Join(pluginsDir, entry.Name()))
		}
	}
}

func writePluginFiles(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for rel, data := range files {
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return err
		}
	}
	return nil
}

func NewCacheKey() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func NewAPIKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func ClearSiteCache(siteID int) {
	db := database.GetDB()
	key := NewCacheKey()
	db.Exec("UPDATE websites SET fastcgi_cache_key = ? WHERE id = ?", key, siteID)
	if err := RegenerateSiteNginx(siteID); err != nil {
		log.Printf("刷新站点 Nginx 配置失败 site=%d: %v", siteID, err)
	}
}

func ClearWPSiteRuntimeCaches(siteID int, domain, webRoot string) {
	ClearSiteCache(siteID)
	if err := ClearWPRedisObjectCache(domain, webRoot); err != nil {
		log.Printf("清理 Redis Object Cache 失败 domain=%s: %v", domain, err)
	}
}

func ClearWPRedisObjectCache(domain, webRoot string) error {
	prefixes := redisObjectCachePrefixes(domain, webRoot)
	for _, prefix := range prefixes {
		if err := deleteRedisKeysByPrefix(prefix); err != nil {
			return err
		}
	}
	return nil
}

func redisObjectCachePrefixes(domain, webRoot string) []string {
	seen := make(map[string]bool)
	var prefixes []string
	add := func(prefix string) {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" || seen[prefix] {
			return
		}
		seen[prefix] = true
		prefixes = append(prefixes, prefix)
	}

	if strings.TrimSpace(webRoot) != "" {
		if data, err := os.ReadFile(filepath.Join(webRoot, "wp-config.php")); err == nil {
			content := string(data)
			add(extractWPConfigStringConstant(content, "WP_REDIS_PREFIX"))
			add(extractWPConfigStringConstant(content, "WP_CACHE_KEY_SALT"))
		}
	}
	add(wpCacheKeySalt(domain))
	return prefixes
}

func extractWPConfigStringConstant(content, name string) string {
	re := regexp.MustCompile(`(?m)^\s*define\s*\(\s*['"]` + regexp.QuoteMeta(name) + `['"]\s*,\s*['"]([^'"]*)['"]\s*\)\s*;`)
	matches := re.FindStringSubmatch(content)
	if len(matches) != 2 {
		return ""
	}
	return matches[1]
}

func deleteRedisKeysByPrefix(prefix string) error {
	keys, err := exec.Command("redis-cli", "--scan", "--pattern", prefix+"*").Output()
	if err != nil {
		return err
	}

	var batch []string
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		args := append([]string{"DEL"}, batch...)
		batch = nil
		return exec.Command("redis-cli", args...).Run()
	}
	for _, key := range strings.Split(string(keys), "\n") {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		batch = append(batch, key)
		if len(batch) >= 200 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

func RegenerateSiteNginx(siteID int) error {
	db := database.GetDB()
	var domain, aliases, siteType, systemUser, webRoot, documentRootSubdir, logDir, accessLogMode, cacheKey, templateVer string
	var phpPoolPath, nginxConfPath string
	var sslEnabled, fCacheEnabled, xmlrpcEnabled, cdnRealIPEnabled int
	var fCacheTTL int
	var sslCertPath, sslKeyPath, status string

	err := db.QueryRow(
		`SELECT domain, aliases, site_type, system_user, web_root, document_root_subdir, log_dir, ssl_enabled,
		        access_log_mode, fastcgi_cache_enabled, fastcgi_cache_ttl, fastcgi_cache_key,
		        ssl_cert_path, ssl_key_path, template_version, xmlrpc_enabled, php_pool_path, nginx_conf_path, cdn_realip_enabled, status
		 FROM websites WHERE id = ?`, siteID,
	).Scan(&domain, &aliases, &siteType, &systemUser, &webRoot, &documentRootSubdir, &logDir, &sslEnabled, &accessLogMode, &fCacheEnabled, &fCacheTTL, &cacheKey, &sslCertPath, &sslKeyPath, &templateVer, &xmlrpcEnabled, &phpPoolPath, &nginxConfPath, &cdnRealIPEnabled, &status)
	if err != nil || domain == "" {
		if err != nil {
			return fmt.Errorf("查询站点失败(site %d): %w", siteID, err)
		}
		return fmt.Errorf("站点域名为空(site %d)", siteID)
	}

	if templateVer == "" {
		templateVer = "v1.0"
	}
	if cacheKey == "" {
		cacheKey = NewCacheKey()
		db.Exec("UPDATE websites SET fastcgi_cache_key = ? WHERE id = ?", cacheKey, siteID)
	}

	cfg := config.AppConfig
	engine := NewTemplateEngine(cfg.Panel.BackupDir)

	var aliasList []string
	if aliases != "" {
		aliasList = strings.Split(aliases, "\n")
	}

	data := &NginxSiteData{
		Domain:        domain,
		Aliases:       aliasList,
		ServerNames:   buildServerNames(domain, aliasList),
		WebRoot:       EffectiveDocumentRoot(webRoot, siteType, documentRootSubdir),
		LogDir:        logDir,
		SystemUser:    systemUser,
		SiteType:      siteType,
		PHPProxy:      "unix:" + phpSocketPath(cfg, phpPoolPath, domain),
		TemplateVer:   templateVer,
		AccessLogMode: accessLogMode,
		UseSSL:        sslEnabled == 1,
		FCacheEnabled: fCacheEnabled == 1,
		FCacheTTL:     fCacheTTL,
		FCacheKey:     cacheKey,
		XMLRPCEnabled: xmlrpcEnabled == 1,
	}
	if cdnRealIPEnabled == 1 {
		groups, _ := GetWebsiteCDNRealIPGroups(siteID)
		runtime, err := ResolveCDNRealIPRuntime(&models.Website{ID: siteID, CDNRealIPEnabled: true, CDNRealIPGroups: groups})
		if err != nil {
			return fmt.Errorf("CDN Real IP 配置无效(site %d): %w", siteID, err)
		}
		if runtime.Enabled {
			data.CDNRealIPEnabled = true
			data.CDNRealIPHeader = runtime.HeaderName
			data.CDNRealIPRanges = runtime.IPRanges
			data.CDNRealIPCompat = runtime.Compatible
		}
	}
	if data.UseSSL {
		data.SSLCertPath = sslCertPath
		data.SSLKeyPath = sslKeyPath
	}

	config, err := engine.RenderNginxConfig(data)
	if err != nil {
		return fmt.Errorf("渲染 Nginx 配置失败(site %d): %w", siteID, err)
	}

	if status == string(models.StatusPaused) {
		// 站点已暂停：只刷新磁盘上的配置内容，不恢复 sites-enabled 软链接、不 reload，
		// 避免批量模板刷新时把已暂停的站点重新暴露为可访问。
		if err := engine.ApplyNginxConfigKeepDisabled(config, nginxConfPath); err != nil {
			return fmt.Errorf("应用 Nginx 配置失败(site %d): %w", siteID, err)
		}
		return nil
	}

	if err := engine.ApplyNginxConfig(config, nginxConfPath, nginxEnabledPath(cfg, nginxConfPath, domain)); err != nil {
		return fmt.Errorf("应用 Nginx 配置失败(site %d): %w", siteID, err)
	}
	return nil
}

// RegenerateAllSitesNginx 重建全部网站的 Nginx 配置，用于模板更新后批量刷新。
func RegenerateAllSitesNginx() error {
	db := database.GetDB()
	rows, err := db.Query("SELECT id FROM websites")
	if err != nil {
		log.Printf("[Nginx重建] 查询网站列表失败: %v", err)
		return err
	}
	defer rows.Close()

	var failures []string
	for rows.Next() {
		var siteID int
		if err := rows.Scan(&siteID); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if err := RegenerateSiteNginx(siteID); err != nil {
			log.Printf("[Nginx重建] 站点 %d 更新失败: %v", siteID, err)
			failures = append(failures, err.Error())
		}
	}
	if err := rows.Err(); err != nil {
		failures = append(failures, err.Error())
	}
	if len(failures) > 0 {
		return fmt.Errorf("部分站点 Nginx 配置更新失败: %s", strings.Join(failures, "; "))
	}
	log.Printf("[Nginx重建] 全部网站 Nginx 配置已更新")
	return nil
}

// RegenerateAllSitesFPM 重建全部网站的 PHP-FPM pool 配置，
// 用于 open_basedir 等模板变更后批量刷新旧站点。
func RegenerateAllSitesFPM() error {
	db := database.GetDB()
	rows, err := db.Query("SELECT id, domain, system_user, web_root, log_dir, php_pool_path, php_fpm_max_children FROM websites")
	if err != nil {
		log.Printf("[FPM重建] 查询网站列表失败: %v", err)
		return err
	}
	defer rows.Close()

	cfg := config.AppConfig
	engine := NewTemplateEngine(cfg.Panel.BackupDir)
	var failures []string

	for rows.Next() {
		var siteID, maxChildren int
		var domain, systemUser, webRoot, logDir, phpPoolPath string
		if err := rows.Scan(&siteID, &domain, &systemUser, &webRoot, &logDir, &phpPoolPath, &maxChildren); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if err := ensureSitePrimaryGroup(systemUser); err != nil {
			log.Printf("[FPM重建] %s: 站点用户组检查失败: %v", domain, err)
			failures = append(failures, fmt.Sprintf("%s: %v", domain, err))
			continue
		}

		poolName := phpPoolName(phpPoolPath, domain)
		phpData := &PHPFPMPoolData{
			Domain:     domain,
			PoolName:   poolName,
			SystemUser: systemUser,
			WebRoot:    webRoot,
			SocketPath: cfg.Paths.PHPFPMSock,
			SocketName: poolName,
			// 沿用该站点持久化的 pm.max_children，不按当前硬件/站点数重新计算——
			// 见 PHPFPMPoolData.MaxChildren 注释。
			MaxChildren: strconv.Itoa(maxChildren),
		}
		phpConfig, err := engine.RenderPHPFPMPool(phpData)
		if err != nil {
			log.Printf("[FPM重建] %s: 渲染配置失败: %v", domain, err)
			failures = append(failures, fmt.Sprintf("%s: %v", domain, err))
			continue
		}

		if err := engine.ApplyPHPFPMPool(phpConfig, phpPoolPath, logDir); err != nil {
			log.Printf("[FPM重建] %s: 应用配置失败: %v", domain, err)
			failures = append(failures, fmt.Sprintf("%s: %v", domain, err))
			continue
		}
	}
	log.Printf("[FPM重建] 全部网站 PHP-FPM pool 配置已更新")
	if err := rows.Err(); err != nil {
		return err
	}
	if len(failures) > 0 {
		return fmt.Errorf("部分站点 PHP-FPM Pool 重建失败: %s", strings.Join(failures, "; "))
	}
	return nil
}
