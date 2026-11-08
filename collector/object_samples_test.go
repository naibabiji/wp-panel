package collector

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

type fakeProc struct {
	pid       int
	comm      string
	uid       uint32
	cpuTicks  uint64
	startTime uint64
	rssPages  int64
}

func writeFakeProc(t *testing.T, root string, uptime float64, procs []fakeProc) {
	t.Helper()
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		os.RemoveAll(filepath.Join(root, e.Name()))
	}
	if err := os.WriteFile(filepath.Join(root, "uptime"), []byte(fmt.Sprintf("%.2f 100.00\n", uptime)), 0644); err != nil {
		t.Fatal(err)
	}
	for _, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(p.pid))
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		// 字段 3..24：state ppid pgrp session tty tpgid flags minflt cminflt majflt cmajflt
		// utime stime cutime cstime priority nice threads itrealvalue starttime vsize rss
		stat := fmt.Sprintf("%d (%s) S 1 1 1 0 -1 0 0 0 0 0 %d 0 0 0 20 0 1 0 %d 1000 %d 0 0\n",
			p.pid, p.comm, p.cpuTicks, p.startTime, p.rssPages)
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0644); err != nil {
			t.Fatal(err)
		}
		status := fmt.Sprintf("Name:\t%s\nUid:\t%d\t%d\t%d\t%d\n", p.comm, p.uid, p.uid, p.uid, p.uid)
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func sampleByKey(samples []ObjectSample) map[string]ObjectSample {
	m := make(map[string]ObjectSample)
	for _, s := range samples {
		m[s.Type+":"+s.Key] = s
	}
	return m
}

func TestParseProcStatHandlesSpacesAndParensInComm(t *testing.T) {
	data := []byte("42 (php-fpm: pool (a) b) S 1 1 1 0 -1 0 0 0 0 0 150 50 0 0 20 0 1 0 9000 1000 10 0 0\n")
	p, ok := parseProcStat(data, 4096)
	if !ok || p.comm != "php-fpm: pool (a) b" || p.cpuTicks != 200 || p.startTime != 9000 || p.rssBytes != 40960 {
		t.Fatalf("parseProcStat = %+v ok=%v", p, ok)
	}
}

func TestObjectSamplerFirstScanOnlyBuildsBaseline(t *testing.T) {
	root := t.TempDir()
	owners := map[uint32]siteOwner{1001: {id: 7, domain: "a.example"}}
	s := &objectSampler{procRoot: root, pageSize: 4096}
	// 长期运行的 worker 已累计大量 CPU，首次扫描不能把它算作一分钟内的占用。
	writeFakeProc(t, root, 1000, []fakeProc{{pid: 10, comm: "php-fpm8.3", uid: 1001, cpuTicks: 500000, startTime: 100, rssPages: 100}})
	if samples, _, _, ok := s.scan(owners, time.Unix(1000, 0)); ok || samples != nil {
		t.Fatalf("first scan returned samples %+v ok=%v", samples, ok)
	}
	if obj, ok := LookupPIDObject(10); !ok || obj.Type != ObjectTypeSite || obj.Label != "a.example" {
		t.Fatalf("pid lookup = %+v ok=%v", obj, ok)
	}
}

