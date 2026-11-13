package main

import (
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"
	"time"

	"serval/core/hosts"
	"serval/core/sshconn"
	"serval/core/vault"
	"serval/design"
	"serval/locales"

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
	a.challengeDialog(c, colors)
	a.hostDialog(c, colors)
	a.groupDialog(c, colors)
	a.proxiesDialog(c, colors)
	a.forwardsDialog(c, colors)
	a.keysDialog(c, colors)
	a.snippetsDialog(c, colors)
	a.settingsDialog(c, colors)
	a.restoreDialog(c, colors)
	a.syncMergeDialog(c)
	a.syncOverwriteDialog(c)
	a.syncConflictDialog(c)
	a.setupDialog(c, colors)
	a.deleteDialog(c)
	a.uploadConfirmDialog(c)
	a.folderConflictDialog(c)
	a.lockConfirmDialog(c)
	a.fileOpDialog(c, colors)
	a.fileDeleteDialog(c)
	a.editorDialog(c, colors)
	a.flushToasts(c)
	a.paletteDialog(c, colors)
	a.broadcastDialog(c)
	// 焦点可能在这一帧才从窗格里的 FocusWithin 改过来，镜像源要跟着重算。
	a.syncBroadcastSource()
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
	c.OnShortcut(mods, ui.KeyI, a.toggleAI)
	c.OnShortcut(mods, ui.KeyBackslash, func() { a.sidebarOpen = !a.sidebarOpen })
	c.OnShortcut(mods, ui.KeyJ, a.toggleFiles)
	c.OnShortcut(mods, ui.KeyComma, func() {
		if !a.settingsPanel.open {
			a.openSettings()
		}
	})
	if t := a.current(); t != nil {
		c.OnShortcut(mods, ui.KeyF, func() { a.openSearch(t) })
		if t.local || t.client != nil {
			c.OnShortcut(mods, ui.KeyD, func() { a.split(t, false) })
			c.OnShortcut(mods|ui.Alt, ui.KeyD, func() { a.split(t, true) })
		}
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

// sidebarIconButton 是侧栏底部工具栏的紧凑图标按钮，带 Tooltip、Label 和可选数量角标。
func sidebarIconButton(c *ui.Context, colors design.Colors, key, icon, label string, count int, fn func()) ui.Element {
	t := c.Theme()
	tip := label
	if count > 0 {
		tip = fmt.Sprintf("%s（%d）", label, count)
	}
	b := ui.ButtonBase(c).Key(key).Label(label).Tooltip(tip).
		Height(30).Grow(1).Center().Gap(3).Radius(design.RadiusRow).Transition(design.Hover)
	if b.Hovered() {
		b.Background(colors.Hover)
	}
	b.Children(func() {
		ui.Icon(c, design.Icon(icon)).FontSize(15).TextColor(t.TextMuted)
		if count > 0 {
			ui.Text(c, fmt.Sprint(count)).FontSize(design.TextSmall - 1).TextColor(colors.TextFaint).FontFeatures("tnum")
		}
	})
	return b.OnClick(fn)
}

func (a *app) groupMenu(group string) func(m *ui.Menu) {
	return func(m *ui.Menu) {
		if m.Item(locales.GroupNew()).Chosen() {
			a.openNewGroup()
		}
		if m.Item(locales.GroupRename()).Chosen() {
			a.openRenameGroup(group)
		}
		empty := len(a.allHostsInGroup(group)) == 0
		if m.Item(locales.GroupDeleteEmpty()).Disabled(!empty).Danger().Chosen() {
			a.deleteEmptyGroup(group)
		}
	}
}

func (a *app) groupDialog(c *ui.Context, colors design.Colors) {
	if !a.groupSheet.open {
		return
	}
	t := c.Theme()
	title, submit := locales.GroupNew(), "创建分组"
	if a.groupSheet.rename {
		title, submit = locales.GroupRename(), locales.Save()
	}
	sheet(c, colors, &a.groupSheet.open, &a.groupSheet.fresh, 340, 20, func() {
		ui.Column(c).Gap(12).Children(func() {
			ui.Text(c, title).FontSize(design.TextHeading).FontWeight(600)
			ui.Field(c, "分组名称", func() {
				c.Key("groupNameInput")
				in := ui.TextInput(c, &a.groupSheet.name).Placeholder("例如：生产环境").AutoFocus()
				if in.Submitted() {
					a.submitGroupDialog()
				}
			})
			if a.groupSheet.err != "" {
				ui.Text(c, a.groupSheet.err).FontSize(design.TextSmall).TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				ui.Button(c, locales.Cancel()).Key("groupCancel").OnClick(func() { a.groupSheet = groupDialogState{} })
				ui.PrimaryButton(c, submit).Key("groupSubmit").OnClick(a.submitGroupDialog)
			})
		})
	})
	if !a.groupSheet.open {
		a.groupSheet = groupDialogState{}
	}
}

// sidebar 是主机列表。它透出窗口的渐变和系统的毛玻璃，不画自己的底色。
func (a *app) sidebar(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	bar := c.TitleBar()
	// 侧栏的宽度一步到位（不让终端逐帧改尺寸），内容从左侧滑入、淡入。
	ui.Column(c).Key("sidebar").Width(240).Shrink(0).DragWindow().Transition(design.SlideIn(-14)).Children(func() {
		ui.Box(c).Height(max(bar.Height, 12)).Shrink(0)

		ui.Column(c).Padding(2, 12, 10, 12).Children(func() {
			// 搜索框 + 快捷键提示 + 顶部“+”新建菜单（添加主机 / 本地终端 / 新建分组）。
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Row(c).Key("hostFilterRow").Height(32).Grow(1).MinWidth(0).Padding(0, 4, 0, 10).Gap(6).
					AlignItems(ui.Center).Radius(design.RadiusRow).Background(colors.Chip).Children(func() {
					ui.Icon(c, design.Icon("search")).FontSize(14).TextColor(t.TextMuted).Shrink(0)
					c.Key("hostFilter")
					ui.TextInput(c, &a.hostFilter).Placeholder("过滤主机").Label("过滤主机").
						Grow(1).MinWidth(0).FontSize(design.TextSmall).Border(0, ui.Transparent).Background(ui.Transparent)
					cmd := ui.ButtonBase(c).Key("openPalette").Label(locales.ViewSearchHostsAndActions()).Size(22, 22).Radius(6).Shrink(0).
						Tooltip("命令面板（" + shortcutText("K") + "）")
					if cmd.Hovered() {
						cmd.Background(colors.Hover)
					}
					cmd.Children(func() {
						ui.Text(c, shortcutText("K")).FontSize(design.TextSmall).TextColor(colors.TextFaint)
					}).OnClick(a.openPalette)
				})
				addMenuBtn := ui.ButtonBase(c).Key("sidebarAddMenu").Label("新建").Tooltip("新建主机、本地终端或分组").
					Size(30, 30).Center().Radius(design.RadiusRow).Shrink(0).Transition(design.Hover)
				if addMenuBtn.Hovered() {
					addMenuBtn.Background(colors.Hover)
				}
				addMenuBtn.Children(func() {
					ui.Icon(c, design.Icon("plus")).FontSize(15).TextColor(t.TextMuted)
				}).PopoverMenuButton(func(m *ui.Menu) {
					if m.Item(locales.AddHost()).Chosen() {
						a.openAdd()
					}
					if m.Item(locales.LocalTerminal()).Chosen() {
						a.openLocalTerminal()
					}
					m.Separator()
					if m.Item(locales.GroupNew()).Chosen() {
						a.openNewGroup()
					}
				})
			})
		})

		filtering := strings.TrimSpace(a.hostFilter) != ""
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).Padding(0, 10, 10, 10).Gap(12).Children(func() {
				if filtering {
					ui.Row(c).Padding(4, 8).Radius(design.RadiusRow).Background(colors.Hover).AlignItems(ui.Center).Gap(6).Children(func() {
						ui.Text(c, locales.HostFilterSortHint()).FontSize(design.TextSmall - 1).TextColor(colors.TextFaint).Grow(1)
						ui.ButtonBase(c).Key("clearHostFilter").Label("清空").Children(func() {
							ui.Text(c, "清空").FontSize(design.TextSmall - 1).TextColor(t.Accent).FontWeight(600)
						}).OnClick(func() { a.hostFilter = "" })
					})
				}
				if len(a.hosts) == 0 && len(a.customGroups) == 0 {
					a.sidebarEmpty(c)
					return
				}
				matched := false
				for _, group := range a.groups() {
					rows := a.hostsInGroup(group)
					if len(rows) > 0 || (!filtering && slices.Contains(a.customGroups, group)) {
						matched = true
						break
					}
				}
				if !matched {
					ui.Text(c, "没有匹配的主机").FontSize(design.TextSmall).TextColor(t.TextMuted).Padding(10, 10)
					return
				}
				for _, group := range a.groups() {
					rows := a.hostsInGroup(group)
					isCustomEmpty := !filtering && len(rows) == 0 && slices.Contains(a.customGroups, group)
					if len(rows) == 0 && !isCustomEmpty {
						continue
					}
					collapsed := a.groupCollapsed(group)
					ui.Column(c).Key("group:" + group).Gap(2).Children(func() {
						iconName, action := "chevron-down", "折叠"
						if collapsed {
							iconName, action = "chevron-right", "展开"
						}
						header := ui.ButtonBase(c).Key("groupToggle:"+group).Label(action+" "+group).
							Height(24).Padding(0, 6).Gap(4).Justify(ui.Start).AlignItems(ui.Center).
							Radius(design.RadiusRow).Transition(design.Hover).PopoverMenu(a.groupMenu(group))
						var groupInsertLine bool
						if !filtering {
							if dragged, ok := ui.DragOver[hostDrag](header); ok {
								if h, ok := a.hostByID(dragged.ID); ok && h.Group != group {
									groupInsertLine = true
								}
							}
							if dropped, ok := ui.Drop[hostDrag](header); ok {
								if err := a.moveHostOrder(dropped.ID, "", group); err != nil {
									a.notice = "调整主机分组失败：" + err.Error()
								}
							}
						}
						if groupInsertLine {
							header.DrawOver(func(painter *ui.Painter, r ui.Rect) {
								painter.Fill(ui.Rect{X: r.X + 4, Y: r.Y + r.H - 2, W: max(0, r.W-8), H: 2}, t.Accent, 1)
							})
						}
						if header.Hovered() {
							header.Background(colors.Hover)
						}
						header.Children(func() {
							ui.Icon(c, design.Icon(iconName)).FontSize(12).TextColor(colors.TextFaint).Shrink(0)
							ui.Text(c, group).FontSize(design.TextSmall).FontWeight(500).TextColor(colors.TextFaint).SingleLine()
						}).OnClick(func() { a.toggleGroup(group) })
						if collapsed {
							return
						}
						for _, h := range rows {
							a.hostRow(c, colors, h)
						}
					})
				}
			})
		})

		ui.Column(c).Padding(6, 10, 10, 10).Gap(4).Children(func() {
			for _, message := range []string{a.hostsError, a.notice} {
				if message != "" {
					ui.Text(c, message).Key("notice").FontSize(design.TextSmall).TextColor(colors.Danger).
						Padding(8, 10).Margin(0, 0, 4, 0).Radius(design.RadiusRow).Background(colors.DangerSoft).Selectable()
				}
			}
			a.syncStatus(c, colors)
			ui.Row(c).Key("sidebarToolbar").Gap(4).AlignItems(ui.Center).Children(func() {
				sidebarIconButton(c, colors, "openKeys", "key-round", locales.ViewKeys(), len(a.keys), a.openKeys)
				sidebarIconButton(c, colors, "openProxies", "globe", locales.Proxy(), len(a.proxies), a.openProxies)
				sidebarIconButton(c, colors, "openForwards", "arrow-left-right", locales.PortForward(), len(a.forwards), a.openForwards)
				sidebarIconButton(c, colors, "openAI", "sparkles", locales.AiAssistant(), 0, a.openAI)
				sidebarIconButton(c, colors, "openSettings", "settings", locales.Settings(), 0, a.openSettings).
					Tooltip(locales.Settings() + "（" + shortcutText(",") + "）")
			})
		})
	})
}

