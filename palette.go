package main

import (
	"cmp"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"serval/core/hosts"
	"serval/core/vault"
	"serval/design"

	"github.com/egoist/mygo/ui"
)

// palette 是命令面板的状态。它是连接主机和执行操作的主要入口：两三个按键就能到达任何地方。
type palette struct {
	open     bool
	query    string
	selected int
	// fresh 表示面板刚打开，这一帧从起始姿态弹出。
	fresh bool
}

// paletteItem 是命令面板里的一行。host 为空时是操作。
type paletteItem struct {
	key      string
	title    string
	subtitle string
	icon     string
	host     *hosts.Host
	run      func()
}

func (a *app) openPalette() {
	a.pal = palette{open: true, fresh: true}
}

// fuzzyScore 判断 query 的字符是否按顺序出现在 text 里，并给出分数；不匹配时返回 -1。
//
// 从开头、单词边界开始的连续匹配分数最高：“web” 把 web-01 排在 my-new-box 前面，
// “db” 把 prod-db 排在 dashboard 前面。只从第一个出现的字符开始贪心匹配会错过更好的起点，
// 所以对首字符的每个出现位置都试一次，取最高分。
func fuzzyScore(query, text string) int {
	query, text = strings.ToLower(query), strings.ToLower(text)
	if query == "" {
		return 0
	}
	first, size := utf8.DecodeRuneInString(query)
	best := -1
	for start := 0; start < len(text); {
		found := strings.IndexRune(text[start:], first)
		if found < 0 {
			break
		}
		if s := scoreFrom(query[size:], text, start+found, first); s > best {
			best = s
		}
		start += found + size
	}
	if best < 0 {
		return -1
	}
	return best - len(text)/16
}

// scoreFrom 从 text 的 position 处开始，贪心匹配剩余的 query。
func scoreFrom(rest, text string, position int, first rune) int {
	bonus := func(position, last int) int {
		switch {
		case position == 0:
			return 8
		case position == last+1:
			return 6
		case strings.ContainsRune("-_. /@:", rune(text[position-1])):
			return 5
		}
		return 1
	}
	score := bonus(position, -2)
	last := position + utf8.RuneLen(first) - 1
	at := last + 1
	for _, want := range rest {
		found := strings.IndexRune(text[at:], want)
		if found < 0 {
			return -1
		}
		position := at + found
		score += bonus(position, last)
		last = position + utf8.RuneLen(want) - 1
		at = last + 1
	}
	return score
}

// parseQuickConnect 解析 user@host[:port] 格式的快速连接目标。
func parseQuickConnect(s string) (user, address string, port int, ok bool) {
	s = strings.TrimSpace(s)
	at := strings.LastIndex(s, "@") // 用户名里也可能有 @，和 hosts.ParseJumpSpec 一致
	if at <= 0 || at == len(s)-1 {
		return "", "", 0, false
	}
	user = s[:at]
	target := s[at+1:]
	if user == "" || target == "" {
		return "", "", 0, false
	}
	if strings.ContainsAny(user, " \t\r\n") || strings.ContainsAny(target, " \t\r\n") {
		return "", "", 0, false
	}
	port = 22
	if host, portStr, err := net.SplitHostPort(target); err == nil {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 && p <= 65535 {
			return user, host, p, true
		}
		return "", "", 0, false
	}
	if strings.Contains(target, ":") {
		return "", "", 0, false
	}
	return user, target, port, true
}

// paletteItems 返回匹配 query 的条目，主机在前，操作在后。
func (a *app) paletteItems(query string) []paletteItem {
	type scored struct {
		paletteItem
		score int
	}
	var matched []scored
	if user, addr, port, ok := parseQuickConnect(query); ok {
		target := fmt.Sprintf("%s@%s", user, addr)
		if port != 22 {
			target = fmt.Sprintf("%s:%d", target, port)
		}
		tempHost := hosts.Host{
			ID:      "temp:" + target,
			Name:    target,
			Address: addr,
			Port:    port,
			User:    user,
		}
		matched = append(matched, scored{
			paletteItem: paletteItem{
				key:      "quick:" + target,
				title:    fmt.Sprintf("连接 %s", target),
				subtitle: "快速连接（不保存）",
				icon:     "terminal",
				host:     &tempHost,
				run:      func() { a.open(tempHost) },
			},
			score: 10000,
		})
	}
	add := func(item paletteItem, haystack string) {
		if s := fuzzyScore(query, haystack); s >= 0 {
			matched = append(matched, scored{item, s})
		}
	}
	for i := range a.hosts {
		h := &a.hosts[i]
		add(paletteItem{key: "host:" + h.ID, title: h.Name, subtitle: h.Target(), host: h, run: func() { a.open(*h) }},
			h.Name+" "+h.Target())
	}
	// 同类内按分数稳定排序。查询为空时最近连接过的主机排在前面（本机记录，不同步），其余保持原有顺序。
	recent := func(it paletteItem) int {
		if it.host == nil {
			return len(a.local.RecentHosts)
		}
		if i := slices.Index(a.local.RecentHosts, it.host.ID); i >= 0 {
			return i
		}
		return len(a.local.RecentHosts)
	}
	slices.SortStableFunc(matched, func(x, y scored) int {
		if c := cmp.Compare(y.score, x.score); c != 0 || query != "" {
			return c
		}
		return cmp.Compare(recent(x.paletteItem), recent(y.paletteItem))
	})

	var actions []scored
	action := func(icon, title string, run func()) {
		if s := fuzzyScore(query, title); s >= 0 {
			actions = append(actions, scored{paletteItem{key: "action:" + title, title: title, icon: icon, run: run}, s})
		}
	}
	if t := a.current(); t != nil {
		action("search", "在终端中查找", func() { a.openSearch(t) })
		action("columns-2", "向右拆分", func() { a.split(t, false) })
		action("rows-2", "向下拆分", func() { a.split(t, true) })
		action("x", "关闭当前窗格", func() { a.closeFocused(t) })
	}
	if a.broadcastOn {
		action("radio", "关闭广播", a.toggleBroadcast)
	} else {
		action("radio", "广播输入", a.toggleBroadcast)
	}
	action("folder", "显示或收起文件", a.toggleFiles)
	action("terminal", "新建本地终端", a.openLocalTerminal)
	action("panel-left", "显示或收起侧栏", func() { a.sidebarOpen = !a.sidebarOpen })
	action("plus", "添加主机", func() { a.openAdd() })
	action("key-round", "密钥", a.openKeys)
	action("globe", "代理", a.openProxies)
	action("settings", "打开设置", a.openSettings)
	action("server", "已知主机", a.openSettings)
	if a.vault != nil && a.vault.State() == vault.Unlocked {
		action("lock", "锁定保险库", a.askLockVault)
	}
	action("sun-moon", "外观：跟随系统", func() { a.setAppearance(appearanceSystem) })
	action("sun-moon", "外观：浅色", func() { a.setAppearance(appearanceLight) })
	action("sun-moon", "外观：深色", func() { a.setAppearance(appearanceDark) })
	slices.SortStableFunc(actions, func(x, y scored) int { return cmp.Compare(y.score, x.score) })

	items := make([]paletteItem, 0, len(matched)+len(actions))
	for _, m := range matched {
		items = append(items, m.paletteItem)
	}
	for _, m := range actions {
		items = append(items, m.paletteItem)
	}
	return items
}

