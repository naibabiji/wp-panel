package executor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

func TestSiteStatePlanDryRunExecuteAndReplay(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	oldReload := runWebsiteStateNginxReload
	runWebsiteStateNginxReload = func() ([]byte, error) { return nil, nil }
	t.Cleanup(func() { runWebsiteStateNginxReload = oldReload })

	plan, err := PrepareSiteStatePlan(context.Background(), "pause", site.Domain, "dev")
	if err != nil || !plan.OK || plan.Mode != "dry_run" || plan.PlanDigest == "" || plan.ExpiresAt == "" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	result, err := ExecuteSiteStatePlan(context.Background(), plan.PlanDigest, "dev", "pause")
	if err != nil || !result.OK || result.Mode != "execute" || !result.Consumed || result.OperationID == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var status string
	if err := database.GetDB().QueryRow("SELECT status FROM websites WHERE id=?", site.ID).Scan(&status); err != nil || status != "paused" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	var logStatus string
	if err := database.GetDB().QueryRow("SELECT status FROM operation_logs WHERE operation=? AND target=? ORDER BY id DESC LIMIT 1", "pause_site", site.Domain).Scan(&logStatus); err != nil || logStatus != "success" {
		t.Fatalf("operation log status=%q err=%v", logStatus, err)
	}
	if _, err := ExecuteSiteStatePlan(context.Background(), plan.PlanDigest, "dev", "pause"); SiteStateControlErrorCode(err) != "plan_already_used" {
		t.Fatalf("replay error=%v code=%s", err, SiteStateControlErrorCode(err))
	}
}