func TestObjectSamplerDeltasNewProcessesAndPIDReuse(t *testing.T) {
	root := t.TempDir()
	owners := map[uint32]siteOwner{1001: {id: 7, domain: "a.example"}}
	s := &objectSampler{procRoot: root, pageSize: 4096}
	writeFakeProc(t, root, 1000, []fakeProc{
		{pid: 10, comm: "php-fpm8.3", uid: 1001, cpuTicks: 500000, startTime: 100, rssPages: 100},
		{pid: 11, comm: "php-fpm8.3", uid: 0, cpuTicks: 9000, startTime: 50, rssPages: 10},
		{pid: 12, comm: "mariadbd", uid: 105, cpuTicks: 1000, startTime: 60, rssPages: 1000},
		{pid: 13, comm: "tar", uid: 0, cpuTicks: 300, startTime: 99000, rssPages: 5},
	})
	s.scan(owners, time.Unix(1000, 0))

	writeFakeProc(t, root, 1060, []fakeProc{
		{pid: 10, comm: "php-fpm8.3", uid: 1001, cpuTicks: 503000, startTime: 100, rssPages: 120}, // +30s
		{pid: 11, comm: "php-fpm8.3", uid: 0, cpuTicks: 9100, startTime: 50, rssPages: 10},        // +1s master
		{pid: 12, comm: "mariadbd", uid: 105, cpuTicks: 1500, startTime: 60, rssPages: 1000},      // +5s
		// PID 13 被复用：starttime 晚于上次扫描，计入全部 CPU 时间。
		{pid: 13, comm: "php", uid: 1001, cpuTicks: 400, startTime: 100500, rssPages: 5}, // +4s 站点 CLI
		// 上次扫描已存在但未读到的进程只建基线。
		{pid: 14, comm: "gzip", uid: 0, cpuTicks: 7000, startTime: 90000, rssPages: 5},
	})
	samples, start, end, ok := s.scan(owners, time.Unix(1060, 0))
	if !ok || !start.Equal(time.Unix(1000, 0)) || !end.Equal(time.Unix(1060, 0)) {
		t.Fatalf("scan ok=%v window=%v-%v", ok, start, end)
	}
	got := sampleByKey(samples)
	if site := got["site:7"]; site.CPUSeconds != 34 || site.Label != "a.example" || site.RSSBytes != 125*4096 {
		t.Fatalf("site sample = %+v", site)
	}
	if fpm := got["service:PHP-FPM"]; fpm.CPUSeconds != 1 {
		t.Fatalf("php-fpm master sample = %+v", fpm)
	}
	if db := got["service:MariaDB"]; db.CPUSeconds != 5 {
		t.Fatalf("mariadb sample = %+v", db)
	}
	if gz, ok := got["other:gzip"]; ok && gz.CPUSeconds != 0 {
		t.Fatalf("unseen old process counted CPU: %+v", gz)
	}

	// 计数回退（异常数据）时丢弃该进程本次样本。
	writeFakeProc(t, root, 1120, []fakeProc{
		{pid: 10, comm: "php-fpm8.3", uid: 1001, cpuTicks: 100, startTime: 100, rssPages: 120},
	})
	samples, _, _, ok = s.scan(owners, time.Unix(1120, 0))
	if !ok {
		t.Fatal("third scan not ok")
	}
	if site, found := sampleByKey(samples)["site:7"]; found {
		t.Fatalf("negative delta produced sample %+v", site)
	}
}

func TestLimitOtherSamplesMergesRest(t *testing.T) {
	agg := map[string]*ObjectSample{
		"site": {Type: ObjectTypeSite, Key: "1", Label: "a", CPUSeconds: 1},
	}
	for i := 0; i < 12; i++ {
		key := fmt.Sprintf("p%02d", i)
		agg[key] = &ObjectSample{Type: ObjectTypeOther, Key: key, Label: key, CPUSeconds: float64(i + 1), RSSBytes: int64(100 - i)}
	}
	result := limitOtherSamples(agg)
	m := sampleByKey(result)
	if _, ok := m["site:1"]; !ok {
		t.Fatal("site object dropped")
	}
	rest, ok := m["other:"+OtherRestKey]
	if !ok {
		t.Fatalf("rest not merged: %+v", result)
	}
	// CPU 前 5 为 p07..p11，RSS 前 5 为 p00..p04，其余 p05、p06 合并。
	if rest.CPUSeconds != 6+7 || len(result) != 1+10+1 {
		t.Fatalf("rest=%+v len=%d", rest, len(result))
	}
}

func TestObjectSamplerRecordsSiteProcessCountsAndSamplingStart(t *testing.T) {
	root := t.TempDir()
	owners := map[uint32]siteOwner{1001: {id: 7, domain: "a.example"}}
	s := &objectSampler{procRoot: root, pageSize: 4096}
	writeFakeProc(t, root, 1000, []fakeProc{
		{pid: 10, comm: "php-fpm8.3", uid: 1001, cpuTicks: 100, startTime: 100, rssPages: 10},
		{pid: 11, comm: "php", uid: 1001, cpuTicks: 100, startTime: 100, rssPages: 10},
		{pid: 12, comm: "php-fpm8.3", uid: 0, cpuTicks: 100, startTime: 50, rssPages: 10},
	})
	first := time.Unix(1000, 0)
	s.scan(owners, first)
	// 第一次扫描只建基线，但已提供进程数与采样起点。
	if counts := SiteProcessCounts(); counts["7"] != 2 || len(counts) != 1 {
		t.Fatalf("counts after first scan = %v", counts)
	}
	if !SamplingStartedAt().Equal(first) {
		t.Fatalf("sampling start = %v, want %v", SamplingStartedAt(), first)
	}

	writeFakeProc(t, root, 1060, []fakeProc{
		{pid: 10, comm: "php-fpm8.3", uid: 1001, cpuTicks: 200, startTime: 100, rssPages: 10},
	})
	s.scan(owners, time.Unix(1060, 0))
	counts := SiteProcessCounts()
	if counts["7"] != 1 {
		t.Fatalf("counts after second scan = %v", counts)
	}
	// 采样起点只在首次扫描时设置；返回的是副本。
	if !SamplingStartedAt().Equal(first) {
		t.Fatalf("sampling start moved to %v", SamplingStartedAt())
	}
	counts["7"] = 99
	if SiteProcessCounts()["7"] != 1 {
		t.Fatal("SiteProcessCounts must return a copy")
	}
}
