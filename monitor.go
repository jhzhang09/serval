package main

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"

	"serval/core/hosts"
	"serval/core/sshconn"
)

// ServerStats 记录远程服务器的轻量监控指标。
type ServerStats struct {
	CPUPercent       float32   // CPU 总使用率 (0.0 ~ 100.0)
	Load1            float32   // 1 分钟平均负载
	Load5            float32   // 5 分钟平均负载
	MemUsedBytes     uint64    // 内存已用字节数
	MemTotalBytes    uint64    // 内存总量字节数
	DiskUsedBytes    uint64    // 根分区已用字节数
	DiskTotalBytes   uint64    // 根分区总量字节数
	NetRxBytesPerSec uint64    // 网络下行瞬时速率 (bytes/s)
	NetTxBytesPerSec uint64    // 网络上行瞬时速率 (bytes/s)
	UpdatedAt        time.Time // 最后更新时间
}

type statSample struct {
	cpuTotal uint64
	cpuIdle  uint64
	netRx    uint64
	netTx    uint64
	at       time.Time
}

// formatBytes 以易读方式格式化字节数（如 512 MB、2.4 GB）。
func formatBytes(b uint64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
		tb = 1024 * gb
	)
	switch {
	case b >= tb:
		return fmt.Sprintf("%.1f TB", float64(b)/float64(tb))
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%d MB", b/mb)
	case b >= kb:
		return fmt.Sprintf("%d KB", b/kb)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// formatRate 以易读方式格式化瞬时速率（如 12 KB/s、1.5 MB/s）。
func formatRate(bps uint64) string {
	return formatBytes(bps) + "/s"
}

// parseProcMetrics 解析针对 Linux / BSD 综合输出的探针数据。
func parseProcMetrics(raw string, prev statSample, now time.Time) (ServerStats, statSample) {
	var (
		cur     statSample
		stats   ServerStats
		memFree uint64
		buffers uint64
		cached  uint64
	)
	cur.at = now
	stats.UpdatedAt = now

	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)

		switch {
		// /proc/stat CPU 行：cpu user nice system idle iowait irq softirq steal guest guest_nice
		case len(fields) >= 5 && fields[0] == "cpu":
			var total, idle uint64
			for i := 1; i < len(fields); i++ {
				val, _ := strconv.ParseUint(fields[i], 10, 64)
				total += val
				if i == 4 || i == 5 { // idle and iowait
					idle += val
				}
			}
			cur.cpuTotal, cur.cpuIdle = total, idle
			if prev.cpuTotal > 0 && total > prev.cpuTotal {
				dTotal := total - prev.cpuTotal
				dIdle := uint64(0)
				if idle >= prev.cpuIdle {
					dIdle = idle - prev.cpuIdle
				}
				if dTotal > 0 && dTotal >= dIdle {
					stats.CPUPercent = float32(dTotal-dIdle) * 100.0 / float32(dTotal)
				}
			}

		// /proc/meminfo
		case strings.HasPrefix(line, "MemTotal:"):
			if len(fields) >= 2 {
				kbVal, _ := strconv.ParseUint(fields[1], 10, 64)
				stats.MemTotalBytes = kbVal * 1024
			}
		case strings.HasPrefix(line, "MemAvailable:"):
			if len(fields) >= 2 {
				kbVal, _ := strconv.ParseUint(fields[1], 10, 64)
				memFree = kbVal * 1024
			}
		case strings.HasPrefix(line, "MemFree:"):
			if memFree == 0 && len(fields) >= 2 {
				kbVal, _ := strconv.ParseUint(fields[1], 10, 64)
				memFree = kbVal * 1024
			}
		case strings.HasPrefix(line, "Buffers:"):
			if len(fields) >= 2 {
				kbVal, _ := strconv.ParseUint(fields[1], 10, 64)
				buffers = kbVal * 1024
			}
		case strings.HasPrefix(line, "Cached:"):
			if len(fields) >= 2 {
				kbVal, _ := strconv.ParseUint(fields[1], 10, 64)
				cached = kbVal * 1024
			}

		// /proc/net/dev 接口流量
		case strings.Contains(line, ":"):
			if !strings.HasPrefix(fields[0], "lo:") && !strings.HasPrefix(line, "lo:") {
				parts := strings.Split(line, ":")
				if len(parts) == 2 {
					netFields := strings.Fields(parts[1])
					if len(netFields) >= 9 {
						rx, _ := strconv.ParseUint(netFields[0], 10, 64)
						tx, _ := strconv.ParseUint(netFields[8], 10, 64)
						cur.netRx += rx
						cur.netTx += tx
					}
				}
			}

		// df -k / 根目录磁盘信息
		case (strings.HasPrefix(line, "/") || strings.Contains(line, "root")) && len(fields) >= 6:
			// Filesystem 1K-blocks Used Available Use% Mounted on
			totalBlocks, _ := strconv.ParseUint(fields[1], 10, 64)
			usedBlocks, _ := strconv.ParseUint(fields[2], 10, 64)
			if totalBlocks > 0 {
				stats.DiskTotalBytes = totalBlocks * 1024
				stats.DiskUsedBytes = usedBlocks * 1024
			}

		// loadavg 独立一行（3 个浮点数）
		case len(fields) >= 3 && !strings.Contains(line, ":"):
			l1, err1 := strconv.ParseFloat(fields[0], 32)
			l5, err2 := strconv.ParseFloat(fields[1], 32)
			if err1 == nil && err2 == nil && l1 >= 0 && l5 >= 0 && stats.Load1 == 0 {
				stats.Load1 = float32(l1)
				stats.Load5 = float32(l5)
			}
		}
	}

	// 内存计算
	if stats.MemTotalBytes > 0 {
		if memFree == 0 && (buffers > 0 || cached > 0) {
			memFree = buffers + cached
		}
		if stats.MemTotalBytes >= memFree {
			stats.MemUsedBytes = stats.MemTotalBytes - memFree
		}
	}

	// 网络速率计算
	if !prev.at.IsZero() && now.After(prev.at) {
		durationSec := now.Sub(prev.at).Seconds()
		if durationSec > 0.1 {
			if cur.netRx >= prev.netRx {
				stats.NetRxBytesPerSec = uint64(float64(cur.netRx-prev.netRx) / durationSec)
			}
			if cur.netTx >= prev.netTx {
				stats.NetTxBytesPerSec = uint64(float64(cur.netTx-prev.netTx) / durationSec)
			}
		}
	}

	return stats, cur
}

