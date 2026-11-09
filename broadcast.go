package main

import (
	"fmt"
	"slices"

	"serval/design"

	"github.com/egoist/mygo/ui"
)

// 广播把当前窗格的键盘输入同时送到选中的其他窗格，用来对多台机器做同样的操作。
//
// 远程窗格没有“按键”回调能覆盖测试里的 Type 和真实键盘：终端把按键编码后写进 Conn。
// zmodemConn.Write 成功写进 shell 之后，只有“当前焦点且已加入广播”的那个窗格会把字节投递回主线程。
// 其他窗格是用 Terminal.Send 收的，它们的 Write 不会再转发，否则 A 和 B 会互相复制直到栈满。
//
// 终端自己的应答（光标位置、设备属性、焦点、配色）也写进同一条 Conn，必须滤掉，不能当成用户输入。
// 本地终端没有 Conn，只能在 OnKey 里转发认得出的按键，并且返回 false，让本机 shell 照常收到。
// 输入法直接提交的文字不经过 OnKey，本地窗格做输入源时不会广播；远程窗格会转发实际写入的字节。

// broadcastAsk 是等待用户确认的生产环境窗格。
type broadcastAsk struct {
	tab  *tab
	pane *pane
}

// isTerminalReply 判断这段字节是不是终端自己写回程序的应答，而不是用户按的键。
//
// 方向键以 A/B/C/D/H/F 结尾，Kitty 键盘协议以 u 或 ~ 结尾，都不能当成应答丢掉。
// 单独的 ESC 是用户按的 Esc。光标报告、设备属性、状态报告的参数里才有数字。
func isTerminalReply(p []byte) bool {
	if len(p) == 0 || p[0] != 0x1b {
		return false
	}
	// OSC / DCS / PM / APC：颜色、剪贴板一类的回答。用户按键不会以这些开头。
	if len(p) >= 2 && (p[1] == ']' || p[1] == 'P' || p[1] == '^' || p[1] == '_') {
		return true
	}
	if len(p) < 3 || p[1] != '[' {
		return false
	}
	body := p[2:]
	if len(body) == 1 && (body[0] == 'I' || body[0] == 'O') {
		return true // 焦点进入、离开（mode 1004）
	}
	i := 0
	if body[0] == '?' || body[0] == '>' || body[0] == '<' || body[0] == '=' {
		i++
	}
	hasDigit := false
	for ; i < len(body)-1; i++ {
		c := body[i]
		if c >= '0' && c <= '9' {
			hasDigit = true
			continue
		}
		if c == ';' {
			continue
		}
		return false
	}
	if !hasDigit {
		return false
	}
	switch body[len(body)-1] {
	case 'R', 'c', 'n':
		return true
	default:
		return false
	}
}

// localBroadcastBytes 把本机终端认得出的按键编成 shell 能读的字节。
// 带 Command、Super、Alt 的组合留给应用或输入法，这里不猜编码。
func localBroadcastBytes(mods ui.Modifiers, key ui.Key) []byte {
	if mods&(ui.Cmd|ui.Super|ui.Alt) != 0 {
		return nil
	}
	if mods&ui.Ctrl != 0 {
		if extra := mods &^ ui.Ctrl; extra != 0 && extra != ui.Shift {
			return nil
		}
		if key >= ui.KeyA && key <= ui.KeyZ {
			return []byte{byte(key-ui.KeyA) + 1}
		}
		return nil
	}
	switch key {
	case ui.KeyEnter:
		return []byte{'\r'}
	case ui.KeyTab:
		return []byte{'\t'}
	case ui.KeyBackspace:
		return []byte{0x7f}
	case ui.KeyEscape:
		return []byte{0x1b}
	case ui.KeyUp:
		return []byte{0x1b, '[', 'A'}
	case ui.KeyDown:
		return []byte{0x1b, '[', 'B'}
	case ui.KeyRight:
		return []byte{0x1b, '[', 'C'}
	case ui.KeyLeft:
		return []byte{0x1b, '[', 'D'}
	case ui.KeyHome:
		return []byte{0x1b, '[', 'H'}
	case ui.KeyEnd:
		return []byte{0x1b, '[', 'F'}
	case ui.KeyDelete:
		return []byte{0x1b, '[', '3', '~'}
	case ui.KeyPageUp:
		return []byte{0x1b, '[', '5', '~'}
	case ui.KeyPageDown:
		return []byte{0x1b, '[', '6', '~'}
	}
	if mods&ui.Shift != 0 {
		if key >= ui.KeyA && key <= ui.KeyZ {
			return []byte{'A' + byte(key-ui.KeyA)}
		}
		return nil
	}
	switch {
	case key >= ui.KeyA && key <= ui.KeyZ:
		return []byte{'a' + byte(key-ui.KeyA)}
	case key >= ui.Key0 && key <= ui.Key9:
		return []byte{'0' + byte(key-ui.Key0)}
	case key == ui.KeySpace:
		return []byte{' '}
	default:
		return nil
	}
}

// syncBroadcastSource 记下当前该镜像哪一个窗格。输入 goroutine 只读这个原子值，不碰 app 的其他字段。
func (a *app) syncBroadcastSource() {
	var id int64
	if a.broadcastOn {
		if t := a.current(); t != nil && t.focus != nil && a.broadcast[t.focus.id] && !t.focus.ended {
			id = int64(t.focus.id)
		}
	}
	a.broadcastSource.Store(id)
}

func (a *app) clearBroadcast() {
	a.broadcastOn = false
	a.broadcast = nil
	a.broadcastAsk = nil
	a.syncBroadcastSource()
}

