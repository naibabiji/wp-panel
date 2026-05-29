package executor

import "testing"

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a    string
		b    string
		want int
	}{
		{a: "v1.2.0", b: "v1.1.9", want: 1},
		{a: "v1.2.0", b: "v1.2.0", want: 0},
		{a: "v1.2.0-rc5", b: "v1.2.0-rc4", want: 1},
		{a: "v1.2.0", b: "v1.2.0-rc5", want: 1},
		{a: "v1.2.0-rc4", b: "v1.2.0", want: -1},
	}

	for _, tt := range tests {
		got := CompareVersions(tt.a, tt.b)
		if (got > 0 && tt.want <= 0) || (got == 0 && tt.want != 0) || (got < 0 && tt.want >= 0) {
			t.Fatalf("CompareVersions(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
		}
	}
}
