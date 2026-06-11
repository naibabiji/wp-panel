package executor

import (
	"strings"
	"testing"
)

func TestRenderSiteLogrotateConfigIncludesAllSiteLogs(t *testing.T) {
	config := renderSiteLogrotateConfig("example.com", "/www/wwwlogs/example.com", 7)
	for _, want := range []string{
		"/www/wwwlogs/example.com/access.log",
		"/www/wwwlogs/example.com/error.log",
		"/www/wwwlogs/example.com/wp-security.log",
		"/www/wwwlogs/example.com/php-error.log",
		"/www/wwwlogs/example.com/php-slow.log",
		"rotate 7",
		"copytruncate",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("logrotate config missing %q:\n%s", want, config)
		}
	}
}

func TestCleanSiteLogrotateLogDirRejectsOutsideRoot(t *testing.T) {
	for _, input := range []string{
		"/tmp/example.com",
		"/www/wwwlogs/../secret",
		"www/wwwlogs/example.com",
		"",
	} {
		if _, err := cleanSiteLogrotateLogDir(input); err == nil {
			t.Fatalf("expected %q to be rejected", input)
		}
	}
}

func TestCleanSiteLogrotateLogDirAllowsSiteLogRoot(t *testing.T) {
	got, err := cleanSiteLogrotateLogDir("/www/wwwlogs/example.com/../example.com")
	if err != nil {
		t.Fatalf("expected site log dir to be accepted: %v", err)
	}
	if got != "/www/wwwlogs/example.com" {
		t.Fatalf("clean log dir = %q", got)
	}
}
