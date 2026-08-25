package executor

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func newSiteMigrationBatchPlannerTest(t *testing.T, available uint64) *SiteMigrationBatchPlanner {
	t.Helper()
	database.Close()
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`INSERT INTO site_migration_peers(id,status,protocol_version) VALUES ('peer_00000000001','paired',?)`, siteMigrationProtocolVersion); err != nil {
		t.Fatal(err)
	}
	planner, err := NewSiteMigrationBatchPlanner(database.GetDB(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	planner.available = func(string) (uint64, error) { return available, nil }
	planner.now = func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) }
	return planner
}

func TestSiteMigrationBatchPlannerKeepsSiteFailuresIndependent(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	result, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{
		{Domain: "one.example.com", SiteType: "wordpress", FileBytes: 100, DatabaseBytes: 50},
		{Domain: "two.example.com", Aliases: []string{"one.example.com"}, SiteType: "php", FileBytes: 100},
		{Domain: "three.example.com", SiteType: "php", FileBytes: 200},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sites) != 3 || result.Sites[0].Status != "draft" || result.Sites[1].Status != "failed_manual" || result.Sites[2].Status != "draft" {
		t.Fatalf("sites=%+v", result.Sites)
	}
	var active, failed int
	if err := planner.db.QueryRow(`SELECT SUM(status='draft'),SUM(status='failed_manual') FROM site_migration_sites WHERE batch_id=?`, result.BatchID).Scan(&active, &failed); err != nil {
		t.Fatal(err)
	}
	if active != 2 || failed != 1 {
		t.Fatalf("active=%d failed=%d", active, failed)
	}
	if err := planner.QueueTargetBatch(context.Background(), result.BatchID); err != nil {
		t.Fatal(err)
	}
	if err := planner.QueueTargetBatch(context.Background(), result.BatchID); err != nil {
		t.Fatalf("lost queue response must be safely replayable: %v", err)
	}
	var queued int
	if err := planner.db.QueryRow(`SELECT COUNT(*) FROM site_migration_sites WHERE batch_id=? AND status='queued'`, result.BatchID).Scan(&queued); err != nil || queued != 2 {
		t.Fatalf("queued=%d err=%v", queued, err)
	}
}

func TestSiteMigrationBatchPlannerQueuesOnlyPreparedTargetSite(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	result, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{
		{Domain: "one.example.com", SiteType: "wordpress", FileBytes: 100},
		{Domain: "two.example.com", SiteType: "php", FileBytes: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := planner.QueueTargetSiteForPeer(context.Background(), "peer_00000000001", result.BatchID, result.Sites[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := planner.QueueTargetSiteForPeer(context.Background(), "peer_00000000001", result.BatchID, result.Sites[0].ID); err != nil {
		t.Fatalf("lost response replay failed: %v", err)
	}
	var first, second string
	if err := planner.db.QueryRow(`SELECT status FROM site_migration_sites WHERE id=?`, result.Sites[0].ID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := planner.db.QueryRow(`SELECT status FROM site_migration_sites WHERE id=?`, result.Sites[1].ID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first != "queued" || second != "draft" {
		t.Fatalf("first=%q second=%q", first, second)
	}
}

func TestSiteMigrationBatchPlannerReplacesUnclaimedDraftForSamePeer(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	first, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: "replace.example.com", SiteType: "php", FileBytes: 100}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: "replace.example.com", SiteType: "php", FileBytes: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sites[0].ID == second.Sites[0].ID {
		t.Fatal("new batch reused stale task id")
	}
	var oldTasks, oldBatches, newTasks int
	_ = planner.db.QueryRow(`SELECT COUNT(*) FROM site_migration_sites WHERE id=?`, first.Sites[0].ID).Scan(&oldTasks)
	_ = planner.db.QueryRow(`SELECT COUNT(*) FROM site_migration_batches WHERE id=?`, first.BatchID).Scan(&oldBatches)
	_ = planner.db.QueryRow(`SELECT COUNT(*) FROM site_migration_sites WHERE id=?`, second.Sites[0].ID).Scan(&newTasks)
	if oldTasks != 0 || oldBatches != 0 || newTasks != 1 {
		t.Fatalf("oldTasks=%d oldBatches=%d newTasks=%d", oldTasks, oldBatches, newTasks)
	}
}

func TestSiteMigrationBatchPlannerRejectsReservationAtomically(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 100)
	_, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: "one.example.com", SiteType: "wordpress", FileBytes: 100}})
	if err == nil {
		t.Fatal("insufficient reservation was accepted")
	}
	var batches, sites int
	_ = planner.db.QueryRow(`SELECT COUNT(*) FROM site_migration_batches`).Scan(&batches)
	_ = planner.db.QueryRow(`SELECT COUNT(*) FROM site_migration_sites`).Scan(&sites)
	if batches != 0 || sites != 0 {
		t.Fatalf("batches=%d sites=%d", batches, sites)
	}
}

func TestSiteMigrationBatchPlannerDoesNotOversubscribeExistingReservations(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 20_000_000)
	if _, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: "one.example.com", SiteType: "wordpress", FileBytes: 100}}); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: "two.example.com", SiteType: "wordpress", FileBytes: 130}}); err == nil {
		t.Fatal("second batch overbooked target space")
	}
	var batches int
	if err := planner.db.QueryRow(`SELECT COUNT(*) FROM site_migration_batches`).Scan(&batches); err != nil || batches != 1 {
		t.Fatalf("batches=%d err=%v", batches, err)
	}
}

