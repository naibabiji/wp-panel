package collector

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func Start() {
	go runLoop()
	log.Println("系统指标采集器已启动(每1分钟)")
}

func runLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	collect()

	for range ticker.C {
		collect()
		cleanup()
	}
}

func cleanup() {
	db := database.GetDB()
	if db == nil {
		return
	}
	cutoff := time.Now().UTC().Add(-15 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	db.Exec("DELETE FROM monitoring_metrics WHERE recorded_at < ?", cutoff)
}

func collect() {
	db := database.GetDB()
	if db == nil {
		return
	}

	cpu, _ := readCPUPercent()
	memTotal, memUsed, memPercent := readMemoryStats()
	load1, load5, load15 := readLoadAvg()
	diskRead, diskWrite := readDiskIO()

	_, err := db.Exec(
		`INSERT INTO monitoring_metrics
		 (cpu_percent, memory_percent, memory_used_bytes, memory_total_bytes,
		  disk_read_bytes, disk_write_bytes, load_avg_1, load_avg_5, load_avg_15, recorded_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		cpu, memPercent, memUsed, memTotal, diskRead, diskWrite, load1, load5, load15,
	)
	if err != nil {
		log.Printf("采集器写入失败: %v", err)
	}
}

var prevCPUIdle, prevCPUTotal float64

func readCPUPercent() (float64, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, err
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			if len(fields) < 5 {
				continue
			}
			var total, idle float64
			for i, f := range fields[1:] {
				v, _ := strconv.ParseFloat(f, 64)
				total += v
				if i == 3 {
					idle = v
				}
			}
			if total == 0 {
				return 0, nil
			}
			deltaTotal := total - prevCPUTotal
			deltaIdle := idle - prevCPUIdle
			prevCPUTotal, prevCPUIdle = total, idle
			if deltaTotal <= 0 {
				return 0, nil
			}
			pct := (1 - deltaIdle/deltaTotal) * 100
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			return pct, nil
		}
	}
	return 0, nil
}

func readMemoryStats() (int64, int64, float64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}
	var total, available int64
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(fields[1], 10, 64)
		v *= 1024
		switch fields[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			available = v
		}
	}
	if total == 0 {
		return 0, 0, 0
	}
	used := total - available
	percent := float64(used) / float64(total) * 100
	return total, used, percent
}

func readLoadAvg() (float64, float64, float64) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0
	}
	l1, _ := strconv.ParseFloat(fields[0], 64)
	l5, _ := strconv.ParseFloat(fields[1], 64)
	l15, _ := strconv.ParseFloat(fields[2], 64)
	return l1, l5, l15
}

func readDiskIO() (int64, int64) {
	return 0, 0
}
