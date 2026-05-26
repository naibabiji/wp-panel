package executor

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

const cacheConfPath = "/etc/nginx/conf.d/wppanel-cache.conf"

func EnsureFastCGICacheConfig() {
	os.MkdirAll("/var/cache/nginx/fastcgi", 0755)
	content := `# WP Panel — FastCGI 缓存
fastcgi_cache_path /var/cache/nginx/fastcgi levels=1:2 keys_zone=WP_CACHE:200m inactive=60m max_size=2g;
`
	os.WriteFile(cacheConfPath, []byte(content), 0644)
}

func EnsureCacheHelperPlugin(pluginFS embed.FS) {
	pkgDir := "/www/server/panel/packages"
	os.MkdirAll(pkgDir, 0755)
	dst := filepath.Join(pkgDir, "wp-panel-optimizer.php")

	data, err := pluginFS.ReadFile("wp-panel-optimizer/wp-panel-optimizer.php")
	if err != nil {
		return
	}
	os.WriteFile(dst, data, 0644)
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
	RegenerateSiteNginx(siteID)
}

func RegenerateSiteNginx(siteID int) {
	db := database.GetDB()
	var domain, aliases, siteType, systemUser, webRoot, logDir, accessLogMode, cacheKey string
	var sslEnabled, fCacheEnabled int
	var fCacheTTL int

	err := db.QueryRow(
		`SELECT domain, aliases, site_type, system_user, web_root, log_dir, ssl_enabled,
		        access_log_mode, fastcgi_cache_enabled, fastcgi_cache_ttl, fastcgi_cache_key
		 FROM websites WHERE id = ?`, siteID,
	).Scan(&domain, &aliases, &siteType, &systemUser, &webRoot, &logDir, &sslEnabled, &accessLogMode, &fCacheEnabled, &fCacheTTL, &cacheKey)
	if err != nil || domain == "" {
		return
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
		WebRoot:       webRoot,
		LogDir:        logDir,
		SystemUser:    systemUser,
		SiteType:      siteType,
		PHPProxy:      "unix:" + filepath.Join(cfg.Paths.PHPFPMSock, domain+".sock"),
		TemplateVer:   "v1.0",
		AccessLogMode: accessLogMode,
		UseSSL:        sslEnabled == 1,
		FCacheEnabled: fCacheEnabled == 1,
		FCacheTTL:     fCacheTTL,
		FCacheKey:     cacheKey,
	}
	if data.UseSSL {
		data.SSLCertPath = "/www/server/certificates/" + domain + "/fullchain.pem"
		data.SSLKeyPath = "/www/server/certificates/" + domain + "/privkey.pem"
	}

	config, err := engine.RenderNginxConfig(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "渲染Nginx配置失败(site %d): %v\n", siteID, err)
		return
	}

	nginxConfPath := filepath.Join(cfg.Paths.NginxSitesAvailable, domain+".conf")
	nginxEnabledPath := filepath.Join(cfg.Paths.NginxSitesEnabled, domain+".conf")
	if err := engine.ApplyNginxConfig(config, nginxConfPath, nginxEnabledPath); err != nil {
		fmt.Fprintf(os.Stderr, "应用Nginx配置失败(site %d): %v\n", siteID, err)
		return
	}
}

// RegenerateAllSitesNginx 重建全部网站的 Nginx 配置，用于模板更新后批量刷新。
func RegenerateAllSitesNginx() {
	db := database.GetDB()
	rows, err := db.Query("SELECT id FROM websites")
	if err != nil {
		log.Printf("[Nginx重建] 查询网站列表失败: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var siteID int
		if err := rows.Scan(&siteID); err != nil {
			continue
		}
		RegenerateSiteNginx(siteID)
	}
	log.Printf("[Nginx重建] 全部网站 Nginx 配置已更新")
}

// RegenerateAllSitesFPM 重建全部网站的 PHP-FPM pool 配置，
// 用于 open_basedir 等模板变更后批量刷新旧站点。
func RegenerateAllSitesFPM() {
	db := database.GetDB()
	rows, err := db.Query("SELECT id, domain, system_user, web_root, log_dir FROM websites")
	if err != nil {
		log.Printf("[FPM重建] 查询网站列表失败: %v", err)
		return
	}
	defer rows.Close()

	cfg := config.AppConfig
	engine := NewTemplateEngine(cfg.Panel.BackupDir)

	for rows.Next() {
		var siteID int
		var domain, systemUser, webRoot, logDir string
		if err := rows.Scan(&siteID, &domain, &systemUser, &webRoot, &logDir); err != nil {
			continue
		}

		phpData := &PHPFPMPoolData{
			Domain:     domain,
			SystemUser: systemUser,
			WebRoot:    webRoot,
			SocketPath: cfg.Paths.PHPFPMSock,
		}
		phpConfig, err := engine.RenderPHPFPMPool(phpData)
		if err != nil {
			log.Printf("[FPM重建] %s: 渲染配置失败: %v", domain, err)
			continue
		}

		poolPath := filepath.Join(cfg.Paths.PHPFPMPool, domain+".conf")
		if err := engine.ApplyPHPFPMPool(phpConfig, poolPath, logDir); err != nil {
			log.Printf("[FPM重建] %s: 应用配置失败: %v", domain, err)
			continue
		}
	}
	log.Printf("[FPM重建] 全部网站 PHP-FPM pool 配置已更新")
}
