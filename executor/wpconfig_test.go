package executor

import (
	"fmt"
	"os"
	"path/filepath"
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

func TestExtractWPTablePrefix(t *testing.T) {
	prefix, ok := extractWPTablePrefix("<?php\n$table_prefix = 'wp_ab12cd34_';\n")
	if !ok {
		t.Fatal("expected table prefix to be detected")
	}
	if prefix != "wp_ab12cd34_" {
		t.Fatalf("unexpected table prefix: %q", prefix)
	}
}

func TestExtractWPTablePrefixWithoutTrailingUnderscore(t *testing.T) {
	prefix, ok := extractWPTablePrefix("<?php\n$table_prefix = 'wp_sadfasdfasf';\n")
	if !ok {
		t.Fatal("expected table prefix to be detected")
	}
	if prefix != "wp_sadfasdfasf" {
		t.Fatalf("unexpected table prefix: %q", prefix)
	}
	if !IsValidWPTablePrefix(prefix) {
		t.Fatalf("expected prefix %q to be valid", prefix)
	}
}

func TestFixWPConfigCredentialsKeepsTablePrefixVariableName(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "wp-config.php")
	content := "<?php\ndefine('DB_NAME', 'old_db');\ndefine('DB_USER', 'old_user');\n$table_prefix = 'wp_sadfasdfasf';\n/* That's all, stop editing! Happy publishing. */\n"
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	if err := FixWPConfigCredentials(dir, "example.com", "new_db", "new_user", "wp_"); err != nil {
		t.Fatal(err)
	}

	updatedBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(updatedBytes)
	if !strings.Contains(updated, "$table_prefix = 'wp_';") {
		t.Fatalf("table prefix variable was not preserved:\n%s", updated)
	}
	if strings.Contains(updated, "\n = 'wp_';") {
		t.Fatalf("table prefix variable name was dropped:\n%s", updated)
	}
}

func TestFixWPConfigCredentialsRepairsDroppedTablePrefixVariableName(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "wp-config.php")
	content := "<?php\ndefine('DB_NAME', 'old_db');\ndefine('DB_USER', 'old_user');\n/**\n * WordPress database table prefix.\n */\n = 'wp_sadfasdfasf';\n/* That's all, stop editing! Happy publishing. */\n"
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	if err := FixWPConfigCredentials(dir, "example.com", "new_db", "new_user", "wp_"); err != nil {
		t.Fatal(err)
	}

	updatedBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(updatedBytes)
	if !strings.Contains(updated, "$table_prefix = 'wp_';") {
		t.Fatalf("damaged table prefix assignment was not repaired:\n%s", updated)
	}
}

func TestFixWPConfigCredentialsRejectsSymlinkAndPreservesTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.php")
	original := "<?php\ndefine('DB_NAME', 'old');\ndefine('DB_USER', 'old');\n$table_prefix = 'wp_';\n"
	if err := os.WriteFile(target, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "wp-config.php")); err != nil {
		t.Fatal(err)
	}
	if err := FixWPConfigCredentials(dir, "example.com", "new", "new", "wp_"); err == nil {
		t.Fatal("expected symlink rejection")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != original {
		t.Fatalf("outside target changed: %q err=%v", got, err)
	}
}

func TestFixWPConfigCredentialsLintFailurePreservesOriginal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wp-config.php")
	original := "<?php\ndefine('DB_NAME', 'old');\ndefine('DB_USER', 'old');\n$table_prefix = 'wp_';\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	oldLint := lintManagedPHPFile
	lintManagedPHPFile = func(string) error { return fmt.Errorf("invalid php") }
	t.Cleanup(func() { lintManagedPHPFile = oldLint })
	if err := FixWPConfigCredentials(dir, "example.com", "new", "new", "wp_"); err == nil {
		t.Fatal("expected lint failure")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != original {
		t.Fatalf("original changed: %q err=%v", got, err)
	}
}
