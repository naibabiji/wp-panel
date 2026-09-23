package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeMigrationTargetPublishOps struct {
	events []string
	failAt string
}

type fakeMigrationTargetConfigureOps struct {
	events []string
	failAt string
}

func (f *fakeMigrationTargetConfigureOps) step(event string) error {
	f.events = append(f.events, event)
	if f.failAt == event {
		return errors.New("injected " + event)
	}
	return nil
}
func (f *fakeMigrationTargetConfigureOps) PublishIdentity(_, _, _ string, ssl bool) (siteMigrationPublishedIdentity, error) {
	if err := f.step("identity"); err != nil {
		return siteMigrationPublishedIdentity{}, err
	}
	return siteMigrationPublishedIdentity{SecretPath: "/var/wp-panel/site-secrets/example.com/wp-panel-config.json", CertPath: "/cert/example.com/fullchain.pem", KeyPath: "/cert/example.com/privkey.pem", SSLEnabled: ssl}, nil
}
func (f *fakeMigrationTargetConfigureOps) ResetIdentity(_ string, _ bool) error {
	return f.step("reset_identity")
}
func (f *fakeMigrationTargetConfigureOps) ApplyConfigs(_ siteMigrationPublishSpec, _ SiteMigrationRuntimeSettings, _ siteMigrationPublishedIdentity, maxChildren int) error {
	if maxChildren <= 0 {
		return errors.New("invalid max children")
	}
	return f.step("configs")
}
func (f *fakeMigrationTargetConfigureOps) ResetRuntime(siteMigrationPublishSpec) error {
	return f.step("reset_runtime")
}
func (f *fakeMigrationTargetConfigureOps) Health(_ context.Context, _ siteMigrationPublishSpec, _ siteMigrationPublishedIdentity) error {
	return f.step("health")
}

func (f *fakeMigrationTargetPublishOps) step(event string) error {
	f.events = append(f.events, event)
	if f.failAt == event {
		return errors.New("injected " + event)
	}
	return nil
}
func (f *fakeMigrationTargetPublishOps) ImportDatabase(_ context.Context, _, database string) error {
	return f.step("import:" + database)
}
func (f *fakeMigrationTargetPublishOps) ResetDatabase(database, user, _ string) error {
	return f.step("reset-db:" + database + ":" + user)
}
func (f *fakeMigrationTargetPublishOps) CopyTree(_, target string) error {
	return f.step("copy:" + target)
}
func (f *fakeMigrationTargetPublishOps) ResetTree(target string) error {
	return f.step("reset-tree:" + target)
}
func (f *fakeMigrationTargetPublishOps) RewriteWordPressConfig(_ string, _ string, identity siteMigrationDatabaseIdentity) error {
	return f.step("wp-config:" + identity.Database)
}
func (f *fakeMigrationTargetPublishOps) SetOwner(_, owner string) error {
	return f.step("owner:" + owner)
}

