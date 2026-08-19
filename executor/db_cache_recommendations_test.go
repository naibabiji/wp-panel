package executor

import "testing"

func TestRecommendInnoDBBufferPoolSizeMB(t *testing.T) {
	cases := []struct {
		totalMB uint64
		want    int
	}{
		{512, 64},
		{1024, 128},
		{2048, 384},
		{4096, 768},
		{8192, 1536},
		{16384, 3072},
		{32768, 6144},
		{65536, 12288}, // 25% of 64GB = 16384, capped at 12288
	}
	for _, tc := range cases {
		facts := SystemFacts{TotalMemoryBytes: tc.totalMB * 1024 * 1024}
		got := RecommendInnoDBBufferPoolSizeMB(facts)
		if got != tc.want {
			t.Fatalf("RecommendInnoDBBufferPoolSizeMB(%dMB) = %d, want %d", tc.totalMB, got, tc.want)
		}
	}
}

func TestRecommendInnoDBBufferPoolSizeNeverExceeds30PercentOfRAM(t *testing.T) {
	// 一个刻意设计得比硬上限还大的极端场景，用来验证 hardCap 生效。
	facts := SystemFacts{TotalMemoryBytes: 600 * 1024 * 1024}
	got := RecommendInnoDBBufferPoolSizeMB(facts)
	hardCap := 600 * 3 / 10
	if got > hardCap {
		t.Fatalf("RecommendInnoDBBufferPoolSizeMB() = %d exceeds 30%% hard cap of %d", got, hardCap)
	}
}

func TestRecommendRedisMaxmemoryMB(t *testing.T) {
	cases := []struct {
		totalMB uint64
		want    int
	}{
		{512, 32},
		{1024, 64},
		{2048, 128},
		{4096, 192},
		{8192, 256},
		{16384, 512},
		{32768, 768},
		{65536, 1024},
	}
	for _, tc := range cases {
		facts := SystemFacts{TotalMemoryBytes: tc.totalMB * 1024 * 1024}
		got := RecommendRedisMaxmemoryMB(facts)
		if got != tc.want {
			t.Fatalf("RecommendRedisMaxmemoryMB(%dMB) = %d, want %d", tc.totalMB, got, tc.want)
		}
	}
}

func TestRecommendRedisMaxmemoryNeverExceeds10PercentOfRAM(t *testing.T) {
	facts := SystemFacts{TotalMemoryBytes: 300 * 1024 * 1024}
	got := RecommendRedisMaxmemoryMB(facts)
	hardCap := 300 / 10
	if got > hardCap {
		t.Fatalf("RecommendRedisMaxmemoryMB() = %d exceeds 10%% hard cap of %d", got, hardCap)
	}
}

func TestRecommendRedisMaxmemoryAbsoluteCapIs1024MB(t *testing.T) {
	facts := SystemFacts{TotalMemoryBytes: 256 * 1024 * 1024 * 1024} // 256GB
	got := RecommendRedisMaxmemoryMB(facts)
	if got > 1024 {
		t.Fatalf("RecommendRedisMaxmemoryMB() = %d exceeds absolute 1024MB cap", got)
	}
}
