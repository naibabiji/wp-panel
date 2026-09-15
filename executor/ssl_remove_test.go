package executor

import (
	"errors"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/models"
)

func withSSLRemoveStubs(t *testing.T) {
	t.Helper()
	oldApply, oldPersist, oldRestore, oldRemove := applyHTTPNginxConfig, persistSSLDisabled, restoreSSLState, removeSSLCertDir
	t.Cleanup(func() {
		applyHTTPNginxConfig, persistSSLDisabled = oldApply, oldPersist
		restoreSSLState, removeSSLCertDir = oldRestore, oldRemove
	})
}

func TestRemoveSSLCertificateStopsBeforeNginxWhenDatabaseSaveFails(t *testing.T) {
	withSSLRemoveStubs(t)
	nginxCalled, removeCalled := false, false
	persistSSLDisabled = func(int) error { return errors.New("database failed") }
	applyHTTPNginxConfig = func(*models.Website) error { nginxCalled = true; return nil }
	removeSSLCertDir = func(string) error { removeCalled = true; return nil }

	result := removeSSLCertificate(&models.Website{ID: 1}, "/certs/example.com")
	if result.Success || nginxCalled || removeCalled {
		t.Fatalf("result=%+v nginxCalled=%v removeCalled=%v", result, nginxCalled, removeCalled)
	}
}

func TestRemoveSSLCertificateRestoresDatabaseWhenNginxFails(t *testing.T) {
	withSSLRemoveStubs(t)
	restored, removeCalled := false, false
	persistSSLDisabled = func(int) error { return nil }
	applyHTTPNginxConfig = func(*models.Website) error { return errors.New("nginx failed") }
	restoreSSLState = func(*models.Website) error { restored = true; return nil }
	removeSSLCertDir = func(string) error { removeCalled = true; return nil }

	result := removeSSLCertificate(&models.Website{ID: 1, SSLEnabled: true}, "/certs/example.com")
	if result.Success || !restored || removeCalled {
		t.Fatalf("result=%+v restored=%v removeCalled=%v", result, restored, removeCalled)
	}
}

func TestRemoveSSLCertificateReportsDatabaseRestoreFailure(t *testing.T) {
	withSSLRemoveStubs(t)
	persistSSLDisabled = func(int) error { return nil }
	applyHTTPNginxConfig = func(*models.Website) error { return errors.New("nginx failed") }
	restoreSSLState = func(*models.Website) error { return errors.New("restore failed") }

	result := removeSSLCertificate(&models.Website{ID: 1}, "/certs/example.com")
	if result.Success || !strings.Contains(result.Message, "原 SSL 状态恢复失败") {
		t.Fatalf("result=%+v", result)
	}
}

func TestRemoveSSLCertificateReportsCleanupFailureAfterHTTPCommit(t *testing.T) {
	withSSLRemoveStubs(t)
	persistSSLDisabled = func(int) error { return nil }
	applyHTTPNginxConfig = func(*models.Website) error { return nil }
	removeSSLCertDir = func(string) error { return errors.New("remove failed") }

	result := removeSSLCertificate(&models.Website{ID: 1}, "/certs/example.com")
	if result.Success || !strings.Contains(result.Message, "SSL 已关闭并恢复为 HTTP") {
		t.Fatalf("result=%+v", result)
	}
}

func TestRemoveSSLCertificateSucceedsInSafeOrder(t *testing.T) {
	withSSLRemoveStubs(t)
	steps := make([]string, 0, 3)
	persistSSLDisabled = func(int) error { steps = append(steps, "database"); return nil }
	applyHTTPNginxConfig = func(*models.Website) error { steps = append(steps, "nginx"); return nil }
	removeSSLCertDir = func(string) error { steps = append(steps, "certificate"); return nil }

	result := removeSSLCertificate(&models.Website{ID: 1, Domain: "example.com"}, "/certs/example.com")
	if !result.Success || strings.Join(steps, ",") != "database,nginx,certificate" {
		t.Fatalf("result=%+v steps=%v", result, steps)
	}
}
