package main

import (
	"fmt"
	"path"
	"runtime"
	"slices"
	"strings"

	"serval/core/hosts"
	"serval/core/vault"
	"serval/design"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// chromeShortcut 返回应用快捷键的修饰键。
//
// macOS 用 Command，不和终端冲突。其他平台不能直接用 Ctrl：Ctrl+D、Ctrl+W、Ctrl+K
// 都是终端里的常用键，所以用 Ctrl+Shift，和主流终端的习惯一致。
func chromeShortcut() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Cmd
	}
	return ui.Ctrl | ui.Shift
}

// shortcutText 返回显示给用户的快捷键文字。
func shortcutText(key string) string {
	if runtime.GOOS == "darwin" {
		return "⌘" + key
	}
	return "Ctrl+Shift+" + key
}

func (a *app) view(c *ui.Context) {
	a.touchActive()
	dark := a.isDark(c.Theme().Dark)
	c.SetTheme(design.Theme(dark))
	colors := design.Of(dark)

	backdrop(c, colors)
	// 保险库锁定时只显示解锁页面：解锁前连主机名都不该看到。
	if a.vault.State() == vault.Locked {
		a.lockScreen(c, colors)
		a.flushToasts(c)
		return
	}
	a.shortcuts(c)
	ui.Row(c).Fill().AlignItems(ui.Stretch).LinearGradient(colors.Glow).Children(func() {
		if a.sidebarOpen {
			a.sidebar(c, colors)
		}
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			a.tabBar(c, colors)
			a.workspace(c, colors)
		})
	})
	a.hostKeyDialog(c)
	a.authDialog(c, colors)
	a.hostDialog(c, colors)
	a.keysDialog(c, colors)
	a.settingsDialog(c, colors)
	a.setupDialog(c, colors)
	a.deleteDialog(c)
	a.lockConfirmDialog(c)
	a.flushToasts(c)
	a.paletteDialog(c, colors)
}

// backdrop 给窗口铺上底色渐变。开启毛玻璃时渐变略透明，让桌面的颜色微微透出来，
// 文字仍然有足够的对比度。
func backdrop(c *ui.Context, colors design.Colors) {
	wash := colors.Wash
	// 开启“减少透明度”时窗口已经不用毛玻璃（见 vibrancyFor），这里再保证底色完全不透明，
	// 防止设置刚切换、窗口还没来得及更新时透出桌面。
	if c.Vibrancy() && !c.Preferences().ReduceTransparency {
		stops := make([]ui.GradientStop, len(wash.Stops))
		for i, s := range wash.Stops {
			stops[i] = ui.GradientStop{Color: s.Color.Alpha(0.9), At: s.At}
		}
		wash.Stops = stops
	}
	c.Root().LinearGradient(wash)
}

func (a *app) shortcuts(c *ui.Context) {
	mods := chromeShortcut()
	c.OnShortcut(mods, ui.KeyK, a.openPalette)
	c.OnShortcut(mods, ui.KeyBackslash, func() { a.sidebarOpen = !a.sidebarOpen })
	c.OnShortcut(mods, ui.KeyJ, a.toggleFiles)
	if t := a.current(); t != nil {
		c.OnShortcut(mods, ui.KeyD, func() { a.split(t, false) })
		c.OnShortcut(mods|ui.Alt, ui.KeyD, func() { a.split(t, true) })
		c.OnShortcut(mods, ui.KeyW, func() { a.closeFocused(t) })
	}
}

// iconButton 是工具栏上的图标按钮。图标没有文字，所以必须带标签和提示。
func iconButton(c *ui.Context, colors design.Colors, icon, label string, fn func()) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Size(28, 28).Center().Radius(design.RadiusRow).Label(label).Tooltip(label).Shrink(0)
	if b.Hovered() {
		b.Background(colors.Hover)
	}
	b.Children(func() {
		ui.Icon(c, design.Icon(icon)).FontSize(15).TextColor(t.TextMuted)
	})
	return b.OnClick(fn)
}