func TestPrepareSiteStatePlanBlocksActiveAIDevelopmentAccess(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	if _, err := database.GetDB().Exec(`INSERT INTO website_ai_development_access
		(site_id,status,system_user,web_root,original_shell,original_home)
		VALUES (?, 'enabled', 'wp', '/tmp', '/bin/bash', '/tmp')`, site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSiteStatePlan(context.Background(), "pause", site.Domain, "dev"); SiteStateControlErrorCode(err) != "ai_development_active" {
		t.Fatalf("error=%v code=%s", err, SiteStateControlErrorCode(err))
	}
}

func TestExecuteSiteStatePlanRejectsActionMismatch(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	plan, err := PrepareSiteStatePlan(context.Background(), "pause", site.Domain, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteSiteStatePlan(context.Background(), plan.PlanDigest, "dev", "resume"); SiteStateControlErrorCode(err) != "plan_invalid" {
		t.Fatalf("error=%v code=%s", err, SiteStateControlErrorCode(err))
	}
}

func TestExecuteSiteStatePlanBecomesStaleWhenStatusChanges(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	plan, err := PrepareSiteStatePlan(context.Background(), "pause", site.Domain, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec("UPDATE websites SET status='paused' WHERE id=?", site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteSiteStatePlan(context.Background(), plan.PlanDigest, "dev", "pause"); SiteStateControlErrorCode(err) != "plan_stale" {
		t.Fatalf("error=%v code=%s", err, SiteStateControlErrorCode(err))
	}
}

func TestSiteStatePlanResumeAndPostStateVerification(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusPaused, true)
	oldReload := runWebsiteStateNginxReload
	runWebsiteStateNginxReload = func() ([]byte, error) { return nil, nil }
	t.Cleanup(func() { runWebsiteStateNginxReload = oldReload })

	plan, err := PrepareSiteStatePlan(context.Background(), "resume", site.Domain, "dev")
	if err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteSiteStatePlan(context.Background(), plan.PlanDigest, "dev", "resume")
	if err != nil || !result.OK || result.FromStatus != "paused" || result.ToStatus != "active" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var status string
	if err := database.GetDB().QueryRow("SELECT status FROM websites WHERE id=?", site.ID).Scan(&status); err != nil || status != "active" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	var operation, logStatus string
	if err := database.GetDB().QueryRow("SELECT operation,status FROM operation_logs WHERE target=? ORDER BY id DESC LIMIT 1", site.Domain).Scan(&operation, &logStatus); err != nil || operation != "enable_site" || logStatus != "success" {
		t.Fatalf("operation=%q status=%q err=%v", operation, logStatus, err)
	}
}

func TestExecuteSiteStatePlanRejectsExpiredPlan(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	plan, err := PrepareSiteStatePlan(context.Background(), "pause", site.Domain, "dev")
	if err != nil {
		t.Fatal(err)
	}
	file, err := readSiteStatePlan(plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	file.Payload.ExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if err := writeSiteStatePlan(plan.PlanDigest, file); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteSiteStatePlan(context.Background(), plan.PlanDigest, "dev", "pause"); SiteStateControlErrorCode(err) != "plan_expired" {
		t.Fatalf("error=%v code=%s", err, SiteStateControlErrorCode(err))
	}
}

func TestPrepareSiteStatePlanBlocksMaintenanceAndMigration(t *testing.T) {
	t.Run("maintenance", func(t *testing.T) {
		site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
		if _, err := database.GetDB().Exec(`UPDATE websites SET maintenance_security=? WHERE id=?`, `{"window":{"state":"unlocked"}}`, site.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareSiteStatePlan(context.Background(), "pause", site.Domain, "dev"); SiteStateControlErrorCode(err) != "maintenance_window_active" {
			t.Fatalf("error=%v code=%s", err, SiteStateControlErrorCode(err))
		}
	})
	t.Run("migration", func(t *testing.T) {
		site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
		if _, err := database.GetDB().Exec(`INSERT INTO site_migration_peers(id,status,protocol_version) VALUES ('peer_00000000001','paired',1);
			INSERT INTO site_migration_batches(id,peer_id,direction) VALUES ('batch_0000000001','peer_00000000001','source');
			INSERT INTO site_migration_sites(id,batch_id,source_site_id,source_domain,target_domain,site_type,status,stage)
			VALUES ('migration_0000001','batch_0000000001',?,'runtime.example.com','runtime.example.com','wordpress','queued','publishing')`, site.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.GetDB().Exec(`INSERT INTO site_migration_locks(domain,site_id,migration_site_id,direction,status) VALUES (?,?,?,?,?)`, site.Domain, site.ID, "migration_0000001", "source", "active"); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareSiteStatePlan(context.Background(), "pause", site.Domain, "dev"); SiteStateControlErrorCode(err) != "migration_locked" {
			t.Fatalf("error=%v code=%s", err, SiteStateControlErrorCode(err))
		}
	})
}

func TestPrepareSiteStatePlanReportsBusyLock(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	if !TryAcquireSiteOpLock(site.ID, "test") {
		t.Fatal("failed to acquire test site lock")
	}
	defer ReleaseSiteOpLock(site.ID)
	if _, err := PrepareSiteStatePlan(context.Background(), "pause", site.Domain, "dev"); SiteStateControlErrorCode(err) != "site_lock_busy" {
		t.Fatalf("error=%v code=%s", err, SiteStateControlErrorCode(err))
	}
}

func TestExecuteSiteStatePlanConcurrentDigestIsConsumedOnce(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	oldReload := runWebsiteStateNginxReload
	runWebsiteStateNginxReload = func() ([]byte, error) { return nil, nil }
	t.Cleanup(func() { runWebsiteStateNginxReload = oldReload })
	plan, err := PrepareSiteStatePlan(context.Background(), "pause", site.Domain, "dev")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ExecuteSiteStatePlan(context.Background(), plan.PlanDigest, "dev", "pause")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes, alreadyUsed, busy := 0, 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if SiteStateControlErrorCode(err) == "plan_already_used" {
			alreadyUsed++
		} else if SiteStateControlErrorCode(err) == "site_lock_busy" {
			busy++
		}
	}
	if successes != 1 || alreadyUsed+busy != 1 {
		t.Fatalf("concurrent outcomes: successes=%d already_used=%d busy=%d", successes, alreadyUsed, busy)
	}
	if _, err := ExecuteSiteStatePlan(context.Background(), plan.PlanDigest, "dev", "pause"); SiteStateControlErrorCode(err) != "plan_already_used" {
		t.Fatalf("post-concurrency replay error=%v code=%s", err, SiteStateControlErrorCode(err))
	}
}

func TestSiteStatePlanLogsOperationAndRollbackOutcomes(t *testing.T) {
	t.Run("operation failed", func(t *testing.T) {
		site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
		oldReload := runWebsiteStateNginxReload
		calls := 0
		runWebsiteStateNginxReload = func() ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte("failed"), errors.New("reload failed")
			}
			return nil, nil
		}
		t.Cleanup(func() { runWebsiteStateNginxReload = oldReload })
		plan, err := PrepareSiteStatePlan(context.Background(), "pause", site.Domain, "dev")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ExecuteSiteStatePlan(context.Background(), plan.PlanDigest, "dev", "pause"); SiteStateControlErrorCode(err) != "operation_failed" {
			t.Fatalf("error=%v code=%s", err, SiteStateControlErrorCode(err))
		}
		assertLatestSiteStateLog(t, site.Domain, "failed")
		if _, err := ExecuteSiteStatePlan(context.Background(), plan.PlanDigest, "dev", "pause"); SiteStateControlErrorCode(err) != "plan_already_used" {
			t.Fatalf("failed-plan replay error=%v code=%s", err, SiteStateControlErrorCode(err))
		}
	})
	t.Run("rollback required", func(t *testing.T) {
		site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
		oldReload := runWebsiteStateNginxReload
		runWebsiteStateNginxReload = func() ([]byte, error) { return []byte("failed"), errors.New("reload failed") }
		t.Cleanup(func() { runWebsiteStateNginxReload = oldReload })
		plan, err := PrepareSiteStatePlan(context.Background(), "pause", site.Domain, "dev")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ExecuteSiteStatePlan(context.Background(), plan.PlanDigest, "dev", "pause"); SiteStateControlErrorCode(err) != "rollback_required" {
			t.Fatalf("error=%v code=%s", err, SiteStateControlErrorCode(err))
		}
		assertLatestSiteStateLog(t, site.Domain, "failed")
	})
}

func assertLatestSiteStateLog(t *testing.T, domain, wantStatus string) {
	t.Helper()
	var operation, status string
	if err := database.GetDB().QueryRow("SELECT operation,status FROM operation_logs WHERE target=? ORDER BY id DESC LIMIT 1", domain).Scan(&operation, &status); err != nil {
		t.Fatal(err)
	}
	if operation != "pause_site" || status != wantStatus {
		t.Fatalf("operation=%q status=%q", operation, status)
	}
}