// syncStatus 在侧栏底部放一个不占终端宽度的状态。没开同步时不显示。
func (a *app) syncStatus(c *ui.Context, colors design.Colors) {
	if !a.syncConfigured() && a.syncErr == "" && !a.syncing {
		return
	}
	caption := "等待同步"
	tip := "已开启同步，还没有成功对齐过"
	icon := "cloud"
	color := c.Theme().TextMuted
	switch {
	case a.syncing:
		caption = "正在同步"
		tip = "正在与同步服务对齐"
		icon = "refresh-cw"
	case a.syncErr != "":
		caption = "同步失败"
		tip = a.syncErr
		icon = "cloud-off"
		color = colors.Danger
	case !a.lastSync.IsZero():
		caption = "同步正常"
		tip = "上次同步 " + formatSyncTime(a.lastSync)
	}
	row := ui.ButtonBase(c).Key("syncStatus").Label("同步状态").Tooltip(tip).Height(28).Padding(0, 8).Gap(8).
		Justify(ui.Start).AlignItems(ui.Center).Radius(design.RadiusRow).Transition(design.Hover)
	if row.Hovered() {
		row.Background(colors.Hover)
	}
	row.Children(func() {
		ui.Icon(c, design.Icon(icon)).FontSize(13).TextColor(color)
		ui.Text(c, caption).FontSize(design.TextSmall).TextColor(color).SingleLine()
	}).OnClick(func() {
		if !a.settingsPanel.open {
			a.openSettings()
		}
		a.settingsPanel.tab = settingsTabSync
	})
}

func (a *app) sidebarEmpty(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Padding(10, 10).Gap(6).Children(func() {
		ui.Text(c, locales.ViewNoHosts()).FontWeight(600)
		ui.Text(c, locales.ViewNoHostsHint()).
			FontSize(design.TextSmall).TextColor(t.TextMuted)
		if a.vault != nil && a.vault.Empty() {
			ui.Button(c, "从同步恢复").Key("restoreOpen").OnClick(a.openRestore)
		}
	})
}

