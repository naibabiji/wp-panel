package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWPPluginUpdateServicePreviewAndConfirmFixedCandidate(t *testing.T) {
	artifacts, store, siteID := newWPPluginUpdateServiceFixture(t)
	now := time.Now().UTC()
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET last_success_at=? WHERE site_id=?`, wpUpdateDBTime(now), siteID); err != nil {
		t.Fatal(err)
	}
	source := writePluginPackageFixture(t, "sample", "sample.php", "1.1.0")
	digest, _, err := hashRegularFile(source)
	if err != nil {
		t.Fatal(err)
	}
	service := &WPPluginUpdateService{
		db: store.db, store: store, artifacts: artifacts, confirmations: newWPPluginConfirmationStore(),
		fetchOffer: func(context.Context, string) (wpPluginOffer, error) {
			return wpPluginOffer{Slug: "sample", Version: "1.1.0", DownloadURL: "https://downloads.wordpress.org/plugin/sample.1.1.0.zip"}, nil
		},
		download: func(context.Context, string, string) (string, string, error) { return source, digest, nil },
		now:      func() time.Time { return now },
	}
	preview, err := service.Preview(context.Background(), siteID, "admin", "sample/sample.php")
	if err != nil || preview.ComponentKey != "sample/sample.php" || preview.TargetVersion != "1.1.0" || preview.ConfirmationToken == "" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if _, err := service.Confirm(context.Background(), siteID, "admin", "sample/sample.php", preview.ConfirmationToken, "1.2.0", "fresh"); !errors.Is(err, ErrWPPluginUpdateConflict) {
		t.Fatalf("changed target error=%v", err)
	}
	preview, err = service.Preview(context.Background(), siteID, "admin", "sample/sample.php")
	if err != nil {
		t.Fatal(err)
	}
	model, err := service.Confirm(context.Background(), siteID, "admin", "sample/sample.php", preview.ConfirmationToken, "1.1.0", "fresh")
	if err != nil || model.Status != wpUpdateQueued || model.ComponentKey != "sample/sample.php" || model.VerificationLevel != "structure_only" {
		t.Fatalf("model=%+v err=%v", model, err)
	}
}

func TestWPPluginUpdateServiceConfirmReleasesSiteLockBeforeDownload(t *testing.T) {
	artifacts, store, siteID := newWPPluginUpdateServiceFixture(t)
	now := time.Now().UTC()
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET last_success_at=? WHERE site_id=?`, wpUpdateDBTime(now), siteID); err != nil {
		t.Fatal(err)
	}
	source := writePluginPackageFixture(t, "sample", "sample.php", "1.1.0")
	digest, _, err := hashRegularFile(source)
	if err != nil {
		t.Fatal(err)
	}
	lockedDuringDownload := true
	service := &WPPluginUpdateService{
		db: store.db, store: store, artifacts: artifacts, confirmations: newWPPluginConfirmationStore(),
		fetchOffer: func(context.Context, string) (wpPluginOffer, error) {
			return wpPluginOffer{Slug: "sample", Version: "1.1.0", DownloadURL: "https://downloads.wordpress.org/plugin/sample.1.1.0.zip"}, nil
		},
		download: func(context.Context, string, string) (string, string, error) {
			// The site lock only needs to protect the instant it takes to write the
			// task row; a slow download (this callback) must run after it's released,
			// otherwise a concurrent restore request would be blocked for no reason
			// for as long as the download takes.
			lockedDuringDownload = SiteOpLocked(siteID)
			return source, digest, nil
		},
		now: func() time.Time { return now },
	}
	preview, err := service.Preview(context.Background(), siteID, "admin", "sample/sample.php")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Confirm(context.Background(), siteID, "admin", "sample/sample.php", preview.ConfirmationToken, "1.1.0", "fresh"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if lockedDuringDownload {
		t.Fatal("site lock was still held while download() ran; it must be released right after the task row is created")
	}
	if SiteOpLocked(siteID) {
		t.Fatal("site lock should not still be held after Confirm() returns")
	}
}

func TestWPPluginUpdateServicePreviewReportsPluginNotInRepository(t *testing.T) {
	artifacts, store, siteID := newWPPluginUpdateServiceFixture(t)
	now := time.Now().UTC()
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET last_success_at=? WHERE site_id=?`, wpUpdateDBTime(now), siteID); err != nil {
		t.Fatal(err)
	}
	service := &WPPluginUpdateService{
		db: store.db, store: store, artifacts: artifacts, confirmations: newWPPluginConfirmationStore(),
		fetchOffer: func(context.Context, string) (wpPluginOffer, error) {
			return wpPluginOffer{}, errWPPluginOfferNotFound
		},
		now: func() time.Time { return now },
	}
	if _, err := service.Preview(context.Background(), siteID, "admin", "sample/sample.php"); !errors.Is(err, ErrWPPluginUpdateNotInRepository) {
		t.Fatalf("preview error = %v, want ErrWPPluginUpdateNotInRepository", err)
	}
}

func TestWPPluginUpdateServiceConfirmBlockedByActiveSiteRestore(t *testing.T) {
	artifacts, store, siteID := newWPPluginUpdateServiceFixture(t)
	now := time.Now().UTC()
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET last_success_at=? WHERE site_id=?`, wpUpdateDBTime(now), siteID); err != nil {
		t.Fatal(err)
	}
	source := writePluginPackageFixture(t, "sample", "sample.php", "1.1.0")
	digest, _, err := hashRegularFile(source)
	if err != nil {
		t.Fatal(err)
	}
	service := &WPPluginUpdateService{
		db: store.db, store: store, artifacts: artifacts, confirmations: newWPPluginConfirmationStore(),
		fetchOffer: func(context.Context, string) (wpPluginOffer, error) {
			return wpPluginOffer{Slug: "sample", Version: "1.1.0", DownloadURL: "https://downloads.wordpress.org/plugin/sample.1.1.0.zip"}, nil
		},
		download: func(context.Context, string, string) (string, string, error) { return source, digest, nil },
		now:      func() time.Time { return now },
	}
	preview, err := service.Preview(context.Background(), siteID, "admin", "sample/sample.php")
	if err != nil {
		t.Fatal(err)
	}

	if !TryAcquireSiteOpLock(siteID, "wp_update_restore") {
		t.Fatal("test setup: expected to acquire site lock")
	}
	t.Cleanup(func() { ReleaseSiteOpLock(siteID) })
	if _, err := service.Confirm(context.Background(), siteID, "admin", "sample/sample.php", preview.ConfirmationToken, "1.1.0", "fresh"); !errors.Is(err, ErrWPPluginUpdateSiteBusy) {
		t.Fatalf("confirm while site locked for restore error=%v", err)
	}

	ReleaseSiteOpLock(siteID)
	preview, err = service.Preview(context.Background(), siteID, "admin", "sample/sample.php")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Confirm(context.Background(), siteID, "admin", "sample/sample.php", preview.ConfirmationToken, "1.1.0", "fresh"); err != nil {
		t.Fatalf("confirm after lock released should succeed: %v", err)
	}
}

