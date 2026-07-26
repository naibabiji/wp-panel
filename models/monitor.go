package models

import "time"

type MonitoringMetric struct {
	ID               int       `json:"id"`
	CPUPercent       float64   `json:"cpu_percent"`
	MemoryPercent    float64   `json:"memory_percent"`
	MemoryUsedBytes  int64     `json:"memory_used_bytes"`
	MemoryTotalBytes int64     `json:"memory_total_bytes"`
	DiskReadBytes    int64     `json:"disk_read_bytes"`
	DiskWriteBytes   int64     `json:"disk_write_bytes"`
	LoadAvg1         float64   `json:"load_avg_1"`
	LoadAvg5         float64   `json:"load_avg_5"`
	LoadAvg15        float64   `json:"load_avg_15"`
	RecordedAt       time.Time `json:"recorded_at"`
}

type SystemStats struct {
	CPUPercent       float64 `json:"cpu_percent"`
	MemoryPercent    float64 `json:"memory_percent"`
	MemoryUsedBytes  int64   `json:"memory_used_bytes"`
	MemoryTotalBytes int64   `json:"memory_total_bytes"`
	SwapUsedBytes    int64   `json:"swap_used_bytes"`
	SwapTotalBytes   int64   `json:"swap_total_bytes"`
	DiskUsedBytes    int64   `json:"disk_used_bytes"`
	DiskTotalBytes   int64   `json:"disk_total_bytes"`
	DiskReadBytes    int64   `json:"disk_read_bytes"`
	DiskWriteBytes   int64   `json:"disk_write_bytes"`
	LoadAvg1         float64 `json:"load_avg_1"`
	LoadAvg5         float64 `json:"load_avg_5"`
	LoadAvg15        float64 `json:"load_avg_15"`
	Uptime           int64   `json:"uptime"`
}

type MetricsQuery struct {
	Range string `form:"range" binding:"required,oneof=24h 7d 15d 30d"`
}
