package handlers

import (
	"strings"
	"testing"
)

func TestSMTPTransferRoundTrip(t *testing.T) {
	want := smtpTransferConfig{
		Host: "smtp.example.com", Port: "587", Encryption: "starttls",
		User: "alerts@example.com", Pass: "secret-token", AdminEmail: "admin@example.com",
	}
	code, err := encodeSMTPTransfer(want)
	if err != nil {
		t.Fatalf("encodeSMTPTransfer() error = %v", err)
	}
	if !strings.HasPrefix(code, smtpTransferPrefix) || strings.Contains(code, want.Pass) {
		t.Fatalf("encoded code has unexpected format: %q", code)
	}
	got, err := decodeSMTPTransfer(code)
	if err != nil {
		t.Fatalf("decodeSMTPTransfer() error = %v", err)
	}
	if got != want {
		t.Fatalf("decoded config = %#v, want %#v", got, want)
	}
	if err := validateSMTPTransfer(got); err != nil {
		t.Fatalf("validateSMTPTransfer() error = %v", err)
	}
}

func TestSMTPTransferRejectsTamperingAndUnsupportedVersion(t *testing.T) {
	code, err := encodeSMTPTransfer(smtpTransferConfig{
		Host: "smtp.example.com", Port: "465", Encryption: "ssl", User: "user", Pass: "pass",
	})
	if err != nil {
		t.Fatalf("encodeSMTPTransfer() error = %v", err)
	}
	tamperAt := len(smtpTransferPrefix) + 5
	replacement := byte('A')
	if code[tamperAt] == replacement {
		replacement = 'B'
	}
	tampered := code[:tamperAt] + string(replacement) + code[tamperAt+1:]
	if _, err := decodeSMTPTransfer(tampered); err == nil {
		t.Fatal("decodeSMTPTransfer() accepted tampered code")
	}
	if _, err := decodeSMTPTransfer(strings.Replace(code, "V1", "V2", 1)); err == nil {
		t.Fatal("decodeSMTPTransfer() accepted unsupported version")
	}
}

func TestValidateSMTPTransferRejectsInvalidFields(t *testing.T) {
	tests := []smtpTransferConfig{
		{Port: "587", Encryption: "starttls"},
		{Host: "smtp.example.com", Port: "70000", Encryption: "starttls"},
		{Host: "smtp.example.com", Port: "587", Encryption: "invalid"},
		{Host: "smtp.example.com", Port: "587", Encryption: "starttls", AdminEmail: "not-an-email"},
	}
	for _, cfg := range tests {
		if err := validateSMTPTransfer(cfg); err == nil {
			t.Fatalf("validateSMTPTransfer(%#v) error = nil", cfg)
		}
	}
}
