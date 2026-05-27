package executor

import (
	"strings"
	"testing"
)

func TestRenderNginxBannedIPs(t *testing.T) {
	got := renderNginxBannedIPs(map[string]bool{
		"2001:db8::1":        true,
		"192.0.2.10":         true,
		"bad value":          true,
		"198.51.100.0/24":    true,
		"2001:db8:abcd::/48": true,
		"203.0.113.10":       false,
	})

	if !strings.Contains(got, "geo $wppanel_banned_ip") {
		t.Fatalf("missing geo variable: %s", got)
	}
	for _, want := range []string{
		"192.0.2.10 1;",
		"198.51.100.0/24 1;",
		"2001:db8::1 1;",
		"2001:db8:abcd::/48 1;",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in config:\n%s", want, got)
		}
	}
	if strings.Contains(got, "bad value") {
		t.Fatalf("invalid address leaked into config:\n%s", got)
	}
	if strings.Contains(got, "203.0.113.10") {
		t.Fatalf("false set entry leaked into config:\n%s", got)
	}
}

func TestRenderCloudflareRealIPConfig(t *testing.T) {
	got := renderCloudflareRealIPConfig([]string{
		"203.0.113.0/24",
		"bad value",
		"203.0.113.0/24",
		"2001:db8::/32",
	})

	if strings.Count(got, "203.0.113.0/24") != 1 {
		t.Fatalf("expected duplicate IPv4 CIDR to be removed:\n%s", got)
	}
	if !strings.Contains(got, "set_real_ip_from 2001:db8::/32;") {
		t.Fatalf("missing IPv6 CIDR:\n%s", got)
	}
	if !strings.Contains(got, "real_ip_header CF-Connecting-IP;") {
		t.Fatalf("missing CF-Connecting-IP header:\n%s", got)
	}
	if strings.Contains(got, "bad value") {
		t.Fatalf("invalid address leaked into config:\n%s", got)
	}
}
