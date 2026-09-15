package executor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

func withSSLPublishStubs(t *testing.T) {
	t.Helper()
	oldApply, oldPersist, oldRestoreState, oldRestoreDir := applySSLNginxConfig, persistSSLState, restoreSSLState, restoreSSLCertDir
	t.Cleanup(func() {
		applySSLNginxConfig, persistSSLState = oldApply, oldPersist
		restoreSSLState, restoreSSLCertDir = oldRestoreState, oldRestoreDir
	})
}

func sslPublishDirs(t *testing.T, withOld bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	certDir := filepath.Join(root, "example.com")
	stageDir := filepath.Join(root, "example.com.pending")
	if err := os.MkdirAll(stageDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "fullchain.pem"), []byte("new-cert"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "privkey.pem"), []byte("new-key"), 0600); err != nil {
		t.Fatal(err)
	}
	if withOld {
		if err := os.MkdirAll(certDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(certDir, "fullchain.pem"), []byte("old-cert"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return certDir, stageDir
}

func readPublishedCert(t *testing.T, certDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(certDir, "fullchain.pem"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPublishSSLCertificateDoesNotTouchNginxWhenDatabaseSaveFails(t *testing.T) {
	withSSLPublishStubs(t)
	certDir, stageDir := sslPublishDirs(t, true)
	nginxCalled := false
	persistSSLState = func(int, string, string, time.Time, string) error { return errors.New("database failed") }
	applySSLNginxConfig = func(*models.Website, string, string) error {
		nginxCalled = true
		return nil
	}

	err := publishSSLCertificate(&models.Website{ID: 1}, certDir, stageDir,
		filepath.Join(certDir, "fullchain.pem"), filepath.Join(certDir, "privkey.pem"), time.Now(), "auto")
	if err == nil || nginxCalled {
		t.Fatalf("err=%v nginxCalled=%v", err, nginxCalled)
	}
	if got := readPublishedCert(t, certDir); got != "old-cert" {
		t.Fatalf("certificate=%q, want old certificate", got)
	}
}

func TestPublishSSLCertificateRemovesNewCertificateWhenFirstEnableDatabaseSaveFails(t *testing.T) {
	withSSLPublishStubs(t)
	certDir, stageDir := sslPublishDirs(t, false)
	nginxCalled := false
	persistSSLState = func(int, string, string, time.Time, string) error { return errors.New("database failed") }
	applySSLNginxConfig = func(*models.Website, string, string) error {
		nginxCalled = true
		return nil
	}

	err := publishSSLCertificate(&models.Website{ID: 1}, certDir, stageDir,
		filepath.Join(certDir, "fullchain.pem"), filepath.Join(certDir, "privkey.pem"), time.Now(), "auto")
	if err == nil || nginxCalled {
		t.Fatalf("err=%v nginxCalled=%v", err, nginxCalled)
	}
	if _, statErr := os.Stat(certDir); !os.IsNotExist(statErr) {
		t.Fatalf("certificate directory still exists after rollback: %v", statErr)
	}
}

func TestPublishSSLCertificateRestoresDatabaseAndCertificateWhenNginxFails(t *testing.T) {
	withSSLPublishStubs(t)
	certDir, stageDir := sslPublishDirs(t, true)
	persistSSLState = func(int, string, string, time.Time, string) error { return nil }
	applySSLNginxConfig = func(*models.Website, string, string) error { return errors.New("nginx failed") }
	databaseRestored := false
	restoreSSLState = func(*models.Website) error {
		databaseRestored = true
		return nil
	}

	err := publishSSLCertificate(&models.Website{ID: 1, SSLEnabled: true}, certDir, stageDir,
		filepath.Join(certDir, "fullchain.pem"), filepath.Join(certDir, "privkey.pem"), time.Now(), "auto")
	if err == nil || !databaseRestored {
		t.Fatalf("err=%v databaseRestored=%v", err, databaseRestored)
	}
	if got := readPublishedCert(t, certDir); got != "old-cert" {
		t.Fatalf("certificate=%q, want old certificate", got)
	}
}

func TestPublishSSLCertificateCommitsNewCertificate(t *testing.T) {
	withSSLPublishStubs(t)
	certDir, stageDir := sslPublishDirs(t, true)
	persistSSLState = func(int, string, string, time.Time, string) error { return nil }
	applySSLNginxConfig = func(*models.Website, string, string) error { return nil }

	if err := publishSSLCertificate(&models.Website{ID: 1}, certDir, stageDir,
		filepath.Join(certDir, "fullchain.pem"), filepath.Join(certDir, "privkey.pem"), time.Now(), "auto"); err != nil {
		t.Fatal(err)
	}
	if got := readPublishedCert(t, certDir); got != "new-cert" {
		t.Fatalf("certificate=%q, want new certificate", got)
	}
	matches, err := filepath.Glob(certDir + ".previous-*")
	if err != nil || len(matches) != 0 {
		t.Fatalf("old certificate backups=%v err=%v", matches, err)
	}
}

func TestPublishSSLCertificateReportsRecoveryFailure(t *testing.T) {
	withSSLPublishStubs(t)
	certDir, stageDir := sslPublishDirs(t, false)
	persistSSLState = func(int, string, string, time.Time, string) error { return errors.New("database failed") }
	restoreSSLCertDir = func(string, string, bool) error { return errors.New("restore failed") }

	err := publishSSLCertificate(&models.Website{ID: 1}, certDir, stageDir,
		filepath.Join(certDir, "fullchain.pem"), filepath.Join(certDir, "privkey.pem"), time.Now(), "auto")
	if err == nil || !strings.Contains(err.Error(), "恢复旧证书也失败") {
		t.Fatalf("err=%v", err)
	}
}