func TestSiteMigrationTargetPublisherOrdersDataPublication(t *testing.T) {
	resourceService, _, root := setupMigrationTargetResourceServiceTest(t)
	if err := resourceService.Create(context.Background(), "migration_0000001", SiteMigrationTargetSpec{Domain: "example.com", SiteType: "wordpress"}); err != nil {
		t.Fatal(err)
	}
	publisher, err := NewSiteMigrationTargetPublisher(resourceService.db, resourceService.cfg, root)
	if err != nil {
		t.Fatal(err)
	}
	ops := &fakeMigrationTargetPublishOps{}
	publisher.ops = ops
	publisher.now = freezerTestTime
	if err := publisher.PublishData(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	want := []string{"import:" + opsDatabaseName(t, resourceService), "copy:" + opsWebRoot(t, resourceService), "wp-config:" + opsDatabaseName(t, resourceService), "owner:" + opsSystemUser(t, resourceService)}
	if strings.Join(ops.events, "|") != strings.Join(want, "|") {
		t.Fatalf("events=%v want=%v", ops.events, want)
	}
	var stage string
	_ = resourceService.db.QueryRow(`SELECT stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&stage)
	if stage != "configuring_target" {
		t.Fatalf("stage=%q", stage)
	}
	var published int
	_ = resourceService.db.QueryRow(`SELECT COUNT(*) FROM site_migration_resources WHERE migration_site_id='migration_0000001' AND resource_type IN ('database_import','file_publish') AND status='published'`).Scan(&published)
	if published != 2 {
		t.Fatalf("published=%d", published)
	}
}

func TestSiteMigrationTargetPublisherLeavesImportIntentOnFailure(t *testing.T) {
	resourceService, _, root := setupMigrationTargetResourceServiceTest(t)
	if err := resourceService.Create(context.Background(), "migration_0000001", SiteMigrationTargetSpec{Domain: "example.com", SiteType: "wordpress"}); err != nil {
		t.Fatal(err)
	}
	publisher, _ := NewSiteMigrationTargetPublisher(resourceService.db, resourceService.cfg, root)
	ops := &fakeMigrationTargetPublishOps{failAt: "import:" + opsDatabaseName(t, resourceService)}
	publisher.ops = ops
	publisher.now = freezerTestTime
	if err := publisher.PublishData(context.Background(), "migration_0000001"); err == nil {
		t.Fatal("import failure reported success")
	}
	var status, code, resourceStatus string
	_ = resourceService.db.QueryRow(`SELECT status,error_code FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &code)
	_ = resourceService.db.QueryRow(`SELECT status FROM site_migration_resources WHERE migration_site_id='migration_0000001' AND resource_type='database_import'`).Scan(&resourceStatus)
	if status != "failed_retryable" || code != "database_import_failed" || resourceStatus != "created" {
		t.Fatalf("status=%q code=%q resource=%q", status, code, resourceStatus)
	}
}

func TestSiteMigrationTargetPublisherRetriesDatabaseImport(t *testing.T) {
	resourceService, _, root := setupMigrationTargetResourceServiceTest(t)
	if err := resourceService.Create(context.Background(), "migration_0000001", SiteMigrationTargetSpec{Domain: "example.com", SiteType: "wordpress"}); err != nil {
		t.Fatal(err)
	}
	publisher, _ := NewSiteMigrationTargetPublisher(resourceService.db, resourceService.cfg, root)
	ops := &fakeMigrationTargetPublishOps{failAt: "import:" + opsDatabaseName(t, resourceService)}
	publisher.ops = ops
	publisher.now = freezerTestTime
	if err := publisher.PublishData(context.Background(), "migration_0000001"); err == nil {
		t.Fatal("database import failure was not reported")
	}
	ops.failAt = ""
	if err := publisher.PublishData(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	events := strings.Join(ops.events, "|")
	if !strings.Contains(events, "reset-db:") || strings.Count(events, "import:") != 2 {
		t.Fatalf("events=%v", ops.events)
	}
}

func TestSiteMigrationTargetPublisherRetriesPartialFilePublish(t *testing.T) {
	resourceService, _, root := setupMigrationTargetResourceServiceTest(t)
	if err := resourceService.Create(context.Background(), "migration_0000001", SiteMigrationTargetSpec{Domain: "example.com", SiteType: "wordpress"}); err != nil {
		t.Fatal(err)
	}
	publisher, _ := NewSiteMigrationTargetPublisher(resourceService.db, resourceService.cfg, root)
	ops := &fakeMigrationTargetPublishOps{failAt: "copy:" + opsWebRoot(t, resourceService)}
	publisher.ops = ops
	publisher.now = freezerTestTime
	if err := publisher.PublishData(context.Background(), "migration_0000001"); err == nil {
		t.Fatal("file publish failure was not reported")
	}
	ops.failAt = ""
	if err := publisher.PublishData(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	events := strings.Join(ops.events, "|")
	if strings.Count(events, "import:") != 1 || !strings.Contains(events, "reset-tree:") || strings.Count(events, "copy:") != 2 {
		t.Fatalf("events=%v", ops.events)
	}
}

func TestSiteMigrationTargetPublisherConfiguresRecordAndAwaitsCutover(t *testing.T) {
	resourceService, root := setupConfiguredMigrationPublisherTest(t)
	publisher, _ := NewSiteMigrationTargetPublisher(resourceService.db, resourceService.cfg, root)
	ops := &fakeMigrationTargetConfigureOps{}
	publisher.now = freezerTestTime
	if err := publisher.ConfigureAndHealth(context.Background(), "migration_0000001", ops); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ops.events, "|") != "identity|configs|health" {
		t.Fatalf("events=%v", ops.events)
	}
	var stage, status string
	var siteID int64
	_ = resourceService.db.QueryRow(`SELECT stage,status,target_site_id FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&stage, &status, &siteID)
	if stage != "awaiting_cutover" || status != "awaiting_cutover" || siteID <= 0 {
		t.Fatalf("stage=%q status=%q site=%d", stage, status, siteID)
	}
	var apiKey, aliases string
	var maxChildren int
	if err := resourceService.db.QueryRow(`SELECT plugin_api_key,aliases,php_fpm_max_children FROM websites WHERE id=?`, siteID).Scan(&apiKey, &aliases, &maxChildren); err != nil {
		t.Fatal(err)
	}
	if apiKey != "fixed-site-api-key-0000000000000000" || aliases != "www.example.com" || maxChildren <= 0 {
		t.Fatalf("api=%q aliases=%q max=%d", apiKey, aliases, maxChildren)
	}
	var lockSiteID int64
	_ = resourceService.db.QueryRow(`SELECT site_id FROM site_migration_locks WHERE migration_site_id='migration_0000001' AND direction='target' AND status='active'`).Scan(&lockSiteID)
	if lockSiteID != siteID {
		t.Fatalf("lock site=%d want=%d", lockSiteID, siteID)
	}
	var cronEnabled int
	var cronCommand string
	if err := resourceService.db.QueryRow(`SELECT enabled,command FROM cron_jobs WHERE site_id=? AND task_type='wp_cron'`, siteID).Scan(&cronEnabled, &cronCommand); err != nil || cronEnabled != 0 || cronCommand != "example.com" {
		t.Fatalf("cron enabled=%d command=%q err=%v", cronEnabled, cronCommand, err)
	}
	var groupBindings int
	if err := resourceService.db.QueryRow(`SELECT COUNT(*) FROM website_cdn_realip_groups wg JOIN cdn_realip_groups g ON g.id=wg.group_id WHERE wg.website_id=? AND g.name='Private CDN'`, siteID).Scan(&groupBindings); err != nil || groupBindings != 1 {
		t.Fatalf("CDN group bindings=%d err=%v", groupBindings, err)
	}
}

func TestSiteMigrationTargetPublisherHealthFailureKeepsOwnedWebsiteRecord(t *testing.T) {
	resourceService, root := setupConfiguredMigrationPublisherTest(t)
	publisher, _ := NewSiteMigrationTargetPublisher(resourceService.db, resourceService.cfg, root)
	ops := &fakeMigrationTargetConfigureOps{failAt: "health"}
	publisher.ops = &fakeMigrationTargetPublishOps{}
	publisher.now = freezerTestTime
	if err := publisher.ConfigureAndHealth(context.Background(), "migration_0000001", ops); err == nil {
		t.Fatal("health failure reported success")
	}
	var status, code string
	var websites, records int
	_ = resourceService.db.QueryRow(`SELECT status,error_code FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &code)
	_ = resourceService.db.QueryRow(`SELECT COUNT(*) FROM websites WHERE domain='example.com'`).Scan(&websites)
	_ = resourceService.db.QueryRow(`SELECT COUNT(*) FROM site_migration_resources WHERE migration_site_id='migration_0000001' AND resource_type='website_record' AND status='published'`).Scan(&records)
	if status != "failed_retryable" || code != "target_health_failed" || websites != 1 || records != 1 {
		t.Fatalf("status=%q code=%q websites=%d records=%d", status, code, websites, records)
	}
}

func TestSiteMigrationTargetPublisherRetriesHealthWithoutRepublishing(t *testing.T) {
	resourceService, root := setupConfiguredMigrationPublisherTest(t)
	publisher, _ := NewSiteMigrationTargetPublisher(resourceService.db, resourceService.cfg, root)
	ops := &fakeMigrationTargetConfigureOps{failAt: "health"}
	publisher.now = freezerTestTime
	if err := publisher.ConfigureAndHealth(context.Background(), "migration_0000001", ops); err == nil {
		t.Fatal("health failure was not reported")
	}
	ops.failAt = ""
	if err := publisher.ConfigureAndHealth(context.Background(), "migration_0000001", ops); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ops.events, "|") != "identity|configs|health|health" {
		t.Fatalf("events=%v", ops.events)
	}
	var websites int
	_ = resourceService.db.QueryRow(`SELECT COUNT(*) FROM websites WHERE domain='example.com'`).Scan(&websites)
	if websites != 1 {
		t.Fatalf("websites=%d", websites)
	}
}

func TestSiteMigrationTargetPublisherRetriesIdentityPublish(t *testing.T) {
	resourceService, root := setupConfiguredMigrationPublisherTest(t)
	publisher, _ := NewSiteMigrationTargetPublisher(resourceService.db, resourceService.cfg, root)
	ops := &fakeMigrationTargetConfigureOps{failAt: "identity"}
	publisher.now = freezerTestTime
	if err := publisher.ConfigureAndHealth(context.Background(), "migration_0000001", ops); err == nil {
		t.Fatal("identity failure was not reported")
	}
	ops.failAt = ""
	if err := publisher.ConfigureAndHealth(context.Background(), "migration_0000001", ops); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ops.events, "|") != "identity|reset_identity|identity|configs|health" {
		t.Fatalf("events=%v", ops.events)
	}
}

