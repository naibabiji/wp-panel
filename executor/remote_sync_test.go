package executor

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/naibabiji/wp-panel/database"
)

func TestValidateRemoteBackupSettingsRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name       string
		host       string
		port       int
		username   string
		authType   string
		remotePath string
	}{
		{name: "host command chars", host: "example.com;reboot", port: 22, username: "root", authType: "password", remotePath: "/backup"},
		{name: "bad port", host: "example.com", port: 70000, username: "root", authType: "password", remotePath: "/backup"},
		{name: "bad username", host: "example.com", port: 22, username: "root;id", authType: "password", remotePath: "/backup"},
		{name: "bad auth", host: "example.com", port: 22, username: "root", authType: "agent", remotePath: "/backup"},
		{name: "bad path chars", host: "example.com", port: 22, username: "root", authType: "password", remotePath: "/backup;rm"},
		{name: "path traversal", host: "example.com", port: 22, username: "root", authType: "password", remotePath: "/backup/../other"},
		{name: "ipv6 unsupported", host: "2001:db8::1", port: 22, username: "root", authType: "password", remotePath: "/backup"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateRemoteBackupSettings(tt.host, tt.port, tt.username, tt.authType, tt.remotePath); err == nil {
				t.Fatal("ValidateRemoteBackupSettings error = nil, want error")
			}
		})
	}
}

func TestValidateRemoteBackupSettingsAcceptsSafeValues(t *testing.T) {
	if err := ValidateRemoteBackupSettings("backup.example.com", 2222, "wp_backup", "key", "/srv/wp-panel/backups"); err != nil {
		t.Fatalf("ValidateRemoteBackupSettings safe domain: %v", err)
	}
	if err := ValidateRemoteBackupSettings("192.0.2.10", 22, "root", "password", "~/backups"); err != nil {
		t.Fatalf("ValidateRemoteBackupSettings safe IP: %v", err)
	}
}

func TestLocalBackupRelPathRequiresBackupsRoot(t *testing.T) {
	got, err := localBackupRelPath(backupsRoot + "/example.com/db/site.sql.gz")
	if err != nil {
		t.Fatalf("localBackupRelPath inside root: %v", err)
	}
	if got != "example.com/db/site.sql.gz" {
		t.Fatalf("localBackupRelPath = %q", got)
	}
	if _, err := localBackupRelPath("/tmp/site.sql.gz"); err == nil {
		t.Fatal("localBackupRelPath outside root error = nil, want error")
	}
}

func TestValidateS3BackupSettingsRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name        string
		endpoint    string
		bucket      string
		region      string
		accessKeyID string
		secretKey   string
		prefix      string
	}{
		{name: "plain http", endpoint: "http://example.com", bucket: "wp-backups", region: "auto", accessKeyID: "key123", secretKey: "secret"},
		{name: "query", endpoint: "https://example.com?x=1", bucket: "wp-backups", region: "auto", accessKeyID: "key123", secretKey: "secret"},
		{name: "bad bucket", endpoint: "https://example.com", bucket: "Bad_Bucket", region: "auto", accessKeyID: "key123", secretKey: "secret"},
		{name: "bad region", endpoint: "https://example.com", bucket: "wp-backups", region: "auto;rm", accessKeyID: "key123", secretKey: "secret"},
		{name: "bad access key", endpoint: "https://example.com", bucket: "wp-backups", region: "auto", accessKeyID: "key id", secretKey: "secret"},
		{name: "empty secret", endpoint: "https://example.com", bucket: "wp-backups", region: "auto", accessKeyID: "key123", secretKey: ""},
		{name: "prefix traversal", endpoint: "https://example.com", bucket: "wp-backups", region: "auto", accessKeyID: "key123", secretKey: "secret", prefix: "wp/../other"},
		{name: "prefix command chars", endpoint: "https://example.com", bucket: "wp-backups", region: "auto", accessKeyID: "key123", secretKey: "secret", prefix: "wp;rm"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateS3BackupSettings(tt.endpoint, tt.bucket, tt.region, tt.accessKeyID, tt.secretKey, tt.prefix); err == nil {
				t.Fatal("ValidateS3BackupSettings error = nil, want error")
			}
		})
	}
}

