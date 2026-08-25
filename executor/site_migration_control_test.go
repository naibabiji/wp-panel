package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

type migrationRoundTripper func(*http.Request) (*http.Response, error)

func (f migrationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type fakeMigrationWorkflowPairing struct {
	result SiteMigrationBatchPlanResult
	calls  int
}

func (f *fakeMigrationWorkflowPairing) RemoteCreateTargetBatch(context.Context, string, string, string, []SiteMigrationBatchSitePlan) (*SiteMigrationBatchPlanResult, error) {
	f.calls++
	return &f.result, nil
}

type fakeMigrationWorkflowSource struct {
	declarations []SiteMigrationSourceDeclaration
	queued       int
}

func (f *fakeMigrationWorkflowSource) DeclareBatch(_ context.Context, _, _, _ string, declarations []SiteMigrationSourceDeclaration) error {
	f.declarations = append([]SiteMigrationSourceDeclaration(nil), declarations...)
	return nil
}
func (f *fakeMigrationWorkflowSource) QueueBatch(context.Context, string, string) error {
	f.queued++
	return nil
}

func TestSiteMigrationWorkflowStartsOnlyAcceptedTargetSites(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.php"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := planner.db.Exec(`INSERT INTO websites(name,domain,aliases,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path) VALUES ('one','one.example.com','www.one.example.com','wordpress','wp_one',?,'/logs/one','db_one','user_one','/php/one','/nginx/one')`, root)
	if err != nil {
		t.Fatal(err)
	}
	siteID, _ := result.LastInsertId()
	pairing := &fakeMigrationWorkflowPairing{result: SiteMigrationBatchPlanResult{BatchID: "batch_0000000001", Sites: []SiteMigrationBatchPlanSiteResult{{ID: "migration_0000001", Domain: "one.example.com", Status: "draft"}, {ID: "migration_0000002", Domain: "blocked.example.com", Status: "failed_manual"}}}}
	source := &fakeMigrationWorkflowSource{}
	service := &SiteMigrationWorkflowService{db: planner.db, cfg: &config.Config{}, pairing: pairing, source: source, databaseSizes: func(*config.Config) (map[string]int64, error) { return map[string]int64{"db_one": 7}, nil }}
	estimate, err := service.Estimate(context.Background(), []int64{siteID})
	if err != nil || estimate.TotalFileBytes != 5 || estimate.TotalDatabaseBytes != 7 || estimate.TotalReservationBytes != 27+6+siteMigrationShardMaxExtra {
		t.Fatalf("estimate=%+v err=%v", estimate, err)
	}
	if _, err := service.Start(context.Background(), "peer_00000000001", "batch_0000000001", "admin", []int64{siteID}); err != nil {
		t.Fatal(err)
	}
	if len(source.declarations) != 1 || source.declarations[0].ID != "migration_0000001" || source.declarations[0].SourceSiteID != siteID || source.queued != 1 {
		t.Fatalf("declarations=%+v queued=%d", source.declarations, source.queued)
	}
}

func TestSiteMigrationWorkflowRejectsBatchWithoutAcceptedTargetSites(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	root := t.TempDir()
	result, err := planner.db.Exec(`INSERT INTO websites(name,domain,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path) VALUES ('one','one.example.com','php','php_one',?,'/logs/one','db_one','user_one','/php/one','/nginx/one')`, root)
	if err != nil {
		t.Fatal(err)
	}
	siteID, _ := result.LastInsertId()
	pairing := &fakeMigrationWorkflowPairing{result: SiteMigrationBatchPlanResult{BatchID: "batch_0000000001", Sites: []SiteMigrationBatchPlanSiteResult{{ID: "migration_0000001", Domain: "one.example.com", Status: "failed_manual", ErrorCode: "target_domain_conflict"}}}}
	source := &fakeMigrationWorkflowSource{}
	deleted := 0
	service := &SiteMigrationWorkflowService{db: planner.db, cfg: &config.Config{}, pairing: pairing, source: source, databaseSizes: func(*config.Config) (map[string]int64, error) { return map[string]int64{"db_one": 0}, nil }, deleteRemote: func(context.Context, string, string) error { deleted++; return nil }}
	if _, err := service.Start(context.Background(), "peer_00000000001", "batch_0000000001", "admin", []int64{siteID}); err == nil {
		t.Fatal("a batch without any accepted target site must not report a successful start")
	}
	if len(source.declarations) != 0 || source.queued != 0 {
		t.Fatalf("declarations=%+v queued=%d", source.declarations, source.queued)
	}
	if deleted != 1 {
		t.Fatalf("rejected target tasks deleted=%d", deleted)
	}
}

func TestSiteMigrationWorkflowRejectsPausedSourceBeforeRemoteRequest(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	root := t.TempDir()
	result, err := planner.db.Exec(`INSERT INTO websites(name,domain,status,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path) VALUES ('one','one.example.com','paused','php','php_one',?,'/logs/one','db_one','user_one','/php/one','/nginx/one')`, root)
	if err != nil {
		t.Fatal(err)
	}
	siteID, _ := result.LastInsertId()
	pairing := &fakeMigrationWorkflowPairing{}
	prepareCalls := 0
	service := &SiteMigrationWorkflowService{db: planner.db, cfg: &config.Config{}, pairing: pairing, source: &fakeMigrationWorkflowSource{}, databaseSizes: func(*config.Config) (map[string]int64, error) { return map[string]int64{"db_one": 0}, nil }, prepareStart: func(context.Context, []int64) error { prepareCalls++; return nil }}
	if _, err := service.Estimate(context.Background(), []int64{siteID}); err == nil {
		t.Fatal("paused website estimate was accepted")
	}
	if _, err := service.Start(context.Background(), "peer_00000000001", "batch_0000000001", "admin", []int64{siteID}); err == nil {
		t.Fatal("paused website migration was accepted")
	}
	if pairing.calls != 0 {
		t.Fatalf("remote target was called %d times", pairing.calls)
	}
	if prepareCalls != 0 {
		t.Fatalf("paused website changed migration state before rejection: prepareCalls=%d", prepareCalls)
	}
}

func TestSiteMigrationPrepareStartForgetsUnstartedSourceHistory(t *testing.T) {
	preparation, _, siteID := setupSiteMigrationSourcePreparationTest(t)
	if err := preparation.DeclareBatch(context.Background(), "peer_00000000001", "batch_0000000001", "admin", []SiteMigrationSourceDeclaration{{ID: "migration_0000001", SourceSiteID: siteID, Domain: "one.example.com", Aliases: []string{"www.one.example.com"}, SiteType: "wordpress"}}); err != nil {
		t.Fatal(err)
	}
	control := &SiteMigrationControlService{db: preparation.db, stagingRoot: t.TempDir()}
	if err := control.prepareStart(context.Background(), []int64{siteID}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"site_migration_sites", "site_migration_batches", "site_migration_locks"} {
		var count int
		if err := preparation.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("table=%s count=%d err=%v", table, count, err)
		}
	}
	var websiteCount int
	if err := preparation.db.QueryRow(`SELECT COUNT(*) FROM websites WHERE id=?`, siteID).Scan(&websiteCount); err != nil || websiteCount != 1 {
		t.Fatalf("websiteCount=%d err=%v", websiteCount, err)
	}
}

