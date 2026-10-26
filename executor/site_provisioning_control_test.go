package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func TestSiteProvisioningNormalizesAliasesAndKeepsPlanInputStable(t *testing.T) {
	aliases, err := normalizeSiteProvisioningAliases([]string{" WWW.Example.com ", "alias.example.com", "www.example.com", ""}, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !equalStringSlices(aliases, []string{"alias.example.com", "www.example.com"}) {
		t.Fatalf("aliases=%v", aliases)
	}
	if _, err := normalizeSiteProvisioningAliases([]string{"bad alias"}, "example.com"); SiteProvisioningErrorCode(err) != "invalid_domain" {
		t.Fatalf("invalid alias error=%v code=%s", err, SiteProvisioningErrorCode(err))
	}
}

func TestSiteProvisioningPlanDigestDoesNotContainPrivateResourceValues(t *testing.T) {
	resources := siteProvisioningResources{SystemUser: "wp_example", DBName: "db_example", WebRoot: "/srv/private/example.com"}
	payload := makeSiteProvisioningPayload("example.com", []string{"www.example.com"}, resources, "dev", nowForProvisioningTest(), nowForProvisioningTest().Add(siteProvisioningPlanTTL))
	data := strings.Join([]string{payload.Domain, strings.Join(payload.Aliases, ","), payload.ResourcesHash, payload.OptionsDigest}, "|")
	if strings.Contains(data, "db_example") || strings.Contains(data, "/srv/private") {
		t.Fatalf("plan payload contains private resource value: %s", data)
	}
	if payload.SSLEnabled || payload.SiteType != "wordpress" || payload.ExpiresAt != "" {
		t.Fatalf("unexpected fixed payload: %+v", payload)
	}
}

func TestSiteProvisioningLockIsCrossDescriptorAndFailClosed(t *testing.T) {
	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{DataDir: t.TempDir()}}
	t.Cleanup(func() { config.AppConfig = oldConfig })
	first, err := acquireSiteCreationLock()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := acquireSiteCreationLock(); SiteProvisioningErrorCode(err) != "site_create_busy" {
		t.Fatalf("second acquire error=%v code=%s", err, SiteProvisioningErrorCode(err))
	}
}

func TestPrepareSiteProvisioningPlanChecksConflictsAndWritesOnlyPlan(t *testing.T) {
	openTestDB(t)
	root, err := os.MkdirTemp("/tmp", "wpp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{DataDir: filepath.Join(root, "data")}, Paths: config.PathsConfig{
		WWWRoot: filepath.Join(root, "www"), WWWLogs: filepath.Join(root, "logs"), PHPFPMPool: filepath.Join(root, "pool"),
		NginxSitesAvailable: filepath.Join(root, "available"), NginxSitesEnabled: filepath.Join(root, "enabled"), PHPFPMSock: filepath.Join(root, "sock"),
	}}
	t.Cleanup(func() { config.AppConfig = oldConfig })
	plan, err := PrepareSiteProvisioningPlan(context.Background(), "provision.example.com", []string{"www.provision.example.com"}, "dev")
	if err != nil || !plan.OK || plan.PlanDigest == "" || plan.Mode != "dry_run" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	path, err := siteProvisioningPlanPath(plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("plan file missing: %v", err)
	}
}

