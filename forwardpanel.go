package main

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"serval/core/hosts"
	"serval/core/sshconn"
	"serval/core/vault"
	"serval/design"

	"github.com/egoist/mygo/ui"
)

// liveTunnel 是一条已经听上的转发。tunnel 的计数可以在转发协程里读，其余字段只在主线程用。
type liveTunnel struct {
	tunnel *sshconn.Tunnel
	tabID  int
}

// forwardPanel 是端口转发规则的列表面板。
type forwardPanel struct {
	open, fresh bool
	editing     bool
	id          string

	name, bindHost, bindPort, targetHost, targetPort string
	kind                                             int
	hostID                                           string
	err                                              string

	deleting *hosts.Forward
}

var forwardKinds = []string{hosts.ForwardLocal, hosts.ForwardRemote, hosts.ForwardDynamic}

func (a *app) openForwards() { a.forwardPanel = forwardPanel{open: true, fresh: true} }

func (a *app) openForwardAdd() {
	a.forwardPanel.editing = true
	a.forwardPanel.id = ""
	a.forwardPanel.err = ""
	a.forwardPanel.name = ""
	a.forwardPanel.bindHost = "127.0.0.1"
	a.forwardPanel.bindPort = ""
	a.forwardPanel.targetHost = ""
	a.forwardPanel.targetPort = ""
	a.forwardPanel.kind = 0
	a.forwardPanel.hostID = ""
}

func (a *app) editForward(f hosts.Forward) {
	p := &a.forwardPanel
	p.editing, p.id, p.err = true, f.ID, ""
	p.name, p.bindHost = f.Name, f.ListenHost()
	p.bindPort = strconv.Itoa(f.BindPort)
	p.targetHost = f.TargetHost
	if f.TargetPort > 0 {
		p.targetPort = strconv.Itoa(f.TargetPort)
	} else {
		p.targetPort = ""
	}
	p.kind = 0
	for i, k := range forwardKinds {
		if k == f.Kind {
			p.kind = i
		}
	}
	p.hostID = f.HostID
}

func (a *app) forwardByID(id string) (hosts.Forward, bool) {
	for _, f := range a.forwards {
		if f.ID == id {
			return f, true
		}
	}
	return hosts.Forward{}, false
}

func (a *app) saveForward() {
	p := &a.forwardPanel
	if p.kind < 0 || p.kind >= len(forwardKinds) {
		p.err = "请选择转发类型。"
		return
	}
	kind := forwardKinds[p.kind]
	bindHost, msg := normalizeForwardBind(p.bindHost)
	if msg != "" {
		p.err = msg
		return
	}
	port, err := strconv.Atoi(strings.TrimSpace(p.bindPort))
	if err != nil || port < 1 || port > 65535 {
		p.err = "监听端口必须是 1 到 65535 之间的数字。"
		return
	}
	targetHost := strings.TrimSpace(p.targetHost)
	var targetPort int
	if kind != hosts.ForwardDynamic {
		if targetHost == "" {
			p.err = "请填写目标地址。"
			return
		}
		targetPort, err = strconv.Atoi(strings.TrimSpace(p.targetPort))
		if err != nil || targetPort < 1 || targetPort > 65535 {
			p.err = "目标端口必须是 1 到 65535 之间的数字。"
			return
		}
	} else {
		targetHost = ""
	}
	if p.hostID != "" {
		if _, ok := a.hostByID(p.hostID); !ok {
			p.err = "绑定的主机已经删除，请重新选择。"
			return
		}
	}
	name := strings.TrimSpace(p.name)
	if name == "" {
		switch kind {
		case hosts.ForwardRemote:
			name = fmt.Sprintf("远程 %d", port)
		case hosts.ForwardDynamic:
			name = fmt.Sprintf("动态 %d", port)
		default:
			name = fmt.Sprintf("本地 %d", port)
		}
	}
	for _, f := range a.forwards {
		if f.Name == name && f.ID != p.id {
			p.err = fmt.Sprintf("已经有一条叫 %s 的转发，请换一个名称。", name)
			return
		}
	}
	f := hosts.Forward{
		ID: p.id, Name: name, Kind: kind, HostID: p.hostID,
		BindHost: bindHost, BindPort: port,
		TargetHost: targetHost, TargetPort: targetPort,
	}
	if f.ID == "" {
		f.ID = vault.NewID()
		p.id = f.ID
	}
	live := a.tunnels[f.ID]
	if err := a.apply(forwardChange(f)); err != nil {
		p.err = "没能保存这条转发。"
		return
	}
	p.editing, p.err = false, ""
	if live != nil {
		// 先等旧监听放开端口再开新的，否则同一个端口会报占用。
		a.restartForward(f.ID, live)
		return
	}
	a.ensureForward(f)
}

