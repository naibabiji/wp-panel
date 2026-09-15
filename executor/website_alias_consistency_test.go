package executor

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

func setupWebsiteAliasTest(t *testing.T) *models.Website {
	t.Helper()
	openTestDB(t)
	root := t.TempDir()
	oldCfg := config.AppConfig
	oldChange, oldApply := changeWebsiteAliases, applyAliasNginx
	config.AppConfig = &config.Config{
		Panel: config.PanelConfig{BackupDir: filepath.Join(root, "backups")},
		Paths: config.PathsConfig{
			NginxSitesAvailable: filepath.Join(root, "available"),
			NginxSitesEnabled:   filepath.Join(root, "enabled"),
			PHPFPMSock:          filepath.Join(root, "php"),
		},
	}
	t.Cleanup(func() {
		config.AppConfig = oldCfg
		changeWebsiteAliases, applyAliasNginx = oldChange, oldApply
	})
	site := &models.Website{
		ID: 91, Domain: "example.com", Aliases: "www.example.com", Status: models.StatusActive,
		SiteType: "wordpress", WebRoot: filepath.Join(root, "www"), LogDir: filepath.Join(root, "logs"),
		NginxConfPath: filepath.Join(root, "available", "example.com.conf"), PHPPoolPath: filepath.Join(root, "php", "example.com.conf"),
	}
	if _, err := database.GetDB().Exec(`INSERT INTO websites
		(id,name,domain,aliases,status,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path)
		VALUES (91,'alias','example.com','www.example.com','active','wordpress','u',?,'','db','u',?,?)`,
		site.WebRoot, site.PHPPoolPath, site.NginxConfPath); err != nil {
		t.Fatal(err)
	}
	return site
}

func runAliasUpdate(site *models.Website) TaskResult {
	return executeUpdateDomains(&Task{Payload: &UpdateDomainsPayload{
		Site: site, NewDomain: site.Domain, Aliases: []string{"cdn.example.com"},
	}})
}

func TestAliasUpdateDatabaseFailureDoesNotApplyNginx(t *testing.T) {
	site := setupWebsiteAliasTest(t)
	changeWebsiteAliases = func(int, string, string) error { return errors.New("database failed") }
	nginxCalled := false
	applyAliasNginx = func(*TemplateEngine, string, string, string) error { nginxCalled = true; return nil }
	result := runAliasUpdate(site)
	if result.Success || nginxCalled || site.Aliases != "www.example.com" {
		t.Fatalf("result=%+v nginxCalled=%v aliases=%q", result, nginxCalled, site.Aliases)
	}
}

func TestAliasUpdateNginxFailureRestoresDatabase(t *testing.T) {
	site := setupWebsiteAliasTest(t)
	var transitions [][2]string
	changeWebsiteAliases = func(_ int, from, to string) error {
		transitions = append(transitions, [2]string{from, to})
		return nil
	}
	applyAliasNginx = func(*TemplateEngine, string, string, string) error { return errors.New("nginx failed") }
	result := runAliasUpdate(site)
	if result.Success || len(transitions) != 2 || transitions[1] != [2]string{"cdn.example.com", "www.example.com"} {
		t.Fatalf("result=%+v transitions=%v", result, transitions)
	}
}

func TestAliasUpdateReportsRecoveryFailure(t *testing.T) {
	site := setupWebsiteAliasTest(t)
	call := 0
	changeWebsiteAliases = func(int, string, string) error {
		call++
		if call == 2 {
			return errors.New("restore failed")
		}
		return nil
	}
	applyAliasNginx = func(*TemplateEngine, string, string, string) error { return errors.New("nginx failed") }
	result := runAliasUpdate(site)
	if result.Success || !strings.Contains(result.Message, "状态恢复失败") {
		t.Fatalf("result=%+v", result)
	}
}

func TestAliasUpdateSuccessPersistsAndApplies(t *testing.T) {
	site := setupWebsiteAliasTest(t)
	nginxCalled := false
	applyAliasNginx = func(*TemplateEngine, string, string, string) error { nginxCalled = true; return nil }
	result := runAliasUpdate(site)
	if !result.Success || !nginxCalled {
		t.Fatalf("result=%+v nginxCalled=%v", result, nginxCalled)
	}
	var aliases string
	if err := database.GetDB().QueryRow("SELECT aliases FROM websites WHERE id=91").Scan(&aliases); err != nil || aliases != "cdn.example.com" {
		t.Fatalf("aliases=%q err=%v", aliases, err)
	}
}

func TestUpdateWebsiteAliasesRejectsStaleValue(t *testing.T) {
	setupWebsiteAliasTest(t)
	if err := updateWebsiteAliases(91, "stale.example.com", "new.example.com"); err == nil {
		t.Fatal("stale aliases unexpectedly overwritten")
	}
}
