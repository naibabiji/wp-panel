package executor

import "testing"

func TestParseIncidentMemory(t *testing.T) {
	memory := parseIncidentMemory(`MemTotal:        8388608 kB
MemAvailable:    1048576 kB
SwapTotal:       2097152 kB
SwapFree:         524288 kB
`)
	if memory.total != 8*1024*1024*1024 {
		t.Fatalf("total = %d", memory.total)
	}
	if memory.available != 1024*1024*1024 {
		t.Fatalf("available = %d", memory.available)
	}
	if memory.swapTotal-memory.swapFree != 1536*1024*1024 {
		t.Fatalf("swap used = %d", memory.swapTotal-memory.swapFree)
	}
}

func TestParseIncidentProcessesAggregatesAndLimits(t *testing.T) {
	processes := parseIncidentProcesses(`php-fpm8.3 1048576
php-fpm8.3 524288
mariadbd 1048576
redis-server 262144
nginx 1024
`, 3)
	if len(processes) != 3 {
		t.Fatalf("processes = %d, want 3", len(processes))
	}
	if processes[0].name != "PHP-FPM" || processes[0].rss != 1536*1024*1024 {
		t.Fatalf("first process = %+v", processes[0])
	}
	if processes[1].name != "MariaDB" {
		t.Fatalf("second process = %+v", processes[1])
	}
	if processes[2].name != "Redis" {
		t.Fatalf("third process = %+v", processes[2])
	}
}

func TestFormatIncidentBytes(t *testing.T) {
	if got := formatIncidentBytes(1536 * 1024 * 1024); got != "1.5 GB" {
		t.Fatalf("formatIncidentBytes = %q", got)
	}
	if got := formatIncidentBytes(512 * 1024); got != "<1 MB" {
		t.Fatalf("small formatIncidentBytes = %q", got)
	}
}
