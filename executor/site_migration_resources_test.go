package executor

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func TestRemoveMigrationSystemUserRemovesOrphanPrimaryGroup(t *testing.T) {
	if !IsCommandAllowed("groupdel", []string{"wp_example_com_0caaf24a"}) {
		t.Fatal("migration group deletion is not allowed by the command boundary")
	}
	var commands []string
	lookup := func(string) (*user.User, error) { return nil, user.UnknownUserError("wp_example_com_0caaf24a") }
	run := func(command string, args ...string) (string, error) {
		commands = append(commands, command+":"+strings.Join(args, ","))
		if command == "getent" {
			return "wp_example_com_0caaf24a:x:999:", nil
		}
		return "", nil
	}
	if err := removeMigrationSystemUserWith("wp_example_com_0caaf24a", lookup, run); err != nil {
		t.Fatal(err)
	}
	want := []string{"getent:group,wp_example_com_0caaf24a", "groupdel:wp_example_com_0caaf24a"}
	if strings.Join(commands, "|") != strings.Join(want, "|") {
		t.Fatalf("commands=%v want=%v", commands, want)
	}
}

func TestRemoveMigrationSystemUserDeletesUserBeforeRemainingGroup(t *testing.T) {
	var commands []string
	lookup := func(name string) (*user.User, error) { return &user.User{Username: name}, nil }
	run := func(command string, args ...string) (string, error) {
		commands = append(commands, command+":"+strings.Join(args, ","))
		if command == "getent" {
			return "wp_example_com_0caaf24a:x:999:", nil
		}
		return "", nil
	}
	if err := removeMigrationSystemUserWith("wp_example_com_0caaf24a", lookup, run); err != nil {
		t.Fatal(err)
	}
	want := []string{"userdel:-r,-f,wp_example_com_0caaf24a", "getent:group,wp_example_com_0caaf24a", "groupdel:wp_example_com_0caaf24a"}
	if strings.Join(commands, "|") != strings.Join(want, "|") {
		t.Fatalf("commands=%v want=%v", commands, want)
	}
}

type fakeMigrationTargetResourceOps struct {
	plan        siteMigrationTargetPlan
	events      []string
	dbPasswords []string
	failAt      string
}

func (f *fakeMigrationTargetResourceOps) step(value string) error {
	f.events = append(f.events, value)
	if f.failAt != "" && strings.HasPrefix(value, f.failAt) {
		return errors.New("injected " + value)
	}
	return nil
}
func (f *fakeMigrationTargetResourceOps) EnsureAvailable(plan siteMigrationTargetPlan) error {
	f.plan = plan
	return f.step("ensure")
}
func (f *fakeMigrationTargetResourceOps) CreateUser(value string) error {
	return f.step("create-user:" + value)
}
func (f *fakeMigrationTargetResourceOps) RemoveUser(value string) error {
	return f.step("remove-user:" + value)
}
func (f *fakeMigrationTargetResourceOps) CreateDirectory(value string, _ os.FileMode) error {
	return f.step("create-dir:" + value)
}
func (f *fakeMigrationTargetResourceOps) RemoveDirectory(value string) error {
	return f.step("remove-dir:" + value)
}
func (f *fakeMigrationTargetResourceOps) SetOwner(value, owner string) error {
	return f.step("owner:" + value + ":" + owner)
}
func (f *fakeMigrationTargetResourceOps) CreateDatabase(name, user, password string) error {
	f.dbPasswords = append(f.dbPasswords, password)
	return f.step("create-db:" + name + ":" + user)
}
func (f *fakeMigrationTargetResourceOps) DropDatabase(name, user string) error {
	return f.step("drop-db:" + name + ":" + user)
}