// raised 让一个元素像卡片一样浮起：细边加两层阴影，一层贴近、一层散开。
func raised(e ui.Element, colors design.Colors, radius float32) ui.Element {
	return e.Radius(radius).Background(colors.Card).Border(1, colors.CardBorder).
		Shadow(0, 1, 2, 0, colors.CardShadow).Shadow(0, 8, 24, -8, colors.CardShadow)
}

// pillMatch 是选中高亮的共享身份：同一个列表里只有一枚高亮，选中项变化时，
// 新的高亮从旧的位置滑过去，而不是在新位置突然出现。
type pillMatch struct{ list string }

// selectionPill 在选中的行里画一枚高亮，铺在行的内容下面。
// raised 为 true 时是浅色下的白色浮起胶囊（深色下是一层提亮）；否则是一层淡淡的底色。
func selectionPill(c *ui.Context, colors design.Colors, list string, bg ui.Color, raised bool) {
	// 绝对定位的元素默认画在同级元素上面，必须用负的 ZIndex 压到文字下面，否则会盖住内容。
	pill := ui.Box(c).Key("pill").Absolute().Top(0).Left(0).Right(0).Bottom(0).ZIndex(-1).Radius(design.RadiusRow).
		Background(bg).AccessHidden(true).PassThrough()
	if raised {
		pill.Shadow(0, 1, 2, 0, colors.SelectedShadow)
		if !colors.Dark {
			pill.Border(1, colors.CardBorder)
		}
	}
	pill.Transition(ui.ElementTransition{Animation: &design.Glide, Match: pillMatch{list}})
}

// sidebar 是主机列表。它透出窗口的渐变和系统的毛玻璃，不画自己的底色。
func (a *app) sidebar(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	bar := c.TitleBar()
	// 侧栏的宽度一步到位（不让终端逐帧改尺寸），内容从左侧滑入、淡入。
	ui.Column(c).Key("sidebar").Width(240).Shrink(0).DragWindow().Transition(design.SlideIn(-14)).Children(func() {
		ui.Box(c).Height(max(bar.Height, 12)).Shrink(0)

		ui.Column(c).Padding(2, 12, 12, 12).Children(func() {
			search := ui.ButtonBase(c).Key("search").Label("搜索主机和操作").Height(32).Padding(0, 10).Gap(8).
				Justify(ui.Start).AlignItems(ui.Center).Radius(design.RadiusRow).Background(colors.Chip).Transition(design.Hover)
			if search.Hovered() {
				search.Background(colors.Hover)
			}
			search.Children(func() {
				ui.Icon(c, design.Icon("search")).FontSize(14).TextColor(t.TextMuted)
				ui.Text(c, "搜索").TextColor(t.TextMuted).Grow(1)
				ui.Text(c, shortcutText("K")).FontSize(design.TextSmall).TextColor(colors.TextFaint)
			}).OnClick(a.openPalette)
		})

		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).Padding(0, 10, 10, 10).Gap(16).Children(func() {
				if len(a.hosts) == 0 {
					a.sidebarEmpty(c)
				}
				for _, group := range a.groups() {
					ui.Column(c).Key("group:" + group).Gap(2).Children(func() {
						ui.Text(c, group).FontSize(design.TextSmall).FontWeight(500).TextColor(colors.TextFaint).Padding(0, 10, 4, 10)
						for _, h := range a.hosts {
							if h.Group == group {
								a.hostRow(c, colors, h)
							}
						}
					})
				}
			})
		})

		ui.Column(c).Padding(6, 10, 12, 10).Gap(2).Children(func() {
			for _, message := range []string{a.hostsError, a.notice} {
				if message != "" {
					ui.Text(c, message).Key("notice").FontSize(design.TextSmall).TextColor(colors.Danger).
						Padding(8, 10).Margin(0, 0, 6, 0).Radius(design.RadiusRow).Background(colors.DangerSoft).Selectable()
				}
			}
			// ButtonBase 默认居中，这里要和上面的主机列表左对齐。
			add := ui.ButtonBase(c).Key("addHost").Label("添加主机").Height(30).Padding(0, 10).Gap(8).
				Justify(ui.Start).AlignItems(ui.Center).Radius(design.RadiusRow).Transition(design.Hover)
			if add.Hovered() {
				add.Background(colors.Hover)
			}
			add.Children(func() {
				ui.Icon(c, design.Icon("plus")).FontSize(14).TextColor(t.TextMuted)
				ui.Text(c, "添加主机").TextColor(t.TextMuted)
			}).OnClick(func() { a.openAdd() })
			keysButton := ui.ButtonBase(c).Key("openKeys").Label("密钥").Height(30).Padding(0, 10).Gap(8).
				Justify(ui.Start).AlignItems(ui.Center).Radius(design.RadiusRow).Transition(design.Hover)
			if keysButton.Hovered() {
				keysButton.Background(colors.Hover)
			}
			keysButton.Children(func() {
				ui.Icon(c, design.Icon("key-round")).FontSize(14).TextColor(t.TextMuted)
				ui.Text(c, "密钥").TextColor(t.TextMuted).Grow(1)
				if n := len(a.keys); n > 0 {
					ui.Text(c, fmt.Sprint(n)).FontSize(design.TextSmall).TextColor(colors.TextFaint)
				}
			}).OnClick(a.openKeys)
			settingsButton := ui.ButtonBase(c).Key("openSettings").Label("设置").Height(30).Padding(0, 10).Gap(8).
				Justify(ui.Start).AlignItems(ui.Center).Radius(design.RadiusRow).Transition(design.Hover)
			if settingsButton.Hovered() {
				settingsButton.Background(colors.Hover)
			}
			settingsButton.Children(func() {
				ui.Icon(c, design.Icon("settings")).FontSize(14).TextColor(t.TextMuted)
				ui.Text(c, "设置").TextColor(t.TextMuted)
			}).OnClick(a.openSettings)
		})
	})
}