func TestExecuteSiteProvisioningPlanConsumesAndCallsCoreOnce(t *testing.T) {
	openTestDB(t)
	root, err := os.MkdirTemp("/tmp", "wpp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{DataDir: filepath.Join(root, "data")}, Paths: config.PathsConfig{
		WWWRoot: filepath.Join(root, "www"), WWWLogs: filepath.Join(root, "logs"), PHPFPMPool: filepath.Join(root, "pool"),
		NginxSitesAvailable: filepath.Join(root, "available"), NginxSitesEnabled: filepath.Join(root, "enabled"), PHPFPMSock: filepath.Join(root, "sock"),
	}}
	t.Cleanup(func() { config.AppConfig = oldConfig })
	oldCore := executeSiteProvisioningCore
	calls := 0
	executeSiteProvisioningCore = func(task *Task) TaskResult {
		calls++
		payload := task.Payload.(*CreateSitePayload)
		return TaskResult{Success: true, Data: map[string]interface{}{"id": int64(123), "domain": payload.Domain}}
	}
	t.Cleanup(func() { executeSiteProvisioningCore = oldCore })
	plan, err := PrepareSiteProvisioningPlan(context.Background(), "execute.example.com", nil, "dev")
	if err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteSiteProvisioningPlan(context.Background(), plan.PlanDigest, "dev")
	if err != nil || !result.OK || result.SiteID != 123 || !result.Consumed || calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
	if _, err := ExecuteSiteProvisioningPlan(context.Background(), plan.PlanDigest, "dev"); SiteProvisioningErrorCode(err) != "plan_already_used" {
		t.Fatalf("replay err=%v code=%s", err, SiteProvisioningErrorCode(err))
	}
	var status string
	if err := database.GetDB().QueryRow("SELECT status FROM operation_logs WHERE operation='create_site' AND target=? ORDER BY id DESC LIMIT 1", "execute.example.com").Scan(&status); err != nil || status != "success" {
		t.Fatalf("operation status=%q err=%v", status, err)
	}
	var startedCount int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM operation_logs WHERE operation='create_site' AND target=? AND status='started'", "execute.example.com").Scan(&startedCount); err != nil || startedCount != 1 {
		t.Fatalf("started log count=%d err=%v", startedCount, err)
	}
	executeSiteProvisioningCore = func(*Task) TaskResult {
		return TaskResult{Success: false, ErrorCode: "rollback_required", Message: "网站创建失败，资源恢复不完整，请人工检查", Diagnostic: "delete db_private: permission denied"}
	}
	failedPlan, err := PrepareSiteProvisioningPlan(context.Background(), "failed.example.com", nil, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteSiteProvisioningPlan(context.Background(), failedPlan.PlanDigest, "dev"); SiteProvisioningErrorCode(err) != "rollback_required" || strings.Contains(err.Error(), "db_private") {
		t.Fatalf("failed execution err=%v code=%s", err, SiteProvisioningErrorCode(err))
	}
	var loggedMessage string
	if err := database.GetDB().QueryRow("SELECT message FROM operation_logs WHERE operation='create_site' AND target=? AND status='failed' ORDER BY id DESC LIMIT 1", "failed.example.com").Scan(&loggedMessage); err != nil || !strings.Contains(loggedMessage, "db_private") {
		t.Fatalf("failed diagnostic log=%q err=%v", loggedMessage, err)
	}
}

