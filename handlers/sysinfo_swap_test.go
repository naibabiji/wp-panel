package handlers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSwapStatsFromMeminfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(path, []byte("MemTotal: 4194304 kB\nSwapTotal: 2097148 kB\nSwapFree: 1572864 kB\n"), 0600); err != nil {
		t.Fatal(err)
	}

	total, used := readSwapStatsFrom(path)
	if total != 2097148*1024 || used != (2097148-1572864)*1024 {
		t.Fatalf("readSwapStatsFrom() = total %d, used %d", total, used)
	}
}
