package handlers

import (
	"strconv"
	"testing"

	"github.com/naibabiji/wp-panel/executor"
)

func TestFindPHPIniValueSkipsComments(t *testing.T) {
	content := "; memory_limit = 128M\n# memory_limit = 192M\nmemory_limit = 256M\n"
	if got := findPHPIniValue(content, "memory_limit"); got != "256M" {
		t.Fatalf("findPHPIniValue() = %q, want 256M", got)
	}
}

func TestPHPConfigRequiresPoolRebuild(t *testing.T) {
	if !phpConfigRequiresPoolRebuild("post_max_size") {
		t.Fatal("post_max_size should rebuild PHP-FPM pools")
	}
	if phpConfigRequiresPoolRebuild("max_input_vars") {
		t.Fatal("max_input_vars should only reload PHP-FPM")
	}
	// opcache 参数只存在于全局 ini，不是每站点 pool 里的字段，改动只需要 reload，
	// 不应该触发全站点 PHP-FPM pool 批量重建。
	for _, key := range []string{"opcache.memory_consumption", "opcache.max_accelerated_files"} {
		if phpConfigRequiresPoolRebuild(key) {
			t.Fatalf("%s should only reload PHP-FPM, not rebuild pools", key)
		}
	}
}

// TestAdaptiveConfigFallbackMatchesRecommendFormula 确认 opcache 两项的展示兜底值
// 不是写死的固定数字，而是复用跟"重新计算推荐值"按钮相同的公式现算出来的——避免
// 两处出现不一致的数字造成困惑（代码审核发现的问题）。
func TestAdaptiveConfigFallbackMatchesRecommendFormula(t *testing.T) {
	facts := executor.CollectSystemFacts()
	wantMem := strconv.Itoa(executor.RecommendOPcacheMemoryConsumptionMB(facts))
	wantFiles := strconv.Itoa(executor.RecommendOPcacheMaxAcceleratedFiles(facts))

	if got := adaptiveConfigFallback("opcache.memory_consumption"); got != wantMem {
		t.Fatalf("adaptiveConfigFallback(opcache.memory_consumption) = %q, want %q", got, wantMem)
	}
	if got := adaptiveConfigFallback("opcache.max_accelerated_files"); got != wantFiles {
		t.Fatalf("adaptiveConfigFallback(opcache.max_accelerated_files) = %q, want %q", got, wantFiles)
	}
	if got := adaptiveConfigFallback("memory_limit"); got != "" {
		t.Fatalf("adaptiveConfigFallback(memory_limit) should be empty (static key), got %q", got)
	}
	if _, ok := configDefaults["opcache.memory_consumption"]; ok {
		t.Fatal("opcache.memory_consumption should no longer have a static fallback in configDefaults")
	}
}

func TestOpcacheKeysAreWhitelistedAndIntegerValidated(t *testing.T) {
	for _, key := range []string{"opcache.memory_consumption", "opcache.max_accelerated_files"} {
		if !softConfigAllowed["PHP"][key] {
			t.Fatalf("%s should be in the PHP config whitelist", key)
		}
		if msg := validateSoftwareConfigValue("zh-CN", "PHP", key, "256"); msg != "" {
			t.Fatalf("expected %s=256 to be valid, got error: %s", key, msg)
		}
		if msg := validateSoftwareConfigValue("zh-CN", "PHP", key, "not-a-number"); msg == "" {
			t.Fatalf("expected %s=not-a-number to be rejected", key)
		}
	}
}
