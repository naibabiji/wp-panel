package handlers

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	prevCPU cpuTicks
	cpuMu   sync.Mutex
)

type cpuTicks struct {
	total  float64
	idle   float64
	iowait float64
	steal  float64
}

func readCPUStats() (float64, float64, float64, error) {
	current, err := readCPUTicks()
	if err != nil {
		return 0, 0, 0, err
	}
	if current.total == 0 {
		return 0, 0, 0, nil
	}

	cpuMu.Lock()
	if prevCPU.total == 0 {
		prevCPU = current
		cpuMu.Unlock()
		time.Sleep(200 * time.Millisecond)
		current, err = readCPUTicks()
		if err != nil {
			return 0, 0, 0, err
		}
		cpuMu.Lock()
	}
	deltaTotal := current.total - prevCPU.total
	deltaIdle := current.idle - prevCPU.idle
	deltaIOWait := current.iowait - prevCPU.iowait
	deltaSteal := current.steal - prevCPU.steal
	prevCPU = current
	cpuMu.Unlock()
	if deltaTotal <= 0 {
		return 0, 0, 0, nil
	}
	busy := deltaTotal - deltaIdle - deltaIOWait - deltaSteal
	return clampPercent(busy / deltaTotal * 100),
		clampPercent(deltaIOWait / deltaTotal * 100),
		clampPercent(deltaSteal / deltaTotal * 100), nil
}

func readCPUTicks() (cpuTicks, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTicks{}, err
	}
	return parseCPUTicks(string(data)), nil
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

func readSwapStats() (int64, int64) {
	return readSwapStatsFrom("/proc/meminfo")
}

func readSwapStatsFrom(path string) (int64, int64) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}

	var total, free int64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseInt(fields[1], 10, 64)
		value *= 1024
		switch fields[0] {
		case "SwapTotal:":
			total = value
		case "SwapFree:":
			free = value
		}
	}
	return total, total - free
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
