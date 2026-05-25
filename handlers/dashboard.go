package handlers

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

var (
	announcementCache     string
	announcementFetchedAt time.Time
	announcementMu        sync.RWMutex
)

type DashboardHandler struct{}

func (h *DashboardHandler) GetStats(c *gin.Context) {
	stats := collectCurrentStats()

	c.JSON(http.StatusOK, models.SuccessResponse(stats))
}

func (h *DashboardHandler) GetMetrics(c *gin.Context) {
	var query models.MetricsQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误: range 必须是 24h、7d 或 30d"))
		return
	}

	labels, cpu, memory, load := queryMetrics(query.Range)

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"labels": labels,
		"cpu":    cpu,
		"memory": memory,
		"load":   load,
	}))
}

func collectCurrentStats() *models.SystemStats {
	stats := &models.SystemStats{}

	cpu, _ := readCPUPercent()
	memTotal, memUsed, memPercent := readMemoryStats()
	diskTotal, diskUsed := readDiskStats()
	load1, load5, load15 := readLoadAvg()
	uptime := readUptime()

	stats.CPUPercent = cpu
	stats.MemoryPercent = memPercent
	stats.MemoryUsedBytes = memUsed
	stats.MemoryTotalBytes = memTotal
	stats.DiskTotalBytes = diskTotal
	stats.DiskUsedBytes = diskUsed
	stats.LoadAvg1 = load1
	stats.LoadAvg5 = load5
	stats.LoadAvg15 = load15
	stats.Uptime = uptime

	return stats
}

func queryMetrics(r string) ([]string, []float64, []float64, []float64) {
	db := database.GetDB()
	var since string

	switch r {
	case "24h":
		since = time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	case "7d":
		since = time.Now().UTC().Add(-7 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	case "15d":
		since = time.Now().UTC().Add(-15 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	default:
		return nil, nil, nil, nil
	}

	query := `SELECT recorded_at, cpu_percent, memory_percent, load_avg_1
	           FROM monitoring_metrics
	           WHERE recorded_at > ?
	           ORDER BY recorded_at ASC`

	rows, err := db.Query(query, since)
	if err != nil {
		return nil, nil, nil, nil
	}
	defer rows.Close()

	var labels []string
	var cpu []float64
	var memory []float64
	var load []float64

	format := "15:04"
	if r == "7d" {
		format = "01-02 15:04"
	} else if r == "15d" {
		format = "01-02"
	}

	for rows.Next() {
		var ts time.Time
		var c, m, l float64
		if err := rows.Scan(&ts, &c, &m, &l); err != nil {
			continue
		}
		labels = append(labels, ts.Format(format))
		cpu = append(cpu, c)
		memory = append(memory, m)
		load = append(load, l)
	}

	if labels == nil {
		labels = []string{}
		cpu = []float64{}
		memory = []float64{}
		load = []float64{}
	}

	return labels, cpu, memory, load
}

func GetAnnouncement(c *gin.Context) {
	announcementMu.RLock()
	if announcementCache != "" && time.Since(announcementFetchedAt) < 30*time.Minute {
		cache := announcementCache
		announcementMu.RUnlock()
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"content": cache}))
		return
	}
	announcementMu.RUnlock()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://raw.githubusercontent.com/naibabiji/wp-panel/main/ANNOUNCEMENT.md")
	if err != nil {
		announcementMu.RLock()
		cache := announcementCache
		announcementMu.RUnlock()
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"content": cache}))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		announcementMu.RLock()
		cache := announcementCache
		announcementMu.RUnlock()
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"content": cache}))
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		announcementMu.RLock()
		cache := announcementCache
		announcementMu.RUnlock()
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"content": cache}))
		return
	}

	announcementMu.Lock()
	announcementCache = strings.TrimSpace(string(body))
	announcementFetchedAt = time.Now()
	cache := announcementCache
	announcementMu.Unlock()

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"content": cache}))
}
