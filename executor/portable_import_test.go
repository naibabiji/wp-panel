package executor

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func portableDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func portableTestMaterials(t *testing.T) ([]byte, map[string][]byte, []byte) {
	t.Helper()
	statements := "CREATE TABLE example;\nINSERT INTO example VALUES (42);\n"
	half := len(statements) / 2
	objects := []struct{ kind, path, content string }{
		{"sql", "database.sql", statements},
		{"file", "wp-config.php", "<?php /* source configuration */"},
		{"file", "wp-load.php", "<?php"},
		{"file", "wp-settings.php", "<?php"},
		{"file", "index.php", "<?php"},
		{"file", "database.sql", "public documentation"},
		{"file", "custom/文档.txt", "custom text"},
		{"file", "custom/empty.txt", ""},
	}
	var index bytes.Buffer
	writeRecord := func(record portableRecord) {
		t.Helper()
		if err := json.NewEncoder(&index).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	files := make(map[string][]byte)
	artifacts := make([]PortableArtifact, 0, 2)
	var zipBuffer bytes.Buffer
	writer := zip.NewWriter(&zipBuffer)
	part := "part-00001.zip"
	member := 0
	closePart := func() {
		t.Helper()
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		files[part] = bytes.Clone(zipBuffer.Bytes())
		artifacts = append(artifacts, PortableArtifact{part, int64(zipBuffer.Len()), portableDigest(zipBuffer.Bytes())})
		zipBuffer.Reset()
	}
	writeBlock := func(kind, path, value string, offset int) {
		t.Helper()
		member++
		name := fmt.Sprintf("data/%05d", member)
		out, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(out, value); err != nil {
			t.Fatal(err)
		}
		writeRecord(portableRecord{Kind: "block", Target: kind, Path: path, Package: part, Member: name, Offset: int64(offset), Length: int64(len(value)), SHA256: portableDigest([]byte(value))})
	}
	for _, obj := range objects {
		switch {
		case obj.kind == "sql":
			writeBlock("sql", obj.path, obj.content[:half], 0)
			closePart()
			part, member, writer = "part-00002.zip", 0, zip.NewWriter(&zipBuffer)
			writeBlock("sql", obj.path, obj.content[half:], half)
		case obj.path == "custom/文档.txt":
			writeRecord(portableRecord{Kind: "directory", Path: "custom"})
			writeRecord(portableRecord{Kind: "directory", Path: "custom/empty"})
			writeBlock(obj.kind, obj.path, obj.content, 0)
		case obj.content != "":
			writeBlock(obj.kind, obj.path, obj.content, 0)
		}
		writeRecord(portableRecord{Kind: obj.kind, Path: obj.path, Size: int64(len(obj.content)), SHA256: portableDigest([]byte(obj.content))})
	}
	writeRecord(portableRecord{Kind: "excluded", Path: "wp-content/object-cache.php"})
	closePart()
	files["files.ndjson"] = index.Bytes()
	manifest, err := json.Marshal(map[string]any{
		"magic": "wp-panel-backup-development", "format_version": 1, "id": strings.Repeat("a", 32), "complete": true, "mode": "move",
		"source": map[string]string{"home": "https://backup.test", "siteurl": "https://backup.test", "prefix": "chosen_"},
		"tables": []map[string]any{{"name": "chosen_options", "rows": 2}, {"name": "external_table", "rows": 1}},
		"index":  PortableArtifact{"files.ndjson", int64(index.Len()), portableDigest(index.Bytes())}, "packages": artifacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	files["manifest.json"] = manifest
	var bundle bytes.Buffer
	tarWriter := tar.NewWriter(&bundle)
	for _, name := range []string{"manifest.json", "files.ndjson", "part-00001.zip", "part-00002.zip"} {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(files[name])), Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return manifest, files, bundle.Bytes()
}

func TestPortableImportAssemblesFilesAndSeparateSQL(t *testing.T) {
	raw, _, bundle := portableTestMaterials(t)
	m, address, err := parsePortableManifest(raw)
	if err != nil || address.Host != "backup.test" {
		t.Fatalf("manifest: %v %v", address, err)
	}
	service := portableTestService(t)
	input, work := t.TempDir(), t.TempDir()
	task, err := service.Begin(raw, int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(input, portableBundleName), bundle, 0600); err != nil {
		t.Fatal(err)
	}
	tree, err := service.verifyAndStage(task.ID, input, work, raw, m, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(tree, "index.php"))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(service.cfg.Paths.WWWRoot, "website")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := publishRestoredTree(tree, target); !os.IsExist(err) {
		t.Fatalf("existing target must be preserved: %v", err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := publishRestoredTree(tree, target); err != nil {
		t.Fatal(err)
	}
	if err := service.cleanupPortableTrees(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(target, "index.php"))
	if err != nil || !os.SameFile(before, after) || after.Mode().Perm() != 0644 {
		t.Fatalf("published file must keep its inode and mode: %v, %v", after, err)
	}
	if entries, err := os.ReadDir(input); err != nil || len(entries) != 1 || entries[0].Name() != portableBundleName {
		t.Fatalf("restore must read directly from the uploaded TAR: %v, %v", entries, err)
	}
	for name, want := range map[string]string{
		"database.sql":          "CREATE TABLE example;\nINSERT INTO example VALUES (42);\n",
		"website/database.sql":  "public documentation",
		"website/custom/文档.txt": "custom text", "website/custom/empty.txt": "",
		"source-config.php": "<?php /* source configuration */",
	} {
		base := work
		if strings.HasPrefix(name, "website/") {
			base = service.cfg.Paths.WWWRoot
		}
		got, err := os.ReadFile(filepath.Join(base, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q (%v), want %q", name, got, err, want)
		}
	}
	if info, err := os.Stat(filepath.Join(target, "custom/empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty directory: %v", err)
	}
	for _, name := range []string{"wp-config.php", "wp-content/object-cache.php"} {
		if _, err := os.Stat(filepath.Join(target, name)); !os.IsNotExist(err) {
			t.Errorf("unexpected restored runtime config %s: %v", name, err)
		}
	}
}

func TestPortableImportResetsCorruptTARBeforeStaging(t *testing.T) {
	raw, _, bundle := portableTestMaterials(t)
	manifest, _, err := parsePortableManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	service := portableTestService(t)
	task, err := service.Begin(raw, int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	inputBytes := bytes.NewReader(bundle)
	archive := tar.NewReader(inputBytes)
	if _, err := archive.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Next(); err != nil {
		t.Fatal(err)
	}
	indexOffset := len(bundle) - inputBytes.Len()
	bundle[indexOffset] ^= 1
	input := t.TempDir()
	filename := filepath.Join(input, portableBundleName)
	if err := os.WriteFile(filename, bundle, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.verifyAndStage(task.ID, input, t.TempDir(), raw, manifest, true, func() error {
		t.Fatal("cleanup must wait for backup validation")
		return nil
	}); !errors.Is(err, errPortableArtifactMismatch) {
		t.Fatalf("expected artifact mismatch, got %v", err)
	}
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		t.Fatalf("corrupt upload must be reset: %v", err)
	}
	if entries, err := os.ReadDir(service.cfg.Paths.WWWRoot); err != nil || len(entries) != 0 {
		t.Fatalf("corrupt archive must fail before staging: %v, %v", entries, err)
	}
}

func portableTestService(t *testing.T) *PortableImports {
	t.Helper()
	previous := database.DB
	data := t.TempDir()
	if err := database.Open(filepath.Join(data, "test.db")); err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := database.RunUpgrades(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { database.DB.Close(); database.DB = previous })
	cfg := &config.Config{Panel: config.PanelConfig{DataDir: data}, Paths: config.PathsConfig{
		WWWRoot: t.TempDir(), WWWLogs: t.TempDir(), Certificates: data,
		PHPFPMSock: "/run/php", PHPFPMPool: data, NginxSitesAvailable: data, NginxSitesEnabled: filepath.Join(data, "enabled"),
	}}
	service := NewPortableImports(database.GetDB(), cfg)
	service.retryDBQuery = func(string) (string, error) { return "", nil }
	return service
}

func TestPortableImportResumesAndResetsOnlyBadUpload(t *testing.T) {
	service := portableTestService(t)
	raw, files, _ := portableTestMaterials(t)
	task, err := service.Begin(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Submit(task.ID, nil, ""); err == nil || !strings.Contains(err.Error(), "files.ndjson") {
		t.Fatalf("missing file error: %v", err)
	}
	var count int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM websites").Scan(&count); err != nil || count != 0 {
		t.Fatalf("website created before verification: %d (%v)", count, err)
	}
	index := files["files.ndjson"]
	cut := len(index) / 2
	if _, err := service.Upload(task.ID, "files.ndjson", 0, index[:cut]); err != nil {
		t.Fatal(err)
	}
	resumed, err := NewPortableImports(service.db, service.cfg).Begin(raw, 0)
	if err != nil || resumed.ID != task.ID || resumed.Offsets["files.ndjson"] != int64(cut) {
		t.Fatalf("resumed task: %+v %v", resumed, err)
	}
	if _, err := service.Upload(task.ID, "files.ndjson", int64(cut), index[cut:]); err != nil {
		t.Fatal(err)
	}
	bad := bytes.Clone(files["part-00001.zip"])
	bad[0] ^= 1
	if _, err := service.Upload(task.ID, "part-00001.zip", 0, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Upload(task.ID, "part-00002.zip", 0, files["part-00002.zip"]); err != nil {
		t.Fatal(err)
	}
	if err := service.Submit(task.ID, []string{" WWW.Backup.test ", "www.backup.test", task.Domain}, ""); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		portableWaitForFailure(t, service, task.ID)
		retry, err := service.PrepareRetry(task.ID, nil, 0)
		if err != nil || retry.ID != task.ID || retry.Offsets["part-00001.zip"] != 0 ||
			retry.Offsets["part-00002.zip"] != int64(len(files["part-00002.zip"])) || strings.Join(retry.Aliases, ",") != "www.backup.test" {
			t.Fatalf("retry did not retain good files: %+v, %v", retry, err)
		}
		if attempt == 0 {
			if _, err := service.Upload(task.ID, "part-00001.zip", 0, bad); err != nil {
				t.Fatal(err)
			}
			if err := service.Submit(task.ID, nil, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := service.Upload(task.ID, "part-00001.zip", 0, files["part-00001.zip"]); err != nil {
		t.Fatal(err)
	}
	if err := service.phase(task.ID, "running", "database", ""); err != nil {
		t.Fatal(err)
	}
	dir, err := service.taskDir(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	staleWork, err := os.MkdirTemp(dir, "verify-")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteTask(task.ID); !errors.Is(err, ErrPortableState) {
		t.Fatalf("delete running: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restarted := NewPortableImports(service.db, service.cfg)
	restarted.retryDBQuery = service.retryDBQuery
	restarted.Start(ctx)
	interrupted, err := restarted.Detail(task.ID)
	if err != nil || interrupted.Status != "interrupted" || interrupted.Phase != "database" {
		t.Fatalf("interrupted task: %+v %v", interrupted, err)
	}
	retry, err := restarted.PrepareRetry(task.ID, nil, 0)
	if err != nil || retry.Offsets["part-00001.zip"] != int64(len(files["part-00001.zip"])) {
		t.Fatalf("interrupted retry did not reuse uploaded files: %+v, %v", retry, err)
	}
	if _, err := os.Stat(staleWork); !os.IsNotExist(err) {
		t.Fatalf("interrupted retry left stale verification files: %v", err)
	}
}

func portableWaitForFailure(t *testing.T, service *PortableImports, id string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		service.mu.Lock()
		task, err := service.task(id)
		finished := service.runningID == ""
		service.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if finished && task.Status == "failed" && task.Phase == "validating" && task.Message != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("bad package was not reported")
}

func TestPortableImportKeepsUploadedFileOnReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read the fixture despite its denied file permissions")
	}
	service := portableTestService(t)
	raw, files, _ := portableTestMaterials(t)
	task, err := service.Begin(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range task.Files {
		if _, err := service.Upload(task.ID, file.Name, 0, files[file.Name]); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := service.taskDir(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	const partName = "part-00001.zip"
	part := filepath.Join(dir, partName)
	if err := os.Chmod(part, 0); err != nil {
		t.Fatal(err)
	}
	if err := service.Submit(task.ID, nil, ""); err != nil {
		t.Fatal(err)
	}
	portableWaitForFailure(t, service, task.ID)
	failed, err := service.Detail(task.ID)
	if err != nil || !strings.Contains(failed.Message, "读取备份材料 "+partName+" 失败") {
		t.Fatalf("expected an explicit read error: %+v, %v", failed, err)
	}
	if err := os.Chmod(part, 0600); err != nil {
		t.Fatal(err)
	}
	retained, err := os.ReadFile(part)
	if err != nil || !bytes.Equal(retained, files[partName]) {
		t.Fatalf("uploaded file was lost or changed after a read error: %v", err)
	}
	retry, err := service.PrepareRetry(task.ID, nil, 0)
	if err != nil || retry.Offsets[partName] != int64(len(retained)) {
		t.Fatalf("retry did not reuse the retained file: %+v, %v", retry, err)
	}
}

func TestPortableImportCleansSuccessAndRetainsWebsite(t *testing.T) {
	service := portableTestService(t)
	raw, _, _ := portableTestMaterials(t)
	task, err := service.Begin(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := service.taskDir(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.phase(task.ID, "running", "publishing", ""); err != nil {
		t.Fatal(err)
	}
	webRoot := t.TempDir()
	siteFile := filepath.Join(webRoot, "index.php")
	if err := os.WriteFile(siteFile, []byte("restored site"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := service.completePortableSite(task, siteProvisioningResources{SiteName: task.Domain, WebRoot: webRoot}, 4, false, "", "", nil, "", "", "fixture", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	service.finishRun(task.ID, true)
	success, err := service.Detail(task.ID)
	if err != nil || success.Status != "succeeded" || success.ExpiresAt <= 0 || success.ExpiresAt > time.Now().Unix() || success.SiteID == 0 {
		t.Fatalf("failed cleanup must leave success eligible for retry: %+v, %v", success, err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := service.cleanupExpired(); err != nil {
		t.Fatal(err)
	}
	cleaned, err := service.Detail(task.ID)
	if err != nil || cleaned.Status != "succeeded" || cleaned.ExpiresAt != 0 {
		t.Fatalf("cleared staging must leave a success record: %+v, %v", cleaned, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("staged files remain: %v", err)
	}
	if err := service.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Detail(task.ID); !errors.Is(err, ErrPortableMissing) {
		t.Fatalf("deleted task: %v", err)
	}
	var domain string
	if err := service.db.QueryRow("SELECT domain FROM websites WHERE id=?", success.SiteID).Scan(&domain); err != nil || domain != task.Domain {
		t.Fatalf("website after deleting task: %q, %v", domain, err)
	}
	if _, err := os.Stat(siteFile); err != nil {
		t.Fatalf("restored file after deleting task: %v", err)
	}
}

func TestPortableImportImmediateSuccessCleanup(t *testing.T) {
	service := portableTestService(t)
	raw, _, _ := portableTestMaterials(t)
	task, err := service.Begin(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.setPhase(task.ID, "failed", "database", "fixture", time.Hour); err != nil {
		t.Fatal(err)
	}
	retried, err := service.PrepareRetry(task.ID, nil, 0)
	if err != nil || retried.Phase != portableRetryUploadPhase {
		t.Fatalf("prepare retry: %+v, %v", retried, err)
	}
	dir, err := service.taskDir(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.phase(task.ID, "succeeded", "completed", ""); err != nil {
		t.Fatal(err)
	}
	service.finishRun(retried.ID, true)
	record, err := service.Detail(task.ID)
	if err != nil || record.Status != "succeeded" || record.ExpiresAt != 0 {
		t.Fatalf("immediate cleanup failed: %+v, %v", record, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("staged files remain: %v", err)
	}
}

func TestPortableImportRetryReuploadsExpiredTask(t *testing.T) {
	service := portableTestService(t)
	raw, files, _ := portableTestMaterials(t)
	task, err := service.Begin(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Upload(task.ID, "files.ndjson", 0, files["files.ndjson"]); err != nil {
		t.Fatal(err)
	}
	if err := service.setPhase(task.ID, "failed", "database", "fixture", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec("UPDATE portable_import_tasks SET expires_at=1 WHERE id=?", task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PrepareRetry(task.ID, nil, 0); !errors.Is(err, ErrPortableRetryUpload) {
		t.Fatalf("expired retry: %v", err)
	}
	if _, err := service.PrepareRetry(task.ID, []byte("different manifest"), 0); err == nil {
		t.Fatal("retry accepted a different backup")
	}
	retry, err := service.PrepareRetry(task.ID, raw, 0)
	if err != nil || retry.ID != task.ID || retry.Status != "receiving" || retry.Offsets["files.ndjson"] != 0 {
		t.Fatalf("reupload expired backup: %+v, %v", retry, err)
	}
	if _, err := service.db.Exec("UPDATE portable_import_tasks SET expires_at=1 WHERE id=?", task.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.cleanupExpired(); err != nil {
		t.Fatal(err)
	}
	expiredRetry, err := service.Detail(task.ID)
	if err != nil || expiredRetry.Status != "failed" || expiredRetry.Phase != "uploading" || expiredRetry.Message == "" {
		t.Fatalf("expired retry history: %+v, %v", expiredRetry, err)
	}
	if _, err := service.PrepareRetry(task.ID, raw, 0); err != nil {
		t.Fatal(err)
	}
	dir, err := service.taskDir(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("deleted task left staged files: %v", err)
	}
}

func TestPortableImportTemporaryCertificate(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	aliases := []string{"www.backup.test", "extra.backup.test"}
	until, err := writePortableTemporaryCertificate("backup.test", aliases, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	if until.Before(time.Now().Add(29*24*time.Hour)) || until.After(time.Now().Add(30*24*time.Hour)) {
		t.Fatalf("unexpected certificate expiration: %v", until)
	}
	fingerprint, err := migrationCertificateFingerprint(cert)
	if err != nil || !PortableCertificateIsTemporary(cert, fingerprint) || PortableCertificateIsTemporary(cert, "unrelated") {
		t.Fatalf("temporary certificate fingerprint: %v", err)
	}
	for _, domain := range append([]string{"backup.test"}, aliases...) {
		if _, err := validateCertificate(cert, domain); err != nil {
			t.Fatalf("certificate does not cover %s: %v", domain, err)
		}
	}
	conf := portableMaintenanceConfig(buildServerNames("backup.test", aliases), dir, cert, key)
	if strings.Count(conf, "server_name backup.test www.backup.test extra.backup.test;") != 2 {
		t.Fatalf("maintenance hostnames missing in one vhost: %s", conf)
	}
	for _, text := range []string{"listen 443 ssl", "return 503", "acme-challenge"} {
		if !strings.Contains(conf, text) {
			t.Errorf("maintenance vhost missing %s", text)
		}
	}
	if strings.Contains(conf, "fastcgi_pass") {
		t.Fatal("maintenance vhost routes to PHP")
	}
}

func TestPortableAliasSuggestion(t *testing.T) {
	for domain, expected := range map[string]string{
		"18kcustoms.com": "www.18kcustoms.com", "example.com.au": "www.example.com.au",
		" Example.CO.UK ": "www.example.co.uk", "shop.example.com": "",
		" WWW.18kcustoms.com ": "18kcustoms.com", "www.example.com.au": "example.com.au",
		"www.shop.example.com": "", "shop.www.example.com": "",
		"example.github.io": "", "www.example.github.io": "", "com.au": "", "bad domain.com": "",
		"www.com": "www.www.com", "www.www.com": "www.com",
	} {
		if got := PortableAliasSuggestion(domain); got != expected {
			t.Errorf("%q: got %q, want %q", domain, got, expected)
		}
	}
}

func TestPortableAliasConflictAndExplicitClear(t *testing.T) {
	service := portableTestService(t)
	raw, files, _ := portableTestMaterials(t)
	task, err := service.Begin(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.db.Exec(createWebsiteInsertSQL, "other", "other.test", "www.backup.test", "other", "/unused", "", "/unused",
		"unused", "unused", "/unused", "/unused", "wordpress", false, "", "", nil, "", "", 30, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range task.Files {
		data := bytes.Clone(files[file.Name])
		if file.Name == "part-00001.zip" {
			data[0] ^= 1
		}
		if _, err := service.Upload(task.ID, file.Name, 0, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.Submit(task.ID, []string{"www.backup.test"}, ""); SiteProvisioningErrorCode(err) != "domain_conflict" || !strings.Contains(err.Error(), "www.backup.test") {
		t.Fatalf("alias conflict error = %v", err)
	}
	if err := service.Submit(task.ID, []string{"saved.backup.test"}, ""); err != nil {
		t.Fatal(err)
	}
	portableWaitForFailure(t, service, task.ID)
	retry, err := service.PrepareRetry(task.ID, nil, 0)
	if err != nil || strings.Join(retry.Aliases, ",") != "saved.backup.test" {
		t.Fatalf("alias preference not retained on retry: %+v %v", retry, err)
	}
	bad := bytes.Clone(files["part-00001.zip"])
	bad[0] ^= 1
	if _, err := service.Upload(task.ID, "part-00001.zip", 0, bad); err != nil {
		t.Fatal(err)
	}
	if err := service.Submit(task.ID, []string{}, ""); err != nil {
		t.Fatal(err)
	}
	portableWaitForFailure(t, service, task.ID)
	retry, err = service.PrepareRetry(task.ID, nil, 0)
	if err != nil || len(retry.Aliases) != 0 {
		t.Fatalf("explicitly cleared aliases reappeared: %+v %v", retry, err)
	}
}

func TestPortableCertificateUsesReadyAliasesAndFallsBackToPrimary(t *testing.T) {
	var calls []string
	expected := time.Now().Add(time.Hour)
	got, err := obtainPortableCertificate([]string{"ready.example.com", "unresolved.example.com"}, func(alias string) bool {
		return alias == "ready.example.com"
	}, func(aliases string) (time.Time, error) {
		calls = append(calls, aliases)
		if aliases != "" {
			return time.Time{}, errors.New("alias did not pass ACME validation")
		}
		return expected, nil
	})
	if err != nil || !got.Equal(expected) || len(calls) != 2 || calls[0] != "ready.example.com" || calls[1] != "" {
		t.Fatalf("certificate requests = %#v, expiry = %v, error = %v", calls, got, err)
	}
}

func TestPortableRetryChecksActualResourcesAndRequiresCurrentConfirmation(t *testing.T) {
	service := portableTestService(t)
	raw, files, _ := portableTestMaterials(t)
	task, err := service.Begin(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range task.Files {
		if _, err := service.Upload(task.ID, file.Name, 0, files[file.Name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.phase(task.ID, "failed", "configuration", "fixture failure"); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(service.cfg.Paths.WWWRoot, task.Domain)
	if err := os.Mkdir(webRoot, 0755); err != nil {
		t.Fatal(err)
	}
	setProof := func(proof portableCleanupProof) {
		t.Helper()
		raw, err := json.Marshal(proof)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.db.Exec("UPDATE portable_import_tasks SET cleanup_proof=? WHERE id=?", string(raw), task.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.RetryPlan(task.ID, nil); err == nil || !strings.Contains(err.Error(), webRoot) {
		t.Fatalf("pre-existing target must be reported as conflict: %v", err)
	}
	identity, err := portablePathIdentity(webRoot)
	if err != nil || identity == "" || identity == "unverified" {
		t.Fatalf("missing fixture directory identity: %q %v", identity, err)
	}
	proof := portableCleanupProof{Attempt: strings.Repeat("a", 32), Owned: map[string]string{webRoot: identity}, Sealed: true}
	setProof(proof)
	if _, err := service.RetryPlan(task.ID, nil); err == nil {
		t.Fatal("potentially published resources must not be listed for cleanup")
	}
	proof.Sealed = false
	setProof(proof)
	plan, err := service.RetryPlan(task.ID, []string{"www.backup.test"})
	if err != nil || plan.Token == "" || !plan.MaterialsReady || len(plan.Resources) != 1 || plan.Resources[0] != webRoot {
		t.Fatalf("retry plan: %+v %v", plan, err)
	}
	if _, err := service.PrepareRetry(task.ID, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := service.Submit(task.ID, nil, ""); err == nil {
		t.Fatal("cleanup requires confirmation")
	}
	if err := service.Submit(task.ID, []string{"other.backup.test"}, plan.Token); err == nil {
		t.Fatal("cleanup confirmation must be bound to aliases")
	}
	if err := os.Rename(webRoot, webRoot+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(webRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := service.Submit(task.ID, []string{"www.backup.test"}, plan.Token); err == nil || !strings.Contains(err.Error(), webRoot) {
		t.Fatalf("replacement path must be reported as conflict: %v", err)
	}
	if _, err := os.Stat(webRoot); err != nil {
		t.Fatalf("replacement directory was touched: %v", err)
	}
}

func TestPortableUploadRetainsAliasSelectionAcrossResume(t *testing.T) {
	service := portableTestService(t)
	raw, files, _ := portableTestMaterials(t)
	task, err := service.Begin(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	index := files["files.ndjson"]
	half := len(index) / 2
	for _, tc := range []struct {
		name string
		in   []string
		want string
	}{
		{"default", []string{"www.backup.test"}, "www.backup.test"},
		{"edited", []string{" CDN.Backup.test ", "cdn.backup.test"}, "cdn.backup.test"},
		{"cleared", []string{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.SaveUploadAliases(task.ID, tc.in); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Upload(task.ID, "files.ndjson", 0, index[:half]); err != nil {
				t.Fatal(err)
			}
			reloaded := NewPortableImports(service.db, service.cfg)
			got, err := reloaded.Detail(task.ID)
			if err != nil || strings.Join(got.Aliases, ",") != tc.want || got.Offsets["files.ndjson"] != int64(half) {
				t.Fatalf("after reload: %+v, %v", got, err)
			}
			got, err = reloaded.Begin(raw, 0)
			if err != nil || got.ID != task.ID || strings.Join(got.Aliases, ",") != tc.want {
				t.Fatalf("after reselecting the same backup: %+v, %v", got, err)
			}
		})
	}
	if _, err := service.SaveUploadAliases(task.ID, nil); err == nil {
		t.Fatal("a missing selection should be rejected")
	}
	if err := service.phase(task.ID, "running", "validating", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveUploadAliases(task.ID, []string{"cdn.backup.test"}); !errors.Is(err, ErrPortableState) {
		t.Fatalf("cannot edit aliases once restore starts: %v", err)
	}
}

func TestPortableImportRejectsAliasReservedByMigration(t *testing.T) {
	store, _ := newSiteMigrationStoreTest(t)
	service := NewPortableImports(store.db, config.AppConfig)
	raw, files, _ := portableTestMaterials(t)
	task, err := service.Begin(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range task.Files {
		if _, err := service.Upload(task.ID, file.Name, 0, files[file.Name]); err != nil {
			t.Fatal(err)
		}
	}
	const alias = "pending.backup.test"
	ctx := context.Background()
	if err := store.acquireLock(ctx, "migration_0000001", nil, alias, "target", time.Now()); err != nil {
		t.Fatal(err)
	}
	aliases := []string{alias}
	assertLocked := func(err error) {
		t.Helper()
		if SiteProvisioningErrorCode(err) != "migration_locked" || !strings.Contains(err.Error(), alias) {
			t.Fatalf("want locked alias %s, got %v", alias, err)
		}
	}
	assertLocked(service.Submit(task.ID, aliases, ""))
	unchanged, err := service.Detail(task.ID)
	if err != nil || unchanged.Status != "receiving" {
		t.Fatalf("locked alias started a restore: %+v, %v", unchanged, err)
	}
	if err := service.phase(task.ID, "failed", "validating", "fixture"); err != nil {
		t.Fatal(err)
	}
	_, err = service.RetryPlan(task.ID, aliases)
	assertLocked(err)
	if err := store.releaseLock(ctx, "migration_0000001", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := checkPortableDomainAndMigration(ctx, task.Domain, aliases); err != nil {
		t.Fatalf("released alias should be available: %v", err)
	}
}

func TestPortableRemoteRetainsPinAndCompletedArtifact(t *testing.T) {
	service := portableTestService(t)
	raw, files, _ := portableTestMaterials(t)
	pin := PortableRemoteSource{Host: "source.example.com", IP: "93.184.215.14"}
	task, err := service.BindRemoteSource("", raw, pin, false)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := service.taskDir(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := task.Files[0].Name
	open := func(_ context.Context, name string) (io.ReadCloser, error) {
		if name != first {
			return nil, errors.New("fixture connection closed")
		}
		return io.NopCloser(bytes.NewReader(files[name])), nil
	}
	if err := service.downloadPortableMaterials(dir, task.Files, open); err == nil {
		t.Fatal("expected interruption after the first artifact")
	}
	if _, err := os.Stat(filepath.Join(dir, portableRemotePartialFile)); !os.IsNotExist(err) {
		t.Fatalf("temporary data was left behind: %v", err)
	}
	reopened := NewPortableImports(service.db, service.cfg)
	gotPin, _, err := reopened.RemoteSource(task.ID)
	if err != nil || gotPin != pin {
		t.Fatalf("reopened task lost source pin: %+v, %v", gotPin, err)
	}
	newIP := PortableRemoteSource{Host: pin.Host, IP: "1.1.1.1"}
	if _, err := reopened.BindRemoteSource(task.ID, raw, newIP, false); err == nil {
		t.Fatal("address changed without an explicit replacement")
	}
	wrong := append(append([]byte(nil), raw...), '\n')
	if _, err := reopened.BindRemoteSource(task.ID, wrong, newIP, true); err == nil {
		t.Fatal("replacement accepted a different manifest")
	}
	open = func(_ context.Context, name string) (io.ReadCloser, error) {
		if name == first {
			t.Fatal("downloaded artifact should have been reused")
		}
		return io.NopCloser(bytes.NewReader(files[name])), nil
	}
	if err := reopened.downloadPortableMaterials(dir, task.Files, open); err != nil {
		t.Fatal(err)
	}
	for _, file := range task.Files {
		if err := verifyPortableArtifact(dir, file); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reopened.BindRemoteSource(task.ID, raw, newIP, true); err != nil {
		t.Fatal(err)
	}
	gotPin, _, err = reopened.RemoteSource(task.ID)
	if err != nil || gotPin != newIP {
		t.Fatalf("explicit source replacement not saved: %+v %v", gotPin, err)
	}
}

func TestPortableRemoteReportsCurrentDownloadWithoutMarkingItComplete(t *testing.T) {
	service := portableTestService(t)
	raw, files, _ := portableTestMaterials(t)
	pin := PortableRemoteSource{Host: "source.example.com", IP: "93.184.215.14"}
	task, err := service.BindRemoteSource("", raw, pin, false)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := service.taskDir(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.setPhase(task.ID, "running", "downloading", "", 0); err != nil {
		t.Fatal(err)
	}
	service.runningID = task.ID
	artifact := task.Files[0]
	payload := files[artifact.Name]
	half := len(payload) / 2
	reader, writer := io.Pipe()
	t.Cleanup(func() { writer.Close() })
	done := make(chan error, 1)
	go func() {
		done <- service.downloadPortableMaterials(dir, []PortableArtifact{artifact}, func(_ context.Context, _ string) (io.ReadCloser, error) {
			return io.NopCloser(io.MultiReader(bytes.NewReader(payload[:half]), reader)), nil
		})
	}()
	defer reader.Close()
	waitForProgress := func(want int64) {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			current, err := service.Detail(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.RemoteDownload != nil && current.RemoteDownload.Received == want {
				if current.RemoteDownload.Name != artifact.Name || current.Offsets[artifact.Name] != 0 {
					t.Fatalf("unverified bytes published as complete: %+v", current)
				}
				return
			}
		}
		t.Fatalf("did not observe %d in-progress bytes", want)
	}
	waitForProgress(int64(half))
	if _, err := writer.Write(payload[half:]); err != nil {
		t.Fatal(err)
	}
	waitForProgress(artifact.Size)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	current, err := service.Detail(task.ID)
	if err != nil || current.RemoteDownload != nil || current.Offsets[artifact.Name] != artifact.Size {
		t.Fatalf("verified artifact progress not promoted: %+v, %v", current, err)
	}
}

func TestPortableSQLRestoreUsesRestrictedSiteClient(t *testing.T) {
	service := portableTestService(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "database.sql")
	if err := os.WriteFile(path, []byte("SELECT 1;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client := `#!/bin/sh
test "$*" = '--binary-mode --local-infile=0 --user=fixture_site --database=fixture_db' || exit 1
test "$MYSQL_PWD" = fixture-password || exit 1
cat >/dev/null
printf '%s\n' 'ERROR 1044 (42000) at line 2: Access denied' >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(dir, "mysql"), []byte(client), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := service.restorePortableSQL("fixture_db", "fixture_site", "fixture-password", path, portableManifest{})
	if err == nil || !strings.Contains(err.Error(), "MariaDB 错误 1044") {
		t.Fatalf("expected the restricted site client to report the database error, got %v", err)
	}
}