// monitorScript 是向远端发送的单次综合探针命令。
const monitorScript = `cat /proc/stat /proc/meminfo /proc/loadavg /proc/net/dev 2>/dev/null; df -k / 2>/dev/null`

// toggleMonitor 开启或关闭指定标签的实时服务器监控。
func (a *app) toggleMonitor(t *tab) {
	if t == nil || t.local || t.host.EffectiveProtocol() != hosts.ProtocolSSH {
		return
	}
	t.monitorOpen = !t.monitorOpen
	if t.host.ID != "" {
		next := a.local
		m := make(map[string]bool, len(next.HostMonitor)+1)
		for k, v := range next.HostMonitor {
			m[k] = v
		}
		m[t.host.ID] = t.monitorOpen
		next.HostMonitor = m
		_ = a.saveLocal(next)
	}
	if t.monitorOpen {
		a.startTabMonitor(t)
	} else {
		a.stopTabMonitor(t)
	}
}

func (a *app) startTabMonitor(t *tab) {
	if t.client == nil || t.monitorStop != nil {
		return
	}
	stop := make(chan struct{})
	t.monitorStop = stop

	go func(client *sshconn.Client) {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()

		var sample statSample
		// 首次先采一次基准数据
		if out, err := client.Output(monitorScript); err == nil {
			now := time.Now()
			stats, next := parseProcMetrics(string(out), sample, now)
			sample = next
			a.post(func() {
				if t.monitorOpen {
					t.stats = stats
				}
			})
		}

		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				out, err := client.Output(monitorScript)
				if err != nil {
					continue
				}
				stats, next := parseProcMetrics(string(out), sample, now)
				sample = next
				a.post(func() {
					if t.monitorOpen {
						t.stats = stats
					}
				})
			}
		}
	}(t.client)
}

func (a *app) stopTabMonitor(t *tab) {
	if t.monitorStop != nil {
		close(t.monitorStop)
		t.monitorStop = nil
	}
	t.stats = ServerStats{}
}
