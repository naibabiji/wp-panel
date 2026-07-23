package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWPThemeUpdateServicePreviewAndConfirmCurrentTheme(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	seedThemeUpdateCandidate(t, store, siteID, "sample-theme", "1.0.0", "1.1.0", "collection-theme")
	webRoot := filepath.Join(t.TempDir(), "wordpress")
	themeRoot := filepath.Join(webRoot, "wp-content", "themes", "sample-theme")
	if err := os.MkdirAll(themeRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeRoot, "style.css"), []byte("/*\nTheme Name: Sample\nVersion: 1.0.0\n*/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := store.db.Exec(`UPDATE websites SET web_root=? WHERE id=?`, webRoot, siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET last_success_at=? WHERE site_id=?`, wpUpdateDBTime(now), siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE site_wp_components SET is_current_theme=1
		WHERE site_id=? AND component_type='theme' AND component_key='sample-theme'`, siteID); err != nil {
		t.Fatal(err)
	}
	artifacts, err := newWPUpdateArtifactService(store, filepath.Join(t.TempDir(), "artifacts"), fakeUpdateDump)
	if err != nil {
		t.Fatal(err)
	}
	source := writeThemePackageFixture(t, "sample-theme", "1.1.0", "")
	digest, _, err := hashRegularFile(source)
	if err != nil {
		t.Fatal(err)
	}
	service := &WPThemeUpdateService{
		db: store.db, store: store, artifacts: artifacts, confirmations: newWPThemeConfirmationStore(),
		fetchOffer: func(context.Context, string) (wpThemeOffer, error) {
			return wpThemeOffer{Slug: "sample-theme", Version: "1.1.0", DownloadURL: "https://downloads.wordpress.org/theme/sample-theme.1.1.0.zip"}, nil
		},
		download: func(context.Context, string, string) (string, string, error) { return source, digest, nil },
		now:      func() time.Time { return now },
	}
	preview, err := service.Preview(context.Background(), siteID, "admin", "sample-theme")
	if err != nil || !preview.CurrentTheme || preview.RiskToken == "" || preview.Template != "" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if _, err := service.Confirm(context.Background(), siteID, "admin", "sample-theme", preview.ConfirmationToken, "", "1.1.0"); !errors.Is(err, ErrWPThemeUpdateConflict) {
		t.Fatalf("missing risk confirmation error=%v", err)
	}
	preview, err = service.Preview(context.Background(), siteID, "admin", "sample-theme")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.Confirm(context.Background(), siteID, "admin", "sample-theme", preview.ConfirmationToken, preview.RiskToken, "1.1.0")
	if err != nil || task.Status != wpUpdateQueued || task.ComponentType != "theme" || task.VerificationLevel != "structure_only" {
		t.Fatalf("task=%+v err=%v", task, err)
	}
}

func TestWPThemeConfirmationRejectsRiskTokenForInactiveTheme(t *testing.T) {
	store := newWPThemeConfirmationStore()
	record, err := store.create(wpThemeConfirmation{
		username: "admin", siteID: 1, domain: "example.test", collectionID: "collection",
		componentKey: "sample-theme", currentVersion: "1.0.0", targetVersion: "1.1.0",
		downloadURL: "https://downloads.wordpress.org/theme/sample-theme.1.1.0.zip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.consume(record.token, "unexpected", "admin", 1, "sample-theme", "1.1.0"); err == nil {
		t.Fatal("inactive theme accepted a risk token")
	}
}
