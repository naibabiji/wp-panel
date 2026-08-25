package executor

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeSiteMigrationCutoverOps struct {
	events []string
	blocks []string
	failAt string
	onStep func(string)
}

func (f *fakeSiteMigrationCutoverOps) step(event string) error {
	f.events = append(f.events, event)
	if f.onStep != nil {
		f.onStep(event)
	}
	if f.failAt == event {
		return errSiteMigrationBusy
	}
	return nil
}
func (f *fakeSiteMigrationCutoverOps) ApplyMarker(_, _, block string) error {
	f.blocks = append(f.blocks, block)
	return f.step("marker_on")
}
func (f *fakeSiteMigrationCutoverOps) RemoveMarker(_, _ string) error {
	return f.step("marker_off")
}
func (f *fakeSiteMigrationCutoverOps) RestoreWordPressCron(_, _, _ string) error {
	return f.step("wp_cron_restore")
}
func (f *fakeSiteMigrationCutoverOps) ReloadCron() error { return f.step("cron_reload") }

func TestSiteMigrationCutoverActivatesAutomaticallyAfterLocalHealth(t *testing.T) {
	service, siteID, _, _ := setupSiteMigrationCutoverTest(t)
	if err := service.ActivateAfterHealth(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(service.ops.(*fakeSiteMigrationCutoverOps).events, "|"); got != "wp_cron_restore|marker_off|cron_reload" {
		t.Fatalf("automatic activation events=%q", got)
	}
	var status, stage, lockStatus string
	var monitoring, cronEnabled int
	if err := service.db.QueryRow(`SELECT status,stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &stage); err != nil {
		t.Fatal(err)
	}
	if err := service.db.QueryRow(`SELECT status FROM site_migration_locks WHERE migration_site_id='migration_0000001' AND direction='target'`).Scan(&lockStatus); err != nil {
		t.Fatal(err)
	}
	if err := service.db.QueryRow(`SELECT monitoring_enabled FROM websites WHERE id=?`, siteID).Scan(&monitoring); err != nil {
		t.Fatal(err)
	}
	if err := service.db.QueryRow(`SELECT enabled FROM cron_jobs WHERE site_id=?`, siteID).Scan(&cronEnabled); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || stage != "completed" || lockStatus != "released" || monitoring != 1 || cronEnabled != 1 {
		t.Fatalf("status=%q stage=%q lock=%q monitoring=%d cron=%d", status, stage, lockStatus, monitoring, cronEnabled)
	}
}

func TestSiteMigrationCutoverRequiresBothObserverWindowsAndActivates(t *testing.T) {
	service, siteID, key, token := setupSiteMigrationCutoverTest(t)
	if err := service.InstallTargetMarker(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	start := freezerTestTime().UTC()
	marker := SiteMigrationMarkerResponse{Task: "migration_0000001", Role: "target", IssuedAt: start.Unix()}
	marker.Signature = signSiteMigrationMarker(key, token, marker)
	for index := 0; index < siteMigrationCutoverProbeCount; index++ {
		at := start.Add(time.Duration(index) * time.Minute)
		service.now = func() time.Time { return at }
		if err := service.recordProbe(context.Background(), "migration_0000001", "target"); err != nil {
			t.Fatal(err)
		}
		attestation, _ := NewSiteMigrationProbeAttestation("migration_0000001", key, token, at, marker)
		if err := service.AcceptSourceProbe(context.Background(), "migration_0000001", attestation); err != nil {
			t.Fatal(err)
		}
	}
	service.now = func() time.Time { return start.Add(2 * time.Minute) }
	if err := service.Confirm(context.Background(), "migration_0000001", "example.com", "admin", "", false); err != nil {
		t.Fatal(err)
	}
	if strings.Join(service.ops.(*fakeSiteMigrationCutoverOps).events, "|") != "marker_on|wp_cron_restore|marker_off|cron_reload" {
		t.Fatalf("events=%v", service.ops.(*fakeSiteMigrationCutoverOps).events)
	}
	var status, stage, lockStatus string
	var monitoring, cronEnabled int
	_ = service.db.QueryRow(`SELECT status,stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &stage)
	_ = service.db.QueryRow(`SELECT status FROM site_migration_locks WHERE migration_site_id='migration_0000001' AND direction='target'`).Scan(&lockStatus)
	_ = service.db.QueryRow(`SELECT monitoring_enabled FROM websites WHERE id=?`, siteID).Scan(&monitoring)
	_ = service.db.QueryRow(`SELECT enabled FROM cron_jobs WHERE site_id=? AND task_type='wp_cron'`, siteID).Scan(&cronEnabled)
	if status != "completed" || stage != "completed" || lockStatus != "released" || monitoring != 1 || cronEnabled != 1 {
		t.Fatalf("status=%q stage=%q lock=%q monitoring=%d cron=%d", status, stage, lockStatus, monitoring, cronEnabled)
	}
}

func TestSiteMigrationCutoverRejectsOneSidedVerification(t *testing.T) {
	service, _, _, _ := setupSiteMigrationCutoverTest(t)
	start := freezerTestTime().UTC()
	for index := 0; index < siteMigrationCutoverProbeCount; index++ {
		service.now = func() time.Time { return start.Add(time.Duration(index) * time.Minute) }
		_ = service.recordProbe(context.Background(), "migration_0000001", "target")
	}
	service.now = func() time.Time { return start.Add(2 * time.Minute) }
	if err := service.Confirm(context.Background(), "migration_0000001", "example.com", "admin", "", false); err == nil {
		t.Fatal("one-sided verification was accepted")
	}
}

func TestSiteMigrationCutoverRejectsSourceOnlyVerification(t *testing.T) {
	service, _, key, token := setupSiteMigrationCutoverTest(t)
	start := freezerTestTime().UTC()
	marker := SiteMigrationMarkerResponse{Task: "migration_0000001", Role: "target", IssuedAt: start.Unix()}
	marker.Signature = signSiteMigrationMarker(key, token, marker)
	for index := 0; index < siteMigrationCutoverProbeCount; index++ {
		at := start.Add(time.Duration(index) * time.Minute)
		service.now = func() time.Time { return at }
		attestation, _ := NewSiteMigrationProbeAttestation("migration_0000001", key, token, at, marker)
		_ = service.AcceptSourceProbe(context.Background(), "migration_0000001", attestation)
	}
	if err := service.Confirm(context.Background(), "migration_0000001", "example.com", "admin", "", false); err == nil {
		t.Fatal("source-only verification was accepted")
	}
}

func TestSiteMigrationCutoverFailureBreaksConsecutiveWindow(t *testing.T) {
	service, _, key, token := setupSiteMigrationCutoverTest(t)
	start := freezerTestTime().UTC()
	marker := SiteMigrationMarkerResponse{Task: "migration_0000001", Role: "target", IssuedAt: start.Unix()}
	marker.Signature = signSiteMigrationMarker(key, token, marker)
	for index := 0; index < siteMigrationCutoverProbeCount; index++ {
		at := start.Add(time.Duration(index) * time.Minute)
		service.now = func() time.Time { return at }
		_ = service.recordProbe(context.Background(), "migration_0000001", "target")
		attestation, _ := NewSiteMigrationProbeAttestation("migration_0000001", key, token, at, marker)
		_ = service.AcceptSourceProbe(context.Background(), "migration_0000001", attestation)
	}
	service.now = func() time.Time { return start.Add(3 * time.Minute) }
	failure, _ := NewSiteMigrationProbeFailureAttestation("migration_0000001", key, service.now())
	if err := service.AcceptSourceProbe(context.Background(), "migration_0000001", failure); err != nil {
		t.Fatal(err)
	}
	if err := service.Confirm(context.Background(), "migration_0000001", "example.com", "admin", "", false); err == nil {
		t.Fatal("window with a latest failure was accepted")
	}
}

func TestSiteMigrationCutoverForceRequiresDomainAndReason(t *testing.T) {
	service, _, _, _ := setupSiteMigrationCutoverTest(t)
	if err := service.Confirm(context.Background(), "migration_0000001", "example.com", "", "operator override", true); err == nil {
		t.Fatal("empty operator was accepted")
	}
	if err := service.Confirm(context.Background(), "migration_0000001", "wrong.example", "admin", "operator override", true); err == nil {
		t.Fatal("wrong domain was accepted")
	}
	if err := service.Confirm(context.Background(), "migration_0000001", "example.com", "admin", "", true); err == nil {
		t.Fatal("empty force reason was accepted")
	}
}

func TestSiteMigrationTargetMarkerRetriesExistingIntent(t *testing.T) {
	service, _, _, _ := setupSiteMigrationCutoverTest(t)
	ops := service.ops.(*fakeSiteMigrationCutoverOps)
	ops.failAt = "marker_on"
	if err := service.InstallTargetMarker(context.Background(), "migration_0000001"); err == nil {
		t.Fatal("marker publish failure was not reported")
	}
	service.now = func() time.Time { return freezerTestTime().Add(time.Second) }
	ops.failAt = ""
	if err := service.InstallTargetMarker(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	var resources int
	_ = service.db.QueryRow(`SELECT COUNT(*) FROM site_migration_resources WHERE migration_site_id='migration_0000001' AND resource_type='target_marker_config'`).Scan(&resources)
	if resources != 1 || strings.Join(ops.events, "|") != "marker_on|marker_on" || len(ops.blocks) != 2 || ops.blocks[0] != ops.blocks[1] {
		t.Fatalf("resources=%d events=%v blocks_equal=%t", resources, ops.events, len(ops.blocks) == 2 && ops.blocks[0] == ops.blocks[1])
	}
}

func TestSiteMigrationCutoverPersistsActivationCommitFailure(t *testing.T) {
	service, _, _, _ := setupSiteMigrationCutoverTest(t)
	if _, err := service.db.Exec(`DELETE FROM site_migration_resources WHERE migration_site_id='migration_0000001' AND resource_type='cron_job'`); err != nil {
		t.Fatal(err)
	}
	if err := service.Confirm(context.Background(), "migration_0000001", "example.com", "admin", "operator override", true); err == nil {
		t.Fatal("cron identity mismatch was accepted")
	}
	var status, stage, code string
	_ = service.db.QueryRow(`SELECT status,stage,error_code FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &stage, &code)
	if status != "failed_retryable" || stage != "activating_target" || code != "activation_commit_failed" {
		t.Fatalf("status=%q stage=%q code=%q", status, stage, code)
	}
}

func TestSiteMigrationCutoverRejectsChangedCronState(t *testing.T) {
	service, siteID, _, _ := setupSiteMigrationCutoverTest(t)
	if _, err := service.db.Exec(`UPDATE cron_jobs SET enabled=1 WHERE site_id=?`, siteID); err != nil {
		t.Fatal(err)
	}
	if err := service.Confirm(context.Background(), "migration_0000001", "example.com", "admin", "operator override", true); err == nil {
		t.Fatal("changed cron state was accepted")
	}
	var status, code string
	_ = service.db.QueryRow(`SELECT status,error_code FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &code)
	if status != "failed_retryable" || code != "activation_commit_failed" {
		t.Fatalf("status=%q code=%q", status, code)
	}
}

func TestSiteMigrationCutoverPersistsChangedTargetLockFailure(t *testing.T) {
	service, _, _, _ := setupSiteMigrationCutoverTest(t)
	ops := service.ops.(*fakeSiteMigrationCutoverOps)
	ops.onStep = func(event string) {
		if event == "marker_off" {
			_, _ = service.db.Exec(`UPDATE site_migration_locks SET status='released' WHERE migration_site_id='migration_0000001' AND direction='target'`)
		}
	}
	if err := service.Confirm(context.Background(), "migration_0000001", "example.com", "admin", "operator override", true); err == nil {
		t.Fatal("changed target lock was accepted")
	}
	var status, stage, code string
	_ = service.db.QueryRow(`SELECT status,stage,error_code FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &stage, &code)
	if status != "failed_retryable" || stage != "activating_target" || code != "activation_commit_failed" {
		t.Fatalf("status=%q stage=%q code=%q", status, stage, code)
	}
}

func TestSiteMigrationCutoverResumesAfterPartialActivationFailure(t *testing.T) {
	service, _, _, _ := setupSiteMigrationCutoverTest(t)
	ops := service.ops.(*fakeSiteMigrationCutoverOps)
	ops.failAt = "marker_off"
	if err := service.Confirm(context.Background(), "migration_0000001", "example.com", "admin", "operator override", true); err == nil {
		t.Fatal("activation failure was not reported")
	}
	ops.failAt = ""
	if err := service.ResumeActivation(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	var status, stage, lockStatus string
	_ = service.db.QueryRow(`SELECT status,stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &stage)
	_ = service.db.QueryRow(`SELECT status FROM site_migration_locks WHERE migration_site_id='migration_0000001' AND direction='target'`).Scan(&lockStatus)
	if status != "completed" || stage != "completed" || lockStatus != "released" {
		t.Fatalf("status=%q stage=%q lock=%q", status, stage, lockStatus)
	}
}

func TestSiteMigrationCutoverRetriesCronRuntimeSync(t *testing.T) {
	service, _, _, _ := setupSiteMigrationCutoverTest(t)
	ops := service.ops.(*fakeSiteMigrationCutoverOps)
	ops.failAt = "cron_reload"
	if err := service.Confirm(context.Background(), "migration_0000001", "example.com", "admin", "operator override", true); err == nil {
		t.Fatal("cron runtime sync failure was not reported")
	}
	var status, stage, lockStatus, code string
	_ = service.db.QueryRow(`SELECT status,stage,error_code FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &stage, &code)
	_ = service.db.QueryRow(`SELECT status FROM site_migration_locks WHERE migration_site_id='migration_0000001' AND direction='target'`).Scan(&lockStatus)
	if status != "failed_retryable" || stage != "activation_runtime_sync" || lockStatus != "released" || code != "cron_runtime_sync_failed" {
		t.Fatalf("status=%q stage=%q lock=%q code=%q", status, stage, lockStatus, code)
	}
	ops.failAt = ""
	if err := service.ResumeActivation(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	_ = service.db.QueryRow(`SELECT status,stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &stage)
	if status != "completed" || stage != "completed" {
		t.Fatalf("status=%q stage=%q", status, stage)
	}
}

func TestSiteMigrationTargetMarkerRoundTrip(t *testing.T) {
	token := strings.Repeat("m", 48)
	marker := SiteMigrationMarkerResponse{Task: "migration_0000001", Role: "target", IssuedAt: freezerTestTime().Unix()}
	marker.Signature = signSiteMigrationMarker("shared-key", token, marker)
	if err := VerifySiteMigrationTargetMarker("migration_0000001", "shared-key", token, marker); err != nil {
		t.Fatal("valid marker rejected")
	}
	marker.Role = "source"
	if err := VerifySiteMigrationTargetMarker("migration_0000001", "shared-key", token, marker); err == nil {
		t.Fatal("modified marker accepted")
	}
	config, err := injectSiteMigrationTargetMarker("server {\n listen 80;\n}\nserver {\n listen 443 ssl;\n}\n", renderSiteMigrationTargetMarkerBlock(strings.Repeat("m", 48), `{"role":"target"}`))
	if err != nil || strings.Count(config, "WP Panel migration target marker begin") != 2 {
		t.Fatalf("config=%q err=%v", config, err)
	}
	if retried, retryErr := injectSiteMigrationTargetMarker(config, renderSiteMigrationTargetMarkerBlock(strings.Repeat("m", 48), `{"role":"target"}`)); retryErr != nil || retried != config {
		t.Fatalf("idempotent marker retry failed: %v", retryErr)
	}
	restored, err := removeSiteMigrationTargetMarker(config)
	if err != nil || strings.Contains(restored, "migration target marker") {
		t.Fatalf("restored=%q err=%v", restored, err)
	}
}

func TestSiteMigrationCutoverRejectsTamperedSourceAttestation(t *testing.T) {
	service, _, key, token := setupSiteMigrationCutoverTest(t)
	at := freezerTestTime().UTC()
	marker := SiteMigrationMarkerResponse{Task: "migration_0000001", Role: "target", IssuedAt: at.Unix()}
	marker.Signature = signSiteMigrationMarker(key, token, marker)
	attestation, err := NewSiteMigrationProbeAttestation("migration_0000001", key, token, at, marker)
	if err != nil {
		t.Fatal(err)
	}
	attestation.Success = false
	if err := service.AcceptSourceProbe(context.Background(), "migration_0000001", attestation); err == nil {
		t.Fatal("tampered source attestation was accepted")
	}
	for _, mutate := range []func(*SiteMigrationProbeAttestation){
		func(value *SiteMigrationProbeAttestation) { value.MarkerSignature = "tampered" },
		func(value *SiteMigrationProbeAttestation) { value.ObservedAt++ },
		func(value *SiteMigrationProbeAttestation) { value.Task = "migration_0000002" },
	} {
		candidate, _ := NewSiteMigrationProbeAttestation("migration_0000001", key, token, at, marker)
		mutate(&candidate)
		if err := service.AcceptSourceProbe(context.Background(), "migration_0000001", candidate); err == nil {
			t.Fatal("tampered source attestation field was accepted")
		}
	}
}

func setupSiteMigrationCutoverTest(t *testing.T) (*SiteMigrationCutoverService, int64, string, string) {
	t.Helper()
	resourceService, root := setupConfiguredMigrationPublisherTest(t)
	publisher, _ := NewSiteMigrationTargetPublisher(resourceService.db, resourceService.cfg, root)
	configureOps := &fakeMigrationTargetConfigureOps{}
	publisher.now = freezerTestTime
	if err := publisher.ConfigureAndHealth(context.Background(), "migration_0000001", configureOps); err != nil {
		t.Fatal(err)
	}
	outbound := strings.Repeat("o", 48)
	if _, err := resourceService.db.Exec(`UPDATE site_migration_peers SET status='paired',outbound_credential=? WHERE id='peer_00000000001'`, outbound); err != nil {
		t.Fatal(err)
	}
	cutover, _ := NewSiteMigrationCutoverService(resourceService.db)
	cutover.ops = &fakeSiteMigrationCutoverOps{}
	cutover.now = freezerTestTime
	var siteID int64
	_ = resourceService.db.QueryRow(`SELECT target_site_id FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&siteID)
	return cutover, siteID, hashMigrationSecret(outbound), strings.Repeat("m", 48)
}