func TestSiteMigrationTargetResourcesCreateDerivedIdentityWithoutPublishingConfigs(t *testing.T) {
	service, ops, root := setupMigrationTargetResourceServiceTest(t)
	spec := SiteMigrationTargetSpec{Domain: "example.com", SiteType: "wordpress"}
	if err := service.Create(context.Background(), "migration_0000001", spec); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ops.plan.SystemUser, "wp_example_com_") || !strings.HasPrefix(ops.plan.DBName, "db_example_com_") || !strings.HasPrefix(ops.plan.DBUser, "user_example_com_") {
		t.Fatalf("plan=%+v", ops.plan)
	}
	if _, err := os.Stat(ops.plan.NginxConfPath); !os.IsNotExist(err) {
		t.Fatalf("Nginx config was published: %v", err)
	}
	if _, err := os.Stat(ops.plan.PHPPoolPath); !os.IsNotExist(err) {
		t.Fatalf("PHP-FPM config was published: %v", err)
	}
	identityPath := filepath.Join(root, "migration_0000001", "identity", "database.json")
	info, err := os.Stat(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("identity mode=%v", info.Mode().Perm())
	}
	content, err := os.ReadFile(identityPath)
	if err != nil || !strings.Contains(string(content), `"password":"fixed-migration-password-123456"`) {
		t.Fatalf("identity content unavailable err=%v", err)
	}
	siteIdentityPath := filepath.Join(root, "migration_0000001", "identity", "wp-panel-config.json")
	siteIdentity, err := os.ReadFile(siteIdentityPath)
	if err != nil || !strings.Contains(string(siteIdentity), `"api_key":"fixed-site-api-key-0000000000000000"`) {
		t.Fatalf("site identity unavailable err=%v", err)
	}
	if info, err := os.Stat(siteIdentityPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0600 {
		t.Fatalf("site identity mode=%v", info.Mode().Perm())
	}
	var resourceCount int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM site_migration_resources WHERE migration_site_id='migration_0000001' AND resource_type!='target_staging_root'`).Scan(&resourceCount); err != nil || resourceCount != 7 {
		t.Fatalf("resources=%d err=%v", resourceCount, err)
	}
	var leaked int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM site_migration_resources WHERE identifier LIKE '%fixed-migration-password%' OR ownership_tag LIKE '%fixed-migration-password%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("credential leaked to SQLite resources=%d err=%v", leaked, err)
	}
	var stage, status string
	_ = database.GetDB().QueryRow(`SELECT stage,status FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&stage, &status)
	if stage != "publishing" || status != "running" {
		t.Fatalf("stage=%q status=%q", stage, status)
	}
	var snapshot string
	_ = database.GetDB().QueryRow(`SELECT settings_snapshot FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&snapshot)
	if !strings.Contains(snapshot, `"existing":"kept"`) || !strings.Contains(snapshot, `"target_spec"`) || strings.Contains(snapshot, "fixed-migration-password") || strings.Contains(snapshot, "fixed-site-api-key") {
		t.Fatalf("unexpected settings snapshot: %s", snapshot)
	}
}

func TestSiteMigrationTargetResourceRejectsDuplicateAliases(t *testing.T) {
	service, ops, _ := setupMigrationTargetResourceServiceTest(t)
	err := service.Create(context.Background(), "migration_0000001", SiteMigrationTargetSpec{
		Domain: "example.com", Aliases: []string{"WWW.example.com", "www.example.com"}, SiteType: "wordpress",
	})
	if err == nil || len(ops.events) != 0 {
		t.Fatalf("err=%v events=%v", err, ops.events)
	}
}

func TestSiteMigrationTargetResourceRequiresOwnedStagingRoot(t *testing.T) {
	service, ops, _ := setupMigrationTargetResourceServiceTest(t)
	if _, err := database.GetDB().Exec(`UPDATE site_migration_resources SET ownership_tag='another_task' WHERE migration_site_id='migration_0000001' AND resource_type='target_staging_root'`); err != nil {
		t.Fatal(err)
	}
	err := service.Create(context.Background(), "migration_0000001", SiteMigrationTargetSpec{Domain: "example.com", SiteType: "wordpress"})
	if err == nil || len(ops.events) != 0 {
		t.Fatalf("err=%v events=%v", err, ops.events)
	}
}

func TestSiteMigrationTargetResourceFailureKeepsCreatedOwnershipRecords(t *testing.T) {
	service, ops, _ := setupMigrationTargetResourceServiceTest(t)
	ops.failAt = "create-db:"
	if err := service.Create(context.Background(), "migration_0000001", SiteMigrationTargetSpec{Domain: "example.com", SiteType: "wordpress"}); err == nil {
		t.Fatal("database creation failure reported success")
	}
	var resourceCount, databaseCount int
	_ = database.GetDB().QueryRow(`SELECT COUNT(*) FROM site_migration_resources WHERE migration_site_id='migration_0000001' AND status='published' AND resource_type!='target_staging_root'`).Scan(&resourceCount)
	_ = database.GetDB().QueryRow(`SELECT COUNT(*) FROM site_migration_resources WHERE migration_site_id='migration_0000001' AND resource_type='database' AND status!='removed'`).Scan(&databaseCount)
	if resourceCount != 4 || databaseCount != 0 {
		t.Fatalf("resources=%d database=%d", resourceCount, databaseCount)
	}
	var status, code string
	_ = database.GetDB().QueryRow(`SELECT status,error_code FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &code)
	if status != "failed_retryable" || code != "target_resource_creation_failed" {
		t.Fatalf("status=%q code=%q", status, code)
	}
}