func (a *app) sidebarEmpty(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Padding(10, 10).Gap(6).Children(func() {
		ui.Text(c, "还没有主机").FontWeight(600)
		ui.Text(c, "添加一台主机开始使用。私钥可以在下面的“密钥”里添加。").
			FontSize(design.TextSmall).TextColor(t.TextMuted)
	})
}

func (a *app) hostRow(c *ui.Context, colors design.Colors, h hosts.Host) {
	t := c.Theme()
	current := a.current()
	selected := current != nil && current.host.ID == h.ID
	connected := slices.ContainsFunc(a.tabs, func(tb *tab) bool { return tb.host.ID == h.ID })
	_, busy := a.connecting[h.ID]
	menu := a.hostMenu(h)

	// 外层负责背景、悬停和选中高亮，里面并排放主机按钮和“更多”按钮。
	// 两个按钮不能嵌套：嵌在按钮里的按钮，读屏软件和键盘都找不到。
	line := ui.Row(c).Key("line:"+h.ID).AlignItems(ui.Center).Padding(0, 6, 0, 0).
		Radius(design.RadiusRow).Transition(design.Hover)
	hovered := line.Hovered()
	if !selected && hovered {
		line.Background(colors.Hover)
	}
	line.Children(func() {
		if selected {
			selectionPill(c, colors, "hosts", colors.Selected, true)
		}
		row := ui.ButtonBase(c).Key("host:"+h.ID).Label(h.Name).Grow(1).MinWidth(0).Padding(6, 4, 6, 10).Gap(10).
			Justify(ui.Start).AlignItems(ui.Center)
		if busy {
			row.Description("正在连接")
		}
		row.Children(func() {
			coat := design.CoatView(c, h.ID, h.Production, 26)
			if busy {
				// 连接中斑纹轻轻呼吸，代替转圈图标：状态就落在这台主机自己的标识上。
				coat.Opacity(coat.Keyframes("breathe", design.Breathe, design.BreatheOptions))
			}
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, h.Name).FontWeight(500).NoWrap().Ellipsis("…")
				sub := h.Target()
				if jumps := hosts.SplitJumps(h.ProxyJump); len(jumps) > 0 {
					// 经跳板机的主机显示途经路线：连不上时，用户第一眼就知道该先查哪一台。
					sub = "经 " + strings.Join(jumps, " → ")
				}
				ui.Text(c, sub).FontSize(design.TextSmall).TextColor(t.TextMuted).NoWrap().Ellipsis("…")
			})
			if connected {
				ui.Box(c).Size(7, 7).Radius(4).Background(colors.Live).Shrink(0).Label("已连接")
			}
		}).OnClick(func() { a.open(h) })
		row.PopoverMenu(menu)

		// “更多”按钮只在悬停或选中时出现，打开和右键相同的菜单：
		// 不知道可以右键的人也能找到编辑和删除。键盘用户在行上按菜单键或 Shift+F10。
		if hovered || selected {
			more := ui.ButtonBase(c).Key("more:"+h.ID).Label("更多操作").Size(24, 24).Radius(6).Shrink(0).Transition(design.Hover)
			if more.Hovered() {
				more.Background(colors.Hover)
			}
			more.Children(func() {
				ui.Icon(c, design.Icon("ellipsis")).FontSize(14).TextColor(t.TextMuted)
			}).PopoverMenuButton(menu)
		}
	})
}

