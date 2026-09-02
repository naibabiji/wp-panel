package executor

import "testing"

func TestParseNftSetIPs(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []string
	}{
		{name: "empty set", output: "table ip wppanel_persist { set banned_ips { type ipv4_addr; } }"},
		{name: "multiple addresses", output: "set banned_ips {\n elements = { 192.0.2.1, 198.51.100.2 }\n}", want: []string{"192.0.2.1", "198.51.100.2"}},
		{name: "element metadata", output: "elements = { 203.0.113.4 timeout 1h expires 20m }", want: []string{"203.0.113.4"}},
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
