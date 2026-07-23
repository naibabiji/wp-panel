package executor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

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

func TestWPCoreUpdateServiceRejectsNonNewerInventoryCandidate(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET wordpress_locale='en_US',collection_id='collection-a',last_success_at=? WHERE site_id=?`, wpUpdateDBTime(now), siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO site_wp_component_updates(site_id,component_type,component_key,target_version,response,locale,collection_id,collected_at)
		VALUES (?,'core','wordpress','7.0.1','upgrade','en_US','collection-a',?)`, siteID, wpUpdateDBTime(now)); err != nil {
		t.Fatal(err)
	}
	service := &WPCoreUpdateService{db: store.db, store: store, now: func() time.Time { return now }}
	if _, err := service.loadCandidate(context.Background(), siteID); !errors.Is(err, ErrWPCoreUpdateConflict) {
		t.Fatalf("same-version candidate error=%v", err)
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