func normalizeForwardBind(s string) (string, string) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "localhost") {
		return "127.0.0.1", ""
	}
	if net.ParseIP(s) == nil {
		return "", "监听地址请填 IP，例如 127.0.0.1。留空则只在本机访问。"
	}
	return s, ""
}

func (a *app) deleteForward(f hosts.Forward) {
	a.stopForward(f.ID)
	if err := a.apply(vault.Change{ID: f.ID, Delete: true}); err != nil {
		a.forwardPanel.err = "没能删除这条转发。"
	}
}

func (a *app) toggleForward(f hosts.Forward) {
	if _, ok := a.tunnels[f.ID]; ok {
		a.stopForward(f.ID)
		return
	}
	t := a.tabForForward(f)
	if t == nil {
		if f.HostID != "" {
			if h, ok := a.hostByID(f.HostID); ok {
				a.forwardErr[f.ID] = "先连接 " + h.Name + "，这条转发会自动开启。"
				return
			}
		}
		a.forwardErr[f.ID] = "先连接一台主机，再启动这条转发。"
		return
	}
	a.startOn(f, t)
}

func (a *app) tabByID(id int) *tab {
	for _, t := range a.tabs {
		if t.id == id {
			return t
		}
	}
	return nil
}

func (a *app) tabForHost(hostID string) *tab {
	if hostID == "" {
		return nil
	}
	for _, t := range a.tabs {
		if t.local || t.client == nil || t.reconnecting || t.host.ID != hostID {
			continue
		}
		return t
	}
	return nil
}

func (a *app) tabForForward(f hosts.Forward) *tab {
	if f.HostID != "" {
		return a.tabForHost(f.HostID)
	}
	if t := a.current(); t != nil && !t.local && t.client != nil && !t.reconnecting {
		return t
	}
	return nil
}

// ensureForward 在绑定的主机已经连着时把规则听起来。没绑定主机的规则只手动启动。
func (a *app) ensureForward(f hosts.Forward) {
	if f.HostID == "" {
		return
	}
	if _, ok := a.tunnels[f.ID]; ok {
		return
	}
	a.startOn(f, a.tabForHost(f.HostID))
}

func (a *app) syncHostForwards(hostID string) {
	if hostID == "" {
		return
	}
	for _, f := range a.forwards {
		if f.HostID == hostID {
			a.ensureForward(f)
		}
	}
}

func (a *app) startOn(f hosts.Forward, t *tab) {
	if t == nil || t.client == nil {
		return
	}
	if _, ok := a.tunnels[f.ID]; ok {
		return
	}
	var tun *sshconn.Tunnel
	var err error
	switch f.Kind {
	case hosts.ForwardRemote:
		tun, err = t.client.ListenRemote(f.ListenHost(), f.BindPort, f.TargetHost, f.TargetPort)
	case hosts.ForwardDynamic:
		tun, err = t.client.ListenDynamic(f.ListenHost(), f.BindPort)
	default:
		tun, err = t.client.ListenLocal(f.ListenHost(), f.BindPort, f.TargetHost, f.TargetPort)
	}
	if err != nil {
		a.forwardErr[f.ID] = forwardStartError(err)
		return
	}
	delete(a.forwardErr, f.ID)
	a.tunnels[f.ID] = &liveTunnel{tunnel: tun, tabID: t.id}
}

func forwardStartError(err error) string {
	switch {
	case errors.Is(err, sshconn.ErrForwardDenied):
		return "这台服务器不允许在那边打开这个端口。"
	case errors.Is(err, sshconn.ErrForwardInUse):
		return "这个端口已经有程序在用，请换一个。"
	default:
		return "没能启动转发，请检查地址和端口后重试。"
	}
}

func (a *app) stopForward(id string) {
	lives := a.takeTunnels(id, 0)
	if len(lives) == 0 {
		return
	}
	// 远程转发的关闭要等服务器确认，不放在主线程。
	go closeLiveTunnels(lives)
}

// takeTunnels 把监听从地图摘下来。id 非空时只摘这一条；否则 tabID 为 0 摘全部，非 0 摘这个标签上的。
// 调用方要在关掉对应的 SSH 连接之前把它们关掉，远程转发的取消请求才能从这条连接发出去。
func (a *app) takeTunnels(id string, tabID int) []*liveTunnel {
	var lives []*liveTunnel
	for key, live := range a.tunnels {
		if id != "" && key != id {
			continue
		}
		if id == "" && tabID != 0 && live.tabID != tabID {
			continue
		}
		lives = append(lives, live)
		delete(a.tunnels, key)
		delete(a.forwardErr, key)
	}
	return lives
}

