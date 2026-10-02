package collector

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

	cpu, ioWait, steal, _ := readCPUPercent()
	memTotal, memUsed, memPercent := readMemoryStats()
	load1, load5, load15 := readLoadAvg()
	diskRead, diskWrite := readDiskIO()

	_, err := db.Exec(
		`INSERT INTO monitoring_metrics
		 (cpu_percent, memory_percent, memory_used_bytes, memory_total_bytes,
		  disk_read_bytes, disk_write_bytes, cpu_iowait_percent, cpu_steal_percent,
		  load_avg_1, load_avg_5, load_avg_15, recorded_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		cpu, memPercent, memUsed, memTotal, diskRead, diskWrite, ioWait, steal, load1, load5, load15,
	)
	if err != nil {
		log.Printf("采集器写入失败: %v", err)
	}
}

var prevCPU cpuTicks

type cpuTicks struct {
	total  float64
	idle   float64
	iowait float64
	steal  float64
}

func readCPUPercent() (float64, float64, float64, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, 0, err
	}
	current := parseCPUTicks(string(data))
	if current.total == 0 {
		return 0, 0, 0, nil
	}
	if prevCPU.total == 0 {
		prevCPU = current
		return 0, 0, 0, nil
	}
	deltaTotal := current.total - prevCPU.total
	deltaIdle := current.idle - prevCPU.idle
	deltaIOWait := current.iowait - prevCPU.iowait
	deltaSteal := current.steal - prevCPU.steal
	prevCPU = current
	if deltaTotal <= 0 {
		return 0, 0, 0, nil
	}
	busy := deltaTotal - deltaIdle - deltaIOWait - deltaSteal
	return clampPercent(busy / deltaTotal * 100),
		clampPercent(deltaIOWait / deltaTotal * 100),
		clampPercent(deltaSteal / deltaTotal * 100), nil
}

func parseCPUTicks(data string) cpuTicks {
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			if len(fields) < 5 {
				continue
			}
			var ticks cpuTicks
			// Linux reports guest time again inside user/nice, so only sum
			// user through steal (the first eight counters).
			for i, f := range fields[1:] {
				if i >= 8 {
					break
				}
				v, _ := strconv.ParseFloat(f, 64)
				ticks.total += v
				if i == 3 {
					ticks.idle = v
				} else if i == 4 {
					ticks.iowait = v
				} else if i == 7 {
					ticks.steal = v
				}
			}
			return ticks
		}
	}
	return cpuTicks{}
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
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

var (
	diskIOMu       sync.Mutex
	prevDiskRead   int64
	prevDiskWrite  int64
	prevDiskSample time.Time
)

func readDiskIO() (int64, int64) {
	readBytes, writeBytes := readDiskIOCounters("/sys/class/block")
	now := time.Now()

	diskIOMu.Lock()
	defer diskIOMu.Unlock()
	if prevDiskSample.IsZero() || readBytes < prevDiskRead || writeBytes < prevDiskWrite {
		prevDiskRead, prevDiskWrite, prevDiskSample = readBytes, writeBytes, now
		return 0, 0
	}
	seconds := now.Sub(prevDiskSample).Seconds()
	if seconds <= 0 {
		return 0, 0
	}
	readRate := int64(float64(readBytes-prevDiskRead) / seconds)
	writeRate := int64(float64(writeBytes-prevDiskWrite) / seconds)
	prevDiskRead, prevDiskWrite, prevDiskSample = readBytes, writeBytes, now
	return readRate, writeRate
}

func readDiskIOCounters(root string) (int64, int64) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, 0
	}
	var readBytes, writeBytes int64
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") ||
			strings.HasPrefix(name, "zram") || strings.HasPrefix(name, "sr") ||
			strings.HasPrefix(name, "fd") {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, name, "partition")); err == nil {
			continue
		}
		slaves, err := os.ReadDir(filepath.Join(root, name, "slaves"))
		if err == nil && len(slaves) > 0 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, name, "stat"))
		if err != nil {
			continue
		}
		fields := strings.Fields(string(data))
		if len(fields) < 7 {
			continue
		}
		sectorsRead, errRead := strconv.ParseInt(fields[2], 10, 64)
		sectorsWritten, errWrite := strconv.ParseInt(fields[6], 10, 64)
		if errRead != nil || errWrite != nil {
			continue
		}
		readBytes += sectorsRead * 512
		writeBytes += sectorsWritten * 512
	}
	return readBytes, writeBytes
}