func TestValidateS3BackupSettingsAcceptsR2StyleConfig(t *testing.T) {
	if err := ValidateS3BackupSettings("https://abc123.r2.cloudflarestorage.com", "wp-panel-backups", "auto", "access-key_123", "secret/key+123", "wp-panel/server-1"); err != nil {
		t.Fatalf("ValidateS3BackupSettings R2 config: %v", err)
	}
}

func TestS3ObjectURLUsesPathStyleAndEscapesKey(t *testing.T) {
	u, err := s3ObjectURL("https://abc123.r2.cloudflarestorage.com", "wp-panel-backups", "wp panel/example.com/db/site backup.sql.gz", "")
	if err != nil {
		t.Fatalf("s3ObjectURL() error = %v", err)
	}
	want := "https://abc123.r2.cloudflarestorage.com/wp-panel-backups/wp%20panel/example.com/db/site%20backup.sql.gz"
	if got := u.String(); got != want {
		t.Fatalf("s3ObjectURL = %q, want %q", got, want)
	}
}

func TestS3ObjectKeyAddsPrefix(t *testing.T) {
	if got := s3ObjectKey("/wp-panel/server-1/", "example.com/db/site.sql.gz"); got != "wp-panel/server-1/example.com/db/site.sql.gz" {
		t.Fatalf("s3ObjectKey = %q", got)
	}
}

func TestProbeS3BackupConnectionUploadsAndDeletesTestObject(t *testing.T) {
	var mu sync.Mutex
	requests := []string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		if r.URL.Path != "/wp-panel-backups/wp-panel/.wp-panel-s3-test.txt" {
			t.Errorf("unexpected path: %s", r.URL.String())
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	withS3TestClient(t, server)

	if err := ProbeS3BackupConnection(server.URL, "wp-panel-backups", "auto", "access-key_123", "secret", "wp-panel"); err != nil {
		t.Fatalf("ProbeS3BackupConnection() error = %v", err)
	}

	got := strings.Join(requests, "\n")
	if !strings.Contains(got, "PUT /wp-panel-backups/wp-panel/.wp-panel-s3-test.txt?") {
		t.Fatalf("PUT test object request missing; got:\n%s", got)
	}
	if !strings.Contains(got, "DELETE /wp-panel-backups/wp-panel/.wp-panel-s3-test.txt?") {
		t.Fatalf("DELETE test object request missing; got:\n%s", got)
	}
}

func TestPutFileToS3UsesMultipartForLargeFiles(t *testing.T) {
	var mu sync.Mutex
	requests := []string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.RawQuery == "uploads=":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`<InitiateMultipartUploadResult><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`))
		case r.Method == http.MethodPut && strings.Contains(r.URL.RawQuery, "partNumber="):
			w.Header().Set("ETag", `"etag-`+r.URL.Query().Get("partNumber")+`"`)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.Contains(r.URL.RawQuery, "uploadId=upload-1"):
			if got := r.Header.Get("Content-Type"); got != "application/xml" {
				t.Errorf("complete content-type = %q, want application/xml", got)
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `<ETag>"etag-1"</ETag>`) {
				t.Errorf("complete body does not preserve quoted ETag: %s", string(body))
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`<CompleteMultipartUploadResult/>`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	withS3TestClient(t, server)
	restore := withS3MultipartSettings(t, 1, 5*1024*1024)
	defer restore()

	path := seedS3TestFile(t, 6*1024*1024)
	if err := putFileToS3(t.Context(), server.URL, "wp-panel-backups", "auto", "access-key_123", "secret", "wp-panel/site.tar.gz", path); err != nil {
		t.Fatalf("putFileToS3() error = %v", err)
	}

	got := strings.Join(requests, "\n")
	for _, want := range []string{
		"POST /wp-panel-backups/wp-panel/site.tar.gz?uploads=",
		"PUT /wp-panel-backups/wp-panel/site.tar.gz?partNumber=1&uploadId=upload-1",
		"PUT /wp-panel-backups/wp-panel/site.tar.gz?partNumber=2&uploadId=upload-1",
		"POST /wp-panel-backups/wp-panel/site.tar.gz?uploadId=upload-1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("request %q missing; got:\n%s", want, got)
		}
	}
}

