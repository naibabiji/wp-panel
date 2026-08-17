package main

import (
	"context"
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

const wpCoreUpdateWorkerShutdownTimeout = 30 * time.Second

type wpCoreUpdateWorkerLifecycle interface {
	Start() error
	Stop(context.Context) error
}

type wpCoreUpdateWorkerFactory func(*config.Config) (wpCoreUpdateWorkerLifecycle, error)

func main() {
	configPath := flag.String("config", "/www/server/panel/config.json", "配置文件路径")
	resetPass := flag.String("passwd", "", "重置管理员密码（8位以上）")
	resetAdmin := flag.Bool("reset-admin", false, "一键重置管理员账号密码")
	refreshWhitelist := flag.Bool("refresh-whitelist", false, "手动触发白名单刷新")
	unbanAll := flag.Bool("unban-all", false, "一键清空所有IP封禁记录")
	banIPNginx := flag.String("banip-nginx", "", "将指定 IP 加入 Nginx 黑名单")
	unbanIPNginx := flag.String("unbanip-nginx", "", "从 Nginx 黑名单移除指定 IP")
	recordFail2banIP := flag.String("record-fail2ban", "", "记录 Fail2ban 封禁 IP")
	banJail := flag.String("ban-jail", "", "Fail2ban jail 名称")
	banTime := flag.Int("ban-bantime", 0, "Fail2ban 本次封禁秒数")
	banCount := flag.Int("ban-count", 0, "Fail2ban 累计封禁次数")
	banRestored := flag.Bool("ban-restored", false, "Fail2ban 重启恢复事件")
	unbanFail2banIP := flag.String("unban-fail2ban", "", "记录 Fail2ban 解封 IP")
	fileBackup := flag.String("file-backup", "", "执行文件备份: siteID:mode")
	runAutoBackup := flag.Bool("run-auto-backup", false, "手动触发自动备份（测试用）")
	showInfo := flag.Bool("info", false, "查看面板信息")
	repairConfigCheck := flag.Bool("repair-config-check", false, "内部使用：只读校验 repair 配置")
	updateWatchdog := flag.String("update-watchdog", "", "内部使用：面板更新健康检查守护")
	flag.Parse()

	if *repairConfigCheck {
		result, err := config.CheckRepairConfig(*configPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			fmt.Fprintln(os.Stderr, "repair_config_output_failed")
			os.Exit(1)
		}
		return
	}

	if *banIPNginx != "" || *unbanIPNginx != "" || *recordFail2banIP != "" || *unbanFail2banIP != "" {
		handleFail2banCLI(*configPath, *banIPNginx, *unbanIPNginx, *recordFail2banIP, *unbanFail2banIP, *banJail, *banTime, *banCount, *banRestored)
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

	if *updateWatchdog != "" {
		executor.RunUpdateWatchdog(cfg, *updateWatchdog)
		return
	}

	if err := database.RunMigrations(); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}
	// 先更新插件包，确保后续迁移复制的是最新版本
	executor.EnsureCacheHelperPlugin(PluginFS)
	executor.AutoDeployPluginUpdates(PluginFS)
	// 异步补装图片优化功能需要的 PHP 扩展和系统二进制，不阻塞启动；装好之后
	// 相应功能会在下次使用时自动感知到，不需要面板重启。
	go executor.EnsurePHPExifExtension()
	go executor.EnsureImageBatchBinaries()
	if err := database.RunUpgrades(); err != nil {
		log.Fatalf("数据库升级失败: %v", err)
	}
	executor.ResetStuckImageOptimizationJobs()
	executor.FinalizePendingPanelUpdate(cfg, Version)

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
	var inventoryWorker *executor.WPInventoryWorker
	var inventoryScheduler *executor.WPInventoryScheduler
	var coreUpdateWorker wpCoreUpdateWorkerLifecycle
	if candidate, err := executor.NewWPInventoryWorker(cfg); err != nil {
		log.Println("WordPress 库存后台任务未启动")
	} else if err := candidate.Start(); err != nil {
		log.Println("WordPress 库存后台任务未启动")
	} else {
		inventoryWorker = candidate
		log.Println("WordPress 库存后台任务已启动")
		if scheduler, err := executor.NewWPInventoryScheduler(); err != nil {
			log.Println("WordPress 库存自动刷新未启动")
		} else if err := scheduler.Start(); err != nil {
			log.Println("WordPress 库存自动刷新未启动")
		} else {
			inventoryScheduler = scheduler
			log.Println("WordPress 库存自动刷新已启动")
		}
	}
	if candidate, err := startWPCoreUpdateWorker(cfg, func(cfg *config.Config) (wpCoreUpdateWorkerLifecycle, error) {
		return executor.NewWPCoreUpdateWorker(cfg)
	}); err != nil {
		log.Println("WordPress 更新后台任务未启动")
	} else {
		coreUpdateWorker = candidate
		log.Println("WordPress 更新后台任务已启动")
	}

	collector.Start()

	executor.ApplyFail2banSettings()
	executor.EnsureOperationLogRetention()
	if err := executor.ApplyRateLimitSettings(); err != nil {
		log.Printf("Nginx 限速配置跳过: %v", err)
	}
	if err := executor.EnsureLogMap(); err != nil {
		log.Printf("Nginx 日志 map 配置跳过: %v", err)
	}
	executor.EnsureAllSiteLogrotateConfigs()
	if err := executor.EnsureNginxBannedIPsConfig(); err != nil {
		log.Printf("Nginx 黑名单初始化失败: %v", err)
	}
	if err := executor.EnsureCloudflareRealIPConfig(); err != nil {
		log.Printf("Cloudflare Real IP 配置跳过: %v", err)
	} else if err := executor.ApplyFail2banSettings(); err != nil {
		log.Printf("Cloudflare Real IP 白名单应用跳过: %v", err)
	}
	executor.EnsureFastCGICacheConfig()
	// WordPress safety baseline (idempotent, only writes if not present)
	executor.EnsureWordPressBaseline()
	// 升级后重建全部 Nginx 和 PHP-FPM 配置，确保新模板规则对旧站生效
	executor.GoSafe(func() {
		if err := executor.RegenerateAllSitesNginx(); err != nil {
			log.Printf("Nginx 批量重建部分失败: %v", err)
		}
	})
	executor.GoSafe(func() {
		if err := executor.RegenerateAllSitesFPM(); err != nil {
			log.Printf("PHP-FPM batch rebuild partially failed: %v", err)
		}
	})
	log.Println("Nginx 日志 map 配置已就绪")
	log.Println("FastCGI 缓存配置已就绪")
	log.Println("Fail2ban 配置初始化完成")
	executor.EnsureWPCommand()
	// 远程备份密码认证依赖 sshpass；启动路径只提示，不自动修改服务器软件状态。
	if _, err := exec.LookPath("sshpass"); err != nil {
		log.Println("sshpass 未安装，远程备份密码认证功能不可用；请通过安装脚本或包管理器手动安装")
	}
	executor.StartProcessGuard()
	executor.StartAlertMonitor(Version)
	executor.StartOOMMonitor()
	executor.StartTelemetry(Version)
	executor.StartPanelAutoUpdateScheduler(Version, *configPath, cfg)
	log.Println("WordPress config baseline ensured")
	log.Println("进程守护已启动")
	log.Println("告警监控已启动")
	log.Println("OOM 事故监控已启动")
	executor.StartAutoBackupScheduler()
	log.Println("自动备份调度器已启动")
	executor.StartRemoteBackupMaintenanceScheduler()
	log.Println("远程备份自动维护调度器已启动")
	executor.StartDBBackupScheduler()
	log.Println("面板数据库备份调度器已启动")
	executor.StartSSLRenewalScheduler()
	log.Println("SSL 自动续期调度器已启动")
	executor.StartWPSecurityEventIngestor()
	log.Println("WordPress 安全探测事件持久化调度器已启动")
	executor.StartFail2banSyncScheduler()
	log.Println("Fail2ban 封禁状态同步调度器已启动")
	go func() {
		for {
			time.Sleep(30 * time.Minute)
			middleware.GlobalSessionStore.CleanExpired()
		}
	}()

	r := router.SetupRouter(cfg, TemplatesFS, StaticFS, Version, *configPath)

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
	executor.GlobalAdminer.DisableAll()
	if coreUpdateWorker != nil {
		if err := stopWPCoreUpdateWorker(coreUpdateWorker, wpCoreUpdateWorkerShutdownTimeout); err != nil {
			log.Println("WordPress 核心更新后台任务关闭超时")
		}
	}
	if inventoryScheduler != nil {
		if err := inventoryScheduler.Stop(context.Background()); err != nil {
			log.Println("WordPress 库存自动刷新关闭失败")
		}
	}
	if inventoryWorker != nil {
		if err := inventoryWorker.Stop(context.Background()); err != nil {
			log.Println("WordPress 库存后台任务关闭失败")
		}
	}
	executor.StopWPSecurityEventIngestor()
}

func startWPCoreUpdateWorker(cfg *config.Config, factory wpCoreUpdateWorkerFactory) (wpCoreUpdateWorkerLifecycle, error) {
	if cfg == nil || factory == nil {
		return nil, fmt.Errorf("invalid core update worker startup")
	}
	worker, err := factory(cfg)
	if err != nil {
		return nil, err
	}
	if worker == nil {
		return nil, fmt.Errorf("core update worker unavailable")
	}
	if err := worker.Start(); err != nil {
		return nil, err
	}
	return worker, nil
}

func stopWPCoreUpdateWorker(worker wpCoreUpdateWorkerLifecycle, timeout time.Duration) error {
	if worker == nil || timeout <= 0 {
		return fmt.Errorf("invalid core update worker shutdown")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return worker.Stop(ctx)
}

func handleFail2banCLI(configPath, banIP, unbanIP, recordIP, unbanRecordIP, jail string, banTime, banCount int, restored bool) {
	if banIP != "" {
		if err := executor.AddNginxBan(banIP); err != nil {
			log.Fatalf("Nginx 封禁失败: %v", err)
		}
	}
	if unbanIP != "" {
		if err := executor.RemoveNginxBan(unbanIP); err != nil {
			log.Fatalf("Nginx 解封失败: %v", err)
		}
	}
	if recordIP == "" && unbanRecordIP == "" {
		return
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if err := database.Open(cfg.SQLite.Path); err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer database.Close()
	if err := database.RunMigrations(); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}
	if recordIP != "" {
		if err := executor.RecordFail2banBan(recordIP, jail, banTime, banCount, restored); err != nil {
			log.Fatalf("Fail2ban 封禁记录失败: %v", err)
		}
	}
	if unbanRecordIP != "" {
		if err := executor.RecordFail2banUnban(unbanRecordIP, jail); err != nil {
			log.Fatalf("Fail2ban 解封记录失败: %v", err)
		}
	}
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
