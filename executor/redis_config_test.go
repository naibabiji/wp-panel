package executor

import (
	"strings"
	"testing"
)

func TestBuildRedisBaselineConfig(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantChanged bool
		wantMemory  string
		wantPolicy  string
	}{
		{
			name:        "stock commented defaults",
			content:     "# maxmemory <bytes>\n# maxmemory-policy noeviction\n",
			wantChanged: true,
			wantMemory:  "128mb",
			wantPolicy:  "allkeys-lru",
		},
		{
			name:        "existing panel maxmemory gets policy",
			content:     "maxmemory 128mb\n# maxmemory-policy noeviction\n",
			wantChanged: true,
			wantMemory:  "128mb",
			wantPolicy:  "allkeys-lru",
		},
		{
			name:        "explicit noeviction is preserved",
			content:     "maxmemory 256mb\nmaxmemory-policy noeviction\n",
			wantChanged: false,
			wantMemory:  "256mb",
			wantPolicy:  "noeviction",
		},
		{
			name:        "custom policy is preserved while memory is filled",
			content:     "  maxmemory-policy\tallkeys-lfu\n",
			wantChanged: true,
			wantMemory:  "128mb",
			wantPolicy:  "allkeys-lfu",
		},
		{
			name:        "complete baseline is idempotent",
			content:     "  maxmemory   128mb\n\tmaxmemory-policy allkeys-lru\n",
			wantChanged: false,
			wantMemory:  "128mb",
			wantPolicy:  "allkeys-lru",
		},
		{
			name:        "last active duplicate wins and file is preserved",
			content:     "maxmemory 128mb\nmaxmemory-policy allkeys-lfu\nmaxmemory-policy noeviction\n",
			wantChanged: false,
			wantMemory:  "128mb",
			wantPolicy:  "noeviction",
		},
		{
			name:        "legacy equals maxmemory survives policy addition",
			content:     "maxmemory = 128mb\n# maxmemory-policy noeviction\n",
			wantChanged: true,
			wantMemory:  "128mb",
			wantPolicy:  "allkeys-lru",
		},
		{
			name:        "active include preserves indirect administrator policy",
			content:     "maxmemory 128mb\ninclude /etc/redis/custom.conf\n# maxmemory-policy noeviction\n",
			wantChanged: false,
			wantMemory:  "128mb",
			wantPolicy:  "",
		},
		{
			name:        "active include only suppresses policy baseline",
			content:     "include /etc/redis/custom.conf\n# maxmemory <bytes>\n# maxmemory-policy noeviction\n",
			wantChanged: true,
			wantMemory:  "128mb",
			wantPolicy:  "",
		},
		{
			name:        "commented include does not suppress policy baseline",
			content:     "maxmemory 128mb\n# include /etc/redis/custom.conf\n# maxmemory-policy noeviction\n",
			wantChanged: true,
			wantMemory:  "128mb",
			wantPolicy:  "allkeys-lru",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := BuildRedisBaselineConfig(tt.content, "128mb")
			if changed != tt.wantChanged {
				t.Fatalf("changed = %v, want %v; content:\n%s", changed, tt.wantChanged, got)
			}
			if value := FindRedisConfigValue(got, "maxmemory"); value != tt.wantMemory {
				t.Fatalf("maxmemory = %q, want %q; content:\n%s", value, tt.wantMemory, got)
			}
			if value := FindRedisConfigValue(got, "maxmemory-policy"); value != tt.wantPolicy {
				t.Fatalf("maxmemory-policy = %q, want %q; content:\n%s", value, tt.wantPolicy, got)
			}
			if strings.Count(got, "maxmemory-policy allkeys-lru") > 1 {
				t.Fatalf("default policy was appended more than once:\n%s", got)
			}
		})
	}
}

func TestReplaceRedisConfigValueKeepsIndentation(t *testing.T) {
	content := "\tmaxmemory   64mb\n"
	got := ReplaceRedisConfigValue(content, "maxmemory", "128mb")
	if got != "\tmaxmemory 128mb\n" {
		t.Fatalf("ReplaceRedisConfigValue() = %q", got)
	}
}

func TestReplaceRedisConfigValueUpdatesDuplicateActiveDirectives(t *testing.T) {
	content := "maxmemory 64mb\nmaxmemory 96mb\n"
	got := ReplaceRedisConfigValue(content, "maxmemory", "128mb")
	if strings.Count(got, "maxmemory 128mb") != 2 {
		t.Fatalf("active duplicates were not updated consistently:\n%s", got)
	}
}

func TestFindRedisConfigValueSupportsLegacyEqualsSyntax(t *testing.T) {
	content := "# maxmemory = 64mb\nmaxmemory = 128mb\n"
	if got := FindRedisConfigValue(content, "maxmemory"); got != "128mb" {
		t.Fatalf("FindRedisConfigValue() = %q, want 128mb", got)
	}
}
