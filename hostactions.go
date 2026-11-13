package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"serval/core/hosts"
	"serval/core/keys"
	"serval/core/serial"
	"serval/core/sshconn"
	"serval/locales"

	"github.com/egoist/mygo/ui"
)

type probeState int

const (
	probeUnknown probeState = iota
	probeChecking
	probeOK
	probeFail
)

// hostHealth 记录某台主机最近一次异步连通性探测结果。只在主线程读写。
type hostHealth struct {
	state   probeState
	latency time.Duration
	err     string
	at      time.Time
}

// probeTarget 返回主机在本地应探测的 TCP 地址（若主机通过跳板机或代理，探测链路的第一跳/代理入口）。
func (a *app) probeTarget(h hosts.Host) (addr string, ok bool) {
	switch h.EffectiveProtocol() {
	case hosts.ProtocolSerial:
		return "", false
	case hosts.ProtocolTelnet:
		if strings.TrimSpace(h.Address) == "" {
			return "", false
		}
		port := h.Port
		if port <= 0 {
			port = 23
		}
		return net.JoinHostPort(h.Address, strconv.Itoa(port)), true
	default:
		entry := h
		if route, err := hosts.ResolveRoute(h, a.hosts, a.localUser); err == nil && len(route) > 0 {
			entry = route[0]
		}
		if hasProxy(entry) {
			if p, err := a.dialProxy(entry); err == nil && p != nil && p.Addr != "" {
				return p.Addr, true
			}
			return "", false
		}
		if hasProxy(h) {
			if p, err := a.dialProxy(h); err == nil && p != nil && p.Addr != "" {
				return p.Addr, true
			}
			return "", false
		}
		if strings.TrimSpace(entry.Address) == "" {
			return "", false
		}
		port := entry.Port
		if port <= 0 {
			port = 22
		}
		return net.JoinHostPort(entry.Address, strconv.Itoa(port)), true
	}
}

