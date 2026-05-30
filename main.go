package main

import (
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/naibabiji/wp-panel/collector"
	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/middleware"
	"github.com/naibabiji/wp-panel/router"

	"golang.org/x/crypto/bcrypt"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	configPath := flag.String("config", "/www/server/panel/config.json", "配置文件路径")
	resetPass := flag.String("passwd", "", "重置管理员密码（8位以上）")
	resetAdmin := flag.Bool("reset-admin", false, "一键重置管理员账号密码")
	refreshWhitelist := flag.Bool("refresh-whitelist", false, "手动触发白名单刷新")
	unbanAll := flag.Bool("unban-all", false, "一键清空所有IP封禁记录")
	banIPNginx := flag.String("banip-nginx", "", "将指定 IP 加入 Nginx 黑名单")
	unbanIPNginx := flag.String("unbanip-nginx", "", "从 Nginx 黑名单移除指定 IP")
	fileBackup := flag.String("file-backup", "", "执行文件备份: siteID:mode")
	runAutoBackup := flag.Bool("run-auto-backup", false, "手动触发自动备份（测试用）")
	showInfo := flag.Bool("info", false, "查看面板信息")
	flag.Parse()

	if *banIPNginx != "" {
		if err := executor.AddNginxBan(*banIPNginx); err != nil {
			log.Fatalf("Nginx 封禁失败: %v", err)
		}
		return
	}
	if *unbanIPNginx != "" {
		if err := executor.RemoveNginxBan(*unbanIPNginx); err != nil {
			log.Fatalf("Nginx 解封失败: %v", err)
		}
		return
	}

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	if *showInfo {
		fmt.Println("WP Panel 面板信息")
		fmt.Println("─────────────────")
		if BuildTime != "" && BuildTime != "unknown" {
			displayTime := BuildTime
			if bt, err := time.Parse(time.RFC3339, BuildTime); err == nil {
				tz := getSysTimezone()
				if loc, err := time.LoadLocation(tz); err == nil {
					displayTime = bt.In(loc).Format("2006-01-02 15:04:05")
				} else {
					displayTime = bt.Local().Format("2006-01-02 15:04:05")
				}
			}
			fmt.Printf("版本: %s (构建: %s)\n", Version, displayTime)
		} else {
			fmt.Printf("版本: %s\n", Version)
		}
		fmt.Printf("HTTPS 端口: %d\n", cfg.Panel.TLSPort)
		fmt.Printf("安全入口: /%s\n", cfg.Panel.RandomSuffix)
		fmt.Printf("数据目录: %s\n", cfg.Panel.DataDir)
		fmt.Printf("配置文件: %s\n", *configPath)
		return
	}

	if err := database.Open(cfg.SQLite.Path); err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer database.Close()

	if err := database.RunMigrations(); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}
	// 先更新插件包，确保后续迁移复制的是最新版本
	executor.EnsureCacheHelperPlugin(PluginFS)
	executor.AutoDeployPluginUpdates(PluginFS)
	if err := database.RunUpgrades(); err != nil {
		log.Fatalf("数据库升级失败: %v", err)
	}

	if *resetAdmin {
		resetAllAdmin(cfg, *configPath)
		return
	}

	if *resetPass != "" {
		resetAdminPassword(cfg, *resetPass)
		return
	}

	if *refreshWhitelist {
		executor.InitQueue(cfg)
		log.Printf("白名单刷新结果: %s", executor.RunWhitelistRefresh())
		return
	}

	if *unbanAll {
		fmt.Println(executor.UnbanAllIPs())
		return
	}

	if *fileBackup != "" {
		parts := strings.SplitN(*fileBackup, ":", 3)
		if len(parts) >= 2 {
			siteID, _ := strconv.Atoi(parts[0])
			keepCount := 3
			if len(parts) >= 3 {
				keepCount, _ = strconv.Atoi(parts[2])
			}
			if keepCount <= 0 {
				keepCount = 3
			}
			msg, err := executor.ExecuteFileBackup(siteID, parts[1], keepCount)
			if err != nil {
				log.Printf("文件备份失败: %v", err)
				os.Exit(1)
			}
			log.Println(msg)
		}
		return
	}

	if *runAutoBackup {
		executor.RunAutoBackup()
		return
	}

	seedAdminUser(cfg)

	log.Println("数据库初始化完成")

	executor.InitQueue(cfg)
	log.Println("任务队列初始化完成")

	collector.Start()

	executor.ApplyFail2banSettings()
	executor.EnsureLogMap()
	if err := executor.EnsureNginxBannedIPsConfig(); err != nil {
		log.Printf("Nginx 黑名单初始化失败: %v", err)
	}
	if err := executor.EnsureCloudflareRealIPConfig(); err != nil {
		log.Printf("Cloudflare Real IP 配置跳过: %v", err)
	}
	executor.EnsureFastCGICacheConfig()
	// 升级后重建全部 Nginx 和 PHP-FPM 配置，确保新模板规则对旧站生效
	executor.GoSafe(func() { executor.RegenerateAllSitesNginx() })
	executor.GoSafe(func() { executor.RegenerateAllSitesFPM() })
	log.Println("Nginx 日志 map 配置已就绪")
	log.Println("FastCGI 缓存配置已就绪")
	log.Println("Fail2ban 配置初始化完成")
	// WordPress safety baseline (idempotent, only writes if not present)
	executor.EnsureWordPressBaseline()
	executor.EnsureWPCommand()
	// 确保 sshpass 已安装（远程备份密码认证需要）
	if _, err := exec.LookPath("sshpass"); err != nil {
		log.Println("sshpass 未安装，正在安装...")
		exec.Command("apt-get", "update").Run()
		if err := exec.Command("apt-get", "install", "-y", "sshpass").Run(); err != nil {
			log.Printf("sshpass 安装失败，远程备份密码认证功能不可用: %v", err)
		} else {
			log.Println("sshpass 安装完成")
		}
	}
	executor.StartProcessGuard()
	executor.StartAlertMonitor(Version)
	executor.StartTelemetry(Version)
	log.Println("WordPress config baseline ensured")
	log.Println("进程守护已启动")
	log.Println("告警监控已启动")
	executor.StartAutoBackupScheduler()
	log.Println("自动备份调度器已启动")
	executor.StartSSLRenewalScheduler()
	log.Println("SSL 自动续期调度器已启动")
	go func() {
		for {
			time.Sleep(30 * time.Minute)
			middleware.GlobalSessionStore.CleanExpired()
		}
	}()

	r := router.SetupRouter(cfg, TemplatesFS, StaticFS, Version)

	if cfg.Panel.TLSPort > 0 && cfg.Panel.TLSCertPath != "" && cfg.Panel.TLSKeyPath != "" {
		go func() {
			addr := fmt.Sprintf(":%d", cfg.Panel.TLSPort)
			log.Printf("WP Panel 启动于端口 %d (HTTPS)", cfg.Panel.TLSPort)
			if err := r.RunTLS(addr, cfg.Panel.TLSCertPath, cfg.Panel.TLSKeyPath); err != nil {
				log.Fatalf("HTTPS 服务启动失败: %v", err)
			}
		}()
	} else {
		go func() {
			addr := fmt.Sprintf(":%d", cfg.Panel.Port)
			log.Printf("WP Panel 启动于端口 %d（HTTP，未配置TLS）", cfg.Panel.Port)
			if err := r.Run(addr); err != nil {
				log.Fatalf("HTTP 服务启动失败: %v", err)
			}
		}()
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("正在关闭面板...")
}