func hostTooltip(h hosts.Host) string {
	proto := "SSH"
	switch h.EffectiveProtocol() {
	case hosts.ProtocolTelnet:
		proto = "Telnet"
	case hosts.ProtocolSerial:
		proto = "串口"
	}
	parts := []string{fmt.Sprintf("%s · %s", proto, h.Target())}
	if h.Distro != "" {
		parts = append(parts, "系统: "+h.Distro)
	}
	if jumps := hosts.SplitJumps(h.ProxyJump); len(jumps) > 0 {
		parts = append(parts, "跳板: "+strings.Join(jumps, " → "))
	}
	if h.Production {
		parts = append(parts, "生产环境")
	}
	return strings.Join(parts, "\n")
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
	var showInsertLine bool
	if strings.TrimSpace(a.hostFilter) == "" {
		if dragged, ok := ui.DragOver[hostDrag](line); ok && dragged.ID != h.ID {
			showInsertLine = true
		}
		if dropped, ok := ui.Drop[hostDrag](line); ok && dropped.ID != h.ID {
			if err := a.moveHostOrder(dropped.ID, h.ID, h.Group); err != nil {
				a.notice = "调整主机排序失败：" + err.Error()
			}
		}
	}
	if showInsertLine {
		line.DrawOver(func(painter *ui.Painter, r ui.Rect) {
			painter.Fill(ui.Rect{X: r.X + 8, Y: r.Y, W: max(0, r.W-16), H: 2}, t.Accent, 1)
		})
	}
	line.Children(func() {
		if selected {
			selectionPill(c, colors, "hosts", colors.Selected, true)
		}
		row := ui.ButtonBase(c).Key("host:"+h.ID).Label(h.Name).Tooltip(hostTooltip(h)).Grow(1).MinWidth(0).Padding(6, 4, 6, 10).Gap(10).
			Justify(ui.Start).AlignItems(ui.Center)
		if strings.TrimSpace(a.hostFilter) == "" {
			row.Drag(hostDrag{ID: h.ID})
		}
		if busy {
			row.Description(locales.ViewConnecting())
		}
		row.Children(func() {
			coat := design.DistroCoatView(c, h.ID, h.Distro, h.Production, 26)
			if busy {
				// 连接中斑纹轻轻呼吸，代替转圈图标：状态就落在这台主机自己的标识上。
				coat.Opacity(coat.Keyframes("breathe", design.Breathe, design.BreatheOptions))
			}
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, h.Name).FontWeight(500).SingleLine()
				sub := h.Target()
				if jumps := hosts.SplitJumps(h.ProxyJump); len(jumps) > 0 {
					// 经跳板机的主机显示途经路线：连不上时，用户第一眼就知道该先查哪一台。
					sub = locales.ViewVia(strings.Join(jumps, " → "))
				}
				ui.Text(c, sub).FontSize(design.TextSmall).TextColor(t.TextMuted).SingleLine()
			})
			if connected {
				ui.Box(c).Size(7, 7).Radius(4).Background(colors.Live).Shrink(0).Label(locales.ViewConnected())
			}
		}).OnClick(func() { a.open(h) })
		row.PopoverMenu(menu)

		// “更多”按钮只在悬停或选中时出现，打开和右键相同的菜单：
		// 不知道可以右键的人也能找到编辑和删除。键盘用户在行上按菜单键或 Shift+F10。
		if hovered || selected {
			more := ui.ButtonBase(c).Key("more:"+h.ID).Label(locales.MoreActions()).Size(24, 24).Radius(6).Shrink(0).Transition(design.Hover)
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
			iconButton(c, colors, "panel-left", locales.ViewShowSidebar(), func() { a.sidebarOpen = true })
		}
		// 标签放不下时一起收窄到只剩斑纹，每个都还能点到、认得出。
		ui.Row(c).Grow(1).MinWidth(0).Gap(4).AlignItems(ui.Center).Children(func() {
			for i, t := range a.tabs {
				a.tabItem(c, colors, i, t)
			}
		})
		if a.broadcastOn {
			theme := c.Theme()
			b := ui.ButtonBase(c).Key("exitAllBroadcast").Label("退出广播").Tooltip("退出全部广播").
				Height(24).Padding(0, 8).Gap(4).Radius(design.RadiusChip).AlignItems(ui.Center).Shrink(0).
				Background(theme.Accent.Alpha(0.14)).Transition(design.Hover)
			if b.Hovered() {
				b.Background(theme.Accent.Alpha(0.22))
			}
			b.Children(func() {
				ui.Icon(c, design.Icon("radio")).FontSize(12).TextColor(theme.Accent)
				ui.Text(c, "退出广播").TextColor(theme.Accent).FontWeight(600).FontSize(design.TextSmall).NoWrap()
				ui.Icon(c, design.Icon("x")).FontSize(11).TextColor(theme.Accent)
			}).OnClick(func() {
				a.clearBroadcast()
				a.toast(ui.Toast{Title: "已关闭广播"})
			})
		}
		if t := a.current(); t != nil {
			if !t.local && !a.isSavedHost(t.host.ID) { // 本地终端不是主机，不能保存
				ui.Button(c, locales.ViewSaveAsHost()).Key("saveAsHost").Height(28).Padding(0, 8).
					Background(colors.Selected).Radius(design.RadiusRow).OnClick(func() { a.openSaveHost(t.host) })
			}
			iconButton(c, colors, "search", locales.ViewFindShortcut(shortcutText("F")), func() { a.openSearch(t) })
			if t.local || t.client != nil {
				iconButton(c, colors, "columns-2", locales.ViewSplitRightShortcut(shortcutText("D")), func() { a.split(t, false) })
				iconButton(c, colors, "rows-2", locales.ViewSplitDown(), func() { a.split(t, true) })
			}
			if !t.local && t.client != nil {
				iconButton(c, colors, "folder", locales.ViewFilesShortcut(shortcutText("J")), a.toggleFiles)
			}
		}
		aiBtn := iconButton(c, colors, "sparkles", "AI Agent（"+shortcutText("I")+"）", a.toggleAI).Key("tabToggleAI")
		if a.ai.open {
			aiBtn.Background(c.Theme().Accent.Alpha(0.14))
		}
	})
}

// tabLabel 是标签和窗格标题上显示的名称。
// 本地终端不显示存进去的 host.Name：侧栏按钮是「本地终端」，标题必须仍是「本机」，
// 英文里也要和 Local terminal 区分开。
func tabLabel(t *tab) string {
	if t.local || t.host.ID == "local" {
		return locales.ThisMachine()
	}
	return t.host.Name
}

// displayTabTitle 在同主机打开多个标签时自动附上序号，如 "prod (2)"。
func displayTabTitle(tabs []*tab, idx int) string {
	base := tabLabel(tabs[idx])
	total := 0
	ordinal := 0
	for i, tb := range tabs {
		if tabLabel(tb) == base {
			total++
			if i == idx {
				ordinal = total
			}
		}
	}
	if total > 1 && ordinal > 1 {
		return fmt.Sprintf("%s (%d)", base, ordinal)
	}
	return base
}