func TestSiteMigrationTargetPublisherBuiltinCDNUsesTargetDefinitionByName(t *testing.T) {
	resourceService, root := setupConfiguredMigrationPublisherTest(t)
	publisher, _ := NewSiteMigrationTargetPublisher(resourceService.db, resourceService.cfg, root)
	settings := SiteMigrationRuntimeSettings{CDNGroups: []SiteMigrationCDNGroupSetting{{Name: "Cloudflare", Provider: "source-drift", HeaderName: "X-Forwarded-For", IPRanges: "192.0.2.0/24", Builtin: true, Enabled: false, Description: "source description"}}}
	if err := publisher.resolveTargetCDNGroups(context.Background(), "migration_0000001", &settings); err != nil {
		t.Fatal(err)
	}
	group := settings.CDNGroups[0]
	if group.TargetID <= 0 || group.Provider != "cloudflare" || group.HeaderName != "CF-Connecting-IP" || !group.Enabled || group.Description != "Cloudflare 官方 IP 段由面板自动拉取" {
		t.Fatalf("resolved builtin=%+v", group)
	}
}

func TestSiteMigrationTargetPublisherRejectsConflictingCustomCDNDefinition(t *testing.T) {
	resourceService, root := setupConfiguredMigrationPublisherTest(t)
	if _, err := resourceService.db.Exec(`INSERT INTO cdn_realip_groups (name,provider,header_name,ip_ranges,builtin,enabled,description) VALUES ('Private CDN','custom','X-Forwarded-For','203.0.113.0/24',0,1,'')`); err != nil {
		t.Fatal(err)
	}
	publisher, _ := NewSiteMigrationTargetPublisher(resourceService.db, resourceService.cfg, root)
	settings := SiteMigrationRuntimeSettings{CDNGroups: []SiteMigrationCDNGroupSetting{{Name: "Private CDN", Provider: "custom", HeaderName: "X-Real-IP", IPRanges: "198.51.100.0/24", Enabled: true}}}
	if err := publisher.resolveTargetCDNGroups(context.Background(), "migration_0000001", &settings); err == nil {
		t.Fatal("conflicting custom CDN definition was accepted")
	}
}

