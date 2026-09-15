package executor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

func setupWebsiteRuntimeStateTest(t *testing.T, status models.WebsiteStatus, withLink bool) (*models.Website, string) {
	t.Helper()
	openTestDB(t)
	root := t.TempDir()
	cfg := &config.Config{Paths: config.PathsConfig{
		NginxSitesAvailable: filepath.Join(root, "available"),
		NginxSitesEnabled:   filepath.Join(root, "enabled"),
	}}
	for _, dir := range []string{cfg.Paths.NginxSitesAvailable, cfg.Paths.NginxSitesEnabled} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	domain := "runtime.example.com"
	nginxConf := filepath.Join(cfg.Paths.NginxSitesAvailable, domain+".conf")
	if err := os.WriteFile(nginxConf, []byte("server {}"), 0644); err != nil {
		t.Fatal(err)
	}
	enabledPath := filepath.Join(cfg.Paths.NginxSitesEnabled, domain+".conf")
	if withLink {
		if err := os.Symlink(nginxConf, enabledPath); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.GetDB().Exec(`INSERT INTO websites (id,name,domain,status,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path)
		VALUES (81,'runtime',?,?, 'u','/www/runtime','/logs/runtime','db','u','/php/runtime',?)`, domain, status, nginxConf); err != nil {
		t.Fatal(err)
	}
	oldCfg := config.AppConfig
	oldChange, oldReload := changeWebsiteStatus, runWebsiteStateNginxReload
	config.AppConfig = cfg
	t.Cleanup(func() {
		config.AppConfig = oldCfg
		changeWebsiteStatus, runWebsiteStateNginxReload = oldChange, oldReload
	})
	return &models.Website{ID: 81, Domain: domain, Status: status, NginxConfPath: nginxConf}, enabledPath
}

func TestPauseSiteDatabaseFailureLeavesNginxUntouched(t *testing.T) {
	site, enabledPath := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	changeWebsiteStatus = func(int, models.WebsiteStatus, models.WebsiteStatus) error { return errors.New("database failed") }
	reloadCalled := false
	runWebsiteStateNginxReload = func() ([]byte, error) { reloadCalled = true; return nil, nil }
	result := executePauseSite(&Task{Payload: &PauseSitePayload{Site: site}})
	if result.Success || reloadCalled {
		t.Fatalf("result=%+v reloadCalled=%v", result, reloadCalled)
	}
	if _, err := os.Lstat(enabledPath); err != nil {
		t.Fatalf("enabled link changed: %v", err)
	}
}

func TestPauseAndEnableSiteCommitMatchingDatabaseAndLinkState(t *testing.T) {
	site, enabledPath := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	runWebsiteStateNginxReload = func() ([]byte, error) { return nil, nil }
	if result := executePauseSite(&Task{Payload: &PauseSitePayload{Site: site}}); !result.Success {
		t.Fatalf("pause result=%+v", result)
	}
	var status string
	if err := database.GetDB().QueryRow("SELECT status FROM websites WHERE id=81").Scan(&status); err != nil || status != "paused" {
		t.Fatalf("paused status=%q err=%v", status, err)
	}
	if _, err := os.Lstat(enabledPath); !os.IsNotExist(err) {
		t.Fatalf("enabled link remains after pause: %v", err)
	}
	site.Status = models.StatusPaused
	if result := executeEnableSite(&Task{Payload: &EnableSitePayload{Site: site}}); !result.Success {
		t.Fatalf("enable result=%+v", result)
	}
	if err := database.GetDB().QueryRow("SELECT status FROM websites WHERE id=81").Scan(&status); err != nil || status != "active" {
		t.Fatalf("active status=%q err=%v", status, err)
	}
	if target, err := os.Readlink(enabledPath); err != nil || filepath.Clean(target) != filepath.Clean(site.NginxConfPath) {
		t.Fatalf("enabled target=%q err=%v", target, err)
	}
}

func TestPauseSiteReloadFailureRestoresStatusAndLink(t *testing.T) {
	site, enabledPath := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	var transitions [][2]models.WebsiteStatus
	changeWebsiteStatus = func(_ int, from, to models.WebsiteStatus) error {
		transitions = append(transitions, [2]models.WebsiteStatus{from, to})
		return nil
	}
	call := 0
	runWebsiteStateNginxReload = func() ([]byte, error) {
		call++
		if call == 1 {
			return []byte("failed"), errors.New("reload failed")
		}
		return nil, nil
	}
	result := executePauseSite(&Task{Payload: &PauseSitePayload{Site: site}})
	if result.Success || len(transitions) != 2 || transitions[1] != [2]models.WebsiteStatus{models.StatusPaused, models.StatusActive} {
		t.Fatalf("result=%+v transitions=%v", result, transitions)
	}
	if _, err := os.Lstat(enabledPath); err != nil {
		t.Fatalf("enabled link was not restored: %v", err)
	}
}

func TestEnableSiteReloadFailureRestoresStatusAndDisabledLink(t *testing.T) {
	site, enabledPath := setupWebsiteRuntimeStateTest(t, models.StatusPaused, false)
	var transitions [][2]models.WebsiteStatus
	changeWebsiteStatus = func(_ int, from, to models.WebsiteStatus) error {
		transitions = append(transitions, [2]models.WebsiteStatus{from, to})
		return nil
	}
	call := 0
	runWebsiteStateNginxReload = func() ([]byte, error) {
		call++
		if call == 1 {
			return []byte("failed"), errors.New("reload failed")
		}
		return nil, nil
	}
	result := executeEnableSite(&Task{Payload: &EnableSitePayload{Site: site}})
	if result.Success || len(transitions) != 2 || transitions[1] != [2]models.WebsiteStatus{models.StatusActive, models.StatusPaused} {
		t.Fatalf("result=%+v transitions=%v", result, transitions)
	}
	if _, err := os.Lstat(enabledPath); !os.IsNotExist(err) {
		t.Fatalf("enabled link remains after rollback: %v", err)
	}
}

func TestWebsiteRuntimeStateReportsRecoveryFailure(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusPaused, false)
	call := 0
	changeWebsiteStatus = func(int, models.WebsiteStatus, models.WebsiteStatus) error {
		call++
		if call == 2 {
			return errors.New("restore failed")
		}
		return nil
	}
	runWebsiteStateNginxReload = func() ([]byte, error) { return nil, errors.New("reload failed") }
	result := executeEnableSite(&Task{Payload: &EnableSitePayload{Site: site}})
	if result.Success || !strings.Contains(result.Message, "恢复未完成") {
		t.Fatalf("result=%+v", result)
	}
}