func TestSiteMigrationTargetResourcesRetryAfterDirectoryFailure(t *testing.T) {
	service, ops, _ := setupMigrationTargetResourceServiceTest(t)
	spec := SiteMigrationTargetSpec{Domain: "example.com", SiteType: "wordpress"}
	ops.failAt = "create-dir:"
	if err := service.Create(context.Background(), "migration_0000001", spec); err == nil {
		t.Fatal("directory failure was not reported")
	}
	ops.failAt = ""
	if err := service.Create(context.Background(), "migration_0000001", spec); err != nil {
		t.Fatal(err)
	}
	var status string
	var published int
	_ = service.db.QueryRow(`SELECT status FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status)
	_ = service.db.QueryRow(`SELECT COUNT(*) FROM site_migration_resources WHERE migration_site_id='migration_0000001' AND resource_type!='target_staging_root' AND status='published'`).Scan(&published)
	if status != "running" || published != 7 || strings.Count(strings.Join(ops.events, "|"), "create-user:") != 1 {
		t.Fatalf("status=%q published=%d events=%v", status, published, ops.events)
	}
}

func TestSiteMigrationTargetResourcesRetryDatabaseWithPersistedPassword(t *testing.T) {
	service, ops, _ := setupMigrationTargetResourceServiceTest(t)
	passwordCalls := 0
	passwords := []string{"retry-password-0000000000000001", "retry-password-0000000000000002"}
	service.password = func(int) string {
		passwordCalls++
		return passwords[passwordCalls-1]
	}
	spec := SiteMigrationTargetSpec{Domain: "example.com", SiteType: "wordpress"}
	ops.failAt = "create-db:"
	if err := service.Create(context.Background(), "migration_0000001", spec); err == nil {
		t.Fatal("database failure was not reported")
	}
	ops.failAt = ""
	if err := service.Create(context.Background(), "migration_0000001", spec); err != nil {
		t.Fatal(err)
	}
	if passwordCalls != 1 || len(ops.dbPasswords) != 2 || ops.dbPasswords[0] != ops.dbPasswords[1] {
		t.Fatalf("password_calls=%d database_passwords=%v", passwordCalls, ops.dbPasswords)
	}
}

func setupMigrationTargetResourceServiceTest(t *testing.T) (*SiteMigrationTargetResourceService, *fakeMigrationTargetResourceOps, string) {
	t.Helper()
	store, _ := newSiteMigrationStoreTest(t)
	if _, err := store.db.Exec(`UPDATE site_migration_batches SET direction='target',status='active' WHERE id='batch_0000000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE site_migration_sites SET stage='preparing_target',settings_snapshot='{"existing":"kept"}' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO site_migration_locks(domain,migration_site_id,direction,status) VALUES ('example.com','migration_0000001','target','active')`); err != nil {
		t.Fatal(err)
	}
	base, err := os.MkdirTemp("/tmp", "wpm-res-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	cfg := &config.Config{}
	cfg.Paths.WWWRoot = filepath.Join(base, "www")
	cfg.Paths.WWWLogs = filepath.Join(base, "logs")
	cfg.Paths.PHPFPMPool = filepath.Join(base, "php-pool")
	cfg.Paths.PHPFPMSock = filepath.Join(base, "php-sock")
	cfg.Paths.NginxSitesAvailable = filepath.Join(base, "nginx-available")
	cfg.Paths.NginxSitesEnabled = filepath.Join(base, "nginx-enabled")
	root := filepath.Join(base, "staging")
	stagingSiteRoot := filepath.Join(root, "migration_0000001")
	if err := os.MkdirAll(stagingSiteRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO site_migration_resources
		(migration_site_id,resource_type,identifier,ownership_tag,status) VALUES (?,?,?,?, 'created')`, "migration_0000001", "target_staging_root", stagingSiteRoot, "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	service, err := NewSiteMigrationTargetResourceService(store.db, cfg, root)
	if err != nil {
		t.Fatal(err)
	}
	ops := &fakeMigrationTargetResourceOps{}
	service.ops = ops
	service.now = freezerTestTime
	service.password = func(int) string { return "fixed-migration-password-123456" }
	service.apiKey = func() string { return "fixed-site-api-key-0000000000000000" }
	return service, ops, root
}
