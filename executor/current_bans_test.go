package executor

import (
	"errors"
	"strings"
	"testing"
)

func TestParseNftSetIPs(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []string
	}{
		{name: "empty set", output: "table ip wppanel_persist { set banned_ips { type ipv4_addr; } }"},
		{name: "multiple addresses", output: "set banned_ips {\n elements = { 192.0.2.1, 198.51.100.2 }\n}", want: []string{"192.0.2.1", "198.51.100.2"}},
		{name: "element metadata", output: "elements = { 203.0.113.4 timeout 1h expires 20m }", want: []string{"203.0.113.4"}},
		{name: "normalizes IPv6", output: "elements = { 2604:a880:cad:d0::1:a6db:2001 }", want: []string{"2604:a880:cad:d0:0:1:a6db:2001"}},
		{name: "ignores invalid values", output: "elements = { nope, 203.0.113.5 }", want: []string{"203.0.113.5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNftSetIPs(tt.output)
			if len(got) != len(tt.want) {
				t.Fatalf("parseNftSetIPs() = %#v, want %v", got, tt.want)
			}
			for _, ip := range tt.want {
				if !got[ip] {
					t.Fatalf("parseNftSetIPs() missing %s: %#v", ip, got)
				}
			}
		})
	}
}

func TestReadCurrentBanEnforcementMergesFamiliesAndPreservesPartialRead(t *testing.T) {
	oldCommand := currentBanCommand
	oldEnsure := currentBanEnsurePersist
	t.Cleanup(func() {
		currentBanCommand = oldCommand
		currentBanEnsurePersist = oldEnsure
	})
	currentBanEnsurePersist = func() error { return nil }
	currentBanCommand = func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "nft" && strings.Contains(joined, "list set ip wppanel_persist"):
			return "elements = { 192.0.2.1 }", nil
		case name == "nft" && strings.Contains(joined, "list set ip6 wppanel_persist"):
			return "", errors.New("ip6 unavailable")
		default:
			return "", errors.New("unexpected command")
		}
	}
	state := readCurrentBanEnforcement(fail2banSnapshot{
		active:         map[fail2banJailIP]bool{},
		jailStatusRead: map[string]bool{},
	})
	if state.Status.Nftables {
		t.Fatal("Nftables status = true, want incomplete")
	}
	if !state.Persist["192.0.2.1"] {
		t.Fatalf("successful IPv4 result was discarded: %#v", state.Persist)
	}
}

func TestReadCurrentBanEnforcementIPv6OnlyAndComplete(t *testing.T) {
	oldCommand := currentBanCommand
	oldEnsure := currentBanEnsurePersist
	t.Cleanup(func() {
		currentBanCommand = oldCommand
		currentBanEnsurePersist = oldEnsure
	})
	currentBanEnsurePersist = func() error { return nil }
	tests := []struct {
		name, ipv4, ipv6 string
		ipv4Err, ipv6Err error
		wantComplete     bool
	}{
		{name: "IPv6 survives IPv4 failure", ipv4Err: errors.New("IPv4 unavailable"), ipv6: "elements = { 2001:db8::7 }"},
		{name: "both families complete", ipv4: "elements = { 192.0.2.7 }", ipv6: "elements = { 2001:db8::7 }", wantComplete: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			currentBanCommand = func(name string, args ...string) (string, error) {
				joined := strings.Join(args, " ")
				if name == "nft" && strings.Contains(joined, "list set ip6 wppanel_persist") {
					return tt.ipv6, tt.ipv6Err
				}
				if name == "nft" && strings.Contains(joined, "list set ip wppanel_persist") {
					return tt.ipv4, tt.ipv4Err
				}
				return "", errors.New("unexpected command")
			}
			state := readCurrentBanEnforcement(fail2banSnapshot{active: map[fail2banJailIP]bool{}, jailStatusRead: map[string]bool{}})
			if state.Status.Nftables != tt.wantComplete {
				t.Fatalf("Nftables complete = %v, want %v", state.Status.Nftables, tt.wantComplete)
			}
			if !state.Persist["2001:db8::7"] {
				t.Fatalf("IPv6 result missing: %#v", state.Persist)
			}
		})
	}
}