func TestPrepareSiteProvisioningRejectsDomainAliasResourceAndMigrationConflicts(t *testing.T) {
	t.Run("domain including deleting row", func(t *testing.T) {
		setupSiteProvisioningConflictTest(t)
		insertMinimalWebsite(t, "conflict.example.com")
		if _, err := database.GetDB().Exec(`UPDATE websites SET status='deleting' WHERE domain='conflict.example.com'`); err != nil {
			t.Fatal(err)
		}
		_, err := PrepareSiteProvisioningPlan(context.Background(), "conflict.example.com", nil, "dev")
		if SiteProvisioningErrorCode(err) != "domain_conflict" {
			t.Fatalf("err=%v code=%s", err, SiteProvisioningErrorCode(err))
		}
	})
	t.Run("alias", func(t *testing.T) {
		setupSiteProvisioningConflictTest(t)
		insertMinimalWebsite(t, "other.example.com")
		if _, err := database.GetDB().Exec(`UPDATE websites SET aliases='alias.example.com', status='active' WHERE domain='other.example.com'`); err != nil {
			t.Fatal(err)
		}
		_, err := PrepareSiteProvisioningPlan(context.Background(), "new.example.com", []string{"alias.example.com"}, "dev")
		if SiteProvisioningErrorCode(err) != "domain_conflict" {
			t.Fatalf("err=%v code=%s", err, SiteProvisioningErrorCode(err))
		}
	})
	t.Run("resource", func(t *testing.T) {
		root := setupSiteProvisioningConflictTest(t)
		if err := os.MkdirAll(filepath.Join(root, "www"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "www", "resource.example.com"), []byte("occupied"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := PrepareSiteProvisioningPlan(context.Background(), "resource.example.com", nil, "dev")
		if SiteProvisioningErrorCode(err) != "resource_conflict" {
			t.Fatalf("err=%v code=%s", err, SiteProvisioningErrorCode(err))
		}
	})
	t.Run("migration domain lock", func(t *testing.T) {
		setupSiteProvisioningConflictTest(t)
		if _, err := database.GetDB().Exec(`INSERT INTO site_migration_peers(id,status,protocol_version) VALUES ('peer-provision-test','paired',1); INSERT INTO site_migration_batches(id,peer_id,direction,status) VALUES ('batch-provision-test','peer-provision-test','target','active'); INSERT INTO site_migration_sites(id,batch_id,source_domain,target_domain,site_type,status,stage) VALUES ('migration-provision-test','batch-provision-test','source.example.com','moving.example.com','wordpress','running','preflight'); INSERT INTO site_migration_locks(domain,migration_site_id,direction,status) VALUES ('moving.example.com','migration-provision-test','target','active')`); err != nil {
			t.Fatal(err)
		}
		_, err := PrepareSiteProvisioningPlan(context.Background(), "moving.example.com", nil, "dev")
		if SiteProvisioningErrorCode(err) != "migration_locked" {
			t.Fatalf("err=%v code=%s", err, SiteProvisioningErrorCode(err))
		}
	})
}

func TestExecuteSiteProvisioningPlanConsumesConcurrentDigestOnce(t *testing.T) {
	openTestDB(t)
	root, err := os.MkdirTemp("/tmp", "wpp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{DataDir: filepath.Join(root, "data")}, Paths: config.PathsConfig{
		WWWRoot: filepath.Join(root, "www"), WWWLogs: filepath.Join(root, "logs"), PHPFPMPool: filepath.Join(root, "pool"),
		NginxSitesAvailable: filepath.Join(root, "available"), NginxSitesEnabled: filepath.Join(root, "enabled"), PHPFPMSock: filepath.Join(root, "sock"),
	}}
	t.Cleanup(func() { config.AppConfig = oldConfig })
	oldCore := executeSiteProvisioningCore
	var coreCalls int
	var coreMu sync.Mutex
	executeSiteProvisioningCore = func(*Task) TaskResult {
		coreMu.Lock()
		coreCalls++
		coreMu.Unlock()
		time.Sleep(20 * time.Millisecond)
		return TaskResult{Success: true, Data: map[string]interface{}{"id": int64(456)}}
	}
	t.Cleanup(func() { executeSiteProvisioningCore = oldCore })
	plan, err := PrepareSiteProvisioningPlan(context.Background(), "concurrent.example.com", nil, "dev")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ExecuteSiteProvisioningPlan(context.Background(), plan.PlanDigest, "dev")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	failures := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		code := SiteProvisioningErrorCode(err)
		if code != "plan_already_used" && code != "site_create_busy" {
			t.Fatalf("unexpected concurrent error=%v code=%s", err, code)
		}
		failures++
	}
	if successes != 1 || failures != 1 || coreCalls != 1 {
		t.Fatalf("successes=%d failures=%d coreCalls=%d", successes, failures, coreCalls)
	}
}

