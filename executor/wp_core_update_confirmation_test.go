package executor

import (
	"testing"
	"time"
)

func TestWPCoreConfirmationIsSingleUseAndBound(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := newWPCoreConfirmationStore()
	store.now = func() time.Time { return now }
	record, err := store.create(wpCoreConfirmation{username: "admin", siteID: 7, collectionID: "collection-a", currentVersion: "7.0.1", targetVersion: "7.0.2", locale: "zh_CN", downloadURL: "https://downloads.wordpress.org/release/wordpress-7.0.2.zip"})
	if err != nil || len(record.token) != 64 {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	if _, err := store.consume(record.token, "other", 7, "7.0.2"); err == nil {
		t.Fatal("accepted another session")
	}
	consumed, err := store.consume(record.token, "admin", 7, "7.0.2")
	if err != nil || consumed.collectionID != "collection-a" {
		t.Fatalf("consumed=%+v err=%v", consumed, err)
	}
	if _, err := store.consume(record.token, "admin", 7, "7.0.2"); err == nil {
		t.Fatal("accepted replay")
	}
}

func TestWPCoreConfirmationExpiresAndReplacesSiteToken(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := newWPCoreConfirmationStore()
	store.now = func() time.Time { return now }
	base := wpCoreConfirmation{username: "admin", siteID: 7, collectionID: "collection-a", currentVersion: "7.0.1", targetVersion: "7.0.2", locale: "zh_CN", downloadURL: "https://downloads.wordpress.org/release/wordpress-7.0.2.zip"}
	first, _ := store.create(base)
	second, _ := store.create(base)
	if _, err := store.consume(first.token, "admin", 7, "7.0.2"); err == nil {
		t.Fatal("replaced token remains usable")
	}
	now = now.Add(wpCoreConfirmationTTL + time.Second)
	if _, err := store.consume(second.token, "admin", 7, "7.0.2"); err == nil {
		t.Fatal("expired token remains usable")
	}
}