// tabBar 把标签放进标题栏，省下一整行高度给终端。它和侧栏一样透明，选中的标签浮起来。
func (a *app) tabBar(c *ui.Context, colors design.Colors) {
	bar := c.TitleBar()
	left := float32(4)
	if !a.sidebarOpen {
		left = max(bar.Left, design.Gap)
	}
	height := max(bar.Height, 40)
	ui.Row(c).Height(height).Shrink(0).Padding(0, design.Gap+2, 0, left).Gap(4).AlignItems(ui.Center).DragWindow().Children(func() {
		if !a.sidebarOpen {
			iconButton(c, colors, "panel-left", "显示侧栏", func() { a.sidebarOpen = true })
		}
		// 标签放不下时一起收窄到只剩斑纹，每个都还能点到、认得出。
		ui.Row(c).Grow(1).MinWidth(0).Gap(4).AlignItems(ui.Center).Children(func() {
			for i, t := range a.tabs {
				a.tabItem(c, colors, i, t)
			}
		})
		if t := a.current(); t != nil {
			iconButton(c, colors, "columns-2", fmt.Sprintf("向右拆分（%s）", shortcutText("D")), func() { a.split(t, false) })
			iconButton(c, colors, "rows-2", "向下拆分", func() { a.split(t, true) })
			iconButton(c, colors, "folder", fmt.Sprintf("文件（%s）", shortcutText("J")), a.toggleFiles)
		}
	})
}

func (a *app) tabItem(c *ui.Context, colors design.Colors, i int, t *tab) {
	theme := c.Theme()
	selected := i == a.active
	// 只告诉辅助技术“已选中”。不用 Selected()：它会给按钮画一层强调色背景，
	// 平时被高亮盖住，高亮滑走的那一瞬间会露出一闪的靛蓝色。
	item := ui.ButtonBase(c).Key(fmt.Sprintf("tab:%d", t.id)).Label(t.title()).AccessSelected(selected).
		Height(28).MinWidth(36).MaxWidth(200).Shrink(1).Padding(0, 10).Gap(8).AlignItems(ui.Center).
		Radius(design.RadiusRow).Transition(design.Hover)
	hovered := item.Hovered()
	if !selected && hovered {
		item.Background(colors.Hover)
	}
	item.Children(func() {
		if selected {
			selectionPill(c, colors, "tabs", colors.Selected, true)
		}
		design.CoatView(c, t.host.ID, t.host.Production, 14)
		color := theme.TextMuted
		if selected {
			color = theme.Text
		}
		ui.Text(c, t.title()).TextColor(color).FontWeight(500).NoWrap().Ellipsis("…").MinWidth(0).Shrink(1)
		if hovered || selected {
			ui.ButtonBase(c).Key("close").Label("关闭 "+t.title()).Size(16, 16).Center().Radius(4).Shrink(0).Children(func() {
				ui.Icon(c, design.Icon("x")).FontSize(12).TextColor(colors.TextFaint)
			}).OnClick(func() { a.closeTab(t) })
		}
	}).OnClick(func() { a.active = i })
}

