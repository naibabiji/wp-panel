package handlers

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	prevCPUIdle  float64
	prevCPUTotal float64
	cpuMu        sync.Mutex
)

func readCPUPercent() (float64, error) {
	idle, total, err := readCPUTicks()
	if err != nil {
		return 0, err
	}
	if total == 0 {
		return 0, nil
	}

	cpuMu.Lock()
	if prevCPUTotal == 0 {
		prevCPUTotal, prevCPUIdle = total, idle
		cpuMu.Unlock()
		time.Sleep(200 * time.Millisecond)
		idle, total, err = readCPUTicks()
		if err != nil {
			return 0, err
		}
		cpuMu.Lock()
	}
	deltaTotal := total - prevCPUTotal
	deltaIdle := idle - prevCPUIdle
	prevCPUTotal, prevCPUIdle = total, idle
	cpuMu.Unlock()
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

func readCPUTicks() (float64, float64, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, err
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
			return idle, total, nil
		}
	}
	return 0, 0, nil
}

func readMemoryStats() (int64, int64, float64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}

	var total, available, buffers, cached int64
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
		case "Buffers:":
			buffers = v
		case "Cached:":
			cached = v
		}
	}

	if total == 0 {
		return 0, 0, 0
	}

	if available == 0 && total > 0 {
		available = total - (total - buffers - cached)
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

func readUptime() int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}

	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0
	}

	uptime, _ := strconv.ParseFloat(fields[0], 64)
	return int64(uptime)
}

func readDiskIO() (int64, int64) {
	return 0, 0
}
