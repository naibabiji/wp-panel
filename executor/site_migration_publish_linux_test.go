package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// realFilesMigrationTargetPublishOps copies and rewrites real files. SetOwner
// changes only the group, which an unprivileged test user can still use to
// reproduce the WebRoot/file ownership mismatch production sees as root.
type realFilesMigrationTargetPublishOps struct {
	productionSiteMigrationTargetPublishOps
	gid int
}

func (realFilesMigrationTargetPublishOps) ImportDatabase(context.Context, string, string) error {
	return nil
}

func (realFilesMigrationTargetPublishOps) ResetDatabase(string, string, string) error {
	return nil
}

func (o realFilesMigrationTargetPublishOps) SetOwner(path, _ string) error {
	return filepath.WalkDir(path, func(name string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(name, -1, o.gid)
	})
}

func TestSiteMigrationTargetPublisherOwnsFilesBeforeSecureWPConfigRewrite(t *testing.T) {
	siteGID := -1
	groups, _ := os.Getgroups()
	for _, group := range groups {
		if group != os.Getegid() {
			siteGID = group
			break
		}
	}
	if siteGID < 0 {
		t.Skip("requires a supplementary group to model a distinct site owner")
	}
	resourceService, _, root := setupMigrationTargetResourceServiceTest(t)
	if err := resourceService.Create(context.Background(), "migration_0000001", SiteMigrationTargetSpec{Domain: "example.com", SiteType: "wordpress"}); err != nil {
		t.Fatal(err)
	}
	webRoot := opsWebRoot(t, resourceService)
	// Target resource creation leaves an empty WebRoot owned by the site account.
	if err := os.MkdirAll(webRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Lchown(webRoot, -1, siteGID); err != nil {
		t.Fatal(err)
	}
	files := filepath.Join(root, "migration_0000001", "files")
	if err := os.MkdirAll(filepath.Join(files, "wp-content"), 0755); err != nil {
		t.Fatal(err)
	}
	config := "<?php\ndefine('DB_NAME', 'old');\ndefine('DB_USER', 'old_user');\ndefine('DB_PASSWORD', 'old_password');\n"
	if err := os.WriteFile(filepath.Join(files, "wp-config.php"), []byte(config), 0640); err != nil {
		t.Fatal(err)
	}
	publisher, err := NewSiteMigrationTargetPublisher(resourceService.db, resourceService.cfg, root)
	if err != nil {
		t.Fatal(err)
	}
	publisher.ops = realFilesMigrationTargetPublishOps{productionSiteMigrationTargetPublishOps: productionSiteMigrationTargetPublishOps{cfg: resourceService.cfg}, gid: siteGID}
	publisher.now = freezerTestTime
	if err := publisher.PublishData(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(webRoot, "wp-config.php"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "define('DB_NAME', '"+opsDatabaseName(t, resourceService)+"')") {
		t.Fatalf("wp-config was not rewritten: %s", content)
	}
	for _, name := range []string{"wp-config.php", "wp-content"} {
		info, err := os.Lstat(filepath.Join(webRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		if gid := int(info.Sys().(*syscall.Stat_t).Gid); gid != siteGID {
			t.Fatalf("%s gid=%d want %d", name, gid, siteGID)
		}
	}
}