func TestPutFileToS3AbortsMultipartOnPartFailure(t *testing.T) {
	var mu sync.Mutex
	requests := []string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.RawQuery == "uploads=":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`<InitiateMultipartUploadResult><UploadId>upload-2</UploadId></InitiateMultipartUploadResult>`))
		case r.Method == http.MethodPut && r.URL.Query().Get("partNumber") == "1":
			w.Header().Set("ETag", `"etag-1"`)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.Query().Get("partNumber") == "2":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("part failed"))
		case r.Method == http.MethodDelete && strings.Contains(r.URL.RawQuery, "uploadId=upload-2"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	withS3TestClient(t, server)
	restore := withS3MultipartSettings(t, 1, 5*1024*1024)
	defer restore()

	path := seedS3TestFile(t, 6*1024*1024)
	if err := putFileToS3(t.Context(), server.URL, "wp-panel-backups", "auto", "access-key_123", "secret", "wp-panel/site.tar.gz", path); err == nil {
		t.Fatal("putFileToS3() error = nil, want error")
	}

	got := strings.Join(requests, "\n")
	if !strings.Contains(got, "DELETE /wp-panel-backups/wp-panel/site.tar.gz?uploadId=upload-2") {
		t.Fatalf("abort request missing; got:\n%s", got)
	}
}

func withS3TestClient(t *testing.T, server *httptest.Server) {
	t.Helper()
	oldClient := http.DefaultClient
	http.DefaultClient = server.Client()
	t.Cleanup(func() {
		http.DefaultClient = oldClient
	})
}

func withS3MultipartSettings(t *testing.T, threshold, partSize int64) func() {
	t.Helper()
	oldThreshold := s3MultipartThreshold
	oldPartSize := s3DefaultPartSize
	s3MultipartThreshold = threshold
	s3DefaultPartSize = partSize
	return func() {
		s3MultipartThreshold = oldThreshold
		s3DefaultPartSize = oldPartSize
	}
}

func seedS3TestFile(t *testing.T, size int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create test file: %v", err)
	}
	defer file.Close()
	if err := file.Truncate(size); err != nil {
		t.Fatalf("truncate test file: %v", err)
	}
	return path
}

// ------------------------------------------------------------------
// 远程全量基线探测（RemoteHasFullFileBackup 及底层 remoteHasFullBackup/s3HasFullBackup）
// ------------------------------------------------------------------

func TestRemoteHasFullFileBackupDisabledReturnsTrue(t *testing.T) {
	openTestDB(t)

	hasFull, err := RemoteHasFullFileBackup("example.com")
	if err != nil {
		t.Fatalf("RemoteHasFullFileBackup error = %v, want nil when remote backup disabled", err)
	}
	if !hasFull {
		t.Fatal("RemoteHasFullFileBackup = false, want true when remote backup disabled (no completeness constraint)")
	}
}

func TestRemoteHasFullFileBackupRsyncMissingHostReturnsError(t *testing.T) {
	openTestDB(t)
	db := database.GetDB()
	if _, err := db.Exec(`UPDATE remote_backup_settings SET enabled = 1, backup_type = 'rsync', host = '' WHERE id = 1`); err != nil {
		t.Fatalf("update remote_backup_settings: %v", err)
	}

	// host 为空时应返回 error（未确认远程状态），调用方据此强制走全量备份，
	// 而不是静默当作"远程已有全量基线"处理。
	hasFull, err := RemoteHasFullFileBackup("example.com")
	if err == nil {
		t.Fatal("RemoteHasFullFileBackup error = nil, want error when remote enabled but host is empty")
	}
	if hasFull {
		t.Fatal("RemoteHasFullFileBackup = true, want false alongside the error")
	}
}

func TestRemoteHasFullFileBackupRejectsInvalidBackupType(t *testing.T) {
	openTestDB(t)
	db := database.GetDB()
	if _, err := db.Exec(`UPDATE remote_backup_settings SET enabled = 1, backup_type = 'ftp', host = 'backup.example.com' WHERE id = 1`); err != nil {
		t.Fatalf("update remote_backup_settings: %v", err)
	}

	// 无效 backup_type 不应静默落入 rsync 分支，应和 SyncBackupToRemote 一样拒绝并返回 error。
	hasFull, err := RemoteHasFullFileBackup("example.com")
	if err == nil {
		t.Fatal("RemoteHasFullFileBackup error = nil, want error for invalid backup_type")
	}
	if hasFull {
		t.Fatal("RemoteHasFullFileBackup = true, want false alongside the error")
	}
}