func TestWPPluginUpdateServiceTaskModelIsSiteScopedAndSecretFree(t *testing.T) {
	artifacts, store, siteID := newWPPluginUpdateServiceFixture(t)
	task, err := store.createPluginManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, ComponentKey: "sample/sample.php", CurrentVersion: "1.0.0", TargetVersion: "1.1.0",
		PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/plugin/sample.1.1.0.zip",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	source := writePluginPackageFixture(t, "sample", "sample.php", "1.1.0")
	digest, _, _ := hashRegularFile(source)
	task, _, err = artifacts.snapshotValidateAndSealPluginPackage(context.Background(), task.ID, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	service := &WPPluginUpdateService{db: store.db, store: store}
	model, err := service.Task(context.Background(), task.SiteID, task.ID)
	if err != nil || model.ComponentKey != task.ComponentKey {
		t.Fatalf("model=%+v err=%v", model, err)
	}
	body, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"downloads.wordpress.org", task.PackageSnapshotPath, task.DownloadedSHA256, "lease_owner"} {
		if forbidden != "" && strings.Contains(string(body), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
	if _, err := service.Task(context.Background(), task.SiteID+1, task.ID); !errors.Is(err, ErrWPPluginUpdateNotFound) {
		t.Fatalf("cross-site task error=%v", err)
	}
	latest, err := service.LatestTask(context.Background(), task.SiteID, task.ComponentKey)
	if err != nil || latest.ID != task.ID {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	if _, err := service.LatestTask(context.Background(), task.SiteID, "other/other.php"); !errors.Is(err, ErrWPPluginUpdateNotFound) {
		t.Fatalf("cross-component latest error=%v", err)
	}
}

func TestWPPluginUpdateServiceRejectsAmbiguousCandidate(t *testing.T) {
	artifacts, store, siteID := newWPPluginUpdateServiceFixture(t)
	now := time.Now().UTC()
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET last_success_at=? WHERE site_id=?`, wpUpdateDBTime(now), siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO site_wp_component_updates
		(site_id,component_type,component_key,target_version,response,locale,collection_id,collected_at)
		VALUES (?,'plugin','sample/sample.php','1.1.0','upgrade','duplicate','collection-plugin',?)`, siteID, wpUpdateDBTime(now)); err != nil {
		t.Fatal(err)
	}
	service := &WPPluginUpdateService{
		db: store.db, store: store, artifacts: artifacts, confirmations: newWPPluginConfirmationStore(),
		fetchOffer: func(context.Context, string) (wpPluginOffer, error) {
			return wpPluginOffer{Slug: "sample", Version: "1.1.0", DownloadURL: "https://downloads.wordpress.org/plugin/sample.1.1.0.zip"}, nil
		},
		now: func() time.Time { return now },
	}
	if _, err := service.Preview(context.Background(), siteID, "admin", "sample/sample.php"); !errors.Is(err, ErrWPPluginUpdateConflict) {
		t.Fatalf("ambiguous candidate error=%v", err)
	}
}

func newWPPluginUpdateServiceFixture(t *testing.T) (*wpUpdateArtifactService, *wpUpdateStore, int) {
	t.Helper()
	store, siteID := newWPUpdateStoreTest(t)
	seedPluginUpdateCandidate(t, store, siteID, "sample/sample.php", "1.0.0", "1.1.0", "collection-plugin")
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET last_success_at=? WHERE site_id=?`, wpUpdateDBTime(time.Now().UTC()), siteID); err != nil {
		t.Fatal(err)
	}
	artifacts, err := newWPUpdateArtifactService(store, filepath.Join(t.TempDir(), "artifacts"), fakeUpdateDump)
	if err != nil {
		t.Fatal(err)
	}
	return artifacts, store, siteID
}

func TestDefaultWPPluginPackageDownloaderRejectsRedirectIdentityDrift(t *testing.T) {
	root := t.TempDir()
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		final, _ := url.Parse("https://downloads.wordpress.org/plugin/other.1.1.0.zip")
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("zip")), Request: &http.Request{URL: final}}, nil
	})}
	download := defaultWPPluginPackageDownloader(client, root)
	if path, _, err := download(context.Background(), "https://downloads.wordpress.org/plugin/sample.1.1.0.zip", "1.1.0"); err == nil {
		_ = os.Remove(path)
		t.Fatal("redirect identity drift was accepted")
	}
}