// toggleBroadcast 从命令面板开关广播。打开时把当前窗格加进去；生产环境要先确认。
// 这次确认被取消、而且还没有任何窗格加入时，广播保持关闭。
func (a *app) toggleBroadcast() {
	if a.broadcastOn {
		a.clearBroadcast()
		a.toast(ui.Toast{Title: "已关闭广播"})
		return
	}
	t := a.current()
	if t == nil || t.focus == nil || t.focus.ended || t.focus.term == nil {
		a.toast(ui.Toast{Title: "先打开一个终端，再开启广播。"})
		return
	}
	a.broadcastOn = true
	a.requestJoin(t, t.focus)
	if a.broadcast[t.focus.id] {
		a.toast(ui.Toast{Title: "已开启广播"})
	}
}

// requestJoin 把窗格加入广播。生产环境先弹出确认，确认前不加入。
func (a *app) requestJoin(t *tab, p *pane) {
	if t == nil || p == nil || p.term == nil || p.ended || a.broadcast[p.id] {
		return
	}
	if t.host.Production {
		a.broadcastAsk = &broadcastAsk{tab: t, pane: p}
		a.broadcastOn = true
		return
	}
	a.addBroadcast(p)
}

func (a *app) addBroadcast(p *pane) {
	if p == nil {
		return
	}
	if a.broadcast == nil {
		a.broadcast = map[int]bool{}
	}
	a.broadcast[p.id] = true
	a.broadcastOn = true
	a.syncBroadcastSource()
}

// removeBroadcast 移出一个窗格。最后一个窗格离开且没有待确认的生产环境时，关闭广播。
func (a *app) removeBroadcast(id int) {
	_, member := a.broadcast[id]
	asking := a.broadcastAsk != nil && a.broadcastAsk.pane != nil && a.broadcastAsk.pane.id == id
	if !member && !asking {
		return
	}
	delete(a.broadcast, id)
	if asking {
		a.broadcastAsk = nil
	}
	if len(a.broadcast) == 0 {
		a.broadcast = nil
		if a.broadcastAsk == nil {
			a.broadcastOn = false
		}
	}
	a.syncBroadcastSource()
}

func (a *app) dropTabBroadcast(t *tab) {
	if t != nil && t.root != nil {
		t.root.each(func(p *pane) { delete(a.broadcast, p.id) })
	}
	if a.broadcastAsk != nil && a.broadcastAsk.tab == t {
		a.broadcastAsk = nil
	}
	if len(a.broadcast) == 0 {
		a.broadcast = nil
		if a.broadcastAsk == nil {
			a.broadcastOn = false
		}
	}
	a.syncBroadcastSource()
}

func (a *app) togglePaneBroadcast(t *tab, p *pane) {
	if a.broadcast[p.id] {
		a.removeBroadcast(p.id)
		if !a.broadcastOn {
			a.toast(ui.Toast{Title: "已关闭广播"})
		}
		return
	}
	a.requestJoin(t, p)
}

// mirrorBroadcast 把 data 送到除来源以外的广播窗格。只在主线程调用。
func (a *app) mirrorBroadcast(from int, data []byte) {
	if !a.broadcastOn || !a.broadcast[from] || len(data) == 0 || isTerminalReply(data) {
		return
	}
	for _, t := range a.tabs {
		if t.root == nil {
			continue
		}
		t.root.each(func(p *pane) {
			if p.id == from || !a.broadcast[p.id] || p.ended || p.term == nil {
				return
			}
			p.term.Send(data)
		})
	}
}

// paneOrdinal 是窗格在这棵树里从左到右、从上到下的序号，从 1 开始。
// 同一个主机拆出的窗格标题相同，按钮文字靠序号区分，测试才点得到正确的那一个。
func paneOrdinal(root, target *pane) int {
	if root == nil || target == nil {
		return 0
	}
	n, found := 0, 0
	root.each(func(p *pane) {
		n++
		if p == target {
			found = n
		}
	})
	return found
}

func broadcastButtonLabel(member bool, host string, ordinal int) string {
	verb := "加入广播"
	if member {
		verb = "移出广播"
	}
	return fmt.Sprintf("%s：%s %d", verb, host, ordinal)
}

// broadcastDialog 确认把生产环境加入广播。默认按钮是“取消”。
func (a *app) broadcastDialog(c *ui.Context) {
	ask := a.broadcastAsk
	if ask == nil || ask.tab == nil || ask.pane == nil {
		return
	}
	if !slices.Contains(a.tabs, ask.tab) || ask.tab.root == nil || !ask.tab.root.contains(ask.pane) {
		a.broadcastAsk = nil
		if len(a.broadcast) == 0 {
			a.broadcastOn = false
		}
		a.syncBroadcastSource()
		return
	}
	open := true
	message := fmt.Sprintf("%s 是生产环境。加入以后，广播里的输入会同时发到这个窗格。", ask.tab.host.Name)
	choice := ui.AlertDialog(c, &open, "把生产环境加入广播？", message, "加入广播", "取消")
	if choice == -1 && open {
		return
	}
	pane := ask.pane
	a.broadcastAsk = nil
	if choice == 0 {
		a.addBroadcast(pane)
		a.toast(ui.Toast{Title: "已加入广播"})
		return
	}
	if len(a.broadcast) == 0 {
		a.broadcastOn = false
	}
	a.syncBroadcastSource()
}

// broadcastMark 给已加入的窗格描边。生产环境继续用红色，避免被强调色盖掉“这是生产”这件事。
func broadcastMark(card ui.Element, colors design.Colors, accent ui.Color, member, production bool) {
	switch {
	case member && production:
		card.Border(2, colors.Danger)
	case member:
		card.Border(2, accent)
	case production:
		card.BorderColor(colors.Danger.Alpha(0.45))
	}
}