func (a *app) workspace(c *ui.Context, colors design.Colors) {
	t := a.current()
	if t == nil {
		a.emptyState(c, colors)
		return
	}
	left := float32(0)
	if !a.sidebarOpen {
		left = design.Gap / 2
	}
	// 每张卡片自带一半的间距（Margin），这里再补一半，卡片之间和到窗口边缘都是一样的空隙。
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Padding(0, design.Gap/2, design.Gap/2, left).Children(func() {
		ui.Column(c).Key(t.id).Grow(1).MinWidth(0).Children(func() {
			a.paneView(c, colors, t, t.root)
		})
		if a.filesOpen {
			a.filesDrawer(c, colors, t)
		}
	})
}

// emptyState 是打开应用的第一屏。品牌斑纹放大作为标志，也预告了每台主机都有自己的图案。
func (a *app) emptyState(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	ui.Column(c).Fill().Center().Gap(20).Padding(32).Children(func() {
		design.BrandView(c, 76).Shadow(0, 14, 32, -10, colors.CardShadow)
		ui.Column(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "连接一台主机").FontSize(design.TextDisplay).FontWeight(600).LetterSpacing(-0.4)
			hint := fmt.Sprintf("按 %s 搜索，或者从左侧选择", shortcutText("K"))
			if len(a.hosts) == 0 {
				hint = "添加第一台主机，再在“密钥”里添加你的私钥"
			}
			ui.Text(c, hint).TextColor(t.TextMuted)
		})
		if len(a.hosts) == 0 {
			ui.PrimaryButton(c, "添加主机").Key("addFirst").OnClick(func() { a.openAdd() })
		}
	})
}

func (a *app) paneView(c *ui.Context, colors design.Colors, t *tab, p *pane) {
	if p.term == nil {
		first := func() { a.paneView(c, colors, t, p.first) }
		second := func() { a.paneView(c, colors, t, p.second) }
		// 拆分的分隔线用主题的 Border 色。卡片之间已经有留白，平时不画这条线；
		// 鼠标悬停或聚焦时 MyGo 仍会把它画成强调色，拖动的提示不会丢。
		quiet := *c.Theme()
		quiet.Border = ui.Transparent
		// Themed 默认会铺上主题的底色，这里要透明，否则渐变背景上会多出一块矩形。
		ui.Themed(c, &quiet).Key(p.id).Background(ui.Transparent).Grow(1).MinWidth(0).MinHeight(0).Children(func() {
			if p.vertical {
				ui.SplitVerticalPercent(c, &p.percent, first, second).Grow(1).MinHeight(0)
			} else {
				ui.SplitPercent(c, &p.percent, first, second).Grow(1).MinHeight(0)
			}
		})
		return
	}
	focused := p == t.focus
	dim := !focused && t.root.leaves() > 1
	card := raised(ui.Column(c).Key(p.id).Grow(1).MinWidth(0).MinHeight(0).Margin(design.Gap/2).Clip(), colors, design.RadiusCard)
	// 新窗格从略小、半透明弹到正常大小。用的是姿态动画：只重绘、不重建界面，
	// 也不改变布局尺寸，所以终端不会在动画过程中逐帧改尺寸。
	pose := ui.Pose{}
	if a.arrived[p.id] {
		pose = design.Arrival
		delete(a.arrived, p.id)
		c.Invalidate() // 下一帧把目标改回原位，弹簧从起始姿态开始播放
	}
	card.AnimatePose(pose, design.Pop)
	// 焦点切换时，薄纱淡入淡出，而不是瞬间出现。
	veil := card.AnimateTo("veil", boolToFloat(dim), design.Fade)
	if t.host.Production {
		// 生产环境的卡片描一圈淡红色的边，任何时候瞥一眼都能看到。
		card.BorderColor(colors.Danger.Alpha(0.45))
	}
	card.Children(func() {
		a.paneHeader(c, colors, t, p)
		view := terminal.View(c, p.term).Grow(1).MinHeight(0)
		if focused {
			view.AutoFocus()
		}
		if view.FocusWithin() && !focused {
			t.focus = p
		}
	}).DrawOver(func(painter *ui.Painter, r ui.Rect) {
		// 未激活的窗格蒙一层薄纱，一眼看出键盘输入会去哪里。
		if veil > 0.001 {
			alpha := float32(colors.Veil.A) / 255 * veil
			painter.Fill(r, colors.Veil.Alpha(alpha), design.RadiusCard)
		}
	})
}