// probeAllHosts 在后台并发探测侧栏主机的 TCP 连通性与往返延迟（不阻断主线程，跳过串口主机）。
func (a *app) probeAllHosts(force bool) {
	if a.vault == nil || a.post == nil || len(a.hosts) == 0 {
		return
	}
	if a.probeCancel != nil {
		a.probeCancel()
		a.probeCancel = nil
	}
	if a.health == nil {
		a.health = map[string]hostHealth{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.probeCancel = cancel

	type job struct {
		id   string
		addr string
	}
	var jobs []job
	for _, h := range a.hosts {
		if h.EffectiveProtocol() == hosts.ProtocolSerial {
			if force {
				ports := serial.ListPorts()
				state := probeFail
				errText := "串口设备当前未检测到"
				if slices.Contains(ports, h.Address) {
					state = probeOK
					errText = ""
				}
				a.health[h.ID] = hostHealth{state: state, err: errText, at: time.Now()}
			}
			continue
		}
		addr, ok := a.probeTarget(h)
		if !ok {
			continue
		}
		if !force {
			if cur, exists := a.health[h.ID]; exists && cur.state != probeUnknown && time.Since(cur.at) < 30*time.Second {
				continue
			}
		}
		a.health[h.ID] = hostHealth{state: probeChecking, at: time.Now()}
		jobs = append(jobs, job{id: h.ID, addr: addr})
	}
	if len(jobs) == 0 {
		return
	}

	sem := make(chan struct{}, 6)
	for _, j := range jobs {
		go func(id, addr string) {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			start := time.Now()
			dialer := net.Dialer{Timeout: 1500 * time.Millisecond}
			conn, err := dialer.DialContext(ctx, "tcp", addr)
			rtt := time.Since(start)
			if conn != nil {
				_ = conn.Close()
			}
			a.post(func() {
				if ctx.Err() != nil {
					return
				}
				if a.health == nil {
					a.health = map[string]hostHealth{}
				}
				if err != nil {
					a.health[id] = hostHealth{state: probeFail, err: err.Error(), at: time.Now()}
					return
				}
				a.health[id] = hostHealth{state: probeOK, latency: rtt, at: time.Now()}
			})
		}(j.id, j.addr)
	}
}

// probeOneHost 单独检测一台主机的连通性并给出反馈。
func (a *app) probeOneHost(h hosts.Host) {
	if a.health == nil {
		a.health = map[string]hostHealth{}
	}
	if h.EffectiveProtocol() == hosts.ProtocolSerial {
		ports := serial.ListPorts()
		if slices.Contains(ports, h.Address) {
			a.health[h.ID] = hostHealth{state: probeOK, at: time.Now()}
			a.toast(ui.Toast{Title: fmt.Sprintf("%s 串口设备可用", h.Name)})
		} else {
			a.health[h.ID] = hostHealth{state: probeFail, err: "串口设备当前未检测到", at: time.Now()}
			a.toast(ui.Toast{Title: fmt.Sprintf("%s 串口未就绪", h.Name), Description: "未在系统中检测到 " + h.Address, Type: "error"})
		}
		return
	}
	addr, ok := a.probeTarget(h)
	if !ok {
		a.toast(ui.Toast{Title: fmt.Sprintf("无法检测 %s", h.Name), Description: "主机地址或代理配置不完整。", Type: "error"})
		return
	}
	a.health[h.ID] = hostHealth{state: probeChecking, at: time.Now()}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		start := time.Now()
		conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", addr)
		rtt := time.Since(start)
		if conn != nil {
			_ = conn.Close()
		}
		if a.post == nil {
			return
		}
		a.post(func() {
			if a.health == nil {
				a.health = map[string]hostHealth{}
			}
			if err != nil {
				a.health[h.ID] = hostHealth{state: probeFail, err: err.Error(), at: time.Now()}
				a.toast(ui.Toast{Title: fmt.Sprintf("%s 不可达", h.Name), Description: err.Error(), Type: "error"})
				return
			}
			a.health[h.ID] = hostHealth{state: probeOK, latency: rtt, at: time.Now()}
			a.toast(ui.Toast{Title: fmt.Sprintf("%s 可达 · %s", h.Name, formatLatency(rtt))})
		})
	}()
}

// sshCommandForHost 生成主机的等价命令行连接指令（用于菜单一键复制）。
func sshCommandForHost(h hosts.Host, all []hosts.Host, localUser string) string {
	switch h.EffectiveProtocol() {
	case hosts.ProtocolTelnet:
		port := h.Port
		if port <= 0 {
			port = 23
		}
		if port == 23 {
			return "telnet " + h.Address
		}
		return fmt.Sprintf("telnet %s %d", h.Address, port)
	case hosts.ProtocolSerial:
		return fmt.Sprintf("screen %s %d", h.Address, h.EffectiveBaudRate())
	}

	args := []string{"ssh"}
	if h.Port != 0 && h.Port != 22 {
		args = append(args, "-p", strconv.Itoa(h.Port))
	}
	if route, err := hosts.ResolveRoute(h, all, localUser); err == nil && len(route) > 0 {
		hops := make([]string, 0, len(route))
		for _, j := range route {
			spec := j.Address
			if j.User != "" {
				spec = j.User + "@" + spec
			}
			if j.Port != 0 && j.Port != 22 {
				spec += ":" + strconv.Itoa(j.Port)
			}
			hops = append(hops, spec)
		}
		args = append(args, "-J", strings.Join(hops, ","))
	} else if jumps := hosts.SplitJumps(h.ProxyJump); len(jumps) > 0 {
		args = append(args, "-J", strings.Join(jumps, ","))
	}
	target := h.Address
	user := h.User
	if user == "" {
		user = localUser
	}
	if user != "" {
		target = user + "@" + target
	}
	args = append(args, target)
	return strings.Join(args, " ")
}

// deployKeyToHost 将保险库里的公钥幂等地写入目标主机的 ~/.ssh/authorized_keys。
func (a *app) deployKeyToHost(h hosts.Host, k keys.Key) {
	if h.EffectiveProtocol() != hosts.ProtocolSSH {
		a.toast(ui.Toast{Title: "无法部署公钥", Description: "只有 SSH 主机支持公钥部署。", Type: "error"})
		return
	}
	if strings.TrimSpace(k.PublicKey) == "" {
		a.toast(ui.Toast{Title: "无法部署公钥", Description: "这把私钥没有关联可用的公钥。", Type: "error"})
		return
	}

	var existing *sshconn.Client
	for _, tb := range a.tabs {
		if !tb.local && tb.host.ID == h.ID && tb.client != nil {
			existing = tb.client
			break
		}
	}
	if existing != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			res, err := sshconn.DeployPublicKey(ctx, existing, k.PublicKey)
			a.post(func() { a.finishDeployKeyToast(h, k, res, err) })
		}()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cfg, _, _, _, err := a.prepareDialConfig(ctx, h, nil)
	if err != nil {
		cancel()
		a.toast(ui.Toast{Title: "部署公钥失败", Description: err.Error(), Type: "error"})
		return
	}
	a.toast(ui.Toast{Title: fmt.Sprintf("正在将 %s 部署到 %s…", k.Name, h.Name)})
	go func() {
		defer cancel()
		client, err := sshconn.Dial(ctx, cfg)
		if err != nil {
			a.post(func() { a.finishDeployKeyToast(h, k, sshconn.DeployPublicKeyResult{}, err) })
			return
		}
		defer client.Close()
		res, err := sshconn.DeployPublicKey(ctx, client, k.PublicKey)
		a.post(func() { a.finishDeployKeyToast(h, k, res, err) })
	}()
}