func TestSiteMigrationBatchPlannerIgnoresHistoricalDomainReservations(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	first, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: "one.example.com", Aliases: []string{"www.one.example.com"}, SiteType: "wordpress", FileBytes: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planner.db.Exec(`UPDATE site_migration_sites SET status='queued' WHERE id=?`, first.Sites[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"one.example.com", "www.one.example.com"} {
		result, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: domain, SiteType: "php", FileBytes: 100}})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Sites) != 1 || result.Sites[0].Status != "draft" || result.Sites[0].ErrorCode != "" || result.Sites[0].ReservedBytes == 0 {
			t.Fatalf("domain=%q sites=%+v", domain, result.Sites)
		}
	}
	var status string
	var reserved int64
	if err := planner.db.QueryRow(`SELECT status,reserved_bytes FROM site_migration_sites WHERE id=?`, first.Sites[0].ID).Scan(&status, &reserved); err != nil || status != "queued" || reserved == 0 {
		t.Fatalf("status=%q reserved=%d err=%v", status, reserved, err)
	}
}

func TestSiteMigrationBatchPlannerRejectsCurrentWebsiteDomains(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	if _, err := planner.db.Exec(`INSERT INTO websites(name,domain,aliases,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path)
		VALUES ('existing','one.example.com','www.one.example.com','wordpress','wp_one','/www/one','/logs/one','db_one','user_one','/php/one','/nginx/one')`); err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"one.example.com", "www.one.example.com"} {
		result, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: domain, SiteType: "php", FileBytes: 100}})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Sites) != 1 || result.Sites[0].Status != "failed_manual" || result.Sites[0].ErrorCode != "target_domain_conflict" || result.Sites[0].ReservedBytes != 0 {
			t.Fatalf("domain=%q sites=%+v", domain, result.Sites)
		}
	}
}

func TestSiteMigrationBatchPlannerPersistsNormalizedAliases(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	result, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: "one.example.com", Aliases: []string{"  WWW.One.Example.COM  "}, SiteType: "wordpress", FileBytes: 100}})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := planner.db.QueryRow(`SELECT settings_snapshot FROM site_migration_sites WHERE id=?`, result.Sites[0].ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Aliases []string `json:"source_aliases"`
	}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil || len(snapshot.Aliases) != 1 || snapshot.Aliases[0] != "www.one.example.com" {
		t.Fatalf("snapshot=%q aliases=%v err=%v", raw, snapshot.Aliases, err)
	}
}

func TestSiteMigrationBatchPlannerReplaysDeclaredBatchID(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	plans := []SiteMigrationBatchSitePlan{{Domain: "one.example.com", Aliases: []string{"www.one.example.com"}, SiteType: "wordpress", FileBytes: 100, DatabaseBytes: 50}}
	first, err := planner.CreateTargetBatchWithID(context.Background(), "peer_00000000001", "batch_0000000001", "admin", plans)
	if err != nil {
		t.Fatal(err)
	}
	second, err := planner.CreateTargetBatchWithID(context.Background(), "peer_00000000001", "batch_0000000001", "admin", plans)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Sites) != 1 || len(second.Sites) != 1 || first.Sites[0].ID != second.Sites[0].ID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	plans[0].FileBytes++
	if _, err := planner.CreateTargetBatchWithID(context.Background(), "peer_00000000001", "batch_0000000001", "admin", plans); err == nil {
		t.Fatal("changed replay was accepted")
	}
}

func TestMigrationReservationBytesIncludesPublishPeakAndMargin(t *testing.T) {
	got, err := migrationReservationBytes(100, 50)
	if err != nil || got != 330+101+siteMigrationShardMaxExtra {
		t.Fatalf("got=%d err=%v", got, err)
	}
}