func TestRemoteHasFullFileBackupS3InvalidSettingsReturnsError(t *testing.T) {
	openTestDB(t)
	db := database.GetDB()
	if _, err := db.Exec(`UPDATE remote_backup_settings SET enabled = 1, backup_type = 's3', s3_endpoint = '', s3_bucket = '' WHERE id = 1`); err != nil {
		t.Fatalf("update remote_backup_settings: %v", err)
	}

	hasFull, err := RemoteHasFullFileBackup("example.com")
	if err == nil {
		t.Fatal("RemoteHasFullFileBackup error = nil, want error for invalid S3 settings")
	}
	if hasFull {
		t.Fatal("RemoteHasFullFileBackup = true, want false alongside the error")
	}
}

func TestS3HasFullBackupParsesKeyCount(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "prefix=wp-panel%2Fexample.com%2Ffiles%2Ffile_full_") {
			t.Errorf("unexpected list query: %s", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<ListBucketResult><KeyCount>1</KeyCount></ListBucketResult>`))
	}))
	defer server.Close()
	withS3TestClient(t, server)

	hasFull, err := s3HasFullBackup(t.Context(), server.URL, "wp-panel-backups", "auto", "access-key_123", "secret", "wp-panel", "example.com")
	if err != nil {
		t.Fatalf("s3HasFullBackup() error = %v", err)
	}
	if !hasFull {
		t.Fatal("s3HasFullBackup() = false, want true when KeyCount > 0")
	}
}

func TestS3HasFullBackupNoResultsReturnsFalse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<ListBucketResult><KeyCount>0</KeyCount></ListBucketResult>`))
	}))
	defer server.Close()
	withS3TestClient(t, server)

	hasFull, err := s3HasFullBackup(t.Context(), server.URL, "wp-panel-backups", "auto", "access-key_123", "secret", "wp-panel", "example.com")
	if err != nil {
		t.Fatalf("s3HasFullBackup() error = %v", err)
	}
	if hasFull {
		t.Fatal("s3HasFullBackup() = true, want false when bucket has no matching full-backup objects")
	}
}

// ------------------------------------------------------------------
// updateBackupTransportStatus 回写 db_backups / file_backups 的 transport_status
// ------------------------------------------------------------------

func insertMinimalWebsite(t *testing.T, domain string) {
	t.Helper()
	mustExec(t, database.GetDB(), `INSERT INTO websites (id, name, domain, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
		VALUES (1, 'site', '`+domain+`', 'u1', '/www/wwwroot/`+domain+`', '/www/wwwlogs/`+domain+`', 'db1', 'u1', '/p', '/n')`)
}

