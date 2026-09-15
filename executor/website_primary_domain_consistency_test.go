package executor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

func setupPrimaryDomainTest(t *testing.T) (*models.Website, string) {
	t.Helper()
	openTestDB(t)
	root := t.TempDir()
	oldCfg := config.AppConfig
	oldSecretsRoot := siteSecretsRoot
	oldNginxCustomDir := nginxCustomDir
	oldApplyPHP, oldApplyNginx := applyPrimaryDomainPHP, applyPrimaryDomainNginx
	oldChange := changeWebsitePrimaryDomain
	oldReloadPHP, oldReloadNginx := reloadPrimaryDomainPHP, reloadPrimaryDomainNginx
	oldUpdateURLs := updatePrimaryDomainWPSiteURLs
	oldReadURLs := readPrimaryDomainWPSiteURLs
	config.AppConfig = &config.Config{
		Panel: config.PanelConfig{BackupDir: filepath.Join(root, "backups")},
		Paths: config.PathsConfig{
			WWWRoot: filepath.Join(root, "www"), WWWLogs: filepath.Join(root, "logs"),
			Certificates: filepath.Join(root, "certs"), NginxSitesAvailable: filepath.Join(root, "available"),
			NginxSitesEnabled: filepath.Join(root, "enabled"), PHPFPMSock: "/run/php",
		},
	}
	siteSecretsRoot = filepath.Join(root, "secrets")
	nginxCustomDir = filepath.Join(root, "nginx-custom")
	t.Cleanup(func() {
		config.AppConfig = oldCfg
		siteSecretsRoot = oldSecretsRoot
		nginxCustomDir = oldNginxCustomDir
		applyPrimaryDomainPHP, applyPrimaryDomainNginx = oldApplyPHP, oldApplyNginx
		changeWebsitePrimaryDomain = oldChange
		reloadPrimaryDomainPHP, reloadPrimaryDomainNginx = oldReloadPHP, oldReloadNginx
		updatePrimaryDomainWPSiteURLs = oldUpdateURLs
		readPrimaryDomainWPSiteURLs = oldReadURLs
	})

	oldDomain := "old.example.com"
	newDomain := "new.example.com"
	oldWebRoot := filepath.Join(config.AppConfig.Paths.WWWRoot, oldDomain)
	oldLogDir := filepath.Join(config.AppConfig.Paths.WWWLogs, oldDomain)
	nginxPath := filepath.Join(config.AppConfig.Paths.NginxSitesAvailable, oldDomain+".conf")
	phpPath := filepath.Join(root, "php", oldDomain+".conf")
	for _, dir := range []string{oldWebRoot, oldLogDir, filepath.Dir(nginxPath), filepath.Dir(phpPath), config.AppConfig.Paths.NginxSitesEnabled, filepath.Join(config.AppConfig.Panel.BackupDir, oldDomain), nginxCustomDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(nginxPath, []byte("old nginx"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(phpPath, []byte("old php"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.AppConfig.Panel.BackupDir, oldDomain, "backup.tar"), []byte("backup"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nginxCustomDir, oldDomain+".conf"), []byte("custom"), 0644); err != nil {
		t.Fatal(err)
	}
	enabledPath := nginxEnabledPath(config.AppConfig, nginxPath, oldDomain)
	if err := os.Symlink(nginxPath, enabledPath); err != nil {
		t.Fatal(err)
	}
	site := &models.Website{
		ID: 92, Domain: oldDomain, Status: models.StatusActive, SiteType: "wordpress", SystemUser: "siteuser",
		WebRoot: oldWebRoot, LogDir: oldLogDir, NginxConfPath: nginxPath, PHPPoolPath: phpPath,
		DBName: "wpdb", DBUser: "wpuser", TablePrefix: "wp_", PHPFPMMaxChildren: 10,
	}
	if _, err := database.GetDB().Exec(`INSERT INTO websites
		(id,name,domain,aliases,status,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path,php_fpm_max_children)
		VALUES (92,'domain-test',?,'','active','wordpress','siteuser',?,?,'wpdb','wpuser',?,?,10)`,
		oldDomain, oldWebRoot, oldLogDir, phpPath, nginxPath); err != nil {
		t.Fatal(err)
	}
	reloadPrimaryDomainPHP = func() error { return nil }
	reloadPrimaryDomainNginx = func() error { return nil }
	applyPrimaryDomainPHP = func(*TemplateEngine, string, string, string, string) error { return nil }
	applyPrimaryDomainNginx = func(_ *TemplateEngine, content, target, enabled string) error {
		if err := os.WriteFile(target, []byte(content), 0644); err != nil {
			return err
		}
		_ = os.Remove(enabled)
		return os.Symlink(target, enabled)
	}
	return site, newDomain
}

func runPrimaryDomainUpdate(site *models.Website, newDomain string) TaskResult {
	return executeUpdateDomains(&Task{Payload: &UpdateDomainsPayload{Site: site, NewDomain: newDomain}})
}

func TestPrimaryDomainPreflightRejectsOccupiedTargetWithoutChanges(t *testing.T) {
	site, newDomain := setupPrimaryDomainTest(t)
	target := filepath.Join(config.AppConfig.Paths.WWWRoot, newDomain)
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	result := runPrimaryDomainUpdate(site, newDomain)
	if result.Success || !strings.Contains(result.Message, "预检查") {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat(site.WebRoot); err != nil {
		t.Fatalf("old web root changed: %v", err)
	}
	var domain string
	if err := database.GetDB().QueryRow("SELECT domain FROM websites WHERE id=92").Scan(&domain); err != nil || domain != "old.example.com" {
		t.Fatalf("domain=%q err=%v", domain, err)
	}
}

func TestPrimaryDomainDatabaseFailureRestoresFilesystemAndConfig(t *testing.T) {
	site, newDomain := setupPrimaryDomainTest(t)
	changeWebsitePrimaryDomain = func(int, string, *models.Website) error { return errors.New("database failed") }
	result := runPrimaryDomainUpdate(site, newDomain)
	if result.Success {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat(site.WebRoot); err != nil {
		t.Fatalf("old web root not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.AppConfig.Paths.WWWRoot, newDomain)); !os.IsNotExist(err) {
		t.Fatalf("new web root remains: %v", err)
	}
	content, err := os.ReadFile(site.NginxConfPath)
	if err != nil || string(content) != "old nginx" {
		t.Fatalf("nginx=%q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(config.AppConfig.Panel.BackupDir, "old.example.com", "backup.tar")); err != nil {
		t.Fatalf("old backup not restored: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(nginxCustomDir, "old.example.com.conf")); err != nil || string(content) != "custom" {
		t.Fatalf("old custom nginx not restored: content=%q err=%v", content, err)
	}
}

func TestPrimaryDomainNginxFailureRestoresMovedResources(t *testing.T) {
	site, newDomain := setupPrimaryDomainTest(t)
	applyPrimaryDomainNginx = func(*TemplateEngine, string, string, string) error { return errors.New("nginx failed") }
	result := runPrimaryDomainUpdate(site, newDomain)
	if result.Success || !strings.Contains(result.Message, "Nginx") {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat(site.WebRoot); err != nil {
		t.Fatalf("old web root not restored: %v", err)
	}
	if _, err := os.Stat(site.LogDir); err != nil {
		t.Fatalf("old log dir not restored: %v", err)
	}
}

func TestPrimaryDomainWordPressURLVerificationFailureRollsBack(t *testing.T) {
	site, newDomain := setupPrimaryDomainTest(t)
	updatePrimaryDomainWPSiteURLs = func(string, string, string, string, *config.Config) error { return nil }
	readPrimaryDomainWPSiteURLs = func(string, string, *config.Config) (string, string, error) {
		return "https://old.example.com", "https://old.example.com", nil
	}
	result := executeUpdateDomains(&Task{Payload: &UpdateDomainsPayload{
		Site: site, NewDomain: newDomain,
		OldWPSiteURL: "https://old.example.com", OldWPHomeURL: "https://old.example.com",
		NewWPSiteURL: "https://new.example.com", NewWPHomeURL: "https://new.example.com",
	}})
	if result.Success || !strings.Contains(result.Message, "验证 WordPress") {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat(site.WebRoot); err != nil {
		t.Fatalf("old web root not restored: %v", err)
	}
}

func TestPrimaryDomainReportsRollbackFailure(t *testing.T) {
	site, newDomain := setupPrimaryDomainTest(t)
	changeWebsitePrimaryDomain = func(int, string, *models.Website) error { return errors.New("database failed") }
	reloadPrimaryDomainPHP = func() error { return errors.New("reload failed") }
	result := runPrimaryDomainUpdate(site, newDomain)
	if result.Success || !strings.Contains(result.Message, "状态恢复不完整") || !strings.Contains(result.Message, "PHP-FPM") {
		t.Fatalf("result=%+v", result)
	}
}

func TestPrimaryDomainSuccessPersistsAndVerifies(t *testing.T) {
	site, newDomain := setupPrimaryDomainTest(t)
	result := runPrimaryDomainUpdate(site, newDomain)
	if !result.Success || site.Domain != newDomain {
		t.Fatalf("result=%+v site=%+v", result, site)
	}
	var domain, webRoot, logDir string
	if err := database.GetDB().QueryRow("SELECT domain,web_root,log_dir FROM websites WHERE id=92").Scan(&domain, &webRoot, &logDir); err != nil {
		t.Fatal(err)
	}
	if domain != newDomain || webRoot != filepath.Join(config.AppConfig.Paths.WWWRoot, newDomain) || logDir != filepath.Join(config.AppConfig.Paths.WWWLogs, newDomain) {
		t.Fatalf("domain=%q webRoot=%q logDir=%q", domain, webRoot, logDir)
	}
	if data, err := os.ReadFile(filepath.Join(config.AppConfig.Panel.BackupDir, newDomain, "backup.tar")); err != nil || string(data) != "backup" {
		t.Fatalf("backup not moved: data=%q err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(nginxCustomDir, newDomain+".conf")); err != nil || string(data) != "custom" {
		t.Fatalf("custom nginx not moved: data=%q err=%v", data, err)
	}
}

func TestUpdateWebsitePrimaryDomainRejectsStaleDomain(t *testing.T) {
	site, _ := setupPrimaryDomainTest(t)
	proposed := *site
	proposed.Domain = "new.example.com"
	if err := updateWebsitePrimaryDomain(site.ID, "stale.example.com", &proposed); err == nil {
		t.Fatal("stale domain unexpectedly overwritten")
	}
}