func (a *app) finishDeployKeyToast(h hosts.Host, k keys.Key, res sshconn.DeployPublicKeyResult, err error) {
	if err != nil {
		msg := err.Error()
		var changed *sshconn.HostKeyChangedError
		switch {
		case errors.As(err, &changed):
			msg = locales.AppHostKeyMismatch(h.Name, changed.Host)
		case errors.Is(err, sshconn.ErrHostKeyRejected):
			msg = locales.AppHostKeyNotTrusted(h.Name)
		case errors.Is(err, sshconn.ErrAuthFailed), errors.Is(err, sshconn.ErrNoAuthMethod),
			errors.Is(err, sshconn.ErrKeyEncrypted), errors.Is(err, sshconn.ErrBadPassphrase):
			msg = fmt.Sprintf("认证未通过：请先在主机配置中保存密码/私钥，或先打开一次 %s 的终端再部署。", h.Name)
		}
		a.toast(ui.Toast{Title: "部署公钥失败", Description: msg, Type: "error"})
		return
	}
	if res.AlreadyPresent {
		a.toast(ui.Toast{Title: fmt.Sprintf("%s 已存在公钥 %s", h.Name, k.Name)})
		return
	}
	a.toast(ui.Toast{Title: fmt.Sprintf("公钥 %s 已部署到 %s", k.Name, h.Name)})
}

// openSplit 从侧栏以左右或上下分屏方式打开主机。
// 若该主机已有打开的标签页则切过去并拆分新窗格；否则新建并排双窗格连接。
func (a *app) openSplit(h hosts.Host, vertical bool) {
	if h.EffectiveProtocol() != hosts.ProtocolSSH {
		a.open(h)
		return
	}
	for i, t := range a.tabs {
		if !t.local && t.host.ID == h.ID && t.client != nil {
			a.active = i
			a.split(t, vertical)
			return
		}
	}
	layout := &savedPane{
		Vertical: vertical,
		Percent:  50,
		First:    &savedPane{},
		Second:   &savedPane{},
	}
	a.connectWithLayout(h, nil, layout, 0)
}

// openFilesForHost 从侧栏直达主机的 SFTP 文件抽屉；尚未连接时先连接并在就绪后展开文件抽屉。
func (a *app) openFilesForHost(h hosts.Host) {
	if h.EffectiveProtocol() != hosts.ProtocolSSH {
		a.toast(ui.Toast{Title: "仅 SSH 主机支持 SFTP 文件抽屉"})
		return
	}
	for i, t := range a.tabs {
		if !t.local && t.host.ID == h.ID && t.client != nil {
			a.active = i
			a.filesOpen = true
			a.ensureFiles(t)
			a.saveSessionLayout()
			return
		}
	}
	a.openFilesOnHostID = h.ID
	a.connect(h, nil)
}

// parseTags 解析逗号分隔的标签字符串，去重、去空白并保持顺序。
func parseTags(raw string) []string {
	raw = strings.ReplaceAll(raw, "，", ",")
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		tag := strings.TrimSpace(p)
		if tag != "" && !slices.Contains(out, tag) {
			out = append(out, tag)
		}
	}
	return out
}

// formatTags 将标签切片格式化为逗号分隔文本。
func formatTags(tags []string) string {
	return strings.Join(tags, ", ")
}

// allTags 返回所有主机使用过的去重标签，按不区分大小写顺序排序。
func (a *app) allTags() []string {
	var tags []string
	for _, h := range a.hosts {
		for _, tag := range h.Tags {
			if tag != "" && !slices.Contains(tags, tag) {
				tags = append(tags, tag)
			}
		}
	}
	slices.SortFunc(tags, func(x, y string) int {
		if c := strings.Compare(strings.ToLower(x), strings.ToLower(y)); c != 0 {
			return c
		}
		return strings.Compare(x, y)
	})
	return tags
}