func seedAdminUser(cfg *config.Config) {
	db := database.GetDB()

	var count int
	db.QueryRow("SELECT COUNT(*) FROM admin_users").Scan(&count)
	if count > 0 {
		return
	}

	_, err := db.Exec(
		"INSERT INTO admin_users (username, password_hash) VALUES (?, ?)",
		cfg.Admin.Username, cfg.Admin.PasswordHash,
	)
	if err != nil {
		log.Printf("创建管理员用户失败: %v", err)
		return
	}
	log.Println("管理员用户已从 config.json 初始化")
}

func resetAllAdmin(cfg *config.Config, configPath string) {
	username := "wpadmin"
	webPass := randomString(16)
	basicPass := randomString(16)

	webHash, err := bcrypt.GenerateFromPassword([]byte(webPass), 12)
	if err != nil {
		fmt.Printf("错误: 密码加密失败: %v\n", err)
		os.Exit(1)
	}
	basicHash, err := bcrypt.GenerateFromPassword([]byte(basicPass), 12)
	if err != nil {
		fmt.Printf("错误: 密码加密失败: %v\n", err)
		os.Exit(1)
	}

	// Update SQLite (Web login)
	db := database.GetDB()
	var count int
	db.QueryRow("SELECT COUNT(*) FROM admin_users").Scan(&count)
	if count == 0 {
		_, err = db.Exec("INSERT INTO admin_users (username, password_hash) VALUES (?, ?)", username, string(webHash))
	} else {
		_, err = db.Exec("UPDATE admin_users SET username = ?, password_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1",
			username, string(webHash))
	}
	if err != nil {
		fmt.Printf("错误: 更新数据库失败: %v\n", err)
		os.Exit(1)
	}

	// Update config.json (BasicAuth)
	data, err := os.ReadFile(configPath)
	if err == nil {
		var cfgMap map[string]map[string]interface{}
		if json.Unmarshal(data, &cfgMap) == nil {
			if ba, ok := cfgMap["basic_auth"]; ok {
				ba["username"] = username
				ba["password_hash"] = string(basicHash)
			}
			if admin, ok := cfgMap["admin"]; ok {
				admin["username"] = username
				admin["password_hash"] = string(webHash)
			}
			if newData, err := json.MarshalIndent(cfgMap, "", "  "); err == nil {
				if err := os.WriteFile(configPath, newData, 0600); err != nil {
					fmt.Printf("错误: 写入配置文件失败: %v\n", err)
					fmt.Println("BasicAuth 密码未更新，请检查配置文件权限")
					os.Exit(1)
				}
			}
		}
	}

	fmt.Println("")
	fmt.Println("═══ 管理员账号已重置 ═══")
	fmt.Println("")
	fmt.Println("已将 BasicAuth 和面板 Web 登录的用户名统一修改为 wpadmin")
	fmt.Println("")
	fmt.Println("BasicAuth 认证（浏览器弹窗，随机入口第一层）：")
	fmt.Printf("  密码: %s\n", basicPass)
	fmt.Println("")
	fmt.Println("面板 Web 登录（页面表单，BasicAuth 通过后）：")
	fmt.Printf("  密码: %s\n", webPass)
	fmt.Println("")
	fmt.Println("⚠  登录后请在「面板设置」中修改密码")
	fmt.Println("═══ ═══════════════════ ═══")
	fmt.Println("")
	fmt.Println("正在重启面板...")
	exec.Command("systemctl", "restart", "wp-panel").Run()
}

