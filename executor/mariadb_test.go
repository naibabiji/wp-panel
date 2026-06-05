package executor

import "testing"

func TestTablePrefixFromOptionsTable(t *testing.T) {
	tests := map[string]string{
		"wp_options":            "wp_",
		"wp_sadfasdfasfoptions": "wp_sadfasdfasf",
		"wp_ab12cd34_options":   "wp_ab12cd34_",
		"custom123_options":     "custom123_",
	}

	for tableName, want := range tests {
		got, ok := tablePrefixFromOptionsTable(tableName)
		if !ok {
			t.Fatalf("expected %q to be parsed", tableName)
		}
		if got != want {
			t.Fatalf("tablePrefixFromOptionsTable(%q) = %q, want %q", tableName, got, want)
		}
	}
}

func TestTablePrefixFromOptionsTableRejectsInvalidPrefix(t *testing.T) {
	if _, ok := tablePrefixFromOptionsTable("bad-prefix_options"); ok {
		t.Fatal("expected invalid table prefix to be rejected")
	}
}