// tabMenu 是标签页右键菜单，提供复制标签页和关闭等快捷操作。
func (a *app) tabMenu(t *tab) func(m *ui.Menu) {
	return func(m *ui.Menu) {
		if m.Item(locales.ViewTabDuplicate()).Chosen() {
			a.duplicateTab(t)
		}
		m.Separator()
		if m.Item(locales.ViewCloseTab(tabLabel(t))).Chosen() {
			a.closeTab(t)
		}
		if len(a.tabs) > 1 {
			if m.Item(locales.ViewTabCloseOther()).Chosen() {
				a.closeOtherTabs(t)
			}
			idx := slices.Index(a.tabs, t)
			if idx >= 0 && idx < len(a.tabs)-1 {
				if m.Item(locales.ViewTabCloseRight()).Chosen() {
					a.closeTabsToRight(t)
				}
			}
			m.Separator()
			if m.Item(locales.ViewTabCloseAll()).Danger().Chosen() {
				a.closeAllTabs()
			}
		}
	}
}

func (a *app) tabItem(c *ui.Context, colors design.Colors, i int, t *tab) {
	theme := c.Theme()
	selected := i == a.active
	title := displayTabTitle(a.tabs, i)
	// 只告诉辅助技术“已选中”。不用 Selected()：它会给按钮画一层强调色背景，
	// 平时被高亮盖住，高亮滑走的那一瞬间会露出一闪的靛蓝色。
	item := ui.ButtonBase(c).Key(fmt.Sprintf("tab:%d", t.id)).Label(title).AccessSelected(selected).
		Height(28).MinWidth(36).MaxWidth(200).Shrink(1).Padding(0, 10).Gap(8).AlignItems(ui.Center).
		Radius(design.RadiusRow).Transition(design.Hover).PopoverMenu(a.tabMenu(t)).Drag(tabDrag{ID: t.id})
	var tabInsertLine bool
	if dragged, ok := ui.DragOver[tabDrag](item); ok && dragged.ID != t.id {
		tabInsertLine = true
	}
	if dropped, ok := ui.Drop[tabDrag](item); ok && dropped.ID != t.id {
		a.moveTab(dropped.ID, t.id)
	}
	if tabInsertLine {
		item.DrawOver(func(painter *ui.Painter, r ui.Rect) {
			painter.Fill(ui.Rect{X: r.X, Y: r.Y + 4, W: 2, H: max(0, r.H-8)}, theme.Accent, 1)
		})
	}
	hovered := item.Hovered()
	if !selected && hovered {
		item.Background(colors.Hover)
	}
	item.Children(func() {
		if selected {
			selectionPill(c, colors, "tabs", colors.Selected, true)
		}
		design.DistroCoatView(c, t.host.ID, t.host.Distro, t.host.Production, 14)
		color := theme.TextMuted
		if selected {
			color = theme.Text
		}
		ui.Text(c, title).TextColor(color).FontWeight(500).SingleLine().MinWidth(0).Shrink(1)
		if hovered || selected {
			ui.ButtonBase(c).Key("close").Label(locales.ViewCloseTab(title)).Size(16, 16).Center().Radius(4).Shrink(0).Children(func() {
				ui.Icon(c, design.Icon("x")).FontSize(12).TextColor(colors.TextFaint)
			}).OnClick(func() { a.closeTab(t) })
		}
	}).OnClick(func() {
		a.active = i
		a.saveSessionLayout()
	})
}

func (a *app) workspace(c *ui.Context, colors design.Colors) {
	t := a.current()
	left := float32(0)
	if !a.sidebarOpen {
		left = design.Gap / 2
	}
	// 每张卡片自带一半的间距（Margin），这里再补一半，卡片之间和到窗口边缘都是一样的空隙。
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Padding(0, design.Gap/2, design.Gap/2, left).Children(func() {
		if t == nil {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				a.emptyState(c, colors)
			})
		} else {
			ui.Column(c).Key(t.id).Grow(1).MinWidth(0).Children(func() {
				a.paneView(c, colors, t, t.root)
			})
			if a.filesOpen {
				a.filesDrawer(c, colors, t)
			}
		}
		if a.ai.open {
			a.aiDrawer(c, colors)
		}
	})
}