// closeLiveTunnels 关掉监听。远程转发要等服务器确认，最多等 2 秒：
// 确认到了再让调用方关 SSH 连接；超时也返回，避免取消请求卡住时会话关不掉。
func closeLiveTunnels(lives []*liveTunnel) {
	if len(lives) == 0 {
		return
	}
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		wg.Add(len(lives))
		for _, live := range lives {
			go func(live *liveTunnel) {
				defer wg.Done()
				if live != nil && live.tunnel != nil {
					live.tunnel.Close()
				}
			}(live)
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// restartForward 关掉正在听的规则，端口释放后再按保险库里的最新内容打开。
func (a *app) restartForward(id string, live *liveTunnel) {
	tabID := live.tabID
	delete(a.tunnels, id)
	delete(a.forwardErr, id)
	go func() {
		closeLiveTunnels([]*liveTunnel{live})
		a.post(func() {
			current, ok := a.forwardByID(id)
			if !ok || a.tunnels[id] != nil {
				return
			}
			if current.HostID != "" {
				a.ensureForward(current)
				return
			}
			a.startOn(current, a.tabByID(tabID))
		})
	}()
}

func (a *app) stopTunnelsForTab(tabID int, done func()) {
	lives := a.takeTunnels("", tabID)
	if len(lives) == 0 {
		if done != nil {
			done()
		}
		return
	}
	go func() {
		closeLiveTunnels(lives)
		if done != nil {
			a.post(done)
		}
	}()
}

func (a *app) stopAllTunnels() {
	lives := a.takeTunnels("", 0)
	if len(lives) == 0 {
		return
	}
	go closeLiveTunnels(lives)
}

func (a *app) dropStaleTunnels() {
	for id := range a.tunnels {
		if _, ok := a.forwardByID(id); !ok {
			a.stopForward(id)
		}
	}
}

func (a *app) forwardStatus(f hosts.Forward) string {
	if live := a.tunnels[f.ID]; live != nil && live.tunnel != nil {
		in, out := live.tunnel.Bytes()
		text := "监听 " + live.tunnel.Addr()
		if in+out > 0 {
			text += fmt.Sprintf("  下行 %s  上行 %s", formatTransfer(in), formatTransfer(out))
		}
		if msg := live.tunnel.Err(); msg != "" {
			text += "  " + msg
		}
		return text
	}
	if msg := a.forwardErr[f.ID]; msg != "" {
		return msg
	}
	if f.HostID != "" {
		if h, ok := a.hostByID(f.HostID); ok {
			return "连接 " + h.Name + " 后自动开启"
		}
		return "绑定的主机已经删除"
	}
	return "未启动"
}

func formatTransfer(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func (a *app) forwardsDialog(c *ui.Context, colors design.Colors) {
	p := &a.forwardPanel
	if !p.open || a.setup.open {
		return
	}
	sheet(c, colors, &p.open, &p.fresh, 520, 22, func() {
		if p.editing {
			a.forwardEditPage(c)
		} else {
			a.forwardListPage(c, colors)
		}
	})
	if !p.open {
		a.forwardPanel = forwardPanel{}
	}
	a.forwardDeleteDialog(c)
}

func (a *app) forwardListPage(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	p := &a.forwardPanel
	ui.Column(c).Gap(14).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "端口转发").FontSize(design.TextHeading).FontWeight(600).Grow(1)
			ui.PrimaryButton(c, "添加转发").Key("forwardsAdd").OnClick(a.openForwardAdd)
		})
		if len(a.forwards) == 0 {
			ui.Column(c).Gap(6).Padding(12, 0).Children(func() {
				ui.Text(c, "还没有端口转发").FontWeight(600)
				ui.Text(c, "本地转发把本机端口接到服务器能访问的地址。远程转发在服务器上监听。动态转发是本机的 SOCKS5。").
					FontSize(design.TextSmall).TextColor(t.TextMuted)
			})
		}
		ui.Scroll(c).MaxHeight(360).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				for _, f := range a.forwards {
					a.forwardRow(c, colors, f)
				}
			})
		})
		if p.err != "" {
			ui.Text(c, p.err).TextColor(t.Danger)
		}
		ui.Row(c).Justify(ui.End).Children(func() {
			ui.Button(c, "完成").Key("forwardsClose").OnClick(func() { a.forwardPanel = forwardPanel{} })
		})
	})
}

