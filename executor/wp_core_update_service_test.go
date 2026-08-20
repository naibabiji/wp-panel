package executor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

// newWPCoreUpdateServiceForDownloadAndSealTest builds a service with a real
// store/artifacts backing (needed by downloadAndSeal to create task dirs and
// seal plans) but no network-touching dependencies wired in — these tests
// only exercise the package-acquisition wiring (AcquireCorePackage via
// config.AppConfig), not the official-checksums network round trip that
// downloadAndSeal performs afterward on a successful acquisition; that step
// has no injection point today and is out of scope for this change.
func newWPCoreUpdateServiceForDownloadAndSealTest(t *testing.T) (*WPCoreUpdateService, int) {
	t.Helper()
	store, siteID := newWPUpdateStoreTest(t)
	artifacts, err := newWPUpdateArtifactService(store, t.TempDir(), func(context.Context, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return &WPCoreUpdateService{db: store.db, store: store, artifacts: artifacts, confirmations: newWPCoreConfirmationStore(), now: time.Now}, siteID
}

func createUnsealedCoreUpdateTask(t *testing.T, service *WPCoreUpdateService, siteID int) WPUpdateTask {
	t.Helper()
	task, err := service.store.createCoreManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, CurrentVersion: "7.0.1", TargetVersion: "7.0.2", PackageSource: "wordpress.org",
		DownloadURL: "https://downloads.wordpress.org/release/wordpress-7.0.2.zip",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestWPCoreUpdateServiceDownloadAndSealFailsCleanlyWhenConfigMissing(t *testing.T) {
	service, siteID := newWPCoreUpdateServiceForDownloadAndSealTest(t)
	task := createUnsealedCoreUpdateTask(t, service, siteID)

	oldCfg := config.AppConfig
	config.AppConfig = nil
	t.Cleanup(func() { config.AppConfig = oldCfg })

	record := wpCoreConfirmation{targetVersion: "7.0.2", locale: "en_US"}
	if _, err := service.downloadAndSeal(context.Background(), task, record); err == nil {
		t.Fatal("expected an error when config.AppConfig is not loaded")
	}
}

func TestWPCoreUpdateServiceDownloadAndSealFailsCleanlyWhenCachePathUnconfigured(t *testing.T) {
	service, siteID := newWPCoreUpdateServiceForDownloadAndSealTest(t)
	task := createUnsealedCoreUpdateTask(t, service, siteID)

	oldCfg := config.AppConfig
	config.AppConfig = &config.Config{} // Paths.WordPressPackage left empty
	t.Cleanup(func() { config.AppConfig = oldCfg })

	record := wpCoreConfirmation{targetVersion: "7.0.2", locale: "en_US"}
	if _, err := service.downloadAndSeal(context.Background(), task, record); err == nil {
		t.Fatal("expected an error when the shared package cache path is unconfigured")
	}
}

func TestWPCoreUpdateServicePreviewFixesServerCandidate(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	_, err := store.db.Exec(`UPDATE site_wp_inventory_state SET wordpress_locale='zh_CN',collection_id='collection-a',last_success_at=? WHERE site_id=?`, wpUpdateDBTime(now), siteID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.db.Exec(`INSERT INTO site_wp_component_updates(site_id,component_type,component_key,target_version,response,locale,collection_id,collected_at)
		VALUES (?,'core','wordpress','7.0.2','upgrade','zh_CN','collection-a',?)`, siteID, wpUpdateDBTime(now))
	if err != nil {
		t.Fatal(err)
	}
	// WordPress can return upgrade offers for multiple locales in one core
	// transient. Preview must select the site's installed locale rather than
	// treating the unrelated locale as a conflicting second candidate.
	if _, err := store.db.Exec(`INSERT INTO site_wp_component_updates(site_id,component_type,component_key,target_version,response,locale,collection_id,collected_at)
		VALUES (?,'core','wordpress','7.0.2','upgrade','en_US','collection-a',?)`, siteID, wpUpdateDBTime(now)); err != nil {
		t.Fatal(err)
	}
	service := &WPCoreUpdateService{db: store.db, store: store, confirmations: newWPCoreConfirmationStore(), now: func() time.Time { return now },
		versions: func(context.Context) (string, string, error) { return "8.3.0", "11.8.0", nil },
		fetchOffer: func(context.Context, string, string, string, string) (wpCoreVersionOffer, error) {
			return wpCoreVersionOffer{Version: "7.0.2", Locale: "zh_CN", DownloadURL: "https://downloads.wordpress.org/release/wordpress-7.0.2.zip", PHPMin: "7.2.24", MySQLMin: "5.5"}, nil
		}}
	preview, err := service.Preview(context.Background(), siteID, "admin")
	if err != nil || !preview.Available || preview.CurrentVersion != "7.0.1" || preview.TargetVersion != "7.0.2" || preview.ConfirmationToken == "" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if _, err := service.confirmations.consume(preview.ConfirmationToken, "admin", siteID, "7.0.3"); err == nil {
		t.Fatal("client changed target version")
	}
}

func TestWPCoreUpdateServicePreviewReportsUpToDateWithoutError(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET wordpress_locale='en_US',collection_id='collection-a',last_success_at=? WHERE site_id=?`, wpUpdateDBTime(now), siteID); err != nil {
		t.Fatal(err)
	}
	// No row in site_wp_component_updates for core: the scanner found no
	// newer WordPress release, which is the normal steady state.
	service := &WPCoreUpdateService{db: store.db, store: store, now: func() time.Time { return now }}
	preview, err := service.Preview(context.Background(), siteID, "admin")
	if err != nil {
		t.Fatalf("expected a clean up-to-date response, got err=%v", err)
	}
	if preview.Available {
		t.Fatalf("expected available=false, got %+v", preview)
	}
	if preview.SiteID != siteID || preview.CurrentVersion != "7.0.1" {
		t.Fatalf("expected site/version echoed back, got %+v", preview)
	}
	if preview.ConfirmationToken != "" || preview.TargetVersion != "" {
		t.Fatalf("up-to-date response should not carry a confirmation token or target version: %+v", preview)
	}
}

func TestWPCoreUpdateServicePreviewReconcilesOutOfBandCoreUpdate(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	stamp := wpUpdateDBTime(now.Add(-time.Hour))
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET wordpress_locale='en_US',collection_id='collection-a',last_success_at=? WHERE site_id=?`, stamp, siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO site_wp_component_updates(site_id,component_type,component_key,target_version,response,locale,collection_id,collected_at)
		VALUES (?,'core','wordpress','7.0.2','upgrade','en_US','collection-a',?)`, siteID, stamp); err != nil {
		t.Fatal(err)
	}
	service := &WPCoreUpdateService{db: store.db, store: store, now: func() time.Time { return now },
		identity: func(string) (wpInstalledIdentity, error) {
			return wpInstalledIdentity{Version: "7.0.2", Locale: "en_US"}, nil
		}}
	preview, err := service.Preview(context.Background(), siteID, "admin")
	if err != nil || preview.Available || preview.CurrentVersion != "7.0.2" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	var version, lastSuccess string
	if err := store.db.QueryRow(`SELECT wordpress_version,last_success_at FROM site_wp_inventory_state WHERE site_id=?`, siteID).Scan(&version, &lastSuccess); err != nil {
		t.Fatal(err)
	}
	parsedLastSuccess, err := parseRequiredWPInventoryTime(lastSuccess)
	if err != nil {
		t.Fatal(err)
	}
	expectedLastSuccess, err := parseRequiredWPInventoryTime(stamp)
	if err != nil {
		t.Fatal(err)
	}
	if version != "7.0.2" || !parsedLastSuccess.Equal(expectedLastSuccess) {
		t.Fatalf("version=%q last_success=%q want version reconciled without claiming a full scan", version, lastSuccess)
	}
	var candidates, tasks int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM site_wp_component_updates WHERE site_id=? AND component_type='core'`, siteID).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_tasks WHERE site_id=?`, siteID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if candidates != 0 || tasks != 0 {
		t.Fatalf("candidates=%d tasks=%d", candidates, tasks)
	}
}

func TestWPCoreUpdateServicePreviewUsesLowerLiveVersionAndKeepsHigherCandidate(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	stamp := wpUpdateDBTime(now)
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET wordpress_version='7.0.2',wordpress_locale='en_US',collection_id='collection-a',last_success_at=? WHERE site_id=?`, stamp, siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO site_wp_component_updates(site_id,component_type,component_key,target_version,response,locale,collection_id,collected_at)
		VALUES (?,'core','wordpress','7.0.3','upgrade','en_US','collection-a',?)`, siteID, stamp); err != nil {
		t.Fatal(err)
	}
	service := &WPCoreUpdateService{db: store.db, store: store, confirmations: newWPCoreConfirmationStore(), now: func() time.Time { return now },
		identity: func(string) (wpInstalledIdentity, error) {
			return wpInstalledIdentity{Version: "7.0.1", Locale: "en_US"}, nil
		},
		versions: func(context.Context) (string, string, error) { return "8.3.0", "11.8.0", nil },
		fetchOffer: func(_ context.Context, current, locale, _, _ string) (wpCoreVersionOffer, error) {
			if current != "7.0.1" || locale != "en_US" {
				t.Fatalf("offer request current=%q locale=%q", current, locale)
			}
			return wpCoreVersionOffer{Version: "7.0.3", Locale: "en_US", DownloadURL: "https://downloads.wordpress.org/release/wordpress-7.0.3.zip", PHPMin: "7.4", MySQLMin: "5.5"}, nil
		}}
	preview, err := service.Preview(context.Background(), siteID, "admin")
	if err != nil || !preview.Available || preview.CurrentVersion != "7.0.1" || preview.TargetVersion != "7.0.3" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	var version string
	var candidates int
	if err := store.db.QueryRow(`SELECT wordpress_version FROM site_wp_inventory_state WHERE site_id=?`, siteID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM site_wp_component_updates WHERE site_id=? AND target_version='7.0.3'`, siteID).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if version != "7.0.1" || candidates != 1 {
		t.Fatalf("version=%q candidates=%d", version, candidates)
	}
}

func TestWPCoreUpdateServicePreviewRejectsMissingStore(t *testing.T) {
	service := &WPCoreUpdateService{}
	if _, err := service.Preview(context.Background(), 1, "admin"); !errors.Is(err, ErrWPCoreUpdateInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestWPCoreUpdateServiceRejectsDuplicateSameLocaleCandidatesBeforeVersionComparison(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	stamp := wpUpdateDBTime(now)
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET wordpress_locale='en_US',collection_id='collection-a',last_success_at=? WHERE site_id=?`, stamp, siteID); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"7.0.2", "7.0.1"} {
		if _, err := store.db.Exec(`INSERT INTO site_wp_component_updates(site_id,component_type,component_key,target_version,response,locale,collection_id,collected_at)
			VALUES (?,'core','wordpress',?,'upgrade','en_US','collection-a',?)`, siteID, target, stamp); err != nil {
			t.Fatal(err)
		}
	}
	service := &WPCoreUpdateService{db: store.db, store: store, now: func() time.Time { return now }}
	if _, err := service.loadCandidate(context.Background(), siteID); !errors.Is(err, ErrWPCoreUpdateConflict) {
		t.Fatalf("duplicate same-locale candidates err=%v", err)
	}
}

func TestWPCoreUpdateServiceReportsUpToDateForNonNewerCandidate(t *testing.T) {
	for _, target := range []string{"7.0.1", "6.9.9"} {
		t.Run(target, func(t *testing.T) {
			store, siteID := newWPUpdateStoreTest(t)
			now := time.Now().UTC()
			if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET wordpress_locale='en_US',collection_id='collection-a',last_success_at=? WHERE site_id=?`, wpUpdateDBTime(now), siteID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`INSERT INTO site_wp_component_updates(site_id,component_type,component_key,target_version,response,locale,collection_id,collected_at)
				VALUES (?,'core','wordpress',?,'upgrade','en_US','collection-a',?)`, siteID, target, wpUpdateDBTime(now)); err != nil {
				t.Fatal(err)
			}
			service := &WPCoreUpdateService{db: store.db, store: store, now: func() time.Time { return now }}
			candidate, err := service.loadCandidate(context.Background(), siteID)
			if !errors.Is(err, errWPCoreUpdateNoCandidate) {
				t.Fatalf("non-newer candidate %s error=%v, want errWPCoreUpdateNoCandidate", target, err)
			}
			if candidate.currentVersion != "7.0.1" {
				t.Fatalf("current version = %q, want 7.0.1", candidate.currentVersion)
			}
		})
	}
}

