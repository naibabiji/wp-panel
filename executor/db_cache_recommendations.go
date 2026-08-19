package executor

// RecommendInnoDBBufferPoolSizeMB 计算 MariaDB innodb_buffer_pool_size 建议值（单位 MB）。
//
// wp-panel 部署场景是 MariaDB 和 PHP-FPM、Redis、Nginx、面板自身共用同一台宿主机，
// 不是专用数据库服务器，所以这里按总内存分档取值，比专用 MariaDB 服务器常见的
// 60%~70% 保守得多（大致在总内存的 12.5%~25% 区间），避免挤占其它组件的内存。
// 最后统一夹一层"不超过总内存 30%"的硬上限，防止分档表本身配置不合理时
// 导致数据库因内存不足无法启动。
func RecommendInnoDBBufferPoolSizeMB(facts SystemFacts) int {
	totalMB := int(facts.TotalMemoryBytes / 1024 / 1024)

	var recommended int
	switch {
	case totalMB <= 512:
		recommended = 64
	case totalMB <= 1024:
		recommended = 128
	case totalMB <= 2048:
		recommended = 384
	case totalMB <= 4096:
		recommended = 768
	case totalMB <= 8192:
		recommended = 1536
	case totalMB <= 16384:
		recommended = 3072
	case totalMB <= 32768:
		recommended = 6144
	default:
		recommended = totalMB / 4 // 总内存的 25%
		if recommended > 12288 {
			recommended = 12288
		}
	}

	hardCap := totalMB * 3 / 10 // 总内存的 30%
	if recommended > hardCap {
		recommended = hardCap
	}
	if recommended < 32 {
		recommended = 32
	}
	return recommended
}

// RecommendRedisMaxmemoryMB 计算 Redis maxmemory 建议值（单位 MB）。
//
// Redis 官方文档提醒 maxmemory 并不是进程实际占用内存的上限——allocator 开销、内存
// 碎片和持久化时 fork 产生的额外消耗都在这个值之外，所以这里比 OPcache/MariaDB 更
// 保守：硬上限是总内存的 10%，且绝对值不超过 1024MB。WordPress object cache 场景下
// 如果确实需要更大的缓存，应该是管理员主动调优，而不是面板给出的默认建议。
func RecommendRedisMaxmemoryMB(facts SystemFacts) int {
	totalMB := int(facts.TotalMemoryBytes / 1024 / 1024)

	var recommended int
	switch {
	case totalMB <= 512:
		recommended = 32
	case totalMB <= 1024:
		recommended = 64
	case totalMB <= 2048:
		recommended = 128
	case totalMB <= 4096:
		recommended = 192
	case totalMB <= 8192:
		recommended = 256
	case totalMB <= 16384:
		recommended = 512
	case totalMB <= 32768:
		recommended = 768
	default:
		recommended = 1024
	}

	hardCap := totalMB / 10 // 总内存的 10%
	if hardCap > 1024 {
		hardCap = 1024
	}
	if recommended > hardCap {
		recommended = hardCap
	}
	if recommended < 16 {
		recommended = 16
	}
	return recommended
}
