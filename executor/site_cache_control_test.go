package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

func TestSiteCacheClearPlanDryRunExecuteAndReplay(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	if _, err := database.GetDB().Exec("UPDATE websites SET fastcgi_cache_key=? WHERE id=?", "old-cache-key", site.ID); err != nil {
		t.Fatal(err)
	}
	oldRegenerate := regenerateSiteNginxForCache
	regenerateSiteNginxForCache = func(int) error { return nil }
	t.Cleanup(func() { regenerateSiteNginxForCache = oldRegenerate })

	plan, err := PrepareSiteCacheClearPlan(context.Background(), site.Domain, "dev")
	if err != nil || !plan.OK || plan.Mode != "dry_run" || plan.PlanDigest == "" || plan.Scope != siteCacheClearScope {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	result, err := ExecuteSiteCacheClearPlan(context.Background(), plan.PlanDigest, "dev")
	if err != nil || !result.OK || result.Mode != "execute" || !result.Consumed || result.OperationID == "" || len(result.NotCleared) != 4 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var key string
	if err := database.GetDB().QueryRow("SELECT fastcgi_cache_key FROM websites WHERE id=?", site.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key == "old-cache-key" || key == "" {
		t.Fatalf("cache key was not rotated: %q", key)
	}
	if _, err := ExecuteSiteCacheClearPlan(context.Background(), plan.PlanDigest, "dev"); SiteCacheClearErrorCode(err) != "plan_already_used" {
		t.Fatalf("replay error=%v code=%s", err, SiteCacheClearErrorCode(err))
	}
	var logStatus string
	if err := database.GetDB().QueryRow("SELECT status FROM operation_logs WHERE operation=? AND target=? ORDER BY id DESC LIMIT 1", "clear_site_cache", site.Domain).Scan(&logStatus); err != nil || logStatus != "success" {
		t.Fatalf("log status=%q err=%v", logStatus, err)
	}
}

func TestClearSiteCacheCoreUsesStructuredRollbackCodes(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	if _, err := database.GetDB().Exec("UPDATE websites SET fastcgi_cache_key=? WHERE id=?", "old-cache-key", site.ID); err != nil {
		t.Fatal(err)
	}
	oldRegenerate := regenerateSiteNginxForCache
	calls := 0
	regenerateSiteNginxForCache = func(int) error {
		calls++
		if calls == 1 {
			return errors.New("reload failed")
		}
		return nil
	}
	t.Cleanup(func() { regenerateSiteNginxForCache = oldRegenerate })
	if err := ClearSiteCache(site.ID); err == nil || SiteCacheClearErrorCode(err) != "operation_failed" || !strings.Contains(err.Error(), "已恢复") {
		t.Fatalf("error=%v code=%s", err, SiteCacheClearErrorCode(err))
	}
}

func TestClearSiteCacheExternalCanReenterWhileCallerOwnsSiteLock(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	if _, err := database.GetDB().Exec("UPDATE websites SET fastcgi_cache_key=? WHERE id=?", "old-cache-key", site.ID); err != nil {
		t.Fatal(err)
	}
	oldRegenerate := regenerateSiteNginxForCache
	regenerateSiteNginxForCache = func(int) error { return nil }
	t.Cleanup(func() { regenerateSiteNginxForCache = oldRegenerate })
	if !TryAcquireSiteOpLock(site.ID, "test-owner") {
		t.Fatal("failed to acquire test lock")
	}
	defer ReleaseSiteOpLock(site.ID)
	if err := ClearSiteCache(site.ID); err != nil {
		t.Fatalf("unlocked core was blocked by caller lock: %v", err)
	}
}

func TestSiteCacheClearPlanRejectsExpiredCorruptAndStalePlans(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
		if _, err := database.GetDB().Exec("UPDATE websites SET fastcgi_cache_key=? WHERE id=?", "old-cache-key", site.ID); err != nil {
			t.Fatal(err)
		}
		plan, err := PrepareSiteCacheClearPlan(context.Background(), site.Domain, "dev")
		if err != nil {
			t.Fatal(err)
		}
		file, err := readSiteCachePlan(plan.PlanDigest)
		if err != nil {
			t.Fatal(err)
		}
		file.Payload.ExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
		file.PlanDigest, err = digestSiteCacheClearPlan(file.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeSiteCachePlan(file.PlanDigest, file); err != nil {
			t.Fatal(err)
		}
		if _, err := ExecuteSiteCacheClearPlan(context.Background(), file.PlanDigest, "dev"); SiteCacheClearErrorCode(err) != "plan_expired" {
			t.Fatalf("error=%v code=%s", err, SiteCacheClearErrorCode(err))
		}
	})

	t.Run("corrupt", func(t *testing.T) {
		site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
		plan, err := PrepareSiteCacheClearPlan(context.Background(), site.Domain, "dev")
		if err != nil {
			t.Fatal(err)
		}
		path, err := siteCachePlanPath(plan.PlanDigest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ExecuteSiteCacheClearPlan(context.Background(), plan.PlanDigest, "dev"); SiteCacheClearErrorCode(err) != "plan_invalid" {
			t.Fatalf("error=%v code=%s", err, SiteCacheClearErrorCode(err))
		}
	})

	t.Run("cache key changed", func(t *testing.T) {
		site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
		oldRegenerate := regenerateSiteNginxForCache
		regenerateSiteNginxForCache = func(int) error { return nil }
		t.Cleanup(func() { regenerateSiteNginxForCache = oldRegenerate })
		if _, err := database.GetDB().Exec("UPDATE websites SET fastcgi_cache_key=? WHERE id=?", "old-cache-key", site.ID); err != nil {
			t.Fatal(err)
		}
		plan, err := PrepareSiteCacheClearPlan(context.Background(), site.Domain, "dev")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.GetDB().Exec("UPDATE websites SET fastcgi_cache_key=? WHERE id=?", "changed-key", site.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := ExecuteSiteCacheClearPlan(context.Background(), plan.PlanDigest, "dev"); SiteCacheClearErrorCode(err) != "plan_stale" {
			t.Fatalf("error=%v code=%s", err, SiteCacheClearErrorCode(err))
		}
	})

	t.Run("action and panel version changed", func(t *testing.T) {
		site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
		plan, err := PrepareSiteCacheClearPlan(context.Background(), site.Domain, "dev")
		if err != nil {
			t.Fatal(err)
		}
		file, err := readSiteCachePlan(plan.PlanDigest)
		if err != nil {
			t.Fatal(err)
		}
		file.Payload.Action = "other"
		if err := writeSiteCachePlan(plan.PlanDigest, file); err != nil {
			t.Fatal(err)
		}
		if _, err := ExecuteSiteCacheClearPlan(context.Background(), plan.PlanDigest, "dev"); SiteCacheClearErrorCode(err) != "plan_invalid" {
			t.Fatalf("action error=%v code=%s", err, SiteCacheClearErrorCode(err))
		}

		plan, err = PrepareSiteCacheClearPlan(context.Background(), site.Domain, "dev")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ExecuteSiteCacheClearPlan(context.Background(), plan.PlanDigest, "other-version"); SiteCacheClearErrorCode(err) != "plan_stale" {
			t.Fatalf("version error=%v code=%s", err, SiteCacheClearErrorCode(err))
		}
	})
}

func TestSiteCacheClearPlanRejectsMigrationLockAndBusySite(t *testing.T) {
	t.Run("migration lock", func(t *testing.T) {
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
		if _, err := PrepareSiteCacheClearPlan(context.Background(), site.Domain, "dev"); SiteCacheClearErrorCode(err) != "migration_locked" {
			t.Fatalf("error=%v code=%s", err, SiteCacheClearErrorCode(err))
		}
	})

	t.Run("busy site", func(t *testing.T) {
		site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
		if !TryAcquireSiteOpLock(site.ID, "test-owner") {
			t.Fatal("failed to acquire site lock")
		}
		defer ReleaseSiteOpLock(site.ID)
		if _, err := PrepareSiteCacheClearPlan(context.Background(), site.Domain, "dev"); SiteCacheClearErrorCode(err) != "site_lock_busy" {
			t.Fatalf("error=%v code=%s", err, SiteCacheClearErrorCode(err))
		}
	})
}

func TestSiteCacheClearPlanConsumesConcurrentDigestOnce(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	if _, err := database.GetDB().Exec("UPDATE websites SET fastcgi_cache_key=? WHERE id=?", "old-cache-key", site.ID); err != nil {
		t.Fatal(err)
	}
	oldRegenerate := regenerateSiteNginxForCache
	regenerateSiteNginxForCache = func(int) error { return nil }
	t.Cleanup(func() { regenerateSiteNginxForCache = oldRegenerate })
	plan, err := PrepareSiteCacheClearPlan(context.Background(), site.Domain, "dev")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, executeErr := ExecuteSiteCacheClearPlan(context.Background(), plan.PlanDigest, "dev")
			results <- executeErr
		}()
	}
	wg.Wait()
	close(results)
	successes, rejected := 0, 0
	for executeErr := range results {
		if executeErr == nil {
			successes++
		} else if code := SiteCacheClearErrorCode(executeErr); code == "plan_already_used" || code == "site_lock_busy" {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent error=%v code=%s", executeErr, SiteCacheClearErrorCode(executeErr))
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("concurrent outcomes successes=%d rejected=%d", successes, rejected)
	}
}

func TestSiteCacheClearPlanMapsRollbackFailuresAndLogsFailure(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	if _, err := database.GetDB().Exec("UPDATE websites SET fastcgi_cache_key=? WHERE id=?", "old-cache-key", site.ID); err != nil {
		t.Fatal(err)
	}
	oldRegenerate := regenerateSiteNginxForCache
	regenerateSiteNginxForCache = func(int) error { return errors.New("reload failed") }
	t.Cleanup(func() { regenerateSiteNginxForCache = oldRegenerate })
	plan, err := PrepareSiteCacheClearPlan(context.Background(), site.Domain, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteSiteCacheClearPlan(context.Background(), plan.PlanDigest, "dev"); SiteCacheClearErrorCode(err) != "rollback_required" {
		t.Fatalf("error=%v code=%s", err, SiteCacheClearErrorCode(err))
	}
	var status string
	if err := database.GetDB().QueryRow("SELECT status FROM operation_logs WHERE operation=? AND target=? ORDER BY id DESC LIMIT 1", "clear_site_cache", site.Domain).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("log status=%q err=%v", status, err)
	}
}

func TestSiteCacheClearPlanDetectsUnchangedKeyAndKeyRestoreFailure(t *testing.T) {
	t.Run("unchanged key", func(t *testing.T) {
		site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
		if _, err := database.GetDB().Exec("UPDATE websites SET fastcgi_cache_key=? WHERE id=?", "old-cache-key", site.ID); err != nil {
			t.Fatal(err)
		}
		oldRegenerate := regenerateSiteNginxForCache
		regenerateSiteNginxForCache = func(int) error {
			_, err := database.GetDB().Exec("UPDATE websites SET fastcgi_cache_key=? WHERE id=?", "old-cache-key", site.ID)
			return err
		}
		t.Cleanup(func() { regenerateSiteNginxForCache = oldRegenerate })
		plan, err := PrepareSiteCacheClearPlan(context.Background(), site.Domain, "dev")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ExecuteSiteCacheClearPlan(context.Background(), plan.PlanDigest, "dev"); SiteCacheClearErrorCode(err) != "rollback_required" {
			t.Fatalf("error=%v code=%s", err, SiteCacheClearErrorCode(err))
		}
	})

	t.Run("key restore failure", func(t *testing.T) {
		site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
		if _, err := database.GetDB().Exec("UPDATE websites SET fastcgi_cache_key=? WHERE id=?", "old-cache-key", site.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.GetDB().Exec(`CREATE TRIGGER fail_cache_key_restore BEFORE UPDATE OF fastcgi_cache_key ON websites
			WHEN OLD.fastcgi_cache_key <> 'old-cache-key' BEGIN SELECT RAISE(ABORT,'restore blocked'); END`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = database.GetDB().Exec("DROP TRIGGER fail_cache_key_restore") })
		oldRegenerate := regenerateSiteNginxForCache
		regenerateSiteNginxForCache = func(int) error { return errors.New("reload failed") }
		t.Cleanup(func() { regenerateSiteNginxForCache = oldRegenerate })
		if err := ClearSiteCache(site.ID); SiteCacheClearErrorCode(err) != "rollback_required" {
			t.Fatalf("error=%v code=%s", err, SiteCacheClearErrorCode(err))
		}
	})
}

func TestClearSiteCacheReportsMissingSite(t *testing.T) {
	openTestDB(t)
	if err := ClearSiteCache(99999); SiteCacheClearErrorCode(err) != "site_not_found" {
		t.Fatalf("error=%v code=%s", err, SiteCacheClearErrorCode(err))
	}
}

func TestSiteCacheClearPlanDoesNotMatchAlias(t *testing.T) {
	site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
	if _, err := database.GetDB().Exec("UPDATE websites SET aliases=? WHERE id=?", "\nalias.runtime.example.com\n", site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSiteCacheClearPlan(context.Background(), "alias.runtime.example.com", "dev"); SiteCacheClearErrorCode(err) != "site_not_found" {
		t.Fatalf("error=%v code=%s", err, SiteCacheClearErrorCode(err))
	}
}

func TestSiteCacheClearPlanAllowsPausedAndMigratedSites(t *testing.T) {
	for _, status := range []models.WebsiteStatus{models.StatusPaused, models.StatusMigrated} {
		t.Run(string(status), func(t *testing.T) {
			site, _ := setupWebsiteRuntimeStateTest(t, status, false)
			if _, err := PrepareSiteCacheClearPlan(context.Background(), site.Domain, "dev"); err != nil {
				t.Fatalf("status=%s error=%v", status, err)
			}
		})
	}
	t.Run("maintenance window", func(t *testing.T) {
		site, _ := setupWebsiteRuntimeStateTest(t, models.StatusActive, true)
		if _, err := database.GetDB().Exec(`UPDATE websites SET maintenance_security=? WHERE id=?`, `{"window":{"state":"unlocked"}}`, site.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareSiteCacheClearPlan(context.Background(), site.Domain, "dev"); err != nil {
			t.Fatalf("maintenance window should not block cache clear: %v", err)
		}
	})
}

func TestSiteCacheClearPlanPayloadIsValidJSON(t *testing.T) {
	payload := makeSiteCacheClearPayload(&models.Website{ID: 1, Domain: "example.com", Status: models.StatusActive, FCacheKey: "key"}, "dev", time.Now(), time.Now().Add(time.Minute))
	if _, err := json.Marshal(payload); err != nil {
		t.Fatal(err)
	}
}
