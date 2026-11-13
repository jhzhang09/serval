package main

import (
	"testing"
	"time"
)

func TestParseProcMetrics(t *testing.T) {
	sampleProc1 := `cpu  1000 0 200 8800 0 0 0 0 0 0
MemTotal:        8192000 kB
MemFree:         2048000 kB
MemAvailable:    6144000 kB
0.25 0.30 0.35 1/200 12345
Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 100000       0    0    0    0     0          0         0   100000       0    0    0    0     0       0          0
  eth0: 5000000     100    0    0    0     0          0         0  2000000     100    0    0    0     0       0          0
/dev/sda1       104857600  20971520  83886080  20% /
`
	t0 := time.Now()
	stats1, sample1 := parseProcMetrics(sampleProc1, statSample{}, t0)

	// 第一次采样没有前值，CPU % 和网络速率为 0，但总内存、已用内存和磁盘应正常解析
	if stats1.MemTotalBytes != 8192000*1024 {
		t.Fatalf("MemTotalBytes got %d, want %d", stats1.MemTotalBytes, 8192000*1024)
	}
	wantUsedMem := (8192000 - 6144000) * 1024
	if stats1.MemUsedBytes != uint64(wantUsedMem) {
		t.Fatalf("MemUsedBytes got %d, want %d", stats1.MemUsedBytes, wantUsedMem)
	}
	if stats1.DiskTotalBytes != 104857600*1024 || stats1.DiskUsedBytes != 20971520*1024 {
		t.Fatalf("Disk metrics mismatch: %+v", stats1)
	}
	if stats1.Load1 != 0.25 || stats1.Load5 != 0.30 {
		t.Fatalf("Load avg mismatch: %f, %f", stats1.Load1, stats1.Load5)
	}

	// 第二次采样（1 秒后）：CPU 增加了 200 user, 800 idle（总 1000，非 idle 200 -> 20%）；eth0 rx +100000，tx +50000
	sampleProc2 := `cpu  1200 0 200 9600 0 0 0 0 0 0
MemTotal:        8192000 kB
MemAvailable:    6144000 kB
0.28 0.31 0.35 1/200 12345
  eth0: 5100000     110    0    0    0     0          0         0  2050000     110    0    0    0     0       0          0
/dev/sda1       104857600  20971520  83886080  20% /
`
	t1 := t0.Add(1 * time.Second)
	stats2, _ := parseProcMetrics(sampleProc2, sample1, t1)

	if stats2.CPUPercent < 19.9 || stats2.CPUPercent > 20.1 {
		t.Fatalf("CPUPercent got %f, want ~20.0", stats2.CPUPercent)
	}
	if stats2.NetRxBytesPerSec != 100000 {
		t.Fatalf("NetRxBytesPerSec got %d, want 100000", stats2.NetRxBytesPerSec)
	}
	if stats2.NetTxBytesPerSec != 50000 {
		t.Fatalf("NetTxBytesPerSec got %d, want 50000", stats2.NetTxBytesPerSec)
	}
}

func TestFormatBytesAndRate(t *testing.T) {
	if got := formatBytes(500); got != "500 B" {
		t.Fatalf("got %q", got)
	}
	if got := formatBytes(2048); got != "2 KB" {
		t.Fatalf("got %q", got)
	}
	if got := formatBytes(100 * 1024 * 1024); got != "100 MB" {
		t.Fatalf("got %q", got)
	}
	if got := formatBytes(2560 * 1024 * 1024); got != "2.5 GB" {
		t.Fatalf("got %q", got)
	}
	if got := formatRate(1024 * 1024); got != "1 MB/s" {
		t.Fatalf("got %q", got)
	}
}

func TestToggleMonitor(t *testing.T) {
	a := &app{}
	tb := &tab{local: false}
	a.toggleMonitor(tb)
	if !tb.monitorOpen {
		t.Fatal("第一次调用应开启监控")
	}
	a.toggleMonitor(tb)
	if tb.monitorOpen {
		t.Fatal("再次调用应关闭监控")
	}

	// 本地终端不允许开启监控
	localTab := &tab{local: true}
	a.toggleMonitor(localTab)
	if localTab.monitorOpen {
		t.Fatal("本地终端不应开启服务器监控")
	}
}

func TestDefaultMonitorEnabledByDefault(t *testing.T) {
	h := newHarness(t)
	if !h.a.shouldAutoMonitor("any-host") {
		t.Fatal("新配置默认应开启服务器监控")
	}

	h.a.openSettings()
	h.tt.Frame()
	h.click("新建 SSH 会话时默认开启顶栏监控")
	h.tt.Frame()
	if !h.a.local.DisableDefaultMonitor {
		t.Fatal("取消勾选后应保存 DisableDefaultMonitor=true")
	}
	if h.a.shouldAutoMonitor("any-host") {
		t.Fatal("关闭默认开启后 shouldAutoMonitor 应返回 false")
	}
}