func TestUpdateBackupTransportStatusWritesDBBackups(t *testing.T) {
	openTestDB(t)
	db := database.GetDB()
	insertMinimalWebsite(t, "db.example.com")
	if _, err := db.Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name) VALUES (1, 'backup.sql.gz', 100, 'db1')`); err != nil {
		t.Fatalf("insert db_backups: %v", err)
	}

	updateBackupTransportStatus(BackupSourceDB, 1, "backup.sql.gz", "synced", "")

	var status, message string
	if err := db.QueryRow(`SELECT transport_status, transport_message FROM db_backups WHERE site_id = 1 AND filename = 'backup.sql.gz'`).Scan(&status, &message); err != nil {
		t.Fatalf("query db_backups: %v", err)
	}
	if status != "synced" {
		t.Fatalf("transport_status = %q, want %q", status, "synced")
	}
	if message != "" {
		t.Fatalf("transport_message = %q, want empty", message)
	}
}

func TestUpdateBackupTransportStatusWritesFileBackups(t *testing.T) {
	openTestDB(t)
	db := database.GetDB()
	insertMinimalWebsite(t, "file.example.com")
	if _, err := db.Exec(`INSERT INTO file_backups (site_id, filename, file_size, mode) VALUES (1, 'file_full_20260101_000000.tar.gz', 200, 'full')`); err != nil {
		t.Fatalf("insert file_backups: %v", err)
	}

	updateBackupTransportStatus(BackupSourceFile, 1, "file_full_20260101_000000.tar.gz", "failed", "连接超时")

	var status, message string
	if err := db.QueryRow(`SELECT transport_status, transport_message FROM file_backups WHERE site_id = 1 AND filename = 'file_full_20260101_000000.tar.gz'`).Scan(&status, &message); err != nil {
		t.Fatalf("query file_backups: %v", err)
	}
	if status != "failed" {
		t.Fatalf("transport_status = %q, want %q", status, "failed")
	}
	if message != "连接超时" {
		t.Fatalf("transport_message = %q, want %q", message, "连接超时")
	}
}

func TestUpdateBackupTransportStatusIgnoresZeroSiteID(t *testing.T) {
	openTestDB(t)
	// 不应 panic：siteID 为 0（未落库的记录）时直接跳过写入。
	updateBackupTransportStatus(BackupSourceDB, 0, "whatever.sql.gz", "synced", "")
}

// ------------------------------------------------------------------
// 历史备份远程状态核对（ReconcileBackupTransportStatus 及其辅助函数）
// ------------------------------------------------------------------

func TestParseRemoteFileListStripsRemotePathPrefix(t *testing.T) {
	output := []byte("/home/backup/example.com/db/a.sql.gz\n/home/backup/example.com/files/file_full_x.tar.gz\n\n")
	files := parseRemoteFileList(output, "/home/backup")
	if len(files) != 2 {
		t.Fatalf("files = %+v, want 2 entries", files)
	}
	if !files["example.com/db/a.sql.gz"] || !files["example.com/files/file_full_x.tar.gz"] {
		t.Fatalf("files = %+v, want both relative paths present", files)
	}
}

func TestParseRemoteFileListSkipsLinesNotUnderRemotePath(t *testing.T) {
	output := []byte("/tmp/unrelated.txt\n/home/backup/example.com/db/a.sql.gz\n")
	files := parseRemoteFileList(output, "/home/backup")
	if len(files) != 1 || !files["example.com/db/a.sql.gz"] {
		t.Fatalf("files = %+v, want only the entry under remotePath", files)
	}
}

func TestParseRemoteFileListRejectsSiblingDirectoryWithSamePrefix(t *testing.T) {
	// /home/backup2/x 文本前缀和 /home/backup 相同，但不是它的子目录，不应该被误配进去。
	output := []byte("/home/backup2/x.tar.gz\n/home/backup/example.com/db/a.sql.gz\n")
	files := parseRemoteFileList(output, "/home/backup")
	if len(files) != 1 || !files["example.com/db/a.sql.gz"] {
		t.Fatalf("files = %+v, want only the true subdirectory entry, not the sibling /home/backup2", files)
	}
}

func TestListS3ObjectKeysPaginatesUntilNotTruncated(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.RawQuery)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		if !strings.Contains(r.URL.RawQuery, "continuation-token=") {
			w.Write([]byte(`<ListBucketResult><Contents><Key>wp-panel/a.tar.gz</Key></Contents><IsTruncated>true</IsTruncated><NextContinuationToken>token-1</NextContinuationToken></ListBucketResult>`))
			return
		}
		w.Write([]byte(`<ListBucketResult><Contents><Key>wp-panel/b.tar.gz</Key></Contents><IsTruncated>false</IsTruncated></ListBucketResult>`))
	}))
	defer server.Close()
	withS3TestClient(t, server)

	keys, err := listS3ObjectKeys(t.Context(), server.URL, "wp-panel-backups", "auto", "access-key_123", "secret", "wp-panel")
	if err != nil {
		t.Fatalf("listS3ObjectKeys() error = %v", err)
	}
	if len(keys) != 2 || !keys["wp-panel/a.tar.gz"] || !keys["wp-panel/b.tar.gz"] {
		t.Fatalf("keys = %+v, want both pages' keys", keys)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2 (one per page)", len(requests))
	}
	if !strings.Contains(requests[1], "continuation-token=token-1") {
		t.Fatalf("second request query = %q, want continuation-token=token-1", requests[1])
	}
}

func TestApplyReconciledTransportStatusOnlyUpdatesMatchedLocalRows(t *testing.T) {
	openTestDB(t)
	insertMinimalWebsite(t, "reconcile.example.com")
	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO db_backups (id, site_id, filename, file_size, db_name, transport_status) VALUES (1, 1, 'a.sql.gz', 10, 'db1', 'local')`); err != nil {
		t.Fatalf("insert db_backups a: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO db_backups (id, site_id, filename, file_size, db_name, transport_status) VALUES (2, 1, 'b.sql.gz', 10, 'db1', 'failed')`); err != nil {
		t.Fatalf("insert db_backups b: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO file_backups (id, site_id, filename, file_size, mode, transport_status) VALUES (1, 1, 'file_full_c.tar.gz', 10, 'full', 'local')`); err != nil {
		t.Fatalf("insert file_backups: %v", err)
	}

	pending := []pendingTransportStatusRow{
		{table: "db_backups", id: 1, domain: "reconcile.example.com", filename: "a.sql.gz", subdir: "db"},
		{table: "file_backups", id: 1, domain: "reconcile.example.com", filename: "file_full_c.tar.gz", subdir: "files"},
	}
	remoteKeys := map[string]bool{
		"wp-panel/reconcile.example.com/db/a.sql.gz": true,
		// file_full_c.tar.gz 故意不在远程 key 集合里，模拟"确实还没同步"的情况。
	}

	updated := applyReconciledTransportStatus(pending, remoteKeys, "s3", "wp-panel")
	if updated != 1 {
		t.Fatalf("applyReconciledTransportStatus() = %d, want 1 (only the matched db_backups row)", updated)
	}

	var dbStatus1, dbStatus2, fileStatus string
	if err := db.QueryRow(`SELECT transport_status FROM db_backups WHERE id = 1`).Scan(&dbStatus1); err != nil {
		t.Fatalf("query db_backups id=1: %v", err)
	}
	if err := db.QueryRow(`SELECT transport_status FROM db_backups WHERE id = 2`).Scan(&dbStatus2); err != nil {
		t.Fatalf("query db_backups id=2: %v", err)
	}
	if err := db.QueryRow(`SELECT transport_status FROM file_backups WHERE id = 1`).Scan(&fileStatus); err != nil {
		t.Fatalf("query file_backups id=1: %v", err)
	}
	if dbStatus1 != "synced" {
		t.Fatalf("db_backups id=1 transport_status = %q, want synced (matched remote key)", dbStatus1)
	}
	if dbStatus2 != "failed" {
		t.Fatalf("db_backups id=2 transport_status = %q, want unchanged failed (was never in the pending list)", dbStatus2)
	}
	if fileStatus != "local" {
		t.Fatalf("file_backups id=1 transport_status = %q, want unchanged local (not found in remote key set)", fileStatus)
	}
}

func TestLoadPendingTransportStatusRowsOnlyReturnsLocalRows(t *testing.T) {
	openTestDB(t)
	insertMinimalWebsite(t, "pending.example.com")
	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name, transport_status) VALUES (1, 'a.sql.gz', 10, 'db1', 'local')`); err != nil {
		t.Fatalf("insert db_backups a: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name, transport_status) VALUES (1, 'b.sql.gz', 10, 'db1', 'synced')`); err != nil {
		t.Fatalf("insert db_backups b: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO file_backups (site_id, filename, file_size, mode, transport_status) VALUES (1, 'file_full_c.tar.gz', 10, 'full', 'local')`); err != nil {
		t.Fatalf("insert file_backups: %v", err)
	}

	pending, err := loadPendingTransportStatusRows()
	if err != nil {
		t.Fatalf("loadPendingTransportStatusRows() error = %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %+v, want 2 rows (only transport_status='local')", pending)
	}
	var sawDB, sawFile bool
	for _, p := range pending {
		if p.table == "db_backups" && p.filename == "a.sql.gz" && p.domain == "pending.example.com" && p.subdir == "db" {
			sawDB = true
		}
		if p.table == "file_backups" && p.filename == "file_full_c.tar.gz" && p.domain == "pending.example.com" && p.subdir == "files" {
			sawFile = true
		}
	}
	if !sawDB || !sawFile {
		t.Fatalf("pending = %+v, missing expected rows", pending)
	}
}