func TestWPCoreUpdateTaskModelDoesNotExposeSecrets(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task := createAndSealUpdateTask(t, store, siteID, time.Now().UTC())
	service := &WPCoreUpdateService{db: store.db, store: store}
	model, err := service.Task(context.Background(), siteID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"downloads.wordpress.org", "snapshot.zip", strings.Repeat("a", 64), "lease_owner", "package_snapshot_path"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
}

func TestWPCoreUpdateLatestTaskUsesPublicSiteScopedModel(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task := createAndSealUpdateTask(t, store, siteID, time.Now().UTC())
	service := &WPCoreUpdateService{db: store.db, store: store}
	latest, err := service.LatestTask(context.Background(), siteID)
	if err != nil || latest.ID != task.ID || latest.SiteID != siteID {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	if _, err := service.LatestTask(context.Background(), siteID+1); !errors.Is(err, ErrWPCoreUpdateNotFound) {
		t.Fatalf("cross-site latest error=%v", err)
	}
}

func TestCompareWPVersions(t *testing.T) {
	if compareWPVersions("8.3.1", "8.3") < 0 || compareWPVersions("10.11.6-MariaDB", "5.5") < 0 || compareWPVersions("7.2.23", "7.2.24") >= 0 {
		t.Fatal("version comparison failed")
	}
}

func TestValidWPCoreUpdatePackageURL(t *testing.T) {
	for _, raw := range []string{
		"https://downloads.wordpress.org/release/wordpress-7.0.2.zip",
		"https://wordpress.org/wordpress-7.0.2.zip",
	} {
		if !validWPCoreUpdatePackageURL(raw) {
			t.Fatalf("valid package URL rejected: %s", raw)
		}
	}
	for _, raw := range []string{
		"http://downloads.wordpress.org/release/wordpress.zip",
		"https://api.wordpress.org/core/version-check/1.7/",
		"https://downloads.wordpress.org.evil.example/wordpress.zip",
		"https://downloads.wordpress.org/wordpress.zip?mirror=1",
		"https://user@downloads.wordpress.org/wordpress.zip",
	} {
		if validWPCoreUpdatePackageURL(raw) {
			t.Fatalf("unsafe package URL accepted: %s", raw)
		}
	}
}
