package collector

import (
	"bytes"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

// 每分钟对象级资源采样：把进程 CPU 秒数和 RSS 按网站、公共服务和其他进程汇总，
// 供资源告警做初步归因。只读 /proc，不调用外部命令。

const (
	objectSampleRetention = 7 * 24 * time.Hour
	// Linux 用户态 /proc 时间单位（USER_HZ）在受支持的发行版上固定为 100。
	procClockTicks = 100
	// other 类按 CPU 和 RSS 各保留前 N 名，其余合并。
	otherObjectTopN = 5

	ObjectTypeSite    = "site"
	ObjectTypeService = "service"
	ObjectTypeOther   = "other"
	// OtherRestKey 是 other 类中被合并的其余进程。
	OtherRestKey = "_rest"
)

// ObjectSample 是一次采样区间内某个对象的汇总。
type ObjectSample struct {
	Type       string
	Key        string
	Label      string
	CPUSeconds float64
	RSSBytes   int64
}

// PIDObject 记录最近一次扫描中某个 PID 所属的对象。
type PIDObject struct {
	Type  string
	Key   string
	Label string
}

type procKey struct {
	pid       int
	startTime uint64
}

type procInfo struct {
	pid       int
	comm      string
	uid       uint32
	cpuTicks  uint64
	startTime uint64
	rssBytes  int64
}

type siteOwner struct {
	id     int64
	domain string
}

type objectSampler struct {
	procRoot   string
	baseline   map[procKey]uint64
	lastUptime float64
	lastScan   time.Time
	pageSize   int64
}

var (
	defaultObjectSampler = &objectSampler{procRoot: "/proc", pageSize: int64(os.Getpagesize())}

	pidObjectsMu sync.RWMutex
	pidObjects   map[int]PIDObject
	// siteProcCounts 是最近一次扫描中每个网站（site_id）的进程数；整体替换，替换后不再修改。
	siteProcCounts map[string]int
	// samplingStartedAt 是本次面板启动后首次扫描（建立基线）的时刻。
	samplingStartedAt time.Time
)

// SamplingStartedAt 返回本次面板启动后对象级采样的起点；尚未扫描时为零值。
// 早于该时刻的样本来自上一次运行。
func SamplingStartedAt() time.Time {
	pidObjectsMu.RLock()
	defer pidObjectsMu.RUnlock()
	return samplingStartedAt
}

// SiteProcessCounts 返回最近一次扫描中各网站（按 site_id）的进程数副本。
func SiteProcessCounts() map[string]int {
	pidObjectsMu.RLock()
	defer pidObjectsMu.RUnlock()
	counts := make(map[string]int, len(siteProcCounts))
	for key, n := range siteProcCounts {
		counts[key] = n
	}
	return counts
}

// LookupPIDObject 返回最近一次扫描中该 PID 所属的对象；
// 两次扫描之间启动又退出的进程查不到。
func LookupPIDObject(pid int) (PIDObject, bool) {
	pidObjectsMu.RLock()
	defer pidObjectsMu.RUnlock()
	obj, ok := pidObjects[pid]
	return obj, ok
}

var serviceLabels = map[string]string{
	"mariadbd":     "MariaDB",
	"mysqld":       "MariaDB",
	"redis-server": "Redis",
	"nginx":        "Nginx",
}

func classifyProcess(p procInfo, owners map[uint32]siteOwner) PIDObject {
	if owner, ok := owners[p.uid]; ok {
		return PIDObject{Type: ObjectTypeSite, Key: strconv.FormatInt(owner.id, 10), Label: owner.domain}
	}
	if label, ok := serviceLabels[p.comm]; ok {
		return PIDObject{Type: ObjectTypeService, Key: label, Label: label}
	}
	if strings.HasPrefix(p.comm, "php-fpm") {
		// root 属主的 master 或未映射到网站的池属于共享 PHP-FPM 服务。
		return PIDObject{Type: ObjectTypeService, Key: "PHP-FPM", Label: "PHP-FPM"}
	}
	return PIDObject{Type: ObjectTypeOther, Key: p.comm, Label: p.comm}
}

func collectObjectSamples() {
	db := database.GetDB()
	if db == nil {
		return
	}
	owners := loadSiteOwners()
	samples, start, end, ok := defaultObjectSampler.scan(owners, time.Now())
	if !ok || len(samples) == 0 {
		return
	}
	startText := start.UTC().Format("2006-01-02 15:04:05")
	endText := end.UTC().Format("2006-01-02 15:04:05")
	tx, err := db.Begin()
	if err != nil {
		log.Printf("对象资源采样写入失败: %v", err)
		return
	}
	for _, s := range samples {
		if _, err := tx.Exec(`INSERT INTO resource_object_samples
			(window_start, window_end, object_type, object_key, label, cpu_seconds, rss_bytes)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			startText, endText, s.Type, s.Key, s.Label, s.CPUSeconds, s.RSSBytes); err != nil {
			tx.Rollback()
			log.Printf("对象资源采样写入失败: %v", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("对象资源采样写入失败: %v", err)
	}
}

func loadSiteOwners() map[uint32]siteOwner {
	owners := make(map[uint32]siteOwner)
	rows, err := database.GetDB().Query(`SELECT id, domain, system_user FROM websites WHERE system_user != ''`)
	if err != nil {
		return owners
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var domain, systemUser string
		if rows.Scan(&id, &domain, &systemUser) != nil {
			continue
		}
		u, err := user.Lookup(systemUser)
		if err != nil {
			continue
		}
		uid, err := strconv.ParseUint(u.Uid, 10, 32)
		if err != nil {
			continue
		}
		owners[uint32(uid)] = siteOwner{id: id, domain: domain}
	}
	return owners
}

// scan 读取一次 /proc，与上一次扫描的基线相减。第一次扫描只建立基线，返回 ok=false。
func (s *objectSampler) scan(owners map[uint32]siteOwner, now time.Time) ([]ObjectSample, time.Time, time.Time, bool) {
	uptime, err := readUptime(s.procRoot)
	if err != nil {
		return nil, time.Time{}, time.Time{}, false
	}
	procs := readProcesses(s.procRoot, s.pageSize)
	if len(procs) == 0 {
		return nil, time.Time{}, time.Time{}, false
	}

	firstScan := s.baseline == nil
	lastUptimeTicks := uint64(s.lastUptime * procClockTicks)
	start := s.lastScan
	next := make(map[procKey]uint64, len(procs))
	objects := make(map[int]PIDObject, len(procs))
	procCounts := make(map[string]int)
	agg := make(map[string]*ObjectSample)

	for _, p := range procs {
		key := procKey{pid: p.pid, startTime: p.startTime}
		next[key] = p.cpuTicks
		obj := classifyProcess(p, owners)
		objects[p.pid] = obj
		if obj.Type == ObjectTypeSite {
			procCounts[obj.Key]++
		}
		if firstScan {
			continue
		}
		var delta uint64
		if prev, ok := s.baseline[key]; ok {
			if p.cpuTicks < prev {
				continue
			}
			delta = p.cpuTicks - prev
		} else if p.startTime > lastUptimeTicks {
			// 上次扫描之后才启动的进程，计入它的全部 CPU 时间。
			delta = p.cpuTicks
		} else {
			// 上次扫描时已存在但未读到（如读取失败），只建基线。
			continue
		}
		aggKey := obj.Type + "\x00" + obj.Key
		sample := agg[aggKey]
		if sample == nil {
			sample = &ObjectSample{Type: obj.Type, Key: obj.Key, Label: obj.Label}
			agg[aggKey] = sample
		}
		sample.CPUSeconds += float64(delta) / procClockTicks
		sample.RSSBytes += p.rssBytes
	}

	s.baseline = next
	s.lastUptime = uptime
	s.lastScan = now
	pidObjectsMu.Lock()
	pidObjects = objects
	siteProcCounts = procCounts
	if firstScan {
		samplingStartedAt = now
	}
	pidObjectsMu.Unlock()

	if firstScan || start.IsZero() {
		return nil, time.Time{}, time.Time{}, false
	}
	return limitOtherSamples(agg), start, now, true
}

// limitOtherSamples 保留全部网站与服务对象；other 类按 CPU、RSS 各保留前 N 名，
// 其余合并为一条，控制每分钟写入行数。
func limitOtherSamples(agg map[string]*ObjectSample) []ObjectSample {
	var result, others []ObjectSample
	for _, s := range agg {
		if s.CPUSeconds <= 0 && s.RSSBytes <= 0 {
			continue
		}
		if s.Type == ObjectTypeOther {
			others = append(others, *s)
		} else {
			result = append(result, *s)
		}
	}
	keep := make(map[string]bool)
	sort.Slice(others, func(i, j int) bool { return others[i].CPUSeconds > others[j].CPUSeconds })
	for i := 0; i < len(others) && i < otherObjectTopN; i++ {
		if others[i].CPUSeconds > 0 {
			keep[others[i].Key] = true
		}
	}
	sort.Slice(others, func(i, j int) bool { return others[i].RSSBytes > others[j].RSSBytes })
	for i := 0; i < len(others) && i < otherObjectTopN; i++ {
		keep[others[i].Key] = true
	}
	rest := ObjectSample{Type: ObjectTypeOther, Key: OtherRestKey, Label: "其他进程"}
	for _, s := range others {
		if keep[s.Key] {
			result = append(result, s)
			continue
		}
		rest.CPUSeconds += s.CPUSeconds
		rest.RSSBytes += s.RSSBytes
	}
	if rest.CPUSeconds > 0 || rest.RSSBytes > 0 {
		result = append(result, rest)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Type != result[j].Type {
			return result[i].Type < result[j].Type
		}
		return result[i].Key < result[j].Key
	})
	return result
}

func readUptime(root string) (float64, error) {
	data, err := os.ReadFile(filepath.Join(root, "uptime"))
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, os.ErrInvalid
	}
	return strconv.ParseFloat(fields[0], 64)
}

func readProcesses(root string, pageSize int64) []procInfo {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	procs := make([]procInfo, 0, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		statData, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			continue
		}
		p, ok := parseProcStat(statData, pageSize)
		if !ok {
			continue
		}
		statusData, err := os.ReadFile(filepath.Join(dir, "status"))
		if err != nil {
			continue
		}
		uid, ok := parseStatusUID(statusData)
		if !ok {
			continue
		}
		p.pid = pid
		p.uid = uid
		procs = append(procs, p)
	}
	return procs
}

// parseProcStat 解析 /proc/<pid>/stat。进程名可能包含空格和括号，以最后一个 ')' 分隔。
func parseProcStat(data []byte, pageSize int64) (procInfo, bool) {
	open := bytes.IndexByte(data, '(')
	closeIdx := bytes.LastIndexByte(data, ')')
	if open < 0 || closeIdx <= open {
		return procInfo{}, false
	}
	comm := string(data[open+1 : closeIdx])
	// 字段从 state（第 3 个）开始。
	fields := strings.Fields(string(data[closeIdx+1:]))
	const (
		idxUtime     = 14 - 3
		idxStime     = 15 - 3
		idxStartTime = 22 - 3
		idxRSS       = 24 - 3
	)
	if len(fields) <= idxRSS {
		return procInfo{}, false
	}
	utime, err1 := strconv.ParseUint(fields[idxUtime], 10, 64)
	stime, err2 := strconv.ParseUint(fields[idxStime], 10, 64)
	startTime, err3 := strconv.ParseUint(fields[idxStartTime], 10, 64)
	rssPages, err4 := strconv.ParseInt(fields[idxRSS], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return procInfo{}, false
	}
	if rssPages < 0 {
		rssPages = 0
	}
	return procInfo{comm: comm, cpuTicks: utime + stime, startTime: startTime, rssBytes: rssPages * pageSize}, true
}

func parseStatusUID(data []byte) (uint32, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		// 第一个是真实 UID；PHP-FPM worker 的真实与有效 UID 相同。
		uid, err := strconv.ParseUint(fields[1], 10, 32)
		if err != nil {
			return 0, false
		}
		return uint32(uid), true
	}
	return 0, false
}