func TestEnableSiteRejectsNonPausedState(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusError, false)
	result := executeEnableSite(&Task{Payload: &EnableSitePayload{Site: site}})
	if result.Success || !strings.Contains(result.Message, "不能执行启用") {
		t.Fatalf("result=%+v", result)
	}
}

func TestUpdateWebsiteStatusChecksOriginalState(t *testing.T) {
	setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	if err := updateWebsiteStatus(81, models.StatusPaused, models.StatusActive); err == nil {
		t.Fatal("stale original status unexpectedly updated")
	}
	if err := updateWebsiteStatus(81, models.StatusActive, models.StatusPaused); err != nil {
		t.Fatal(err)
	}
}

func TestEnableSiteSSLWarning(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.Local)
	expired := now.Add(-time.Hour)
	soon := now.AddDate(0, 0, 20)
	later := now.AddDate(0, 0, 31)

	for name, tc := range map[string]struct {
		site *models.Website
		want string
	}{
		"ssl disabled": {site: &models.Website{}, want: ""},
		"expired":      {site: &models.Website{SSLEnabled: true, SSLExpiresAt: &expired}, want: "证书已过期"},
		"expiring":     {site: &models.Website{SSLEnabled: true, SSLExpiresAt: &soon}, want: "2026-10-04"},
		"healthy":      {site: &models.Website{SSLEnabled: true, SSLExpiresAt: &later}, want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			got := enableSiteSSLWarning(tc.site, now)
			if tc.want == "" && got != "" {
				t.Fatalf("warning=%q, want empty", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("warning=%q, want containing %q", got, tc.want)
			}
		})
	}
}
