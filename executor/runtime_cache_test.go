package executor

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRedisObjectCachePrefixesIncludeWPConfigConstants(t *testing.T) {
	webRoot := t.TempDir()
	content := `<?php
define('WP_REDIS_PREFIX', 'vps17.top:');
define('WP_CACHE_KEY_SALT', 'cache-vps17:');
`
	if err := os.WriteFile(filepath.Join(webRoot, "wp-config.php"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	got := redisObjectCachePrefixes("1.vps17.top", webRoot)
	want := []string{"vps17.top:", "cache-vps17:", "1.vps17.top:"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prefixes = %#v, want %#v", got, want)
	}
}

func TestRedisObjectCachePrefixesDeduplicateDefaults(t *testing.T) {
	webRoot := t.TempDir()
	content := `<?php
define("WP_REDIS_PREFIX", "1.vps17.top:");
define('WP_CACHE_KEY_SALT', '1.vps17.top:');
`
	if err := os.WriteFile(filepath.Join(webRoot, "wp-config.php"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	got := redisObjectCachePrefixes("1.vps17.top", webRoot)
	want := []string{"1.vps17.top:"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prefixes = %#v, want %#v", got, want)
	}
}