func boolToFloat(b bool) float32 {
	if b {
		return 1
	}
	return 0
}

// paneHeader 是卡片顶部的一行：哪台主机、在哪个目录、连接是否健康。
func (a *app) paneHeader(c *ui.Context, colors design.Colors, t *tab, p *pane) {
	theme := c.Theme()
	ui.Row(c).Height(32).Shrink(0).Padding(0, 12).Gap(8).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(colors.Hairline).FontSize(design.TextSmall).Children(func() {
		// 按终端列数估计窗格宽度，窄的时候逐级收起次要信息，而不是让文字互相挤压重叠。
		cols, rows := p.term.Size()
		compact, tiny := cols < 70, cols < 50

		design.CoatView(c, t.host.ID, t.host.Production, 14)
		ui.Text(c, t.host.Name).FontWeight(600).FontSize(design.TextBody).NoWrap().Ellipsis("…").MinWidth(0).Shrink(1)
		// 路径放在一个会裁剪的容器里：容器宽度只取剩余空间，文字超出时用省略号截断。
		ui.Row(c).Basis(0).Grow(1).MinWidth(0).Clip().Children(func() {
			if dir := p.term.Dir(); dir != "" {
				ui.Text(c, shortPath(dir)).TextColor(theme.TextMuted).NoWrap().Ellipsis("…").MinWidth(0).Shrink(1)
			}
		})
		switch {
		case p.ended:
			ui.Text(c, "连接已结束").Key("status").TextColor(theme.TextMuted).NoWrap().Shrink(0)
			ui.ButtonBase(c).Key("reconnect").Label("重新连接").Shrink(0).Children(func() {
				ui.Text(c, "重新连接").TextColor(theme.Accent).FontWeight(600).NoWrap()
			}).OnClick(func() { a.reconnect(t) })
		case t.stalled:
			chip(c, "服务器没有响应", theme.Warning, theme.Warning.Alpha(0.14)).Key("status")
		default:
			if t.host.Production {
				label := "生产环境"
				if compact {
					label = "生产"
				}
				chip(c, label, colors.Danger, colors.DangerSoft)
			}
			ui.Row(c).Key("status").Gap(6).AlignItems(ui.Center).Shrink(0).Label("已连接").Children(func() {
				// 圆点第一次出现时（连上、或从“无响应”恢复）放大回弹一次，提示状态变好了。
				dot := ui.Box(c).Size(6, 6).Radius(3).Background(colors.Live)
				s := dot.Keyframes("pulse", design.Pulse)
				dot.Scale(s, s)
				if t.latency > 0 && !tiny {
					ui.Text(c, formatLatency(t.latency)).TextColor(colors.TextFaint).FontFeatures("tnum").NoWrap()
				}
			})
		}
		if !compact {
			ui.Textf(c, "%d × %d", cols, rows).TextColor(colors.TextFaint).FontFeatures("tnum").NoWrap().Shrink(0)
		}
	})
}

