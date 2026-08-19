package executor

// RecommendOPcacheMemoryConsumptionMB 计算 opcache.memory_consumption 建议值（单位 MB）。
//
// 按总内存分档取基础值，再按站点数量做小幅修正，最后夹在
// [64, min(768, 总内存*12.5%)] 区间内，并向下对齐到 32MB 的整数倍。
// 之所以不用"总内存的固定百分比"这种线性公式，是因为小内存 VPS 和大内存服务器对
// OPcache 的实际需求差异很大，线性公式在大内存机器上会给出不合理的巨大数值。
func RecommendOPcacheMemoryConsumptionMB(facts SystemFacts) int {
	totalMB := int(facts.TotalMemoryBytes / 1024 / 1024)

	var base int
	switch {
	case totalMB <= 512:
		base = 64
	case totalMB <= 1024:
		base = 96
	case totalMB <= 2048:
		base = 128
	case totalMB <= 4096:
		base = 192
	case totalMB <= 8192:
		base = 256
	case totalMB <= 16384:
		base = 384
	default:
		base = 512
	}

	var siteAdjust int
	switch {
	case facts.SiteCount <= 3:
		siteAdjust = 0
	case facts.SiteCount <= 8:
		siteAdjust = 32
	case facts.SiteCount <= 20:
		siteAdjust = 64
	case facts.SiteCount <= 40:
		siteAdjust = 128
	default:
		siteAdjust = 256
	}

	hardCap := totalMB * 125 / 1000 // 总内存的 12.5%
	if hardCap > 768 {
		hardCap = 768
	}
	if hardCap < 64 {
		hardCap = 64
	}

	result := base + siteAdjust
	if result > hardCap {
		result = hardCap
	}
	if result < 64 {
		result = 64
	}

	result = (result / 32) * 32
	if result < 64 {
		result = 64
	}
	return result
}

// RecommendPHPFPMMaxChildren 计算新建站点的 pm.max_children 建议值。
//
// 分别按总内存和 CPU 核心数算出两个上限，取较小值。这个值只在建站时计算一次并持久化，
// 之后不会因为服务器又建了多少个站点而重新计算——pm=ondemand 模式下闲置站点本身不占用
// 常驻 worker，多站点之间的资源 overcommit 是安全的，所以这里刻意不考虑站点数量维度。
func RecommendPHPFPMMaxChildren(facts SystemFacts) int {
	totalMB := int(facts.TotalMemoryBytes / 1024 / 1024)

	var memoryCap int
	switch {
	case totalMB < 768:
		memoryCap = 2
	case totalMB < 1536:
		memoryCap = 4
	case totalMB < 3072:
		memoryCap = 6
	case totalMB < 6144:
		memoryCap = 10
	case totalMB < 12288:
		memoryCap = 16
	case totalMB < 24576:
		memoryCap = 24
	default:
		memoryCap = 32
	}

	var cpuCap int
	switch {
	case facts.CPUCores <= 1:
		cpuCap = 6
	case facts.CPUCores == 2:
		cpuCap = 10
	case facts.CPUCores <= 4:
		cpuCap = 16
	case facts.CPUCores <= 8:
		cpuCap = 24
	case facts.CPUCores <= 16:
		cpuCap = 32
	default:
		cpuCap = 48
	}

	if memoryCap < cpuCap {
		return memoryCap
	}
	return cpuCap
}

// RecommendOPcacheMaxAcceleratedFiles 返回 opcache.max_accelerated_files 建议值。
//
// PHP 内部会把这个值向上取整到预定义的质数哈希表档位（如 16229/32531/65407/130987/
// 262237/524521），这里直接按站点数量返回对应的档位值，避免"配置写的是一个数字，
// 实际生效的是另一个数字"这种落差。
func RecommendOPcacheMaxAcceleratedFiles(facts SystemFacts) int {
	switch {
	case facts.SiteCount <= 1:
		return 16229
	case facts.SiteCount <= 3:
		return 32531
	case facts.SiteCount <= 8:
		return 65407
	case facts.SiteCount <= 20:
		return 130987
	case facts.SiteCount <= 50:
		return 262237
	default:
		return 524521
	}
}