func TestReconcileBackupTransportStatusErrorsWhenDisabled(t *testing.T) {
	openTestDB(t)
	// 现在是管理员显式点击"核对远程状态"按钮才会调用，远程备份没启用时应该明确报错，
	// 而不是静默返回成功——按钮式的操作不应该让用户以为"核对过了"却其实什么都没做。
	if _, err := ReconcileBackupTransportStatus(); err == nil {
		t.Fatal("ReconcileBackupTransportStatus() error = nil, want error when remote backup is disabled")
	}
}

func TestReconcileBackupTransportStatusSkipsRemoteCallWhenNothingPending(t *testing.T) {
	openTestDB(t)
	db := database.GetDB()
	if _, err := db.Exec(`UPDATE remote_backup_settings SET enabled = 1, backup_type = 's3', s3_endpoint = 'https://invalid.example.invalid', s3_bucket = 'x', s3_access_key_id = 'k', s3_secret_key = 'secret' WHERE id = 1`); err != nil {
		t.Fatalf("update remote_backup_settings: %v", err)
	}
	// 没有任何 transport_status='local' 的记录时，函数应该在发起任何远程请求之前就直接返回；
	// 如果它真的尝试连了 https://invalid.example.invalid 就会报网络错误，err==nil 证明短路生效。
	updated, err := ReconcileBackupTransportStatus()
	if err != nil {
		t.Fatalf("ReconcileBackupTransportStatus() error = %v, want nil (no remote call should be attempted)", err)
	}
	if updated != 0 {
		t.Fatalf("ReconcileBackupTransportStatus() updated = %d, want 0", updated)
	}
}