// chip 是一枚小标签。
func chip(c *ui.Context, text string, fg, bg ui.Color) ui.Element {
	return ui.Row(c).Height(20).Padding(0, 8).Radius(design.RadiusChip).AlignItems(ui.Center).Shrink(0).Background(bg).Children(func() {
		ui.Text(c, text).TextColor(fg).FontWeight(600).FontSize(design.TextSmall - 0.5).NoWrap()
	})
}

func (a *app) filesDrawer(c *ui.Context, colors design.Colors, t *tab) {
	theme := c.Theme()
	a.ensureFiles(t)
	a.followCwd(t)
	drawer := raised(ui.Column(c).Key("drawer").Width(280).Shrink(0).Margin(design.Gap/2).Clip(), colors, design.RadiusCard)
	// 抽屉的宽度一步到位，终端只改一次尺寸；内容从右侧滑入。
	// 之前用 Collapse 让宽度从 0 长大，会让终端逐帧改尺寸，已改掉。
	drawer.Transition(design.SlideIn(18)).Children(func() {
		ui.Row(c).Height(36).Shrink(0).Padding(0, 6, 0, 14).Gap(2).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(colors.Hairline).Children(func() {
			name := path.Base(t.dir)
			if t.dir == "" {
				name = "文件"
			}
			ui.Text(c, name).Key("filesTitle").FontWeight(600).Grow(1).MinWidth(0).NoWrap().Ellipsis("…").Tooltip(t.dir)
			iconButton(c, colors, "arrow-up", "上一级", func() { a.openDir(t, path.Dir(t.dir)) }).Disabled(t.dir == "" || t.dir == "/")
			iconButton(c, colors, "refresh-cw", "刷新", func() { a.openDir(t, t.dir) }).Disabled(t.dir == "")
		})
		if t.filesError != "" {
			ui.Text(c, t.filesError).TextColor(colors.Danger).FontSize(design.TextSmall).Padding(8, 14).Selectable()
		}
		if t.filesBusy {
			ui.Text(c, "正在打开文件…").TextColor(theme.TextMuted).Padding(10, 14)
		}
		entries := t.entries
		ui.List(c, nil, len(entries), func(i int) {
			a.fileRow(c, colors, t, entries[i].Name, entries[i].Path, entries[i].IsDir, entries[i].Size)
		}).Grow(1).MinHeight(0).Padding(6, 6, 8, 6).Children(func() {
			if len(entries) == 0 && t.files != nil && t.filesError == "" {
				ui.Text(c, "这个目录是空的").TextColor(theme.TextMuted).Padding(8, 8)
			}
		})
	})
}

// fileRow 显示一项文件。目录可以点击进入；文件暂时没有操作，所以用普通行而不是按钮，
// 以免看起来像“被禁用”。
func (a *app) fileRow(c *ui.Context, colors design.Colors, t *tab, name, full string, isDir bool, size int64) {
	theme := c.Theme()
	content := func() {
		icon := "file"
		color := colors.TextFaint
		if isDir {
			icon, color = "folder", theme.Accent
		}
		ui.Icon(c, design.Icon(icon)).FontSize(14).TextColor(color)
		ui.Text(c, name).Grow(1).MinWidth(0).NoWrap().Ellipsis("…")
		if !isDir {
			ui.Text(c, formatSize(size)).FontSize(design.TextSmall).TextColor(colors.TextFaint).FontFeatures("tnum").Shrink(0)
		}
	}
	if !isDir {
		ui.Row(c).Key(full).Height(28).Padding(0, 8).Gap(8).AlignItems(ui.Center).Children(content)
		return
	}
	row := ui.ButtonBase(c).Key(full).Label(name).Height(28).Padding(0, 8).Gap(8).Justify(ui.Start).AlignItems(ui.Center).Radius(design.RadiusRow - 2)
	if row.Hovered() {
		row.Background(colors.Hover)
	}
	row.Children(content).OnClick(func() { a.openDir(t, full) })
}