// emptyState 是打开应用的第一屏。品牌斑纹放大作为标志，也预告了每台主机都有自己的图案；
// 存在主机时提供最近连接快捷入口与常用快捷键提示。
func (a *app) emptyState(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	ui.Column(c).Fill().Center().Gap(20).Padding(32).Children(func() {
		design.BrandView(c, 76).Shadow(0, 14, 32, -10, colors.CardShadow)
		ui.Column(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.ViewConnectHost()).FontSize(design.TextDisplay).FontWeight(600).LetterSpacing(-0.4)
			hint := locales.ViewEmptyHint(shortcutText("K"))
			if len(a.hosts) == 0 {
				hint = locales.ViewAddFirstHint()
			}
			ui.Text(c, hint).TextColor(t.TextMuted)
		})
		if len(a.connecting) > 0 {
			for _, hostID := range slices.Sorted(maps.Keys(a.connecting)) {
				cancel := a.connecting[hostID]
				if h, ok := a.hostByID(hostID); ok {
					phase := a.connectingPhase[hostID]
					if phase == "" {
						phase = locales.ViewConnecting()
					}
					ui.Row(c).Padding(8, 12).Gap(10).AlignItems(ui.Center).Radius(design.RadiusRow).
						Background(colors.Card).Border(1, colors.CardBorder).Children(func() {
						design.DistroCoatView(c, h.ID, h.Distro, h.Production, 18)
						ui.Text(c, h.Name).FontWeight(500)
						ui.Text(c, phase).FontSize(design.TextSmall).TextColor(t.TextMuted)
						ui.Button(c, "取消连接").Key("cancelConnect:"+h.ID).Height(24).Padding(0, 8).OnClick(cancel)
					})
				}
			}
		}
		if len(a.hosts) == 0 {
			ui.PrimaryButton(c, locales.AddHost()).Key("addFirst").OnClick(func() { a.openAdd() })
			return
		}
		var recents []hosts.Host
		for _, id := range a.local.RecentHosts {
			if h, ok := a.hostByID(id); ok {
				recents = append(recents, h)
				if len(recents) >= 5 {
					break
				}
			}
		}
		if len(recents) > 0 {
			ui.Column(c).Width(360).Gap(6).Padding(12).Radius(design.RadiusCard).
				Background(colors.Card).Border(1, colors.CardBorder).Children(func() {
				ui.Text(c, locales.EmptyRecentHosts()).FontSize(design.TextSmall).FontWeight(600).TextColor(colors.TextFaint)
				for _, h := range recents {
					host := h
					btn := ui.ButtonBase(c).Key("recent:"+host.ID).Label("连接 "+host.Name).
						Height(32).Padding(0, 8).Gap(8).Justify(ui.Start).AlignItems(ui.Center).
						Radius(design.RadiusRow).Transition(design.Hover)
					if btn.Hovered() {
						btn.Background(colors.Hover)
					}
					btn.Children(func() {
						design.DistroCoatView(c, host.ID, host.Distro, host.Production, 18)
						ui.Text(c, host.Name).FontWeight(500).Grow(1).SingleLine()
						ui.Text(c, host.Target()).FontSize(design.TextSmall).TextColor(colors.TextFaint).SingleLine()
					}).OnClick(func() { a.open(host) })
				}
			})
		}
		ui.Row(c).Gap(14).AlignItems(ui.Center).Children(func() {
			for _, item := range [][2]string{
				{shortcutText("K"), "命令面板"},
				{shortcutText("I"), locales.AiAssistantTitle()},
				{shortcutText("\\"), "切换侧栏"},
				{shortcutText("J"), "文件抽屉"},
				{shortcutText(","), locales.Settings()},
			} {
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					chip(c, item[0], t.TextMuted, colors.Chip)
					ui.Text(c, item[1]).FontSize(design.TextSmall).TextColor(colors.TextFaint)
				})
			}
		})
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
	// 边框宽度一步到位，不做成动画：宽度变化会让终端改一次尺寸，动画则会连发几十次。
	broadcastMark(card, colors, c.Theme().Accent, a.broadcast[p.id], t.host.Production)
	card.Children(func() {
		a.paneHeader(c, colors, t, p)
		if p.ended && !t.reconnecting {
			ui.Row(c).Key(fmt.Sprintf("endedBanner:%d", p.id)).Height(32).Shrink(0).Padding(0, 12).Gap(8).
				AlignItems(ui.Center).Background(colors.DangerSoft).BorderWidth(0, 0, 1, 0).BorderColor(colors.Hairline).Children(func() {
				ui.Icon(c, design.Icon("cloud-off")).FontSize(14).TextColor(colors.Danger)
				ui.Text(c, locales.PaneDisconnected()).FontSize(design.TextSmall).FontWeight(500).TextColor(c.Theme().Text).Grow(1)
				ui.Button(c, locales.Reconnect()).Key("bannerReconnect").Height(24).Padding(0, 8).OnClick(func() { a.reconnect(t) })
				ui.Button(c, locales.PaneCloseTab()).Key("bannerClose").Height(24).Padding(0, 8).OnClick(func() { a.closeTab(t) })
			})
		}
		if p.searchOpen {
			a.searchBar(c, colors, t, p)
		}
		if p.zmodem != nil {
			a.zmodemBar(c, colors, p)
		}
		// 固定的 Key：查找栏插在终端前面时，终端控件按位置识别会被当成新元素重新挂载，
		// AutoFocus 随之再次生效，把刚交给查找框的焦点抢回去。
		c.Key(fmt.Sprintf("terminal:%d", p.id))
		view := terminal.View(c, p.term).Grow(1).MinHeight(0)
		if p.focusTerm {
			view.Focus()
			if view.FocusWithin() {
				p.focusTerm = false
			}
		}
		if focused {
			view.AutoFocus()
		}
		if view.FocusWithin() && !focused {
			t.focus = p
		}
		a.dropLayer(c, colors, t, p, view)
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
		if cols != p.lastCols || rows != p.lastRows {
			if p.lastCols != 0 || p.lastRows != 0 {
				p.sizeChangedAt = time.Now()
			}
			p.lastCols, p.lastRows = cols, rows
		}
		compact, tiny := cols < 70, cols < 50

		multiPane := t.root.leaves() > 1
		if multiPane {
			design.DistroCoatView(c, t.host.ID, t.host.Distro, t.host.Production, 14)
			ui.Text(c, tabLabel(t)).FontWeight(600).FontSize(design.TextBody).SingleLine().MinWidth(0).Shrink(1)
		} else {
			// 单窗格时主机名已在上方标签页清晰显示，此处仅展示主机专属斑纹，避免信息冗余
			design.DistroCoatView(c, t.host.ID, t.host.Distro, t.host.Production, 14)
		}
		user, dir := paneUserAndDir(t, p)
		if user != "" {
			if user == "root" {
				ui.Row(c).Key(fmt.Sprintf("paneUser:%d", p.id)).Height(20).Padding(0, 7).Gap(4).
					Radius(design.RadiusChip).AlignItems(ui.Center).Shrink(0).Background(colors.DangerSoft).Children(func() {
					ui.Box(c).Size(5, 5).Radius(3).Background(colors.Danger)
					ui.Text(c, user).TextColor(colors.Danger).FontWeight(700).FontSize(design.TextSmall - 0.5).NoWrap()
				})
			} else {
				chip(c, user, theme.TextMuted, colors.Chip).Key(fmt.Sprintf("paneUser:%d", p.id))
			}
		}
		// 路径放在一个会裁剪的容器里：容器宽度只取剩余空间，文字超出时用省略号截断。
		ui.Row(c).Basis(0).Grow(1).MinWidth(0).Clip().Children(func() {
			if dir != "" {
				ui.Text(c, shortPath(dir)).TextColor(theme.TextMuted).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
			}
		})
		switch {
		case t.reconnecting:
			chip(c, locales.ViewReconnecting(t.reconnectAttempt), theme.Warning, theme.Warning.Alpha(0.14)).Key("status")
			ui.ButtonBase(c).Key("cancelReconnect").Label(locales.Cancel()).Shrink(0).Children(func() {
				ui.Text(c, locales.Cancel()).TextColor(theme.Accent).FontWeight(600).NoWrap()
			}).OnClick(func() { a.cancelReconnect(t) })
		case p.ended:
			ui.Text(c, locales.ViewSessionEnded()).Key("status").TextColor(theme.TextMuted).NoWrap().Shrink(0)
			ui.ButtonBase(c).Key("reconnect").Label(locales.Reconnect()).Shrink(0).Children(func() {
				ui.Text(c, locales.Reconnect()).TextColor(theme.Accent).FontWeight(600).NoWrap()
			}).OnClick(func() { a.reconnect(t) })
		case t.local:
			ui.Row(c).Key("status").Gap(6).AlignItems(ui.Center).Shrink(0).Label(locales.ViewLocal()).Children(func() {
				dot := ui.Box(c).Size(6, 6).Radius(3).Background(colors.Live)
				s := dot.Keyframes("pulse", design.Pulse)
				dot.Scale(s, s)
				if !tiny {
					ui.Text(c, locales.ViewLocal()).TextColor(colors.TextFaint).NoWrap()
				}
			})
		case t.stalled:
			chip(c, locales.ViewServerUnresponsive(), theme.Warning, theme.Warning.Alpha(0.14)).Key("status")
		default:
			if t.host.Production {
				label := locales.ViewProduction()
				if compact {
					label = locales.ViewProductionShort()
				}
				chip(c, label, colors.Danger, colors.DangerSoft)
			}
			ui.Row(c).Key("status").Gap(6).AlignItems(ui.Center).Shrink(0).Label(locales.ViewConnected()).Children(func() {
				// 圆点第一次出现时（连上、或从“无响应”恢复）放大回弹一次，提示状态变好了。
				dot := ui.Box(c).Size(6, 6).Radius(3).Background(colors.Live)
				s := dot.Keyframes("pulse", design.Pulse)
				dot.Scale(s, s)
				if t.latency > 0 && !tiny {
					ui.Text(c, formatLatency(t.latency)).TextColor(colors.TextFaint).FontFeatures("tnum").NoWrap()
				}
			})
		}
		if !compact && !p.sizeChangedAt.IsZero() && time.Since(p.sizeChangedAt) < 1500*time.Millisecond {
			ui.Textf(c, "%d × %d", cols, rows).TextColor(colors.TextFaint).FontFeatures("tnum").NoWrap().Shrink(0)
		}
		if t.monitorOpen && !t.local && t.client != nil && !p.ended && !tiny {
			a.paneMonitorMetrics(c, colors, t)
		}
		if !t.local && t.client != nil {
			monitorLabel := locales.MonitorToggleOpen()
			if t.monitorOpen {
				monitorLabel = locales.MonitorToggleClose()
			}
			btn := iconButton(c, colors, "activity", monitorLabel, func() { a.toggleMonitor(t) }).Disabled(p.ended)
			if t.monitorOpen {
				btn.Background(theme.Accent.Alpha(0.12))
			}
		}
		if a.broadcast[p.id] {
			fg, bg := theme.Accent, theme.Accent.Alpha(0.14)
			if t.host.Production {
				fg, bg = colors.Danger, colors.DangerSoft
			}
			b := ui.ButtonBase(c).Key(fmt.Sprintf("paneBroadcast:%d", p.id)).
				Label(fmt.Sprintf("移出窗格广播：%s %d", t.host.Name, paneOrdinal(t.root, p))).
				Tooltip("点击移出广播").
				Height(20).Padding(0, 8).Gap(4).Radius(design.RadiusChip).AlignItems(ui.Center).Shrink(0).
				Background(bg).Transition(design.Hover)
			if b.Hovered() {
				b.Background(fg.Alpha(0.22))
			}
			b.Children(func() {
				ui.Text(c, "广播").TextColor(fg).FontWeight(600).FontSize(design.TextSmall - 0.5).NoWrap()
				ui.Icon(c, design.Icon("x")).FontSize(11).TextColor(fg)
			}).OnClick(func() {
				a.togglePaneBroadcast(t, p)
			})
		}
		moreLabel := locales.PaneMoreActions()
		if t.root.leaves() > 1 {
			moreLabel = fmt.Sprintf("%s %d", locales.PaneMoreActions(), paneOrdinal(t.root, p))
		}
		moreBtn := ui.ButtonBase(c).Key(fmt.Sprintf("paneMore:%d", p.id)).Label(moreLabel).Tooltip(locales.PaneMoreActions()).
			Size(26, 26).Center().Radius(design.RadiusRow).Shrink(0).Transition(design.Hover)
		if moreBtn.Hovered() {
			moreBtn.Background(colors.Hover)
		}
		moreBtn.Children(func() {
			ui.Icon(c, design.Icon("ellipsis")).FontSize(14).TextColor(theme.TextMuted)
		}).PopoverMenuButton(func(m *ui.Menu) {
			broadcastLabel := broadcastButtonLabel(a.broadcast[p.id], t.host.Name, paneOrdinal(t.root, p))
			if m.Item(broadcastLabel).Disabled(p.ended && !a.broadcast[p.id]).Chosen() {
				a.togglePaneBroadcast(t, p)
			}
			if m.Item(locales.AiAnalyzeOutput()).Chosen() {
				a.openAIExplainFromActivePane()
			}
		})
	})
}

