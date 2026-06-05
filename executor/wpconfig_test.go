package executor

import (
	"regexp"
	"strings"
	"testing"
)

func TestEnsureWPConfigCachePrefixesInsertBeforeStopMarker(t *testing.T) {
	content := "<?php\n/* Add any custom values between this line and the \"stop editing\" line. */\n\n/* That's all, stop editing! Happy publishing. */\n"

	updated, inserted := ensureWPConfigCachePrefixes(content, "example.com:")
	if !inserted {
		t.Fatal("expected cache prefixes to be inserted")
	}
	if !strings.Contains(updated, "define('WP_REDIS_PREFIX', 'example.com:');") {
		t.Fatalf("redis prefix missing from updated config:\n%s", updated)
	}
	if !strings.Contains(updated, "define('WP_CACHE_KEY_SALT', 'example.com:');") {
		t.Fatalf("cache key salt missing from updated config:\n%s", updated)
	}
	if strings.Index(updated, "WP_CACHE_KEY_SALT") > strings.Index(updated, "That's all, stop editing") {
		t.Fatalf("cache key salt should be before the stop editing marker:\n%s", updated)
	}
}

func TestEnsureWPConfigCachePrefixesKeepExistingValues(t *testing.T) {
	content := "<?php\ndefine('WP_REDIS_PREFIX', 'redis.example:');\ndefine('WP_CACHE_KEY_SALT', 'old.example:');\n/* That's all, stop editing! Happy publishing. */\n"

	updated, inserted := ensureWPConfigCachePrefixes(content, "new.example:")
	if inserted {
		t.Fatal("expected existing cache prefixes to be kept")
	}
	if !strings.Contains(updated, "redis.example:") {
		t.Fatalf("old redis prefix was not kept:\n%s", updated)
	}
	if !strings.Contains(updated, "old.example:") {
		t.Fatalf("old cache key salt was not kept:\n%s", updated)
	}
	if got := strings.Count(updated, "WP_REDIS_PREFIX"); got != 1 {
		t.Fatalf("expected one redis prefix definition, got %d:\n%s", got, updated)
	}
	if got := strings.Count(updated, "WP_CACHE_KEY_SALT"); got != 1 {
		t.Fatalf("expected one cache key salt definition, got %d:\n%s", got, updated)
	}
}

func TestGenerateWPTablePrefix(t *testing.T) {
	re := regexp.MustCompile(`^wp_[a-f0-9]{8}_$`)
	first := generateWPTablePrefix()
	second := generateWPTablePrefix()

	if !re.MatchString(first) {
		t.Fatalf("unexpected table prefix format: %q", first)
	}
	if first == second {
		t.Fatalf("expected random table prefixes, got %q twice", first)
	}
}
