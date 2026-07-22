package executor

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWPUpdateArtifactServiceValidatesPluginSnapshotBeforeSealing(t *testing.T) {
	service, store, task := preparePluginSnapshotPlan(t)
	source := writePluginPackageFixture(t, "sample", "sample.php", "1.1.0")
	digest, _, err := hashRegularFile(source)
	if err != nil {
		t.Fatal(err)
	}
	sealed, report, err := service.snapshotValidateAndSealPluginPackage(context.Background(), task.ID, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Status != wpUpdateQueued || sealed.VerificationLevel != "structure_only" || report.MainFile != "sample/sample.php" || report.Version != "1.1.0" {
		t.Fatalf("sealed=%+v report=%+v", sealed, report)
	}
	current, err := store.getTask(context.Background(), task.ID)
	if err != nil || current.PackageSnapshotPath == "" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func TestWPUpdateArtifactServiceRejectsPluginIdentityBeforeSealing(t *testing.T) {
	service, store, task := preparePluginSnapshotPlan(t)
	source := writePluginPackageFixture(t, "other", "sample.php", "1.1.0")
	digest, _, err := hashRegularFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.snapshotValidateAndSealPluginPackage(context.Background(), task.ID, source, digest); err == nil {
		t.Fatal("plugin identity drift was accepted")
	}
	current, err := store.getTask(context.Background(), task.ID)
	if err != nil || current.Status != wpUpdatePreparing || current.PlanSealedAt != "" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	if entries, _ := os.ReadDir(service.root); len(entries) != 0 {
		t.Fatalf("failed snapshot left %d task directories", len(entries))
	}
}

func TestWPUpdateArtifactServiceCannotSealPluginThroughUncheckedEntryPoint(t *testing.T) {
	service, store, task := preparePluginSnapshotPlan(t)
	source := filepath.Join(t.TempDir(), "not-a-zip")
	data := []byte("not a validated plugin package")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	if _, err := service.snapshotAndSealPackage(context.Background(), task.ID, source, digest, "structure_only"); err == nil {
		t.Fatal("plugin task was sealed through unchecked snapshot entry point")
	}
	current, err := store.getTask(context.Background(), task.ID)
	if err != nil || current.Status != wpUpdatePreparing || current.PlanSealedAt != "" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	if entries, _ := os.ReadDir(service.root); len(entries) != 0 {
		t.Fatalf("unchecked plugin snapshot left %d task directories", len(entries))
	}
}

func TestWPUpdateArtifactServiceRejectsSnapshotChangedByValidator(t *testing.T) {
	service, store, task := preparePluginSnapshotPlan(t)
	source := filepath.Join(t.TempDir(), "source.zip")
	data := []byte("original")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	_, err := service.snapshotAndSealPackageChecked(context.Background(), task.ID, source, digest, "structure_only", func(snapshot string) error {
		return os.WriteFile(snapshot, []byte("modified"), 0600)
	})
	if err == nil {
		t.Fatal("snapshot changed during validation was sealed")
	}
	current, lookupErr := store.getTask(context.Background(), task.ID)
	if lookupErr != nil || current.Status != wpUpdatePreparing || current.PlanSealedAt != "" {
		t.Fatalf("current=%+v err=%v", current, lookupErr)
	}
}

func TestWPUpdateArtifactServiceValidatesAndClaimsPluginSnapshot(t *testing.T) {
	service, _, task := preparePluginSnapshotPlan(t)
	source := writePluginPackageFixture(t, "sample", "sample.php", "1.1.0")
	digest, _, _ := hashRegularFile(source)
	sealed, _, err := service.snapshotValidateAndSealPluginPackage(context.Background(), task.ID, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := service.validateAndClaimPluginUpdate(context.Background(), sealed.ID, "plugin-worker", "1.0.0")
	if err != nil || claimed.Status != wpUpdateRunning || claimed.LeaseOwner != "plugin-worker" {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
}

func TestWPUpdateArtifactServiceFailsClosedWhenPluginSnapshotChanges(t *testing.T) {
	service, store, task := preparePluginSnapshotPlan(t)
	source := writePluginPackageFixture(t, "sample", "sample.php", "1.1.0")
	digest, _, _ := hashRegularFile(source)
	sealed, _, err := service.snapshotValidateAndSealPluginPackage(context.Background(), task.ID, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sealed.PackageSnapshotPath, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.validateAndClaimPluginUpdate(context.Background(), sealed.ID, "plugin-worker", "1.0.0"); err == nil {
		t.Fatal("changed plugin snapshot was claimed")
	}
	failed, err := store.getTask(context.Background(), sealed.ID)
	if err != nil || failed.Status != wpUpdateFailed || failed.FailureStage != "precheck" {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
}

func TestWPUpdateArtifactServiceFailsPluginClaimOnVersionDrift(t *testing.T) {
	service, store, task := preparePluginSnapshotPlan(t)
	source := writePluginPackageFixture(t, "sample", "sample.php", "1.1.0")
	digest, _, _ := hashRegularFile(source)
	sealed, _, err := service.snapshotValidateAndSealPluginPackage(context.Background(), task.ID, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.validateAndClaimPluginUpdate(context.Background(), sealed.ID, "plugin-worker", "0.9.0"); err == nil {
		t.Fatal("plugin version drift was claimed")
	}
	failed, err := store.getTask(context.Background(), sealed.ID)
	if err != nil || failed.Status != wpUpdateFailed || failed.FailureStage != "precheck" {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
}

func TestWPUpdateArtifactServiceOnlyOneConcurrentPluginClaimSucceeds(t *testing.T) {
	service, _, task := preparePluginSnapshotPlan(t)
	source := writePluginPackageFixture(t, "sample", "sample.php", "1.1.0")
	digest, _, _ := hashRegularFile(source)
	sealed, _, err := service.snapshotValidateAndSealPluginPackage(context.Background(), task.ID, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			_, err := service.validateAndClaimPluginUpdate(context.Background(), sealed.ID, fmt.Sprintf("plugin-worker-%d", worker), "1.0.0")
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful plugin claims=%d, want 1", succeeded)
	}
}

func TestWPUpdateArtifactServiceSnapshotsPackageAndSealsPlan(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	task, err := store.createCoreManualPlan(context.Background(), WPUpdatePlan{SiteID: siteID, CurrentVersion: "7.0.1", TargetVersion: "7.0.2", PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/release.zip"}, now)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "wordpress.zip")
	data := []byte("validated wordpress package")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	artifactRoot := filepath.Join(t.TempDir(), "updates")
	service, err := newWPUpdateArtifactService(store, artifactRoot, fakeUpdateDump)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := service.snapshotAndSealPackage(context.Background(), task.ID, source, digest, "official_verified")
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Status != wpUpdateQueued || sealed.DownloadedSHA256 != digest || !filepath.IsAbs(sealed.PackageSnapshotPath) {
		t.Fatalf("sealed=%+v", sealed)
	}
	got, err := os.ReadFile(sealed.PackageSnapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatal("snapshot bytes changed")
	}
	info, _ := os.Stat(sealed.PackageSnapshotPath)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("snapshot mode=%o", info.Mode().Perm())
	}
}

func TestWPUpdateArtifactServiceRejectsSnapshotDigestMismatch(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task, err := store.createCoreManualPlan(context.Background(), WPUpdatePlan{SiteID: siteID, CurrentVersion: "7.0.1", TargetVersion: "7.0.2", PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/release.zip"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "wordpress.zip")
	if err := os.WriteFile(source, []byte("wrong"), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "updates")
	service, err := newWPUpdateArtifactService(store, root, fakeUpdateDump)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.snapshotAndSealPackage(context.Background(), task.ID, source, strings.Repeat("a", 64), "official_verified"); err == nil {
		t.Fatal("expected digest mismatch")
	}
	current, _ := store.getTask(context.Background(), task.ID)
	if current.Status != wpUpdatePreparing || current.PlanSealedAt != "" {
		t.Fatalf("task=%+v", current)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("artifact root contains %d entries", len(entries))
	}
}

func TestWPUpdateArtifactServiceCreatesDedicatedCoreBackups(t *testing.T) {
	service, store, task, webRoot := prepareClaimedArtifactTask(t, fakeUpdateDump)
	writeWordPressCoreFixture(t, webRoot)
	if err := service.prepareCoreBackups(context.Background(), task.ID, "worker-a"); err != nil {
		t.Fatal(err)
	}
	updated, err := store.getTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.BackupReady || updated.Stage != "backups_ready" {
		t.Fatalf("task=%+v", updated)
	}
	rows, err := store.db.Query(`SELECT kind,file_path,file_size,sha256,protected FROM wp_update_task_backups WHERE task_id=? ORDER BY id`, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var records []wpUpdateBackupRecord
	for rows.Next() {
		var r wpUpdateBackupRecord
		var protected int
		if err := rows.Scan(&r.Kind, &r.FilePath, &r.FileSize, &r.SHA256, &protected); err != nil {
			t.Fatal(err)
		}
		if protected != 1 {
			t.Fatal("backup is not protected")
		}
		records = append(records, r)
	}
	if len(records) != 2 || records[0].Kind != "database" || records[1].Kind != "core_files" {
		t.Fatalf("records=%+v", records)
	}
	names := readTarNames(t, records[1].FilePath)
	for _, required := range []string{"wp-admin", "wp-admin/admin.php", "wp-includes", "wp-includes/version.php", "index.php", "wp-settings.php"} {
		if !containsUpdateArtifactName(names, required) {
			t.Errorf("archive missing %s: %v", required, names)
		}
	}
	for _, forbidden := range []string{"wp-content", "wp-content/plugin.php", "wp-config.php", "customer.php"} {
		if containsUpdateArtifactName(names, forbidden) {
			t.Errorf("archive contains %s", forbidden)
		}
	}
}

func TestWPUpdateArtifactServiceCleansPartialBackupOnFailure(t *testing.T) {
	service, store, task, webRoot := prepareClaimedArtifactTask(t, fakeUpdateDump)
	writeWordPressCoreFixture(t, webRoot)
	if err := os.Remove(filepath.Join(webRoot, "wp-settings.php")); err != nil {
		t.Fatal(err)
	}
	if err := service.prepareCoreBackups(context.Background(), task.ID, "worker-a"); err == nil {
		t.Fatal("expected core backup failure")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_backups WHERE task_id=?`, task.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("backup rows=%d", count)
	}
	current, _ := store.getTask(context.Background(), task.ID)
	if current.BackupReady {
		t.Fatal("backup_ready was set")
	}
	taskDir := filepath.Join(service.root, task.ID)
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "package.zip" {
		t.Fatalf("partial artifacts remain: %v", entries)
	}
}

func TestWPUpdateArtifactServiceLeavesNoBackupWhenDumpFails(t *testing.T) {
	service, store, task, webRoot := prepareClaimedArtifactTask(t, func(context.Context, string, string) error {
		return errors.New("injected dump failure")
	})
	writeWordPressCoreFixture(t, webRoot)
	if err := service.prepareCoreBackups(context.Background(), task.ID, "worker-a"); err == nil {
		t.Fatal("expected database backup failure")
	}
	var count int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_backups WHERE task_id=?`, task.ID).Scan(&count)
	if count != 0 {
		t.Fatalf("backup rows=%d", count)
	}
	entries, err := os.ReadDir(filepath.Join(service.root, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "package.zip" {
		t.Fatalf("partial artifacts remain: %v", entries)
	}
}

func TestWPUpdateArtifactServiceCleansFilesWhenOwnershipIsLost(t *testing.T) {
	var hookStore *wpUpdateStore
	var hookTaskID string
	service, store, task, webRoot := prepareClaimedArtifactTask(t, func(ctx context.Context, dbName, target string) error {
		if err := fakeUpdateDump(ctx, dbName, target); err != nil {
			return err
		}
		_, err := hookStore.db.Exec(`UPDATE wp_update_tasks SET lease_owner='worker-b' WHERE id=?`, hookTaskID)
		return err
	})
	hookStore, hookTaskID = store, task.ID
	writeWordPressCoreFixture(t, webRoot)
	if err := service.prepareCoreBackups(context.Background(), task.ID, "worker-a"); err == nil {
		t.Fatal("expected ownership loss")
	}
	var count int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_backups WHERE task_id=?`, task.ID).Scan(&count)
	if count != 0 {
		t.Fatalf("backup rows=%d", count)
	}
	entries, err := os.ReadDir(filepath.Join(service.root, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "package.zip" {
		t.Fatalf("partial artifacts remain: %v", entries)
	}
}

func TestWPUpdateArtifactServiceRejectsCoreSymlink(t *testing.T) {
	service, store, task, webRoot := prepareClaimedArtifactTask(t, fakeUpdateDump)
	writeWordPressCoreFixture(t, webRoot)
	outside := filepath.Join(t.TempDir(), "secret.php")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(webRoot, "wp-admin", "escape.php")); err != nil {
		t.Fatal(err)
	}
	if err := service.prepareCoreBackups(context.Background(), task.ID, "worker-a"); err == nil {
		t.Fatal("expected symlink rejection")
	}
	var count int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_backups WHERE task_id=?`, task.ID).Scan(&count)
	if count != 0 {
		t.Fatalf("backup rows=%d", count)
	}
}

func TestWPUpdateArtifactServicePreparesPluginDatabaseAndDirectoryBackups(t *testing.T) {
	service, store, task, webRoot := prepareRunningPluginArtifactTask(t, fakeUpdateDump)
	pluginRoot := filepath.Join(webRoot, "wp-content", "plugins", "sample")
	if err := os.MkdirAll(filepath.Join(pluginRoot, "assets"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"sample.php": "<?php /* Plugin Name: Sample */", "assets/app.js": "ok"} {
		if err := os.WriteFile(filepath.Join(pluginRoot, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.preparePluginBackups(context.Background(), task.ID, "worker-plugin"); err != nil {
		t.Fatal(err)
	}
	current, err := store.getTask(context.Background(), task.ID)
	if err != nil || !current.BackupReady || current.Stage != "backups_ready" {
		t.Fatalf("task=%+v err=%v", current, err)
	}
	var kinds string
	if err := store.db.QueryRow(`SELECT group_concat(kind, ',') FROM
		(SELECT kind FROM wp_update_task_backups WHERE task_id=? ORDER BY kind)`, task.ID).Scan(&kinds); err != nil {
		t.Fatal(err)
	}
	if kinds != "database,plugin_files" {
		t.Fatalf("backup kinds=%q", kinds)
	}
	names := readTarNames(t, filepath.Join(service.root, task.ID, "plugin-files.tar.gz"))
	if !containsUpdateArtifactName(names, "sample/sample.php") || !containsUpdateArtifactName(names, "sample/assets/app.js") {
		t.Fatalf("plugin archive names=%v", names)
	}
}

func TestWPUpdateArtifactServiceRejectsPluginSymlinkWithoutCommittingBackups(t *testing.T) {
	service, store, task, webRoot := prepareRunningPluginArtifactTask(t, fakeUpdateDump)
	pluginRoot := filepath.Join(webRoot, "wp-content", "plugins", "sample")
	if err := os.MkdirAll(pluginRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "sample.php"), []byte("plugin"), 0644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(pluginRoot, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := service.preparePluginBackups(context.Background(), task.ID, "worker-plugin"); err == nil {
		t.Fatal("expected plugin symlink rejection")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_backups WHERE task_id=?`, task.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("backup rows=%d err=%v", count, err)
	}
	entries, err := os.ReadDir(filepath.Join(service.root, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "package.zip" {
		t.Fatalf("partial plugin artifacts remain: %v", entries)
	}
}

func prepareClaimedArtifactTask(t *testing.T, dumper wpUpdateDatabaseDumper) (*wpUpdateArtifactService, *wpUpdateStore, WPUpdateTask, string) {
	t.Helper()
	store, siteID := newWPUpdateStoreTest(t)
	webRoot := filepath.Join(t.TempDir(), "wordpress")
	if err := os.Mkdir(webRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE websites SET web_root=?,db_name='wordpress_db' WHERE id=?`, webRoot, siteID); err != nil {
		t.Fatal(err)
	}
	task, err := store.createCoreManualPlan(context.Background(), WPUpdatePlan{SiteID: siteID, CurrentVersion: "7.0.1", TargetVersion: "7.0.2", PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/release.zip"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.zip")
	data := []byte("package")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	service, err := newWPUpdateArtifactService(store, filepath.Join(t.TempDir(), "artifacts"), dumper)
	if err != nil {
		t.Fatal(err)
	}
	task, err = service.snapshotAndSealPackage(context.Background(), task.ID, source, digest, "official_verified")
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.claimCoreUpdate(context.Background(), task.ID, "worker-a", "7.0.1", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return service, store, task, webRoot
}

func prepareRunningPluginArtifactTask(t *testing.T, dumper wpUpdateDatabaseDumper) (*wpUpdateArtifactService, *wpUpdateStore, WPUpdateTask, string) {
	t.Helper()
	store, siteID := newWPUpdateStoreTest(t)
	seedPluginUpdateCandidate(t, store, siteID, "sample/sample.php", "1.0.0", "1.1.0", "collection-plugin")
	webRoot := filepath.Join(t.TempDir(), "wordpress")
	if err := os.MkdirAll(filepath.Join(webRoot, "wp-content", "plugins"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE websites SET web_root=?,db_name='wordpress_db' WHERE id=?`, webRoot, siteID); err != nil {
		t.Fatal(err)
	}
	task, err := store.createPluginManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, ComponentKey: "sample/sample.php", CurrentVersion: "1.0.0", TargetVersion: "1.1.0",
		PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/plugin/sample.1.1.0.zip",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	source := writePluginPackageFixture(t, "sample", "sample.php", "1.1.0")
	service, err := newWPUpdateArtifactService(store, filepath.Join(t.TempDir(), "artifacts"), dumper)
	if err != nil {
		t.Fatal(err)
	}
	digest, _, err := hashRegularFile(source)
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = service.snapshotValidateAndSealPluginPackage(context.Background(), task.ID, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	stamp := wpUpdateDBTime(time.Now().UTC())
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='running',stage='claimed',lease_owner='worker-plugin',
		lease_expires_at=?,started_at=?,updated_at=? WHERE id=? AND status='queued'`, stamp, stamp, stamp, task.ID); err != nil {
		t.Fatal(err)
	}
	task, err = store.getTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, task, webRoot
}

func preparePluginSnapshotPlan(t *testing.T) (*wpUpdateArtifactService, *wpUpdateStore, WPUpdateTask) {
	t.Helper()
	store, siteID := newWPUpdateStoreTest(t)
	seedPluginUpdateCandidate(t, store, siteID, "sample/sample.php", "1.0.0", "1.1.0", "collection-plugin")
	task, err := store.createPluginManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, ComponentKey: "sample/sample.php", CurrentVersion: "1.0.0", TargetVersion: "1.1.0",
		PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/plugin/sample.1.1.0.zip",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	service, err := newWPUpdateArtifactService(store, filepath.Join(t.TempDir(), "artifacts"), fakeUpdateDump)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, task
}

func writePluginPackageFixture(t *testing.T, slug, mainFile, version string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "plugin.zip")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create(slug + "/" + mainFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(w, "<?php\n/*\nPlugin Name: Sample\nVersion: %s\n*/\n", version); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

func fakeUpdateDump(_ context.Context, dbName, target string) error {
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	_, writeErr := gz.Write([]byte("dump:" + dbName))
	closeGzipErr := gz.Close()
	closeFileErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeGzipErr != nil {
		return closeGzipErr
	}
	return closeFileErr
}

func writeWordPressCoreFixture(t *testing.T, root string) {
	t.Helper()
	for _, dir := range []string{"wp-admin", "wp-includes", "wp-content"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{"wp-admin/admin.php": "admin", "wp-includes/version.php": "version", "wp-content/plugin.php": "user", "wp-config.php": "secret", "customer.php": "user"}
	for _, name := range wpCoreRootFiles {
		files[name] = "core"
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func readTarNames(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	sort.Strings(names)
	return names
}
func containsUpdateArtifactName(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