// chip 是一枚小标签。
func chip(c *ui.Context, text string, fg, bg ui.Color) ui.Element {
	return ui.Row(c).Height(20).Padding(0, 8).Radius(design.RadiusChip).AlignItems(ui.Center).Shrink(0).Background(bg).Children(func() {
		ui.Text(c, text).TextColor(fg).FontWeight(600).FontSize(design.TextSmall - 0.5).NoWrap()
	})
}

func (a *app) filesDrawer(c *ui.Context, colors design.Colors, t *tab) {
	a.ensureFiles(t)
	a.followCwd(t)
	if a.filesWidth <= 0 {
		a.filesWidth = 560
	}
	if a.filesSplit <= 0 {
		a.filesSplit = 50
	}
	// 终端与文件抽屉之间的拖动分隔条：负外边距贴在两张卡片之间的空隙里，不改变卡片间距；
	// 拖动改变抽屉宽度，双击恢复默认宽度。
	grip := ui.Box(c).Key("drawerResize").Width(8).Shrink(0).Margin(design.Gap/2, -4).
		Cursor(ui.CursorResizeEW).Focusable().Label("调整文件抽屉宽度")
	dx, _, held := grip.Dragged()
	windowWidth, _ := c.Size()
	maxW := max(float32(320), windowWidth-360)
	switch {
	case grip.DoubleClicked():
		a.filesWidth = 560
	case held && dx != 0:
		a.filesWidth = min(max(a.filesWidth-dx, 320), maxW)
	case grip.Shortcut(0, ui.KeyLeft):
		a.filesWidth = min(a.filesWidth+20, maxW)
	case grip.Shortcut(0, ui.KeyRight):
		a.filesWidth = max(a.filesWidth-20, 320)
	}
	if held || grip.Hovered() {
		accent := c.Theme().Accent
		grip.Draw(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X + r.W/2 - 0.5, Y: r.Y + 8, W: 1, H: max(0, r.H-16)}, accent, 0)
		})
	}
	// 两栏并排，支持拖动调整抽屉总宽和内部左右栏比例；不做宽度动画，避免终端连续收到几十次 SIGWINCH。
	drawer := raised(ui.Column(c).Key("drawer").Width(a.filesWidth).Shrink(0).Margin(design.Gap/2).Clip(), colors, design.RadiusCard)
	// 抽屉的宽度一步到位，终端只改一次尺寸；内容从右侧滑入。
	// 之前用 Collapse 让宽度从 0 长大，会让终端逐帧改尺寸，已改掉。
	drawer.Transition(design.SlideIn(18)).Children(func() {
		// 抽屉本身接收拖放，目标是当前列出的目录，不是终端的工作目录。
		// 平时不铺接收层，避免挡住列表；有文件悬停时再盖一层提示，宽度不变，终端不会跟着改尺寸。
		if !t.local && t.dir != "" {
			if dropped := drawer.DroppedFiles(); len(dropped) > 0 {
				a.uploadInto(t, t.dir, dropped)
			}
			if drawer.FileDragOver() {
				theme := c.Theme()
				ui.Box(c).Absolute().Top(0).Right(0).Bottom(0).Left(0).
					Background(theme.Accent.Alpha(0.10)).Border(1.5, theme.Accent.Alpha(0.6)).
					Justify(ui.Center).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "上传到当前目录").FontWeight(600)
					ui.Text(c, shortPath(t.dir)).FontSize(design.TextSmall).TextColor(theme.TextMuted).SingleLine()
				})
			}
		}
		a.ensureLocalList()
		c.Key("filesSplit")
		ui.SplitPercent(c, &a.filesSplit, func() {
			a.localColumn(c, colors, t)
		}, func() {
			a.remoteColumn(c, colors, t)
		}).Grow(1).MinHeight(0).SplitReset(50)
		if len(t.transfers) > 0 {
			ui.Divider(c)
			a.transferQueueView(c, colors, t)
		}
	})
}

