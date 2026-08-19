package executor

import "testing"

func TestRecommendOPcacheMemoryConsumptionMB(t *testing.T) {
	cases := []struct {
		name      string
		totalMB   uint64
		siteCount int
		want      int
	}{
		{"512MB/1site", 512, 1, 64},
		{"1GB/3sites", 1024, 3, 96},
		{"1GB/10sites", 1024, 10, 128},   // 96+64=160, hardCap=128 -> 128
		{"2GB/10sites", 2048, 10, 192},   // 128+64=192, hardCap=256 -> 192
		{"4GB/20sites", 4096, 20, 256},   // 192+64=256, hardCap=512 -> 256
		{"8GB/30sites", 8192, 30, 384},   // 256+128=384, hardCap=768 -> 384
		{"32GB/60sites", 32768, 60, 768}, // 512+256=768, hardCap=min(768,4096) -> 768
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := SystemFacts{TotalMemoryBytes: tc.totalMB * 1024 * 1024, SiteCount: tc.siteCount}
			got := RecommendOPcacheMemoryConsumptionMB(facts)
			if got != tc.want {
				t.Fatalf("RecommendOPcacheMemoryConsumptionMB(%+v) = %d, want %d", facts, got, tc.want)
			}
			if got%32 != 0 {
				t.Fatalf("result %d is not 32MB-aligned", got)
			}
			if got < 64 {
				t.Fatalf("result %d below the 64MB floor", got)
			}
		})
	}
}

func TestRecommendOPcacheMemoryConsumptionNeverExceedsHardCap(t *testing.T) {
	facts := SystemFacts{TotalMemoryBytes: 256 * 1024 * 1024, SiteCount: 200}
	got := RecommendOPcacheMemoryConsumptionMB(facts)
	// 256MB 总内存的 12.5% = 32MB，低于 64MB 下限，下限优先，结果应仍为 64。
	if got != 64 {
		t.Fatalf("expected floor of 64MB on a tiny server, got %d", got)
	}
}

func TestRecommendPHPFPMMaxChildren(t *testing.T) {
	cases := []struct {
		name     string
		totalMB  uint64
		cpuCores int
		want     int
	}{
		{"512MB/1core", 512, 1, 2},
		{"1GB/1core", 1024, 1, 4},
		{"2GB/1core", 2048, 1, 6},
		{"4GB/1core", 4096, 1, 6},  // memoryCap=10, cpuCap=6 -> min=6
		{"4GB/2core", 4096, 2, 10}, // memoryCap=10, cpuCap=10 -> min=10
		{"8GB/2core", 8192, 2, 10}, // memoryCap=16, cpuCap=10 -> min=10
		{"8GB/4core", 8192, 4, 16}, // memoryCap=16, cpuCap=16 -> min=16
		{"16GB/8core", 16384, 8, 24},
		{"32GB/16core", 32768, 16, 32},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := SystemFacts{TotalMemoryBytes: tc.totalMB * 1024 * 1024, CPUCores: tc.cpuCores}
			got := RecommendPHPFPMMaxChildren(facts)
			if got != tc.want {
				t.Fatalf("RecommendPHPFPMMaxChildren(%+v) = %d, want %d", facts, got, tc.want)
			}
		})
	}
}

func TestRecommendOPcacheMaxAcceleratedFiles(t *testing.T) {
	cases := []struct {
		siteCount int
		want      int
	}{
		{0, 16229},
		{1, 16229},
		{2, 32531},
		{3, 32531},
		{4, 65407},
		{8, 65407},
		{9, 130987},
		{20, 130987},
		{21, 262237},
		{50, 262237},
		{51, 524521},
		{1000, 524521},
	}
	for _, tc := range cases {
		facts := SystemFacts{SiteCount: tc.siteCount}
		got := RecommendOPcacheMaxAcceleratedFiles(facts)
		if got != tc.want {
			t.Fatalf("RecommendOPcacheMaxAcceleratedFiles(siteCount=%d) = %d, want %d", tc.siteCount, got, tc.want)
		}
	}
}