func resetAdminPassword(cfg *config.Config, newPass string) {
	if len(newPass) < 8 {
		fmt.Println("错误: 密码至少8位")
		os.Exit(1)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPass), 12)
	if err != nil {
		fmt.Printf("错误: 密码加密失败: %v\n", err)
		os.Exit(1)
	}

	db := database.GetDB()

	var count int
	db.QueryRow("SELECT COUNT(*) FROM admin_users").Scan(&count)

	if count == 0 {
		_, err = db.Exec(
			"INSERT INTO admin_users (username, password_hash) VALUES (?, ?)",
			cfg.Admin.Username, string(hash),
		)
	} else {
		_, err = db.Exec(
			"UPDATE admin_users SET password_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1",
			string(hash),
		)
	}

	if err != nil {
		fmt.Printf("错误: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("管理员密码已重置\n")
	fmt.Printf("  用户名: %s\n", cfg.Admin.Username)
	fmt.Printf("  新密码: %s\n", newPass)
}

func randomString(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, n)
	for i := range result {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		result[i] = chars[idx.Int64()]
	}
	return string(result)
}

func getSysTimezone() string {
	out, _ := exec.Command("bash", "-c", "timedatectl show --property=Timezone --value 2>/dev/null").CombinedOutput()
	tz := strings.TrimSpace(string(out))
	if tz == "" {
		if data, err := os.ReadFile("/etc/timezone"); err == nil {
			tz = strings.TrimSpace(string(data))
		}
	}
	if tz == "" {
		return "UTC"
	}
	return tz
}