// transferQueueView 展示传输队列面板。
func (a *app) transferQueueView(c *ui.Context, colors design.Colors, t *tab) {
	theme := c.Theme()
	ui.Column(c).Key("transfers").Padding(6, 10, 8, 10).Gap(6).Children(func() {
		ui.Row(c).Height(20).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.ViewTransferQueue()).FontSize(design.TextSmall).FontWeight(600).TextColor(theme.TextMuted)
		})
		for _, tr := range t.transfers {
			item := tr
			ui.Column(c).Key(fmt.Sprintf("tr:%d", item.id)).Gap(2).Padding(4, 6).
				Radius(design.RadiusRow - 2).Background(colors.Hover).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					icon := "arrow-down"
					if item.kind == transferUpload {
						icon = "arrow-up"
					}
					if item.folder {
						icon = "folder"
					}
					ui.Icon(c, design.Icon(icon)).FontSize(12).TextColor(theme.Accent)
					ui.Text(c, item.fileName).FontSize(design.TextSmall).FontWeight(500).Grow(1).SingleLine()
					if !item.done.Load() {
						iconButton(c, colors, "x", locales.ViewCancelTransfer(), func() { a.cancelTransfer(item) })
					}
				})
				ui.Row(c).Children(func() {
					var info string
					if item.done.Load() {
						if item.err != "" {
							info = locales.ViewTransferFailed(item.err)
						} else {
							info = locales.ViewTransferDone()
						}
					} else {
						speed := item.speed.Load()
						speedStr := ""
						if speed > 0 {
							speedStr = " · " + formatSize(speed) + "/s"
						}
						info = fmt.Sprintf("%s / %s%s", formatSize(item.transferred.Load()), formatSize(item.total.Load()), speedStr)
						if item.folder {
							info = locales.ViewFolderProgress(int(item.filesDone.Load()), int(item.filesTotal.Load()), info)
						}
					}
					color := colors.TextFaint
					if item.err != "" {
						color = colors.Danger
					}
					ui.Text(c, info).FontSize(design.TextSmall - 1).TextColor(color).FontFeatures("tnum")
				})
			})
		}
	})
}

// fileRow 显示一项文件。
func (a *app) fileRow(c *ui.Context, colors design.Colors, t *tab, entry sshconn.Entry) {
	theme := c.Theme()
	content := func() {
		icon := "file"
		color := colors.TextFaint
		if entry.IsDir {
			icon, color = "folder", theme.Accent
		}
		ui.Icon(c, design.Icon(icon)).FontSize(14).TextColor(color)
		ui.Text(c, entry.Name).Grow(1).MinWidth(0).SingleLine()
		if !entry.IsDir {
			ui.Text(c, formatSize(entry.Size)).FontSize(design.TextSmall).TextColor(colors.TextFaint).FontFeatures("tnum").Shrink(0)
		}
	}
	menu := func(m *ui.Menu) {
		if entry.IsDir {
			if m.Item(locales.ViewOpen()).Chosen() {
				a.openDir(t, entry.Path)
			}
			if m.Item("下载文件夹").Chosen() {
				a.startDownloadFolder(t, entry.Path, entry.Name)
			}
		} else {
			if m.Item("编辑").Chosen() {
				a.openEditor(t, entry.Path, entry.Name)
			}
			if m.Item(locales.Download()).Chosen() {
				a.startDownload(t, entry.Path, entry.Name)
			}
		}
		if m.Item(locales.Rename()).Chosen() {
			a.openRenameFile(t, entry.Path, entry.Name)
		}
		if m.Item(locales.ChangeMode()).Chosen() {
			a.openPermissions(t, entry.Path, entry.Name, entry.Mode)
		}
		if m.Item(locales.ViewCopyPath()).Chosen() {
			c.WriteClipboard(entry.Path)
			a.toast(ui.Toast{Title: locales.ViewPathCopied()})
		}
		if m.Item(locales.Delete()).Chosen() {
			a.askDeleteFile(t, entry.Path, entry.Name, entry.IsDir)
		}
	}
	row := ui.ButtonBase(c).Key(entry.Path).Label(entry.Name).Height(28).Padding(0, 8).Gap(8).
		Justify(ui.Start).AlignItems(ui.Center).Radius(design.RadiusRow - 2).PopoverMenu(menu)
	// 窗口内拖动交给另一栏。拖到桌面要等副本下好：准备好之后才声明系统拖放，
	// 否则这次手势会变成系统拖动，本机栏收不到。松手在窗口外才开始下那份副本。
	row.Drag(paneDrag{path: entry.Path, name: entry.Name, dir: entry.IsDir})
	if entry.IsDir {
		if item, ok := ui.Drop[paneDrag](row); ok {
			a.acceptPaneDrop(t, false, entry.Path, item)
		}
	}
	if at, ok := row.DragMissed(); ok {
		w, h := c.Size()
		if at.X < 0 || at.Y < 0 || at.X > w || at.Y > h {
			a.prepareDragOut(t, entry)
		}
	}
	if _, ready := a.dragReady(t, entry); ready {
		a.bindDragOut(row, t, entry)
	}
	if row.Hovered() {
		row.Background(colors.Hover)
	}
	if entry.IsDir {
		row.Children(content).OnClick(func() { a.openDir(t, entry.Path) })
	} else {
		row.Children(content)
	}
}

// searchBar 是终端窗格顶部的查找工具条。
func (a *app) searchBar(c *ui.Context, colors design.Colors, t *tab, p *pane) {
	theme := c.Theme()
	ui.Row(c).Height(34).Shrink(0).Padding(0, 10).Gap(6).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(colors.Hairline).Background(colors.Hover).Children(func() {
		ui.Icon(c, design.Icon("search")).FontSize(14).TextColor(theme.TextMuted)
		c.Key(fmt.Sprintf("searchInput:%d", p.id))
		input := ui.TextInput(c, &p.searchQuery).Placeholder(locales.ViewFindPlaceholder()).Label(locales.ViewFindField()).
			Grow(1).FontSize(design.TextSmall).Border(0, ui.Transparent).Background(ui.Transparent)
		// 查找框刚出现的那一帧焦点请求可能还落不了地，确认拿到焦点后才停止请求。
		if p.focusSearch {
			input.Focus()
			if input.FocusWithin() {
				p.focusSearch = false
			}
		}
		input.OnShortcut(0, ui.KeyEscape, func() { a.closeSearch(p) })
		input.OnShortcut(0, ui.KeyEnter, func() { p.term.SearchNext() })
		input.OnShortcut(ui.Shift, ui.KeyEnter, func() { p.term.SearchPrevious() })
		if input.Submitted() {
			p.term.SearchNext()
		}

		if p.searchQuery != "" {
			var countText string
			if p.searchStatus.Searching {
				countText = locales.ViewSearching()
			} else if p.searchStatus.Total == 0 {
				countText = "0/0"
			} else {
				countText = fmt.Sprintf("%d/%d", p.searchStatus.Selected, p.searchStatus.Total)
			}
			ui.Text(c, countText).Key(fmt.Sprintf("searchStatus:%d", p.id)).FontSize(design.TextSmall).TextColor(colors.TextFaint).FontFeatures("tnum").Shrink(0)
		}

		iconButton(c, colors, "arrow-up", locales.ViewPrevious(), func() { p.term.SearchPrevious() }).
			Disabled(p.searchStatus.Total == 0)
		iconButton(c, colors, "arrow-down", locales.ViewNext(), func() { p.term.SearchNext() }).
			Disabled(p.searchStatus.Total == 0)
		iconButton(c, colors, "x", locales.ViewCloseFind(), func() { a.closeSearch(p) })
	})
	// 查询变化时重新搜索；清空查询时结束搜索，旧的高亮随之消失。
	switch {
	case p.searchQuery == "" && p.searchStatus.Text != "":
		p.term.EndSearch()
		p.searchStatus = terminal.SearchStatus{}
	case p.searchQuery != "" && p.searchStatus.Text != p.searchQuery:
		p.term.Search(p.searchQuery)
	}
}

