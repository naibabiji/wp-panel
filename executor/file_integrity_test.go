package executor

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

func TestFileIntegrityScopeIncludesCodeAndExcludesRuntimeContents(t *testing.T) {
	tests := []struct {
		path    string
		dir     bool
		include bool
		descend bool
	}{
		{"wp-config.php", false, true, false},
		{"wp-admin", true, true, true},
		{"wp-admin/includes/file.php", false, true, true},
		{"wp-content/plugins/demo/main.php", false, true, true},
		{"wp-content/themes/inactive/index.php", false, true, true},
		{"wp-content/mu-plugins/hidden.php", false, true, true},
		{"wp-content/object-cache.php", false, true, false},
		{"wp-content/uploads", true, true, false},
		{"wp-content/uploads/2026/photo.jpg", false, false, true},
		{"private-tools", true, true, false},
		{"private-tools/shell.php", false, false, false},
	}
	for _, tt := range tests {
		include, descend := fileIntegrityScope(tt.path, tt.dir)
		if include != tt.include || descend != tt.descend {
			t.Fatalf("fileIntegrityScope(%q)=(%v,%v), want (%v,%v)", tt.path, include, descend, tt.include, tt.descend)
		}
	}
}

func TestFileIntegrityBaselineDetectsCodeChangesWithoutFollowingLinks(t *testing.T) {
	root := t.TempDir()
	dataDir := t.TempDir()
	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{DataDir: dataDir}}
	t.Cleanup(func() { config.AppConfig = oldConfig })

	write := func(rel, content string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("wp-config.php", "config")
	write("wp-admin/core.php", "core")
	write("wp-includes/load.php", "load")
	write("wp-content/plugins/demo/main.php", "plugin-v1")
	write("wp-content/plugins/a/one.php", "one")
	write("wp-content/plugins/a-b/two.php", "two")
	write("wp-content/themes/inactive/index.php", "theme")
	write("wp-content/mu-plugins/hidden.php", "mu")
	write("wp-content/object-cache.php", "dropin")
	write("wp-content/uploads/photo.jpg", "photo-v1")
	outside := filepath.Join(t.TempDir(), "outside.php")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "wp-content", "mu-plugins", "linked.php")); err != nil {
		t.Fatal(err)
	}

	site := &models.Website{ID: 7, WebRoot: root}
	ctx := context.Background()
	if err := writeFileIntegrityBaseline(ctx, site); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	path, err := fileIntegrityPath(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := readFileIntegrityManifest(path, site)
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}

	write("wp-content/plugins/demo/main.php", "plugin-v2")
	if err := os.Remove(filepath.Join(root, "wp-content", "plugins", "a", "one.php")); err != nil {
		t.Fatal(err)
	}
	write("wp-content/mu-plugins/copied.php", "copy")
	if err := os.Remove(filepath.Join(root, "wp-content", "themes", "inactive", "index.php")); err != nil {
		t.Fatal(err)
	}
	write("wp-content/uploads/photo.jpg", "photo-v2")
	if err := os.Remove(filepath.Join(root, "wp-content", "mu-plugins", "linked.php")); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other.php")
	if err := os.WriteFile(other, []byte("other"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(root, "wp-content", "mu-plugins", "linked.php")); err != nil {
		t.Fatal(err)
	}

	diffs, err := compareFileIntegrity(ctx, site, baseline)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	seen := map[string]bool{}
	for _, diff := range diffs {
		seen[diff.EventType+":"+diff.Path] = true
	}
	for _, want := range []string{
		FileSecurityEventIntegrityModified + ":wp-content/plugins/demo/main.php",
		FileSecurityEventIntegrityDeleted + ":wp-content/plugins/a/one.php",
		FileSecurityEventIntegrityAdded + ":wp-content/mu-plugins/copied.php",
		FileSecurityEventIntegrityDeleted + ":wp-content/themes/inactive/index.php",
		FileSecurityEventIntegrityLink + ":wp-content/mu-plugins/linked.php",
	} {
		if !seen[want] {
			t.Fatalf("missing difference %s in %#v", want, diffs)
		}
	}
	if seen[FileSecurityEventIntegrityModified+":wp-content/uploads/photo.jpg"] {
		t.Fatal("runtime upload content entered the integrity baseline")
	}
	currentPath, err := createFileIntegritySnapshot(ctx, site, filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(currentPath)
	streamDiffs, err := compareFileIntegrityManifestFiles(path, currentPath, site)
	if err != nil {
		t.Fatalf("stream compare: %v", err)
	}
	if !reflect.DeepEqual(streamDiffs, diffs) {
		t.Fatalf("stream differences=%#v, map differences=%#v", streamDiffs, diffs)
	}
}

func TestCompareFileIntegrityPathUsesWalkOrder(t *testing.T) {
	ordered := []string{"wp-content/plugins/a", "wp-content/plugins/a/one.php", "wp-content/plugins/a-b", "wp-content/plugins/a-b/two.php"}
	for i := 1; i < len(ordered); i++ {
		if compareFileIntegrityPath(ordered[i-1], ordered[i]) >= 0 {
			t.Fatalf("walk order comparator rejected %q before %q", ordered[i-1], ordered[i])
		}
	}
}

func TestFileIntegrityManifestRejectsDifferentWebRoot(t *testing.T) {
	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{DataDir: t.TempDir()}}
	t.Cleanup(func() { config.AppConfig = oldConfig })
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.php"), []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}
	site := &models.Website{ID: 9, WebRoot: root}
	if err := writeFileIntegrityBaseline(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	path, _ := fileIntegrityPath(site.ID)
	if _, err := readFileIntegrityManifest(path, &models.Website{ID: 9, WebRoot: t.TempDir()}); err == nil {
		t.Fatal("baseline accepted a different web root")
	}
}

func TestPersistIntegrityDiffsDeduplicatesChangesAndResolvesRecovery(t *testing.T) {
	openTestDB(t)
	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO websites
		(id, name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type)
		VALUES (77, 'demo', 'example.com', 'active', 'wp_demo', '/www/wwwroot/example.com', '/www/wwwlogs/example.com', 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf', 'wordpress')`); err != nil {
		t.Fatalf("insert website: %v", err)
	}
	site := &models.Website{ID: 77, Domain: "example.com"}
	diff := fileIntegrityDifference{EventType: FileSecurityEventIntegrityModified, Path: "wp-content/plugins/demo/main.php", Size: 12, Signature: "first"}

	fresh, err := persistIntegrityDiffs(db, site, []fileIntegrityDifference{diff})
	if err != nil || fresh != 1 {
		t.Fatalf("first persist = %d, %v; want 1, nil", fresh, err)
	}
	fresh, err = persistIntegrityDiffs(db, site, []fileIntegrityDifference{diff})
	if err != nil || fresh != 0 {
		t.Fatalf("unchanged persist = %d, %v; want 0, nil", fresh, err)
	}

	diff.Signature = "second"
	fresh, err = persistIntegrityDiffs(db, site, []fileIntegrityDifference{diff})
	if err != nil || fresh != 1 {
		t.Fatalf("changed persist = %d, %v; want 1, nil", fresh, err)
	}
	fresh, err = persistIntegrityDiffs(db, site, nil)
	if err != nil || fresh != 0 {
		t.Fatalf("recovery persist = %d, %v; want 0, nil", fresh, err)
	}
	var resolved sql.NullString
	if err := db.QueryRow(`SELECT resolved_at FROM file_security_events WHERE site_id=? AND event_type=?`, site.ID, diff.EventType).Scan(&resolved); err != nil {
		t.Fatalf("query resolved event: %v", err)
	}
	if !resolved.Valid {
		t.Fatal("recovered integrity event was not resolved")
	}

	fresh, err = persistIntegrityDiffs(db, site, []fileIntegrityDifference{diff})
	if err != nil || fresh != 1 {
		t.Fatalf("reappeared persist = %d, %v; want 1, nil", fresh, err)
	}
}

func TestRemoveFileIntegrityBaselineResolvesCurrentEvents(t *testing.T) {
	openTestDB(t)
	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{DataDir: t.TempDir()}}
	t.Cleanup(func() { config.AppConfig = oldConfig })
	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO websites
		(id, name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type)
		VALUES (78, 'demo', 'example.com', 'active', 'wp_demo', '/www/wwwroot/example.com', '/www/wwwlogs/example.com', 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf', 'wordpress')`); err != nil {
		t.Fatalf("insert website: %v", err)
	}
	path, err := fileIntegrityPath(78)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("baseline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := upsertFileSecurityEvent(db, fileSecurityRecord{SiteID: 78, Domain: "example.com", EventType: FileSecurityEventIntegrityModified, Source: "integrity", Path: "/index.php"}); err != nil {
		t.Fatal(err)
	}
	fileIntegrityStateMu.Lock()
	fileIntegrityRefreshJobs[78] = "test"
	fileIntegrityRetryAfter[78] = time.Now().Add(time.Hour)
	fileIntegrityStateMu.Unlock()

	if err := RemoveWPCodeIntegrityBaseline(78); err != nil {
		t.Fatalf("remove baseline: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("baseline still exists: %v", err)
	}
	var unresolved int
	if err := db.QueryRow(`SELECT COUNT(*) FROM file_security_events WHERE site_id=78 AND source='integrity' AND resolved_at IS NULL`).Scan(&unresolved); err != nil {
		t.Fatal(err)
	}
	if unresolved != 0 {
		t.Fatalf("unresolved integrity events = %d, want 0", unresolved)
	}
	var total, resolved int
	if err := db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN resolved_at IS NOT NULL THEN 1 ELSE 0 END),0) FROM file_security_events WHERE site_id=78 AND source='integrity'`).Scan(&total, &resolved); err != nil {
		t.Fatal(err)
	}
	if total != 1 || resolved != 1 {
		t.Fatalf("integrity history total/resolved = %d/%d, want 1/1", total, resolved)
	}
	if err := upsertFileSecurityEvent(db, fileSecurityRecord{SiteID: 78, Domain: "example.com", EventType: FileSecurityEventSuspiciousFile, Source: "scanner", Path: "/wp-content/uploads/shell.php"}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWPCodeIntegrityBaseline(78); err != nil {
		t.Fatalf("second remove baseline: %v", err)
	}
	var scannerUnresolved int
	if err := db.QueryRow(`SELECT COUNT(*) FROM file_security_events WHERE site_id=78 AND source='scanner' AND resolved_at IS NULL`).Scan(&scannerUnresolved); err != nil {
		t.Fatal(err)
	}
	if scannerUnresolved != 1 {
		t.Fatalf("scanner unresolved events = %d, want 1", scannerUnresolved)
	}
	fileIntegrityStateMu.Lock()
	_, queued := fileIntegrityRefreshJobs[78]
	_, backedOff := fileIntegrityRetryAfter[78]
	fileIntegrityStateMu.Unlock()
	if queued || backedOff {
		t.Fatalf("remove left queued=%v backedOff=%v", queued, backedOff)
	}
}