func TestLoadPendingTransportStatusRowsReturnsErrorWhenTableMissing(t *testing.T) {
	openTestDB(t)
	db := database.GetDB()
	if _, err := db.Exec(`DROP TABLE db_backups`); err != nil {
		t.Fatalf("drop db_backups: %v", err)
	}
	if _, err := loadPendingTransportStatusRows(); err == nil {
		t.Fatal("loadPendingTransportStatusRows() error = nil, want error when db_backups is missing")
	}
}

func TestApplyReconciledTransportStatusSkipsFailedUpdateWithoutCountingIt(t *testing.T) {
	openTestDB(t)
	insertMinimalWebsite(t, "update-fail.example.com")
	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO db_backups (id, site_id, filename, file_size, db_name, transport_status) VALUES (1, 1, 'a.sql.gz', 10, 'db1', 'local')`); err != nil {
		t.Fatalf("insert db_backups: %v", err)
	}

	pending := []pendingTransportStatusRow{
		// id=999 在 db_backups 里不存在，UPDATE 会成功执行但影响 0 行——这里改用一个真正会
		// 报错的场景：table 名故意写错，触发 db.Exec 报错，验证不会被计入 updated。
		{table: "db_backups", id: 1, domain: "update-fail.example.com", filename: "a.sql.gz", subdir: "db"},
	}
	remoteKeys := map[string]bool{
		"update-fail.example.com/db/a.sql.gz": true,
	}

	// 正常情况：这条应该成功回写并计入 updated=1。
	updated := applyReconciledTransportStatus(pending, remoteKeys, "rsync", "")
	if updated != 1 {
		t.Fatalf("applyReconciledTransportStatus() = %d, want 1", updated)
	}
	var status string
	if err := db.QueryRow(`SELECT transport_status FROM db_backups WHERE id = 1`).Scan(&status); err != nil {
		t.Fatalf("query db_backups: %v", err)
	}
	if status != "synced" {
		t.Fatalf("transport_status = %q, want synced", status)
	}

	// 现在把表删掉，模拟回写失败：不应该 panic，也不应该把失败的记录计入 updated。
	if _, err := db.Exec(`DROP TABLE db_backups`); err != nil {
		t.Fatalf("drop db_backups: %v", err)
	}
	updatedAfterDrop := applyReconciledTransportStatus(pending, remoteKeys, "rsync", "")
	if updatedAfterDrop != 0 {
		t.Fatalf("applyReconciledTransportStatus() after dropping table = %d, want 0 (UPDATE should fail, not be counted)", updatedAfterDrop)
	}
}