// dropLayer 让文件拖到远程终端上时用 SFTP 上传，而不是终端默认的把路径粘贴进 shell。
//
// 终端控件自己接收拖放，而且在最里层，外面的元素拿不到。所以只在有文件拖到窗格上方时，在终端上面
// 铺一层透明的接收层：之后的拖动和松开都落在这一层上。平时没有这一层，终端的选择、滚动、光标都不受影响。
// 第一次悬停的那一帧还是终端在接收，接收层要到下一帧才出现；系统的拖放总是先发几次悬停再松开，不会错过。
func (a *app) dropLayer(c *ui.Context, colors design.Colors, t *tab, p *pane, view ui.Element) {
	if t.local || t.client == nil || p.ended {
		p.fileDrag = false
		return
	}
	over := view.FileDragOver()
	if p.fileDrag {
		theme := c.Theme()
		layer := ui.Box(c).Key(fmt.Sprintf("dropLayer:%d", p.id)).Absolute().Top(0).Right(0).Bottom(0).Left(0).
			Radius(design.RadiusCard).Background(theme.Accent.Alpha(0.10)).Border(1.5, theme.Accent.Alpha(0.6)).
			Justify(ui.Center).AlignItems(ui.Center).Label(locales.ViewDropUpload())
		if files := layer.DroppedFiles(); len(files) > 0 {
			a.uploadDropped(t, p, files)
		}
		over = over || layer.FileDragOver()
		target := locales.ViewTerminalDirectory()
		if dir := p.term.Dir(); dir != "" {
			target = shortPath(dir)
		}
		layer.Children(func() {
			ui.Column(c).Gap(4).AlignItems(ui.Center).Padding(12, 18).Radius(design.RadiusRow).Background(colors.Card).Children(func() {
				ui.Text(c, locales.ViewReleaseToUpload()).FontWeight(600)
				ui.Text(c, locales.ViewUploadTo(target)).FontSize(design.TextSmall).TextColor(theme.TextMuted).SingleLine()
			})
		})
	}
	p.fileDrag = over
}

// paneMonitorMetrics 在窗格顶栏显示紧凑的中文监控摘要（CPU / 内存），点击弹出完整监控浮层。
func (a *app) paneMonitorMetrics(c *ui.Context, colors design.Colors, t *tab) {
	s := t.stats
	if s.UpdatedAt.IsZero() {
		ui.Text(c, locales.MonitorProbing()).TextColor(colors.TextFaint).FontSize(design.TextSmall).NoWrap().Shrink(0)
		return
	}
	tTheme := c.Theme()
	cpuColor := colors.TextFaint
	if s.CPUPercent >= 85 {
		cpuColor = colors.Danger
	}
	var memPercent float32
	if s.MemTotalBytes > 0 {
		memPercent = float32(s.MemUsedBytes) * 100.0 / float32(s.MemTotalBytes)
	}
	memColor := colors.TextFaint
	if memPercent >= 85 {
		memColor = colors.Danger
	}

	anchor := ui.ButtonBase(c).Key(fmt.Sprintf("monitorBtn:%d", t.id)).Label(locales.MonitorTitle()).Tooltip(locales.MonitorTitle()).
		Height(24).Padding(0, 6).Gap(8).AlignItems(ui.Center).Radius(design.RadiusRow).Shrink(0).Transition(design.Hover)
	if anchor.Hovered() || t.monitorPopoverOpen {
		anchor.Background(colors.Hover)
	}
	anchor.Children(func() {
		ui.Row(c).Gap(3).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.MonitorCPU()).TextColor(colors.TextFaint).FontSize(design.TextSmall - 1).FontWeight(600)
			ui.Textf(c, "%3.0f%%", s.CPUPercent).TextColor(cpuColor).FontFeatures("tnum")
		})
		if s.MemTotalBytes > 0 {
			ui.Row(c).Gap(3).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.MonitorMemory()).TextColor(colors.TextFaint).FontSize(design.TextSmall - 1).FontWeight(600)
				ui.Textf(c, "%3.0f%%", memPercent).TextColor(memColor).FontFeatures("tnum")
			})
		}
	}).OnClick(func() { t.monitorPopoverOpen = !t.monitorPopoverOpen })

	ui.Popover(c, anchor, &t.monitorPopoverOpen, func() {
		ui.Column(c).Width(240).Padding(12).Gap(8).Children(func() {
			ui.Text(c, locales.MonitorTitle()).FontWeight(600).FontSize(design.TextBody)
			metricRow := func(label, val string, valColor ui.Color) {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, label).FontSize(design.TextSmall).TextColor(tTheme.TextMuted).Grow(1)
					ui.Text(c, val).FontSize(design.TextSmall).TextColor(valColor).FontWeight(600).FontFeatures("tnum")
				})
			}
			metricRow(locales.MonitorCPU(), fmt.Sprintf("%.1f%%", s.CPUPercent), cpuColor)
			metricRow(locales.MonitorLoad(), fmt.Sprintf("%.2f / %.2f", s.Load1, s.Load5), tTheme.Text)
			if s.MemTotalBytes > 0 {
				metricRow(locales.MonitorMemory(), fmt.Sprintf("%s / %s (%.0f%%)", formatBytes(s.MemUsedBytes), formatBytes(s.MemTotalBytes), memPercent), memColor)
			}
			if s.DiskTotalBytes > 0 {
				diskPercent := float32(s.DiskUsedBytes) * 100.0 / float32(s.DiskTotalBytes)
				diskColor := tTheme.Text
				if diskPercent >= 90 {
					diskColor = colors.Danger
				}
				metricRow(locales.MonitorDisk(), fmt.Sprintf("%s / %s (%.0f%%)", formatBytes(s.DiskUsedBytes), formatBytes(s.DiskTotalBytes), diskPercent), diskColor)
			}
			metricRow(locales.MonitorNetUp(), formatRate(s.NetTxBytesPerSec), tTheme.Accent)
			metricRow(locales.MonitorNetDown(), formatRate(s.NetRxBytesPerSec), tTheme.Accent)
		})
	})
}
