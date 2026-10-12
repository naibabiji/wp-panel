package executor

import (
	"context"
	"encoding/base64"
	"errors"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func TestManagedMailSenderBuildsControlledMIMEAndIsIdempotent(t *testing.T) {
	policyService, siteID := setupManagedMailPolicyTest(t)
	authorizeManagedMailSite(t, policyService, siteID, 10, 20, 30)
	sender := NewManagedMailSender(policyService)
	dispatches := 0
	var captured managedMailEnvelope
	sender.dispatch = func(_ context.Context, cfg *SMTPConfig, envelope managedMailEnvelope) managedMailDispatchResult {
		dispatches++
		captured = envelope
		if cfg.User != "api-user" || cfg.FromEmail != "noreply@example.test" {
			t.Fatalf("unexpected SMTP config: %+v", cfg)
		}
		return managedMailDispatchResult{status: "accepted"}
	}
	message := ManagedMailMessage{
		RequestID: "sender-idempotent-0001", FromName: "Untrusted Override",
		To: []string{"User <user@example.test>", "duplicate@example.test"},
		CC: []string{"Duplicate <duplicate@example.test>"}, BCC: []string{"hidden@example.test"},
		ReplyTo: []string{"reply@example.test"}, Subject: "Password reset 测试",
		TextBody: "plain body", HTMLBody: "<p>html body</p>", Headers: map[string]string{"X-Trace-ID": "trace-1"},
		Attachments: []ManagedMailAttachment{{Name: "../report.txt", ContentType: "text/plain", Content: base64.StdEncoding.EncodeToString([]byte("attachment"))}},
	}
	first, err := sender.Send(context.Background(), siteID, message)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "accepted" || first.Existing || dispatches != 1 || first.RecipientCount != 3 {
		t.Fatalf("unexpected first result=%+v dispatches=%d", first, dispatches)
	}
	raw := string(captured.message)
	for _, required := range []string{"From: \"Example Site\" <noreply@example.test>", "Reply-To: <reply@example.test>", "X-Trace-Id: trace-1", "multipart/mixed", "multipart/alternative", "report.txt", "Message-ID:"} {
		if !strings.Contains(raw, required) {
			t.Fatalf("MIME missing %q:\n%s", required, raw)
		}
	}
	if strings.Contains(raw, "Bcc:") || strings.Contains(raw, "api-user") || strings.Contains(raw, "Untrusted Override") || strings.Contains(raw, "../report.txt") {
		t.Fatalf("MIME leaked Bcc/auth user or bypassed locked From Name:\n%s", raw)
	}
	second, err := sender.Send(context.Background(), siteID, message)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Existing || second.Status != "accepted" || dispatches != 1 || second.MessageID != first.MessageID {
		t.Fatalf("idempotent result=%+v dispatches=%d first=%+v", second, dispatches, first)
	}
}

func TestManagedMailSenderRecordsUnknownWithoutRetry(t *testing.T) {
	policyService, siteID := setupManagedMailPolicyTest(t)
	authorizeManagedMailSite(t, policyService, siteID, 3, 15, 50)
	sender := NewManagedMailSender(policyService)
	dispatches := 0
	sender.dispatch = func(context.Context, *SMTPConfig, managedMailEnvelope) managedMailDispatchResult {
		dispatches++
		return managedMailDispatchResult{status: "unknown", errorCode: "data_result_unknown", smtpCode: 451}
	}
	message := ManagedMailMessage{RequestID: "sender-unknown-000001", To: []string{"user@example.test"}, Subject: "Test", TextBody: "Body"}
	result, err := sender.Send(context.Background(), siteID, message)
	if err != nil || result.Status != "unknown" || dispatches != 1 {
		t.Fatalf("result=%+v dispatches=%d err=%v", result, dispatches, err)
	}
	replayed, err := sender.Send(context.Background(), siteID, message)
	if err != nil || replayed.Status != "unknown" || !replayed.Existing || dispatches != 1 {
		t.Fatalf("replayed=%+v dispatches=%d err=%v", replayed, dispatches, err)
	}
	if _, err := policyService.Reserve(context.Background(), siteID, "sender-after-unknown-02", 3, 10, 0); !errors.Is(err, ErrManagedMailQuota) {
		t.Fatalf("unknown result must consume quota: %v", err)
	}
}

func TestManagedMailEnvelopeRejectsHeaderOverridesAndBadAttachments(t *testing.T) {
	policy := ManagedMailSitePolicy{FromName: "Site"}
	cfg := &SMTPConfig{FromEmail: "noreply@example.test"}
	base := ManagedMailMessage{RequestID: "validation-request-001", To: []string{"user@example.test"}, Subject: "Test", TextBody: "Body"}
	base.Headers = map[string]string{"From": "attacker@example.test"}
	if _, _, err := buildManagedMailEnvelope(policy, cfg, base, time.Now()); !errors.Is(err, ErrManagedMailValidation) {
		t.Fatalf("reserved header err=%v", err)
	}
	base.Headers = nil
	base.Attachments = []ManagedMailAttachment{{Name: "file.txt", Content: "not-base64"}}
	if _, _, err := buildManagedMailEnvelope(policy, cfg, base, time.Now()); !errors.Is(err, ErrManagedMailValidation) {
		t.Fatalf("bad attachment err=%v", err)
	}
}

func TestManagedMailSMTPErrorRecognizesServerWideTemporaryRefusal(t *testing.T) {
	result := classifyManagedMailSMTPError(&textproto.Error{Code: 421, Msg: "service temporarily unavailable"}, false)
	if result.status != "temporary_failure" || result.errorCode != "provider_rate_limited" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestManagedMailSMTPErrorClassifiesAuthAndDataResults(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		data      bool
		status    string
		errorCode string
	}{
		{"data rejected", &textproto.Error{Code: 550, Msg: "rejected"}, true, "rejected", "smtp_rejected"},
		{"data temporary", &textproto.Error{Code: 451, Msg: "try later"}, true, "temporary_failure", "smtp_temporary_failure"},
		{"data unknown", errors.New("connection closed"), true, "unknown", "data_result_unknown"},
		{"auth network", errors.New("timeout"), false, "temporary_failure", "smtp_temporary_failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyManagedMailSMTPError(tc.err, tc.data)
			if got.status != tc.status || got.errorCode != tc.errorCode {
				t.Fatalf("result=%+v", got)
			}
		})
	}
	for _, code := range []int{530, 534, 535} {
		if !isManagedMailAuthenticationFailure(code) {
			t.Fatalf("code %d should be an authentication failure", code)
		}
	}
	for _, code := range []int{0, 421, 454, 500} {
		if isManagedMailAuthenticationFailure(code) {
			t.Fatalf("code %d should not be an authentication failure", code)
		}
	}
}
