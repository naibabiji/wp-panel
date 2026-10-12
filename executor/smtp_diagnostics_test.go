package executor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/naibabiji/wp-panel/database"
)

func setupSMTPDiagnosticTestDB(t *testing.T) {
	t.Helper()
	_ = database.Close()
	if err := database.Open(filepath.Join(t.TempDir(), "smtp.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.GetDB().Exec(`CREATE TABLE security_settings (skey TEXT PRIMARY KEY, svalue TEXT)`); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"smtp_host": "smtp.example.test", "smtp_port": "587", "smtp_encryption": "starttls",
		"smtp_user": "sender@example.test", "smtp_pass": "secret-value", "admin_email": "admin@example.test",
	}
	for key, value := range values {
		if _, err := database.GetDB().Exec(`INSERT INTO security_settings(skey,svalue) VALUES(?,?)`, key, value); err != nil {
			t.Fatal(err)
		}
	}
}

func installSMTPDiagnosticHooks(t *testing.T, dial func(context.Context, string, string) (net.Conn, error), session func(context.Context, *SMTPConfig, string, string, string) error) {
	t.Helper()
	oldLookup, oldDial, oldSession := smtpLookupIPAddr, smtpDialContext, smtpRunDiagnosticSession
	smtpLookupIPAddr = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}, {IP: net.ParseIP("2001:db8::10")}}, nil
	}
	smtpDialContext = dial
	smtpRunDiagnosticSession = session
	t.Cleanup(func() {
		smtpLookupIPAddr, smtpDialContext, smtpRunDiagnosticSession = oldLookup, oldDial, oldSession
	})
}

func successfulTestConn() net.Conn {
	client, server := net.Pipe()
	go server.Close()
	return client
}

func TestDiagnoseSMTPSuccessReportsAllStagesWithoutSecrets(t *testing.T) {
	setupSMTPDiagnosticTestDB(t)
	installSMTPDiagnosticHooks(t,
		func(context.Context, string, string) (net.Conn, error) { return successfulTestConn(), nil },
		func(_ context.Context, cfg *SMTPConfig, to, network, ip string) error {
			if cfg.Pass != "secret-value" || to != "admin@example.test" || network != "tcp4" || ip != "192.0.2.10" {
				t.Fatalf("unexpected session input: cfg=%+v to=%q network=%q ip=%q", cfg, to, network, ip)
			}
			return nil
		},
	)
	result, err := DiagnoseSMTP(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.ConclusionCode != "success" || len(result.Stages) != 6 {
		t.Fatalf("unexpected result: %+v", result)
	}
	for _, stage := range result.Stages {
		if strings.Contains(stage.Detail, "secret-value") {
			t.Fatal("diagnostic stage leaked SMTP secret")
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-value") {
		t.Fatalf("serialized diagnostic leaked SMTP secret: %s", encoded)
	}
}

func TestDiagnoseSMTPClassifiesLikelyUpstreamFilterAndAlternativePort(t *testing.T) {
	setupSMTPDiagnosticTestDB(t)
	timeoutErr := &net.DNSError{IsTimeout: true, Err: "timeout"}
	installSMTPDiagnosticHooks(t,
		func(_ context.Context, _ string, address string) (net.Conn, error) {
			_, port, _ := net.SplitHostPort(address)
			if port == "443" || port == "2525" {
				return successfulTestConn(), nil
			}
			return nil, timeoutErr
		},
		func(context.Context, *SMTPConfig, string, string, string) error {
			t.Fatal("SMTP session must not run when TCP is unreachable")
			return nil
		},
	)
	result, err := DiagnoseSMTP(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || result.ConclusionCode != "possible_upstream_filter" || !result.Alternative2525 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDiagnoseSMTPPreservesCompletedStagesOnAuthenticationFailure(t *testing.T) {
	setupSMTPDiagnosticTestDB(t)
	installSMTPDiagnosticHooks(t,
		func(context.Context, string, string) (net.Conn, error) { return successfulTestConn(), nil },
		func(context.Context, *SMTPConfig, string, string, string) error {
			return &smtpDiagnosticError{phase: "authentication", code: "auth_failed", err: errors.New("535 rejected")}
		},
	)
	result, err := DiagnoseSMTP(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.ConclusionCode != "auth_failed" {
		t.Fatalf("conclusion=%q", result.ConclusionCode)
	}
	last := result.Stages[len(result.Stages)-2:]
	if last[0].Name != "tls" || last[0].Status != "passed" || last[1].Name != "authentication" || last[1].Status != "failed" {
		t.Fatalf("unexpected final stages: %+v", last)
	}
}

func TestDiagnoseSMTPDNSFailureDoesNotDial(t *testing.T) {
	setupSMTPDiagnosticTestDB(t)
	oldLookup, oldDial := smtpLookupIPAddr, smtpDialContext
	smtpLookupIPAddr = func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("lookup failed") }
	smtpDialContext = func(context.Context, string, string) (net.Conn, error) { t.Fatal("dial must not run"); return nil, nil }
	t.Cleanup(func() { smtpLookupIPAddr, smtpDialContext = oldLookup, oldDial })
	result, err := DiagnoseSMTP(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.ConclusionCode != "dns_failed" || len(result.Stages) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDiagnoseSMTPStillChecksTimedOutIPv4WhenIPv6HasNoRoute(t *testing.T) {
	setupSMTPDiagnosticTestDB(t)
	timeoutErr := &net.DNSError{IsTimeout: true, Err: "timeout"}
	installSMTPDiagnosticHooks(t,
		func(_ context.Context, network, address string) (net.Conn, error) {
			_, port, _ := net.SplitHostPort(address)
			if port == "443" || port == "2525" {
				if network == "tcp4" {
					return successfulTestConn(), nil
				}
				return nil, syscall.ENETUNREACH
			}
			if network == "tcp4" {
				return nil, timeoutErr
			}
			return nil, syscall.ENETUNREACH
		},
		func(context.Context, *SMTPConfig, string, string, string) error {
			t.Fatal("SMTP session must not run")
			return nil
		},
	)
	result, err := DiagnoseSMTP(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.ConclusionCode != "possible_upstream_filter" || !result.Alternative2525 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestClassifyConnectionError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"refused", syscall.ECONNREFUSED, "refused"},
		{"unreachable", &net.OpError{Op: "dial", Net: "tcp4", Err: syscall.ENETUNREACH}, "unreachable"},
		{"timeout", &net.DNSError{IsTimeout: true, Err: "timeout"}, "timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyConnectionError(tt.err); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