func TestSiteMigrationPrepareStartRejectsSourceTaskWithActiveWorkerLease(t *testing.T) {
	preparation, _, siteID := setupSiteMigrationSourcePreparationTest(t)
	if err := preparation.DeclareBatch(context.Background(), "peer_00000000001", "batch_0000000001", "admin", []SiteMigrationSourceDeclaration{{ID: "migration_0000001", SourceSiteID: siteID, Domain: "one.example.com", Aliases: []string{"www.one.example.com"}, SiteType: "wordpress"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := preparation.db.Exec(`UPDATE site_migration_sites SET status='running',lease_owner='worker_0000000001',lease_expires_at=? WHERE id='migration_0000001'`, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	control := &SiteMigrationControlService{db: preparation.db, stagingRoot: t.TempDir()}
	if err := control.prepareStart(context.Background(), []int64{siteID}); err == nil {
		t.Fatal("active worker lease allowed old source task replacement")
	}
	var tasks, locks, websites int
	_ = preparation.db.QueryRow(`SELECT COUNT(*) FROM site_migration_sites WHERE id='migration_0000001' AND lease_owner='worker_0000000001'`).Scan(&tasks)
	_ = preparation.db.QueryRow(`SELECT COUNT(*) FROM site_migration_locks WHERE migration_site_id='migration_0000001' AND status='active'`).Scan(&locks)
	_ = preparation.db.QueryRow(`SELECT COUNT(*) FROM websites WHERE id=?`, siteID).Scan(&websites)
	if tasks != 1 || locks != 1 || websites != 1 {
		t.Fatalf("tasks=%d locks=%d websites=%d", tasks, locks, websites)
	}
}

func TestSiteMigrationDeleteCompletedTargetForgetsTaskButKeepsWebsite(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	result, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: "kept.example.com", SiteType: "php", FileBytes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	taskID := result.Sites[0].ID
	insert, err := planner.db.Exec(`INSERT INTO websites(name,domain,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path) VALUES ('kept','kept.example.com','php','kept_user','/www/kept','/logs/kept','kept_db','kept_db_user','/php/kept','/nginx/kept')`)
	if err != nil {
		t.Fatal(err)
	}
	siteID, _ := insert.LastInsertId()
	if _, err := planner.db.Exec(`UPDATE site_migration_sites SET status='completed',stage='completed',target_site_id=? WHERE id=?`, siteID, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.db.Exec(`INSERT INTO site_migration_locks(domain,site_id,migration_site_id,direction,status) VALUES ('kept.example.com',?,?,'target','released')`, siteID, taskID); err != nil {
		t.Fatal(err)
	}
	control := &SiteMigrationControlService{db: planner.db, stagingRoot: t.TempDir()}
	scope, err := control.loadTaskCleanupScope(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.deleteTargetTask(context.Background(), scope); err != nil {
		t.Fatal(err)
	}
	var websites, tasks, locks int
	_ = planner.db.QueryRow(`SELECT COUNT(*) FROM websites WHERE id=?`, siteID).Scan(&websites)
	_ = planner.db.QueryRow(`SELECT COUNT(*) FROM site_migration_sites WHERE id=?`, taskID).Scan(&tasks)
	_ = planner.db.QueryRow(`SELECT COUNT(*) FROM site_migration_locks WHERE migration_site_id=?`, taskID).Scan(&locks)
	if websites != 1 || tasks != 0 || locks != 0 {
		t.Fatalf("websites=%d tasks=%d locks=%d", websites, tasks, locks)
	}
}

func TestSiteMigrationActivatedTargetFailureIsRetainedAndDeleteKeepsWebsite(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	result, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: "activated.example.com", SiteType: "php", FileBytes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	taskID := result.Sites[0].ID
	insert, err := planner.db.Exec(`INSERT INTO websites(name,domain,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path) VALUES ('activated','activated.example.com','php','activated_user','/www/activated','/logs/activated','activated_db','activated_db_user','/php/activated','/nginx/activated')`)
	if err != nil {
		t.Fatal(err)
	}
	siteID, _ := insert.LastInsertId()
	if _, err := planner.db.Exec(`UPDATE site_migration_sites SET status='failed_retryable',stage='activation_runtime_sync',target_site_id=?,error_code='cron_runtime_sync_failed' WHERE id=?`, siteID, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.db.Exec(`INSERT INTO site_migration_locks(domain,site_id,migration_site_id,direction,status) VALUES ('activated.example.com',?,?,'target','released')`, siteID, taskID); err != nil {
		t.Fatal(err)
	}
	control := &SiteMigrationControlService{db: planner.db, stagingRoot: t.TempDir()}
	processor := &siteMigrationStageProcessor{cleanup: control}
	if err := processor.HandleFailure(context.Background(), &siteMigrationSite{ID: taskID}, errors.New("cron reload failed")); err != nil {
		t.Fatal(err)
	}
	var websites, tasks int
	_ = planner.db.QueryRow(`SELECT COUNT(*) FROM websites WHERE id=?`, siteID).Scan(&websites)
	_ = planner.db.QueryRow(`SELECT COUNT(*) FROM site_migration_sites WHERE id=? AND status='failed_retryable' AND stage='activation_runtime_sync'`, taskID).Scan(&tasks)
	if websites != 1 || tasks != 1 {
		t.Fatalf("after automatic cleanup websites=%d tasks=%d", websites, tasks)
	}
	if err := control.DeleteTask(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
	_ = planner.db.QueryRow(`SELECT COUNT(*) FROM websites WHERE id=?`, siteID).Scan(&websites)
	_ = planner.db.QueryRow(`SELECT COUNT(*) FROM site_migration_sites WHERE id=?`, taskID).Scan(&tasks)
	if websites != 1 || tasks != 0 {
		t.Fatalf("after explicit delete websites=%d tasks=%d", websites, tasks)
	}
}

func TestSiteMigrationDeleteFailedTargetWithOnlyStagingFiles(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	result, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: "staging.example.com", SiteType: "php", FileBytes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	taskID := result.Sites[0].ID
	stagingRoot := t.TempDir()
	taskRoot := filepath.Join(stagingRoot, taskID)
	if err := os.MkdirAll(filepath.Join(taskRoot, "shards"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskRoot, "shards", "partial"), []byte("temporary"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.db.Exec(`UPDATE site_migration_sites SET status='failed_retryable',stage='transferring_files',error_code='target_transfer_failed' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.db.Exec(`INSERT INTO site_migration_locks(domain,migration_site_id,direction,status) VALUES ('staging.example.com',?,'target','active')`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.db.Exec(`INSERT INTO site_migration_resources(migration_site_id,resource_type,identifier,ownership_tag,status) VALUES (?,'target_staging_root',?,?,'created')`, taskID, taskRoot, taskID); err != nil {
		t.Fatal(err)
	}
	control := &SiteMigrationControlService{db: planner.db, stagingRoot: stagingRoot}
	if err := control.DeleteTask(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(taskRoot); !os.IsNotExist(err) {
		t.Fatalf("target staging files remain after task deletion: %v", err)
	}
	for _, table := range []string{"site_migration_sites", "site_migration_locks", "site_migration_resources"} {
		var count int
		if err := planner.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+map[string]string{"site_migration_sites": "id", "site_migration_locks": "migration_site_id", "site_migration_resources": "migration_site_id"}[table]+`=?`, taskID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("table=%s count=%d err=%v", table, count, err)
		}
	}
}

func TestSiteMigrationDeleteTargetWithRuntimeIntentRequiresRollback(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	result, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: "runtime.example.com", SiteType: "php", FileBytes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	taskID := result.Sites[0].ID
	if _, err := planner.db.Exec(`UPDATE site_migration_sites SET status='failed_retryable',stage='publishing',error_code='target_publish_failed' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.db.Exec(`INSERT INTO site_migration_resources(migration_site_id,resource_type,identifier,ownership_tag,status) VALUES (?,'system_user','wp_runtime',?,'created')`, taskID, taskID); err != nil {
		t.Fatal(err)
	}
	control := &SiteMigrationControlService{db: planner.db, stagingRoot: t.TempDir()}
	if err := control.DeleteTask(context.Background(), taskID); err == nil {
		t.Fatal("target runtime intent was deleted without the rollback service")
	}
	var tasks, resources int
	_ = planner.db.QueryRow(`SELECT COUNT(*) FROM site_migration_sites WHERE id=?`, taskID).Scan(&tasks)
	_ = planner.db.QueryRow(`SELECT COUNT(*) FROM site_migration_resources WHERE migration_site_id=?`, taskID).Scan(&resources)
	if tasks != 1 || resources != 1 {
		t.Fatalf("tasks=%d resources=%d", tasks, resources)
	}
}

func TestSiteMigrationWorkflowRetriesOnlyKnownRetryableState(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	service := &SiteMigrationWorkflowService{db: planner.db}
	result, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: "retry.example.com", SiteType: "wordpress", FileBytes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	taskID := result.Sites[0].ID
	if _, err := planner.db.Exec(`UPDATE site_migration_sites SET status='failed_retryable',stage='publishing',error_code='database_import_failed' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if err := service.Retry(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
	var status, stage, code string
	if err := planner.db.QueryRow(`SELECT status,stage,error_code FROM site_migration_sites WHERE id=?`, taskID).Scan(&status, &stage, &code); err != nil || status != "queued" || stage != "publishing" || code != "" {
		t.Fatalf("status=%q stage=%q code=%q err=%v", status, stage, code, err)
	}
	if _, err := planner.db.Exec(`UPDATE site_migration_sites SET status='interrupted_unknown',error_code='lease_expired' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if err := service.Retry(context.Background(), taskID); err == nil {
		t.Fatal("interrupted_unknown task must not be retried")
	}
}

func TestSiteMigrationControlRetriesTargetActivationThroughCutover(t *testing.T) {
	for _, stage := range []string{"activating_target", "activation_runtime_sync"} {
		t.Run(stage, func(t *testing.T) {
			planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
			result, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: strings.ReplaceAll(stage, "_", "-") + ".example.com", SiteType: "wordpress", FileBytes: 1}})
			if err != nil {
				t.Fatal(err)
			}
			taskID := result.Sites[0].ID
			if _, err := planner.db.Exec(`UPDATE site_migration_sites SET status='failed_retryable',stage=?,error_code='activation_commit_failed' WHERE id=?`, stage, taskID); err != nil {
				t.Fatal(err)
			}
			called := 0
			control := &SiteMigrationControlService{
				db:       planner.db,
				workflow: &SiteMigrationWorkflowService{db: planner.db},
				resumeActivation: func(_ context.Context, got string) error {
					called++
					if got != taskID {
						t.Fatalf("task=%q", got)
					}
					return nil
				},
			}
			if err := control.Retry(context.Background(), taskID); err != nil {
				t.Fatal(err)
			}
			var status, currentStage string
			if err := planner.db.QueryRow(`SELECT status,stage FROM site_migration_sites WHERE id=?`, taskID).Scan(&status, &currentStage); err != nil {
				t.Fatal(err)
			}
			if called != 1 || status != "failed_retryable" || currentStage != stage {
				t.Fatalf("called=%d status=%q stage=%q", called, status, currentStage)
			}
		})
	}
}

func TestSiteMigrationControlRetriesLocalSourceFailureBeforeRemoteTarget(t *testing.T) {
	preparation, _, siteID := setupSiteMigrationSourcePreparationTest(t)
	if err := preparation.DeclareBatch(context.Background(), "peer_00000000001", "batch_0000000001", "admin", []SiteMigrationSourceDeclaration{{ID: "migration_0000001", SourceSiteID: siteID, Domain: "one.example.com", Aliases: []string{"www.one.example.com"}, SiteType: "wordpress"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := preparation.db.Exec(`UPDATE site_migration_sites SET status='failed_retryable',error_code='source_prepare_failed' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	control := &SiteMigrationControlService{db: preparation.db, workflow: &SiteMigrationWorkflowService{db: preparation.db}}
	if err := control.Retry(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	var status, code string
	if err := preparation.db.QueryRow(`SELECT status,error_code FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || code != "" {
		t.Fatalf("status=%q code=%q", status, code)
	}
}

func TestSiteMigrationTargetStatusesArePeerScopedAndComplete(t *testing.T) {
	planner := newSiteMigrationBatchPlannerTest(t, 1<<30)
	result, err := planner.CreateTargetBatch(context.Background(), "peer_00000000001", "admin", []SiteMigrationBatchSitePlan{{Domain: "status.example.com", SiteType: "wordpress", FileBytes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	stagingRoot := t.TempDir()
	control := &SiteMigrationControlService{db: planner.db, stagingRoot: stagingRoot}
	if _, err := planner.db.Exec(`UPDATE site_migration_sites SET status='running',stage='transferring_files' WHERE id=?`, result.Sites[0].ID); err != nil {
		t.Fatal(err)
	}
	shardRoot := filepath.Join(stagingRoot, result.Sites[0].ID, "shards")
	if err := os.MkdirAll(filepath.Join(shardRoot, "extract-000000"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shardRoot, "shard-000000.tar.zst.partial"), []byte("compressed"), 0600); err != nil {
		t.Fatal(err)
	}
	tasks, err := control.TargetStatuses(context.Background(), "peer_00000000001", []string{result.Sites[0].ID})
	if err != nil || len(tasks) != 1 || tasks[0].ID != result.Sites[0].ID || tasks[0].Stage != "transferring_files" || tasks[0].TransferReceivedBytes != int64(len("compressed")) || !tasks[0].TransferExtracting {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	if _, err := control.TargetStatuses(context.Background(), "peer_00000000002", []string{result.Sites[0].ID}); err == nil {
		t.Fatal("cross-peer target status read was accepted")
	}
	if _, err := control.TargetStatuses(context.Background(), "peer_00000000001", []string{result.Sites[0].ID, result.Sites[0].ID}); err == nil {
		t.Fatal("duplicate target status IDs were accepted")
	}
}

func TestSiteMigrationTaskStatusKeepsLocalSourceFailureUntilPrepared(t *testing.T) {
	localUpdated := time.Unix(100, 0).UTC()
	remoteUpdated := time.Unix(200, 0).UTC()
	task := SiteMigrationTaskSummary{Status: "failed_retryable", Stage: "preflight_passed", ErrorCode: "source_prepare_failed", UpdatedAt: localUpdated}
	remote := SiteMigrationRemoteTaskStatus{Status: "draft", Stage: "preflight_passed", UpdatedAt: remoteUpdated}
	mergeSiteMigrationRemoteTargetStatus(&task, remote)
	if task.Status != "failed_retryable" || task.Stage != "preflight_passed" || task.ErrorCode != "source_prepare_failed" || !task.UpdatedAt.Equal(localUpdated) {
		t.Fatalf("local source failure was hidden by target state: %+v", task)
	}

	task.Status, task.Stage, task.ErrorCode = "awaiting_cutover", "transferring_database", ""
	remote.Status, remote.Stage, remote.ErrorCode, remote.TransferReceivedBytes, remote.TransferExtracting = "failed_retryable", "publishing", "database_import_failed", 1234, true
	mergeSiteMigrationRemoteTargetStatus(&task, remote)
	if task.Status != remote.Status || task.Stage != remote.Stage || task.ErrorCode != remote.ErrorCode || task.TransferReceivedBytes != 1234 || !task.TransferExtracting || !task.UpdatedAt.Equal(remoteUpdated) {
		t.Fatalf("prepared source did not expose target state: %+v", task)
	}

	task.Status, task.Stage, task.ErrorCode, task.SourceDecisionPending = "awaiting_cutover", "transferring_database", "", false
	remote.Status, remote.Stage, remote.ErrorCode = "completed", "completed", ""
	mergeSiteMigrationRemoteTargetStatus(&task, remote)
	if task.Status != "completed" || task.Stage != "completed" || !task.SourceDecisionPending {
		t.Fatalf("completed target did not expose source decision: %+v", task)
	}
}

func TestSiteMigrationSourceProbeSignsObservedTargetMarker(t *testing.T) {
	preparation, _, siteID := setupSiteMigrationSourcePreparationTest(t)
	if err := preparation.DeclareBatch(context.Background(), "peer_00000000001", "batch_0000000001", "admin", []SiteMigrationSourceDeclaration{{ID: "migration_0000001", SourceSiteID: siteID, Domain: "one.example.com", Aliases: []string{"www.one.example.com"}, SiteType: "wordpress"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := preparation.Prepare(context.Background(), "peer_00000000001", "migration_0000001", strings.Repeat("m", 48), ""); err != nil {
		t.Fatal(err)
	}
	markerKey := strings.Repeat("a", 64)
	if _, err := preparation.db.Exec(`UPDATE site_migration_peers SET status='paired',inbound_credential_hash=? WHERE id='peer_00000000001'`, markerKey); err != nil {
		t.Fatal(err)
	}
	var snapshotRaw string
	if err := preparation.db.QueryRow(`SELECT settings_snapshot FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&snapshotRaw); err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Token string `json:"source_marker_token"`
	}
	if json.Unmarshal([]byte(snapshotRaw), &snapshot) != nil {
		t.Fatal("invalid fixture snapshot")
	}
	now := time.Unix(1777000000, 0).UTC()
	marker := SiteMigrationMarkerResponse{Task: "migration_0000001", Role: "target", IssuedAt: now.Unix()}
	marker.Signature = signSiteMigrationMarker(markerKey, snapshot.Token, marker)
	payload, _ := json.Marshal(marker)
	control := &SiteMigrationControlService{db: preparation.db, now: func() time.Time { return now }}
	control.client = &http.Client{Transport: migrationRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "one.example.com" || !strings.Contains(req.URL.Path, snapshot.Token) {
			t.Fatalf("unexpected probe URL %s", req.URL.String())
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Cache-Control": []string{"no-store"}}, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	})}
	attestation, err := control.ProbeSource(context.Background(), "peer_00000000001", "migration_0000001")
	if err != nil || !attestation.Success || !verifySiteMigrationProbeAttestation(markerKey, attestation) {
		t.Fatalf("attestation=%+v err=%v", attestation, err)
	}
}