func (a *app) paletteDialog(c *ui.Context, colors design.Colors) {
	if !a.pal.open {
		return
	}
	t := c.Theme()
	before := a.pal.query
	items := a.paletteItems(before)
	if a.pal.selected >= len(items) {
		a.pal.selected = max(0, len(items)-1)
	}
	run := func(item paletteItem) {
		a.pal.open = false
		item.run()
	}
	// 打开时从略小、半透明弹出，和新窗格用同一套姿态，整个应用的“出现”是一种语言。
	sheet(c, colors, &a.pal.open, &a.pal.fresh, 560, 12, func() {
		ui.Column(c).Key("paletteBody").Gap(8).Children(func() {
			ui.Row(c).Gap(9).AlignItems(ui.Center).Padding(0, 2).Children(func() {
				ui.Icon(c, design.Icon("search")).FontSize(16).TextColor(t.TextMuted)
				c.Key("paletteInput")
				input := ui.TextInput(c, &a.pal.query).Placeholder("连接主机，或者输入操作").Label("搜索").
					AutoFocus().Grow(1).FontSize(design.TextHeading).Border(0, ui.Transparent).Background(ui.Transparent)
				move := func(delta int) func() {
					return func() {
						if len(items) > 0 {
							a.pal.selected = (a.pal.selected + delta + len(items)) % len(items)
						}
					}
				}
				input.OnShortcut(0, ui.KeyDown, move(1))
				input.OnShortcut(0, ui.KeyUp, move(-1))
				if input.Submitted() && a.pal.selected < len(items) {
					run(items[a.pal.selected])
				}
			})
			ui.Divider(c)
			ui.Scroll(c).MaxHeight(340).Children(func() {
				ui.Column(c).Gap(1).Children(func() {
					if len(items) == 0 {
						ui.Text(c, fmt.Sprintf("没有找到“%s”", before)).TextColor(t.TextMuted).Padding(10, 8)
					}
					for i, item := range items {
						a.paletteRow(c, colors, i, item, func() { run(item) })
					}
				})
			})
		})
	})
	if a.pal.query != before {
		a.pal.selected = 0
	}
}

func (a *app) paletteRow(c *ui.Context, colors design.Colors, i int, item paletteItem, run func()) {
	t := c.Theme()
	row := ui.ButtonBase(c).Key(item.key).Label(item.title).Height(40).Padding(0, 10).Gap(10).
		Justify(ui.Start).AlignItems(ui.Center).Radius(design.RadiusRow).Transition(design.Hover)
	selected := i == a.pal.selected
	if !selected && row.Hovered() {
		row.Background(colors.Hover)
	}
	row.Children(func() {
		if selected {
			// 键盘选中的一行用淡淡的强调色，按上下键时高亮跟着滑过去。
			selectionPill(c, colors, "palette", t.Accent.Alpha(0.12), false)
		}
		if item.host != nil {
			design.CoatView(c, item.host.ID, item.host.Production, 22)
		} else {
			ui.Box(c).Size(22, 22).Center().Shrink(0).Children(func() {
				ui.Icon(c, design.Icon(item.icon)).FontSize(15).TextColor(t.TextMuted)
			})
		}
		ui.Text(c, item.title).FontWeight(500).SingleLine().MinWidth(0).Shrink(1)
		if item.subtitle != "" {
			ui.Spacer(c)
			ui.Text(c, item.subtitle).FontSize(design.TextSmall).TextColor(t.TextMuted).SingleLine().MinWidth(0).Shrink(1)
		}
	}).OnClick(run)
}
