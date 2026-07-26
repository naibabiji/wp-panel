package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

type incidentMemory struct {
	total     int64
	available int64
	swapTotal int64
	swapFree  int64
}

type incidentProcess struct {
	name string
	rss  int64
}

func captureIncidentResourceSnapshot() string {
	memoryData, _ := os.ReadFile("/proc/meminfo")
	loadData, _ := os.ReadFile("/proc/loadavg")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	processData, _ := exec.CommandContext(ctx, "ps", "-eo", "comm=,rss=", "--sort=-rss").Output()

	memory := parseIncidentMemory(string(memoryData))
	load := strings.Fields(string(loadData))
	parts := make([]string, 0, 3)
	if memory.total > 0 {
		parts = append(parts, fmt.Sprintf("内存 %s/%s", formatIncidentBytes(memory.total-memory.available), formatIncidentBytes(memory.total)))
		if memory.swapTotal > 0 {
			parts = append(parts, fmt.Sprintf("Swap %s/%s", formatIncidentBytes(memory.swapTotal-memory.swapFree), formatIncidentBytes(memory.swapTotal)))
		} else {
			parts = append(parts, "Swap 未配置")
		}
	}
	if len(load) > 0 {
		parts = append(parts, "负载 "+load[0])
	}

	if processes := parseIncidentProcesses(string(processData), 3); len(processes) > 0 {
		items := make([]string, 0, len(processes))
		for _, process := range processes {
			items = append(items, process.name+" "+formatIncidentBytes(process.rss))
		}
		parts = append(parts, "内存占用较高："+strings.Join(items, "、"))
	}
	if len(parts) == 0 {
		return ""
	}
	return "当时资源：" + strings.Join(parts, "，")
}

func parseIncidentMemory(data string) incidentMemory {
	var memory incidentMemory
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		value *= 1024
		switch fields[0] {
		case "MemTotal:":
			memory.total = value
		case "MemAvailable:":
			memory.available = value
		case "SwapTotal:":
			memory.swapTotal = value
		case "SwapFree:":
			memory.swapFree = value
		}
	}
	return memory
}

func parseIncidentProcesses(data string, limit int) []incidentProcess {
	totals := make(map[string]int64)
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		rssKB, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || rssKB <= 0 {
			continue
		}
		totals[oomProcessLabel(fields[0])] += rssKB * 1024
	}
	processes := make([]incidentProcess, 0, len(totals))
	for name, rss := range totals {
		processes = append(processes, incidentProcess{name: name, rss: rss})
	}
	sort.Slice(processes, func(i, j int) bool {
		return processes[i].rss > processes[j].rss
	})
	if len(processes) > limit {
		processes = processes[:limit]
	}
	return processes
}

func formatIncidentBytes(bytes int64) string {
	const (
		mb = int64(1024 * 1024)
		gb = int64(1024 * 1024 * 1024)
	)
	if bytes < mb {
		return "<1 MB"
	}
	if bytes >= gb {
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(gb))
	}
	return fmt.Sprintf("%.0f MB", float64(bytes)/float64(mb))
}
