package collector

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCPUTicksSeparatesIOWaitAndSteal(t *testing.T) {
	ticks := parseCPUTicks("cpu  100 10 20 300 40 5 6 7 80 9\n")
	if ticks.total != 488 || ticks.idle != 300 || ticks.iowait != 40 || ticks.steal != 7 {
		t.Fatalf("ticks=%+v", ticks)
	}
}

func TestReadDiskIOCountersUsesWholeLeafDevices(t *testing.T) {
	root := t.TempDir()
	writeStat := func(name, stat string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, name, "slaves"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "stat"), []byte(stat), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeStat("sda", "1 0 8 0 2 0 16 0 0 0 0\n")
	writeStat("loop0", "1 0 100 0 2 0 100 0 0 0 0\n")
	writeStat("dm-0", "1 0 8 0 2 0 16 0 0 0 0\n")
	if err := os.Symlink(filepath.Join(root, "sda"), filepath.Join(root, "dm-0", "slaves", "sda")); err != nil {
		t.Fatal(err)
	}

	readBytes, writeBytes := readDiskIOCounters(root)
	if readBytes != 8*512 || writeBytes != 16*512 {
		t.Fatalf("read=%d write=%d", readBytes, writeBytes)
	}
}