func TestCopyMigrationTreePreservesInternalSymlinkAndRejectsExistingFile(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "assets"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(source, "assets"), 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "assets", "app.php"), []byte("ok"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("assets/app.php", filepath.Join(source, "current.php")); err != nil {
		t.Fatal(err)
	}
	if err := copyMigrationTree(source, target); err != nil {
		t.Fatal(err)
	}
	if link, err := os.Readlink(filepath.Join(target, "current.php")); err != nil || link != "assets/app.php" {
		t.Fatalf("link=%q err=%v", link, err)
	}
	if info, err := os.Stat(filepath.Join(target, "assets")); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0777 {
		t.Fatalf("directory mode=%v", info.Mode().Perm())
	}
	if err := copyMigrationTree(source, target); err == nil {
		t.Fatal("duplicate publication should fail closed")
	}
}

func TestFixMigrationWPConfigCredentialsReplacesOnlyManagedIdentity(t *testing.T) {
	root := newSecureSiteTestRoot(t)
	config := "<?php\ndefine('DB_NAME', 'old');\ndefine(\"DB_USER\", \"old_user\");\ndefine('DB_PASSWORD', 'old_password');\ndefine('DISABLE_WP_CRON', false);\ndefine('CUSTOM_VALUE', 'keep');\n"
	if err := os.WriteFile(filepath.Join(root, "wp-config.php"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	identity := siteMigrationDatabaseIdentity{Database: "db_new", User: "user_new", Password: "0123456789abcdef0123456789abcdef"}
	if err := fixMigrationWPConfigCredentials(root, "example.com", identity); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(filepath.Join(root, "wp-config.php"))
	for _, want := range []string{"define('DB_NAME', 'db_new')", "define('DB_USER', 'user_new')", "define('DB_PASSWORD', '0123456789abcdef0123456789abcdef')", "define('DISABLE_WP_CRON', true); // WP Panel migration freeze", "define('CUSTOM_VALUE', 'keep')"} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("missing %q in %s", want, content)
		}
	}
	if info, _ := os.Stat(filepath.Join(root, "wp-config.php")); info.Mode().Perm() != 0644 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
}