func (a *app) forwardRow(c *ui.Context, colors design.Colors, f hosts.Forward) {
	t := c.Theme()
	_, running := a.tunnels[f.ID]
	verb := "启动 "
	if running {
		verb = "停止 "
	}
	line := ui.Row(c).Key("forwardline:"+f.ID).AlignItems(ui.Center).Gap(6).Padding(0, 4, 0, 0).Radius(design.RadiusRow).Transition(design.Hover)
	if line.Hovered() {
		line.Background(colors.Hover)
	}
	line.Children(func() {
		row := ui.ButtonBase(c).Key("forward:"+f.ID).Label(f.Name).Grow(1).MinWidth(0).Padding(8, 10).Gap(10).
			Justify(ui.Start).AlignItems(ui.Center)
		row.Children(func() {
			ui.Icon(c, design.Icon("arrow-left-right")).FontSize(18).TextColor(t.TextMuted)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, f.Name).FontWeight(500).SingleLine()
				ui.Text(c, f.Summary()+"  "+a.forwardStatus(f)).FontSize(design.TextSmall).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
			})
		}).OnClick(func() { a.editForward(f) })
		ui.Button(c, verb+f.Name).Key("forwardRun:" + f.ID).OnClick(func() { a.toggleForward(f) })
		ui.Button(c, "删除 "+f.Name).Key("forwardDelete:" + f.ID).OnClick(func() { a.forwardPanel.deleting = &f })
	})
}

func (a *app) forwardEditPage(c *ui.Context) {
	t := c.Theme()
	p := &a.forwardPanel
	title := "添加转发"
	if p.id != "" {
		title = "编辑转发"
	}
	ui.Column(c).Gap(12).Children(func() {
		ui.Text(c, title).FontSize(design.TextHeading).FontWeight(600)
		ui.Field(c, "类型", func() {
			c.Key("forwardKind")
			ui.Segmented(c, &p.kind, "本地转发", "远程转发", "动态转发")
		}).Description("本地转发把本机端口接到服务器能访问的地址。远程转发在服务器上监听，接到本机。动态转发是本机的 SOCKS5，流量经这台服务器出去。")
		ui.Field(c, "名称", func() {
			c.Key("forwardName")
			ui.TextInput(c, &p.name).Placeholder("留空时按类型和端口生成")
		})
		ui.Row(c).Gap(10).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				ui.Field(c, "监听地址", func() {
					c.Key("forwardBindHost")
					ui.TextInput(c, &p.bindHost).Placeholder("127.0.0.1")
				})
			})
			ui.Column(c).Width(110).Children(func() {
				ui.Field(c, "监听端口", func() {
					c.Key("forwardBindPort")
					ui.TextInput(c, &p.bindPort).Placeholder("8080")
				})
			})
		})
		if forwardKinds[p.kind] != hosts.ForwardDynamic {
			ui.Row(c).Gap(10).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					ui.Field(c, "目标地址", func() {
						c.Key("forwardTargetHost")
						ui.TextInput(c, &p.targetHost).Placeholder("127.0.0.1")
					})
				})
				ui.Column(c).Width(110).Children(func() {
					ui.Field(c, "目标端口", func() {
						c.Key("forwardTargetPort")
						ui.TextInput(c, &p.targetPort).Placeholder("5432")
					})
				})
			})
		}
		ui.Field(c, "自动开启", func() {
			c.Key("forwardHost")
			ui.Scroll(c).MaxHeight(120).Children(func() {
				ui.Column(c).Gap(4).Children(func() {
					// 选中的那一个用主按钮，没选中的用普通按钮。不能两个都建，否则文字重复、点击会点错。
					if p.hostID == "" {
						ui.PrimaryButton(c, "不自动开启").Key("forwardHostNone").OnClick(func() { p.hostID = "" })
					} else {
						ui.Button(c, "不自动开启").Key("forwardHostNone").OnClick(func() { p.hostID = "" })
					}
					for _, h := range a.hosts {
						label := "绑定 " + h.Name
						hostID := h.ID
						if p.hostID == hostID {
							ui.PrimaryButton(c, label).Key("forwardHost:" + hostID).OnClick(func() { p.hostID = hostID })
						} else {
							ui.Button(c, label).Key("forwardHost:" + hostID).OnClick(func() { p.hostID = hostID })
						}
					}
				})
			})
		}).Description("绑定主机后，连上它就自动开启，最后一条相关会话断开后关闭。")
		if p.err != "" {
			ui.Text(c, p.err).Key("forwardError").TextColor(t.Danger)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			ui.Button(c, "返回").Key("forwardBack").OnClick(func() {
				p.editing, p.err = false, ""
			})
			ui.PrimaryButton(c, "保存转发").Key("forwardSubmit").OnClick(a.saveForward)
		})
	})
}

func (a *app) forwardDeleteDialog(c *ui.Context) {
	p := &a.forwardPanel
	if p.deleting == nil {
		return
	}
	f := *p.deleting
	open := true
	choice := ui.AlertDialog(c, &open, "删除转发 "+f.Name+"？", "删除后这条转发会停掉，规则也从保险库里去掉。", "删除", "取消")
	if choice == -1 && open {
		return
	}
	p.deleting = nil
	if choice == 0 {
		a.deleteForward(f)
	}
}