func setupSiteProvisioningConflictTest(t *testing.T) string {
	t.Helper()
	openTestDB(t)
	root, err := os.MkdirTemp("/tmp", "wpp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{DataDir: filepath.Join(root, "data")}, Paths: config.PathsConfig{
		WWWRoot: filepath.Join(root, "www"), WWWLogs: filepath.Join(root, "logs"), PHPFPMPool: filepath.Join(root, "pool"),
		NginxSitesAvailable: filepath.Join(root, "available"), NginxSitesEnabled: filepath.Join(root, "enabled"), PHPFPMSock: filepath.Join(root, "sock"),
	}}
	t.Cleanup(func() { config.AppConfig = oldConfig })
	return root
}

func TestSiteProvisioningRollbackFailureUsesStructuredDiagnostic(t *testing.T) {
	steps := []rollbackStep{
		{desc: "delete generated resource", fn: func() error { return errors.New("private path detail") }},
		{desc: "remove temporary marker", fn: func() error { return nil }},
	}
	failures := runCreateSiteRollbacks(steps)
	if len(failures) != 1 || failures[0].Step != "delete generated resource" {
		t.Fatalf("rollback failures=%+v", failures)
	}
	diagnostic := formatCreateSiteRollbackFailures(failures)
	if !strings.Contains(diagnostic, failures[0].Step) || !strings.Contains(diagnostic, failures[0].Error) {
		t.Fatalf("diagnostic=%q", diagnostic)
	}
	public := SiteProvisioningPublicErrorMessage(&SiteProvisioningError{Code: "rollback_required", Message: errors.New("private").Error()})
	if strings.Contains(public, "private") || !strings.Contains(public, "人工检查") {
		t.Fatalf("public=%q", public)
	}
}

func TestSiteProvisioningPlanValidationRejectsExpiryTamperAndReplay(t *testing.T) {
	payload := siteProvisioningPlanPayload{CapabilityID: "server-ai.site-provisioning", Action: "create", Domain: "example.com", SiteType: "wordpress", PanelVersion: "dev", ManifestSHA256: aiPanelManifestPayloadSHA256, GeneratedAt: nowForProvisioningTest().Format(time.RFC3339Nano), ExpiresPlanAt: nowForProvisioningTest().Add(time.Minute).Format(time.RFC3339Nano)}
	digest := digestSiteProvisioningPlan(payload)
	file := siteProvisioningPlanFile{Payload: payload, PlanDigest: digest}
	if err := validateSiteProvisioningPlanFile(file, digest, "dev"); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
	file.Consumed = true
	if SiteProvisioningErrorCode(validateSiteProvisioningPlanFile(file, digest, "dev")) != "plan_already_used" {
		t.Fatal("consumed plan was not rejected")
	}
	file.Consumed = false
	file.Payload.ExpiresPlanAt = nowForProvisioningTest().Add(-time.Minute).Format(time.RFC3339Nano)
	if SiteProvisioningErrorCode(validateSiteProvisioningPlanFile(file, digest, "dev")) != "plan_invalid" {
		t.Fatal("tampered plan was not rejected")
	}
	file.PlanDigest = digestSiteProvisioningPlan(file.Payload)
	if SiteProvisioningErrorCode(validateSiteProvisioningPlanFile(file, file.PlanDigest, "dev")) != "plan_expired" {
		t.Fatal("expired plan was not rejected as expired")
	}
}

func TestSiteProvisioningWordPressPackageFailureUsesStablePublicCode(t *testing.T) {
	err := &SiteProvisioningError{Code: "wordpress_package_unavailable", Message: "private package path"}
	if SiteProvisioningErrorCode(err) != "wordpress_package_unavailable" || strings.Contains(SiteProvisioningPublicErrorMessage(err), "private") {
		t.Fatalf("code=%s public=%q", SiteProvisioningErrorCode(err), SiteProvisioningPublicErrorMessage(err))
	}
}

func nowForProvisioningTest() time.Time { return time.Now().UTC() }