func migrationTargetSpecForTest(t *testing.T, service *SiteMigrationTargetResourceService) siteMigrationPublishSpec {
	t.Helper()
	var raw string
	if err := service.db.QueryRow(`SELECT json_extract(settings_snapshot,'$.target_spec') FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var spec siteMigrationPublishSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

func opsDatabaseName(t *testing.T, service *SiteMigrationTargetResourceService) string {
	return migrationTargetSpecForTest(t, service).DBName
}
func opsWebRoot(t *testing.T, service *SiteMigrationTargetResourceService) string {
	return migrationTargetSpecForTest(t, service).WebRoot
}
func opsSystemUser(t *testing.T, service *SiteMigrationTargetResourceService) string {
	return migrationTargetSpecForTest(t, service).SystemUser
}

func setupConfiguredMigrationPublisherTest(t *testing.T) (*SiteMigrationTargetResourceService, string) {
	t.Helper()
	resourceService, _, root := setupMigrationTargetResourceServiceTest(t)
	if _, err := resourceService.db.Exec(`UPDATE site_migration_sites SET source_site_id=NULL WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := resourceService.db.Exec(`DELETE FROM websites WHERE domain='example.com'`); err != nil {
		t.Fatal(err)
	}
	settingsService, _ := NewSiteMigrationSettingsService(resourceService.db)
	settingsService.now = freezerTestTime
	settings := SiteMigrationRuntimeSettings{Aliases: []string{"www.example.com"}, TemplateVersion: "v1.0", AccessLogMode: "error_only", FastCGICacheTTL: 300, MonitoringEnabled: true, MonitoringInterval: 5, WPPostRevisions: -1, PasswordResetMode: "allow", LogRetentionDays: 7, PHPFPMMaxChildren: 10, MarkerToken: strings.Repeat("m", 48), CDNRealIPEnabled: true, CronJobs: []SiteMigrationCronSetting{{Name: "WP Cron", CronExpression: "*/5 * * * *", TaskType: "wp_cron", BackupMode: "incremental", KeepCount: 3, Enabled: true}}, CDNGroups: []SiteMigrationCDNGroupSetting{{Name: "Private CDN", Provider: "custom", HeaderName: "X-Forwarded-For", IPRanges: "203.0.113.0/24", Enabled: true}}}
	if err := settingsService.StoreTargetSettings(context.Background(), "migration_0000001", settings); err != nil {
		t.Fatal(err)
	}
	if err := resourceService.Create(context.Background(), "migration_0000001", SiteMigrationTargetSpec{Domain: "example.com", Aliases: []string{"www.example.com"}, SiteType: "wordpress"}); err != nil {
		t.Fatal(err)
	}
	publisher, _ := NewSiteMigrationTargetPublisher(resourceService.db, resourceService.cfg, root)
	publisher.ops = &fakeMigrationTargetPublishOps{}
	publisher.now = freezerTestTime
	if err := publisher.PublishData(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	return resourceService, root
}
