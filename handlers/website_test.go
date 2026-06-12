package handlers

import (
	"errors"
	"testing"
)

func TestNormalizeWPSiteURL(t *testing.T) {
	got, err := normalizeWPSiteURL(" https://example.com/wp ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.com/wp" {
		t.Fatalf("normalizeWPSiteURL trimmed to %q", got)
	}

	if got, err := normalizeWPSiteURL(""); err != nil || got != "" {
		t.Fatalf("empty URL = %q, %v; want empty without error", got, err)
	}
}

func TestNormalizeWPSiteURLRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"example.com", "ftp://example.com", "https://"} {
		if _, err := normalizeWPSiteURL(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestReinstallWordPressErrorMessageShowsSafeStage(t *testing.T) {
	msg := reinstallWordPressErrorMessage(errors.New("重建数据库失败: mysql: Access denied for /www/server/panel/config.json"))
	if msg != "WordPress 重装失败：重建数据库失败" {
		t.Fatalf("message = %q", msg)
	}
}

func TestReinstallWordPressErrorMessageHidesUnknownDetails(t *testing.T) {
	msg := reinstallWordPressErrorMessage(errors.New("mysql: Access denied for /www/server/panel/config.json"))
	if msg != "WordPress 重装失败" {
		t.Fatalf("message = %q", msg)
	}
}
