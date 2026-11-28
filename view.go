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

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/plugins/loading"
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
	a.syncTabDrawers()
	ui.Row(c).Fill().AlignItems(ui.Stretch).LinearGradient(colors.Glow).Children(func() {
		if a.sidebarOpen {
			a.sidebar(c, colors)
			a.sidebarResizeHandle(c, colors)
		}
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			a.tabBar(c, colors)
			if a.broadcastOn {
				a.broadcastBar(c, colors)
			}
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
	a.downloadConfirmDialog(c)
	a.folderConflictDialog(c)
	a.lockConfirmDialog(c)
	a.fileOpDialog(c, colors)
	a.fileDeleteDialog(c)
	a.editorDialog(c, colors)
	a.flushToasts(c)
	a.paletteDialog(c, colors)
	a.broadcastDialog(c)
	a.updateDialog(c, colors)
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

func (a *app) hasModalOpen() bool {
	return a.setup.open ||
		a.restore.open ||
		a.auth.open ||
		a.pal.open ||
		a.form.open ||
		a.groupSheet.open ||
		a.keyPanel.open ||
		a.keyPanel.deleting != nil ||
		a.proxyPanel.open ||
		a.proxyPanel.deleting != nil ||
		a.forwardPanel.open ||
		a.forwardPanel.deleting != nil ||
		a.snippetPanel.open ||
		a.snippetPanel.deleting != nil ||
		a.snippetPanel.confirming ||
		a.settingsPanel.open ||
		a.settingsPanel.askSyncMerge ||
		a.editor.open ||
		a.fileOp.open ||
		a.update.sheetOpen ||
		a.deleting != nil ||
		a.fileDelete != nil ||
		len(a.prompts) > 0 ||
		len(a.challenges) > 0 ||
		a.askingLock ||
		a.broadcastAsk != nil ||
		len(a.pendingUploads) > 0 ||
		len(a.pendingDownloads) > 0 ||
		len(a.pendingFolders) > 0 ||
		a.syncOverwriteAsk ||
		a.askConflicts
}

func (a *app) shortcuts(c *ui.Context) {
	mods := chromeShortcut()
	if a.hasModalOpen() {
		// 弹窗打开时只拦截关闭快捷键防止透传关闭整个窗口，其余工作区快捷键不触发。
		c.OnShortcut(mods, ui.KeyW, func() {})
		return
	}
	c.OnShortcut(mods, ui.KeyK, a.openPalette)
	c.OnShortcut(mods, ui.KeyI, a.toggleAI)
	c.OnShortcut(mods, ui.KeyBackslash, func() { a.sidebarOpen = !a.sidebarOpen })
	c.OnShortcut(mods, ui.KeyJ, a.toggleFiles)
	c.OnShortcut(mods, ui.KeyComma, func() {
		if !a.settingsPanel.open {
			a.openSettings()
		}
	})
	c.OnShortcut(mods, ui.KeyE, a.openAIExplainFromActivePane)
	c.OnShortcut(mods, ui.KeyBracketLeft, func() { a.switchTabStep(-1) })
	c.OnShortcut(mods, ui.KeyBracketRight, func() { a.switchTabStep(1) })
	c.OnShortcut(mods|ui.Alt, ui.KeyBracketLeft, func() { a.moveTabStep(-1) })
	c.OnShortcut(mods|ui.Alt, ui.KeyBracketRight, func() { a.moveTabStep(1) })
	c.OnShortcut(mods, ui.KeyT, a.openLocalTerminal)
	c.OnShortcut(mods, ui.KeyN, a.openAdd)
	for i, k := range []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8, ui.Key9} {
		n := i + 1
		c.OnShortcut(mods, k, func() { a.switchToTabNumber(n) })
	}
	// KeyW 始终拦截在应用层：没有任何标签页时也不交给系统默认关闭整个窗口。
	c.OnShortcut(mods, ui.KeyW, func() {
		if t := a.current(); t != nil {
			a.closeFocused(t)
		}
	})
	if t := a.current(); t != nil {
		c.OnShortcut(mods, ui.KeyF, func() { a.openSearch(t) })
		if t.root.leaves() > 1 {
			c.OnShortcut(mods|ui.Alt, ui.KeyLeft, func() { a.focusPaneStep(t, -1) })
			c.OnShortcut(mods|ui.Alt, ui.KeyRight, func() { a.focusPaneStep(t, 1) })
		}
		if t.local || t.client != nil {
			c.OnShortcut(mods, ui.KeyD, func() { a.split(t, false) })
			c.OnShortcut(mods|ui.Alt, ui.KeyD, func() { a.split(t, true) })
		}
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
	pill := ui.Box(c.Key("pill")).Absolute().Top(0).Left(0).Right(0).Bottom(0).ZIndex(-1).Radius(design.RadiusRow).
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
	b := ui.ButtonBase(c.Key(key)).Label(label).Tooltip(tip).
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
	if !a.groupSheet.open || a.setup.open {
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
				ui.Button(c.Key("groupCancel"), locales.Cancel()).OnClick(func() { a.groupSheet = groupDialogState{} })
				ui.PrimaryButton(c.Key("groupSubmit"), submit).OnClick(a.submitGroupDialog)
			})
		})
	})
	if !a.groupSheet.open {
		a.groupSheet = groupDialogState{}
	}
}

func (a *app) titleBar(c *ui.Context) ui.TitleBar {
	if a.testTitleBar != nil {
		return *a.testTitleBar
	}
	return c.TitleBar()
}

// sidebar 是主机列表。它透出窗口的渐变和系统的毛玻璃，不画自己的底色。
func (a *app) sidebar(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	bar := a.titleBar(c)
	// 侧栏的宽度一步到位（不让终端逐帧改尺寸），内容从左侧滑入、淡入。
	ui.Column(c.Key("sidebar")).Width(a.effectiveSidebarWidth()).Shrink(0).DragWindow().Transition(design.SlideIn(-14)).Children(func() {
		ui.Box(c).Height(max(bar.Height, 12)).Shrink(0)

		ui.Column(c).Padding(2, 12, 10, 12).Children(func() {
			// 搜索框 + 清除按钮 + 快捷键提示 + 顶部“+”新建菜单（添加主机 / 本地终端 / 新建分组）。
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Row(c.Key("hostFilterRow")).Height(32).Grow(1).MinWidth(0).Padding(0, 4, 0, 10).Gap(4).
					AlignItems(ui.Center).Radius(design.RadiusRow).Background(colors.Chip).Children(func() {
					ui.Icon(c, design.Icon("search")).FontSize(14).TextColor(t.TextMuted).Shrink(0)
					c.Key("hostFilter")
					ui.TextInput(c, &a.hostFilter).Placeholder("过滤主机").Label("过滤主机").
						Grow(1).MinWidth(0).FontSize(design.TextSmall).Border(0, ui.Transparent).Background(ui.Transparent)
					if a.hostFilter != "" {
						clr := ui.ButtonBase(c.Key("clearHostFilterInput")).Label("清除过滤").Tooltip("清除过滤文字").
							Size(20, 20).Center().Radius(4).Shrink(0).Transition(design.Hover)
						if clr.Hovered() {
							clr.Background(colors.Hover)
						}
						clr.Children(func() {
							ui.Icon(c, design.Icon("x")).FontSize(12).TextColor(colors.TextFaint)
						}).OnClick(func() { a.hostFilter = "" })
					}
					cmd := ui.ButtonBase(c.Key("openPalette")).Label(locales.ViewSearchHostsAndActions()).
						Height(22).MinWidth(22).Padding(0, 5).Center().Radius(6).Shrink(0).
						Tooltip("命令面板（" + shortcutText("K") + "）")
					if cmd.Hovered() {
						cmd.Background(colors.Hover)
					}
					cmd.Children(func() {
						if runtime.GOOS == "darwin" {
							ui.Text(c, shortcutText("K")).FontSize(design.TextSmall).TextColor(colors.TextFaint).NoWrap()
						} else {
							ui.Icon(c, design.Icon("command")).FontSize(13).TextColor(colors.TextFaint)
						}
					}).OnClick(a.openPalette)
				})
				addMenuBtn := ui.ButtonBase(c.Key("sidebarAddMenu")).Label("新建").Tooltip("新建主机、本地终端或分组").
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
				}, menuButtonOptions(colors))
				if len(a.hosts) > 0 {
					probeBtn := ui.ButtonBase(c.Key("probeHosts")).Label("检测连通性").Tooltip("检测全部主机连通性").
						Size(30, 30).Center().Radius(design.RadiusRow).Shrink(0).Transition(design.Hover)
					if probeBtn.Hovered() {
						probeBtn.Background(colors.Hover)
					}
					probeBtn.Children(func() {
						if a.isProbingHosts() {
							loading.Radar(c, loading.Options{Size: 15, Color: t.Accent}).Label("正在检测连通性")
						} else {
							ui.Icon(c, design.Icon("radar")).FontSize(14).TextColor(t.TextMuted)
						}
					}).OnClick(func() { a.probeAllHosts(true) })
				}
			})
			if allTags := a.allTags(); len(allTags) > 0 {
				if a.activeTag != "" && !slices.Contains(allTags, a.activeTag) {
					a.activeTag = ""
				}
				ui.Scroll(c).MaxHeight(58).Children(func() {
					ui.Row(c.Key("tagFilterRow")).Padding(6, 0, 0, 0).Gap(5).Wrap().Children(func() {
						for _, tag := range allTags {
							tagName := tag
							selected := a.activeTag == tagName
							fg, bg := t.TextMuted, colors.Chip
							if selected {
								fg, bg = t.Accent, t.Accent.Alpha(0.16)
							}
							b := ui.ButtonBase(c.Key("tagFilter:"+tagName)).Label("筛选标签 "+tagName).
								Height(22).Padding(0, 8).Radius(design.RadiusChip).AlignItems(ui.Center).
								Background(bg).Transition(design.Hover)
							if !selected && b.Hovered() {
								b.Background(colors.Hover)
							}
							b.Children(func() {
								ui.Text(c, "#"+tagName).FontSize(design.TextSmall - 1).TextColor(fg).FontWeight(500).NoWrap()
							}).OnClick(func() {
								if a.activeTag == tagName {
									a.activeTag = ""
								} else {
									a.activeTag = tagName
								}
							})
						}
					})
				})
			} else if a.activeTag != "" {
				a.activeTag = ""
			}
		})

		filtering := strings.TrimSpace(a.hostFilter) != "" || a.activeTag != ""
		ui.Box(c).Grow(1).MinHeight(0).Clip().Children(func() {
			ui.Scroll(c.Key("sidebarHostScroll")).Fill().TrackScroll(&a.sidebarScroll).Children(func() {
				ui.Column(c).Padding(0, 10, 10, 10).Gap(12).Children(func() {
					if filtering {
						ui.Row(c).Padding(4, 8).Radius(design.RadiusRow).Background(colors.Hover).AlignItems(ui.Center).Gap(6).Children(func() {
							ui.Text(c, locales.HostFilterSortHint()).FontSize(design.TextSmall - 1).TextColor(colors.TextFaint).Grow(1)
							ui.ButtonBase(c.Key("clearHostFilter")).Label("清空").Children(func() {
								ui.Text(c, "清空").FontSize(design.TextSmall - 1).TextColor(t.Accent).FontWeight(600)
							}).OnClick(func() {
								a.hostFilter = ""
								a.activeTag = ""
							})
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
						ui.Column(c.Key("group:" + group)).Gap(2).Children(func() {
							iconName, action := "chevron-down", "折叠"
							if collapsed {
								iconName, action = "chevron-right", "展开"
							}
							header := ui.ButtonBase(c.Key("groupToggle:"+group)).Label(action+" "+group).
								Height(24).Padding(0, 6).Gap(4).Justify(ui.Start).AlignItems(ui.Center).
								Radius(design.RadiusRow).Transition(design.Hover).PopoverMenu(a.groupMenu(group), menuOptions(colors))
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
							hasLive := collapsed && slices.ContainsFunc(rows, func(h hosts.Host) bool {
								return a.hasTabForHost(h.ID)
							})
							header.Children(func() {
								ui.Icon(c, design.Icon(iconName)).FontSize(12).TextColor(colors.TextFaint).Shrink(0)
								ui.Text(c, group).FontSize(design.TextSmall).FontWeight(500).TextColor(colors.TextFaint).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
								if hasLive {
									ui.Box(c).Size(5, 5).Radius(3).Background(colors.Live).Shrink(0)
								}
								if collapsed && len(rows) > 0 {
									ui.Text(c, fmt.Sprint(len(rows))).FontSize(design.TextSmall - 1).TextColor(colors.TextFaint).FontFeatures("tnum").Shrink(0)
								}
							}).OnClick(func() { a.toggleGroup(group) })
							if collapsed {
								return
							}
							for _, h := range rows {
								a.hostRow(c, colors, h)
							}
							if !filtering && len(rows) > 0 {
								lastID := rows[len(rows)-1].ID
								endDrop := ui.Box(c.Key("groupEnd:" + group)).Height(8).Shrink(0)
								var endInsertLine bool
								if dragged, ok := ui.DragOver[hostDrag](endDrop); ok && dragged.ID != lastID {
									endInsertLine = true
								}
								if dropped, ok := ui.Drop[hostDrag](endDrop); ok && dropped.ID != lastID {
									if err := a.moveHostOrder(dropped.ID, "", group); err != nil {
										a.notice = "调整主机排序失败：" + err.Error()
									}
								}
								if endInsertLine {
									endDrop.DrawOver(func(painter *ui.Painter, r ui.Rect) {
										painter.Fill(ui.Rect{X: r.X + 8, Y: r.Y + 2, W: max(0, r.W-16), H: 2}, t.Accent, 1)
									})
								}
							}
						})
					}
				})
			})
			if a.sidebarScroll.Y > 2 {
				ui.Box(c).Absolute().Top(0).Left(0).Right(0).Height(16).PassThrough().AccessHidden(true).
					Material(glass.ScrollEdge{Background: colors.Wash.Stops[0].Color})
			}
			if a.sidebarScroll.MaxY > 0 && a.sidebarScroll.Y < a.sidebarScroll.MaxY-2 {
				ui.Box(c).Absolute().Bottom(0).Left(0).Right(0).Height(16).PassThrough().AccessHidden(true).
					Material(glass.ScrollEdge{Bottom: true, Background: colors.Wash.Stops[1].Color})
			}
		})

		ui.Column(c).Padding(6, 10, 10, 10).Gap(4).Children(func() {
			noticeBanner(c, colors, "hostsError", "dismissHostsError", "关闭系统错误", a.hostsError, func() { a.hostsError = "" })
			noticeBanner(c, colors, "notice", "dismissNotice", "关闭通知", a.notice, func() { a.notice = "" })
			a.syncStatus(c, colors)
			ui.Row(c.Key("sidebarToolbar")).Gap(4).AlignItems(ui.Center).Children(func() {
				sidebarIconButton(c, colors, "openKeys", "key-round", locales.ViewKeys(), len(a.keys), a.openKeys)
				sidebarIconButton(c, colors, "openProxies", "globe", locales.Proxy(), len(a.proxies), a.openProxies)
				fwdBtn := sidebarIconButton(c, colors, "openForwards", "arrow-left-right", locales.PortForward(), len(a.forwards), a.openForwards)
				if len(a.tunnels) > 0 {
					fwdBtn.Tooltip(locales.ViewForwardsRunningTooltip(locales.PortForward(), len(a.tunnels)))
					fwdBtn.Children(func() {
						ui.Box(c).Size(5, 5).Radius(3).Background(colors.Live).Shrink(0)
					})
				}
				snipBtn := sidebarIconButton(c, colors, "openSnippets", "code", "片段", len(a.snippets), a.openSnippets)
				snipTip := "代码片段"
				if len(a.snippets) > 0 {
					snipTip = fmt.Sprintf("代码片段（%d）", len(a.snippets))
				}
				snipBtn.Tooltip(snipTip)
				sidebarIconButton(c, colors, "openSettings", "settings", locales.Settings(), 0, a.openSettings).
					Tooltip(locales.Settings() + "（" + shortcutText(",") + "）")
			})
		})
	})
}

func (a *app) sidebarResizeHandle(c *ui.Context, colors design.Colors) {
	// 放在侧栏最右侧 6px 范围内，紧邻内容区左缘而不伸进内容区，避免后续列遮挡命中区域。
	grip := ui.Box(c.Key("sidebarResize")).Width(6).Shrink(0).Margin(0, 0, 0, -6).
		Cursor(ui.CursorResizeEW).Focusable().Label("调整侧栏宽度")
	dx, _, held := grip.Dragged()
	if a.sidebarWidth <= 0 {
		a.sidebarWidth = 240
	}
	changed := false
	switch {
	case grip.DoubleClicked():
		a.sidebarWidth = 240
		changed = true
	case held && dx != 0:
		a.sidebarWidth = min(max(a.sidebarWidth+dx, 200), 360)
		a.sidebarResizing = true
	case a.sidebarResizing && !held:
		a.sidebarResizing = false
		changed = true
	case grip.Shortcut(0, ui.KeyRight):
		a.sidebarWidth = min(a.effectiveSidebarWidth()+16, 360)
		changed = true
	case grip.Shortcut(0, ui.KeyLeft):
		a.sidebarWidth = max(a.effectiveSidebarWidth()-16, 200)
		changed = true
	}
	if changed && a.devicePath != "" {
		next := a.local
		next.SidebarWidth = a.sidebarWidth
		_ = a.saveLocal(next)
	}
	if held || grip.Hovered() {
		accent := c.Theme().Accent
		grip.Draw(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X + r.W - 1.5, Y: r.Y + 12, W: 1.5, H: max(0, r.H-24)}, accent, 0)
		})
	}
}

func noticeBanner(c *ui.Context, colors design.Colors, key, dismissKey, dismissLabel, message string, dismiss func()) {
	if message == "" {
		return
	}
	ui.Row(c.Key(key)).Padding(6, 6, 6, 10).Margin(0, 0, 4, 0).Gap(6).AlignItems(ui.Start).
		Radius(design.RadiusRow).Background(colors.DangerSoft).Children(func() {
		ui.Text(c, message).FontSize(design.TextSmall).TextColor(colors.Danger).Grow(1).MinWidth(0).Selectable()
		btn := ui.ButtonBase(c.Key(dismissKey)).Size(20, 20).Center().Radius(4).Shrink(0).
			Label(dismissLabel).Tooltip(dismissLabel).Transition(design.Hover)
		if btn.Hovered() {
			btn.Background(colors.Danger.Alpha(0.14))
		}
		btn.Children(func() {
			ui.Icon(c, design.Icon("x")).FontSize(12).TextColor(colors.Danger)
		}).OnClick(dismiss)
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
	row := ui.ButtonBase(c.Key("syncStatus")).Label("同步状态").Tooltip(tip).Height(28).Padding(0, 8).Gap(8).
		Justify(ui.Start).AlignItems(ui.Center).Radius(design.RadiusRow).Transition(design.Hover)
	if row.Hovered() {
		row.Background(colors.Hover)
	}
	row.Children(func() {
		if a.syncing {
			loading.Arc(c, loading.Options{Size: 13, Color: c.Theme().Accent}).Label("正在同步")
		} else {
			ui.Icon(c, design.Icon(icon)).FontSize(13).TextColor(color)
		}
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
			ui.Button(c.Key("restoreOpen"), "从同步恢复").OnClick(a.openRestore)
		}
	})
}

func hostTooltip(h hosts.Host, health hostHealth) string {
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
	if len(h.Tags) > 0 {
		parts = append(parts, "标签: "+strings.Join(h.Tags, ", "))
	}
	switch health.state {
	case probeChecking:
		parts = append(parts, "连通性: 正在检测…")
	case probeOK:
		if health.latency > 0 {
			parts = append(parts, "连通性: 可达 · "+formatLatency(health.latency))
		} else {
			parts = append(parts, "连通性: 可用")
		}
	case probeFail:
		if health.err != "" {
			parts = append(parts, "连通性: 不可达（"+health.err+"）")
		} else {
			parts = append(parts, "连通性: 不可达")
		}
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
	connected := a.hasTabForHost(h.ID)
	busy := a.isHostConnecting(h.ID)
	health := a.health[h.ID]
	menu := a.hostMenu(c, h)
	filtering := strings.TrimSpace(a.hostFilter) != "" || a.activeTag != ""

	// 外层负责背景、悬停和选中高亮，里面并排放主机按钮和“更多”按钮。
	// 两个按钮不能嵌套：嵌在按钮里的按钮，读屏软件和键盘都找不到。
	line := ui.Row(c.Key("line:"+h.ID)).AlignItems(ui.Center).Padding(0, 6, 0, 0).
		Radius(design.RadiusRow).Transition(design.Hover)
	hovered := line.Hovered()
	if !selected && hovered {
		line.Background(colors.Hover)
	}
	var insertAfter bool
	var showInsertLine bool
	if !filtering {
		if dragged, ok := ui.DragOver[hostDrag](line); ok && dragged.ID != h.ID {
			showInsertLine = true
			if dHost, ok := a.hostByID(dragged.ID); ok && dHost.Group == h.Group {
				groupList := a.allHostsInGroup(h.Group)
				fromIdx := slices.IndexFunc(groupList, func(x hosts.Host) bool { return x.ID == dragged.ID })
				toIdx := slices.IndexFunc(groupList, func(x hosts.Host) bool { return x.ID == h.ID })
				if fromIdx >= 0 && toIdx >= 0 && fromIdx < toIdx {
					insertAfter = true
				}
			}
		}
		if dropped, ok := ui.Drop[hostDrag](line); ok && dropped.ID != h.ID {
			if err := a.moveHostOrder(dropped.ID, h.ID, h.Group); err != nil {
				a.notice = "调整主机排序失败：" + err.Error()
			}
		}
	}
	if showInsertLine {
		line.DrawOver(func(painter *ui.Painter, r ui.Rect) {
			y := r.Y
			if insertAfter {
				y = r.Y + max(0, r.H-2)
			}
			painter.Fill(ui.Rect{X: r.X + 8, Y: y, W: max(0, r.W-16), H: 2}, t.Accent, 1)
		})
	}
	line.Children(func() {
		if selected {
			selectionPill(c, colors, "hosts", colors.Selected, true)
		}
		row := ui.ButtonBase(c.Key("host:"+h.ID)).Label(h.Name).Tooltip(hostTooltip(h, health)).Grow(1).MinWidth(0).Padding(6, 4, 6, 10).Gap(10).
			Justify(ui.Start).AlignItems(ui.Center)
		if !filtering {
			row.Drag(hostDrag{ID: h.ID})
		}
		if busy {
			row.Description(locales.ViewConnecting())
		}
		row.Children(func() {
			unreachable := !busy && !connected && health.state == probeFail
			var coat ui.Element
			if unreachable {
				coat = design.DistroCoatViewMuted(c, h.ID, h.Distro, h.Production, 26).Opacity(0.75)
			} else {
				coat = design.DistroCoatView(c, h.ID, h.Distro, h.Production, 26)
			}
			if busy {
				// 连接中斑纹明显呼吸，代替转圈图标：状态就落在这台主机自己的标识上。
				coat.Opacity(coat.Keyframes("breathe", design.Breathe, design.BreatheOptions))
			}
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, h.Name).FontWeight(500).SingleLine().Ellipsis("…")
				sub := h.Target()
				if jumps := hosts.SplitJumps(h.ProxyJump); len(jumps) > 0 {
					// 经跳板机的主机显示途经路线：连不上时，用户第一眼就知道该先查哪一台。
					sub = locales.ViewVia(strings.Join(jumps, " → "))
				}
				ui.Text(c, sub).FontSize(design.TextSmall).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
				if len(h.Tags) > 0 {
					ui.Row(c).Gap(4).Padding(2, 0, 0, 0).Wrap().Children(func() {
						shown := min(len(h.Tags), 3)
						for _, tg := range h.Tags[:shown] {
							ui.Row(c).Height(16).Padding(0, 5).Radius(design.RadiusChip).Background(colors.Chip).AlignItems(ui.Center).Children(func() {
								ui.Text(c, "#"+tg).FontSize(design.TextSmall - 2).TextColor(colors.TextFaint).NoWrap()
							})
						}
						if len(h.Tags) > 3 {
							ui.Textf(c, "+%d", len(h.Tags)-3).FontSize(design.TextSmall - 2).TextColor(colors.TextFaint).NoWrap()
						}
					})
				}
			})
			// 右侧绿点仅表示当前主机已打开会话，不与连通性探测结果复用，避免误判“已连接”。
			if connected {
				tip := locales.ViewConnected()
				if current != nil && current.host.ID == h.ID && current.latency > 0 {
					tip += " · " + formatLatency(current.latency)
				} else if cur := slices.IndexFunc(a.tabs, func(tb *tab) bool { return tb.host.ID == h.ID && tb.latency > 0 }); cur >= 0 {
					tip += " · " + formatLatency(a.tabs[cur].latency)
				}
				ui.Box(c).Size(7, 7).Radius(4).Background(colors.Live).Shrink(0).Label(locales.ViewConnected()).Tooltip(tip)
			}
		}).OnClick(func() { a.clickHost(h) })
		if row.DoubleClicked() {
			a.doubleClickHost(h)
		}
		row.PopoverMenu(menu, menuOptions(colors))

		// “更多”按钮只在悬停或选中时出现，打开和右键相同的菜单：
		// 不知道可以右键的人也能找到编辑和删除。键盘用户在行上按菜单键或 Shift+F10。
		if hovered || selected {
			more := ui.ButtonBase(c.Key("more:"+h.ID)).Label(locales.MoreActions()).Size(24, 24).Radius(6).Shrink(0).Transition(design.Hover)
			if more.Hovered() {
				more.Background(colors.Hover)
			}
			more.Children(func() {
				ui.Icon(c, design.Icon("ellipsis")).FontSize(14).TextColor(t.TextMuted)
			}).PopoverMenuButton(menu, menuButtonOptions(colors))
		}
	})
}

// tabBar 把标签放进标题栏，省下一整行高度给终端。它和侧栏一样透明，选中的标签浮起来。
func (a *app) tabBar(c *ui.Context, colors design.Colors) {
	bar := a.titleBar(c)
	left := float32(4)
	if !a.sidebarOpen {
		left = max(bar.Left, design.Gap)
	}
	right := max(bar.Right+design.Gap, design.Gap+2)
	height := max(bar.Height, 40)
	ui.Row(c).Height(height).Shrink(0).Padding(0, right, 0, left).Gap(4).AlignItems(ui.Center).DragWindow().Children(func() {
		if !a.sidebarOpen {
			iconButton(c, colors, "panel-left", locales.ViewShowSidebar(), func() { a.sidebarOpen = true })
		}
		tabs := slices.Clone(a.tabs)
		hasConnectingTabs := len(tabs) > 0 && len(a.connecting) > 0
		ui.Row(c).Grow(1).MinWidth(0).Gap(4).AlignItems(ui.Center).Children(func() {
			if len(tabs) > 0 || hasConnectingTabs {
				ui.ScrollHorizontal(c.Key("tabScroll")).Shrink(1).MinWidth(0).Height(32).
					Scrollbars(false).Gap(4).AlignItems(ui.Center).Children(func() {
					for i, t := range tabs {
						a.tabItem(c, colors, i, t, tabs)
					}
					if hasConnectingTabs {
						for _, hostID := range slices.Sorted(maps.Keys(a.connecting)) {
							a.connectingTabPill(c, colors, hostID)
						}
					}
				})
			}
			iconButton(c.Key("newTabBtn"), colors, "plus", "新建标签页", func() { a.openLocalTerminal() }).
				Tooltip("新建本地终端")
		})
		if t := a.current(); t != nil {
			if !t.local && !a.isSavedHost(t.host.ID) { // 本地终端不是主机，不能保存
				ui.Button(c.Key("saveAsHost"), locales.ViewSaveAsHost()).Height(28).Padding(0, 8).
					Background(colors.Selected).Radius(design.RadiusRow).OnClick(func() { a.openSaveHost(t.host) })
			}
			searchBtn := iconButton(c.Key("tabToggleSearch"), colors, "search", locales.ViewFindShortcut(shortcutText("F")), func() { a.openSearch(t) })
			if t.focus != nil && t.focus.searchOpen {
				searchBtn.Background(c.Theme().Accent.Alpha(0.14))
			}
			if !t.local && t.client != nil {
				filesBtn := iconButton(c.Key("tabToggleFiles"), colors, "folder", locales.ViewFilesShortcut(shortcutText("J")), a.toggleFiles)
				if a.filesOpen {
					filesBtn.Background(c.Theme().Accent.Alpha(0.14))
				}
			}
		}
		aiBtn := iconButton(c.Key("tabToggleAI"), colors, "sparkles", "AI Agent（"+shortcutText("I")+"）", a.toggleAI)
		if a.ai.open {
			aiBtn.Background(c.Theme().Accent.Alpha(0.14))
		}
		if a.hasRunningAI() {
			aiBtn.Children(func() {
				loading.Pulse(c, loading.Options{Size: 8, Color: c.Theme().Accent}).
					Absolute().Top(2).Right(2).PassThrough().AccessHidden(true)
			})
		}
	})
}

func (a *app) connectingTabPill(c *ui.Context, colors design.Colors, hostID string) {
	theme := c.Theme()
	h, ok := a.connectingHost[hostID]
	if !ok {
		h, ok = a.hostByID(hostID)
	}
	name := h.Name
	if !ok || name == "" {
		name = h.Address
	}
	if name == "" {
		name = hostID
	}
	phase := a.connectingPhase[hostID]
	if phase == "" {
		phase = locales.ViewConnecting()
	}
	cancel := a.connecting[hostID]
	ui.Row(c.Key("connectingTab:"+hostID)).Height(28).MinWidth(110).MaxWidth(240).Shrink(0).
		Padding(0, 8, 0, 10).Gap(6).AlignItems(ui.Center).Radius(design.RadiusRow).
		Background(colors.Card).Border(1, theme.Accent.Alpha(0.35)).Tooltip(phase).Children(func() {
		design.DistroCoatView(c, h.ID, h.Distro, h.Production, 14)
		ui.Text(c, name).TextColor(theme.Text).FontWeight(500).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
		loading.Arc(c, loading.Options{Size: 11, Color: theme.Accent}).Label("正在连接").Shrink(0)
		ui.Text(c, phase).TextColor(theme.TextMuted).FontSize(design.TextSmall - 0.5).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
		ui.ButtonBase(c.Key("cancelConnectTab:"+hostID)).Label("取消连接 "+name).Tooltip("取消连接").
			Size(16, 16).Center().Radius(4).Shrink(0).Children(func() {
			ui.Icon(c, design.Icon("x")).FontSize(12).TextColor(colors.TextFaint)
		}).OnClick(func() {
			if cancel != nil {
				a.cancelConnect(hostID)
			}
		})
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

// tabMenu 是标签页右键菜单，提供复制标签页、移动顺序和关闭等快捷操作。
func (a *app) tabMenu(t *tab) func(m *ui.Menu) {
	return func(m *ui.Menu) {
		if m.Item(locales.ViewTabDuplicate()).Chosen() {
			a.duplicateTab(t)
		}
		if len(a.tabs) > 1 {
			idx := slices.Index(a.tabs, t)
			if m.Item("左移标签页").Disabled(idx <= 0).Chosen() {
				a.moveTabItem(t, -1)
			}
			if m.Item("右移标签页").Disabled(idx < 0 || idx >= len(a.tabs)-1).Chosen() {
				a.moveTabItem(t, 1)
			}
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

func (a *app) tabItem(c *ui.Context, colors design.Colors, i int, t *tab, tabs []*tab) {
	theme := c.Theme()
	selected := a.active >= 0 && a.active < len(a.tabs) && a.tabs[a.active] == t
	title := displayTabTitle(tabs, i)
	// 外层容器承担选中高亮、右键菜单和拖拽；激活与关闭两个按钮并排，避免按钮嵌套（读屏和键盘不可达）。
	line := ui.Row(c.Key(fmt.Sprintf("tabLine:%d", t.id))).Height(28).MinWidth(88).MaxWidth(200).Shrink(0).
		AlignItems(ui.Center).Radius(design.RadiusRow).Transition(design.Hover).
		PopoverMenu(a.tabMenu(t), menuOptions(colors)).Drag(tabDrag{ID: t.id})
	if selected && t.id != a.lastScrolledTabID {
		a.lastScrolledTabID = t.id
		line.ScrollIntoView(ui.ScrollIntoViewOptions{Inline: ui.ScrollNearest})
	}
	var tabInsertLine bool
	var tabInsertRight bool
	if dragged, ok := ui.DragOver[tabDrag](line); ok && dragged.ID != t.id {
		tabInsertLine = true
		from := slices.IndexFunc(a.tabs, func(tb *tab) bool { return tb.id == dragged.ID })
		if from >= 0 && from < i {
			tabInsertRight = true
		}
	}
	if dropped, ok := ui.Drop[tabDrag](line); ok && dropped.ID != t.id {
		a.moveTab(dropped.ID, t.id)
	}
	if tabInsertLine {
		line.DrawOver(func(painter *ui.Painter, r ui.Rect) {
			x := r.X
			if tabInsertRight {
				x = r.X + max(0, r.W-2)
			}
			painter.Fill(ui.Rect{X: x, Y: r.Y + 4, W: 2, H: max(0, r.H-8)}, theme.Accent, 1)
		})
	}
	hovered := line.Hovered()
	if !selected && hovered {
		line.Background(colors.Hover)
	}
	line.Children(func() {
		if selected {
			selectionPill(c, colors, "tabs", colors.Selected, true)
		}
		showClose := hovered || selected
		rightPad := float32(10)
		if showClose {
			rightPad = 4
		}
		// 只告诉辅助技术“已选中”。不用 Selected()：它会给按钮画一层强调色背景，
		// 平时被高亮盖住，高亮滑走的那一瞬间会露出一闪的靛蓝色。
		btn := ui.ButtonBase(c.Key(fmt.Sprintf("tab:%d", t.id))).Label(title).Tooltip(title).AccessSelected(selected).
			Height(28).Grow(1).MinWidth(0).Padding(0, rightPad, 0, 10).Gap(8).Justify(ui.Start).AlignItems(ui.Center)
		btn.Children(func() {
			design.DistroCoatView(c, t.host.ID, t.host.Distro, t.host.Production, 14)
			color := theme.TextMuted
			if selected {
				color = theme.Text
			}
			ui.Text(c, title).TextColor(color).FontWeight(500).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
		}).OnClick(func() {
			if idx := slices.Index(a.tabs, t); idx >= 0 {
				a.active = idx
				a.saveSessionLayout()
			}
		})
		if showClose {
			closeBtn := ui.ButtonBase(c.Key(fmt.Sprintf("tabClose:%d", t.id))).Label(locales.ViewCloseTab(title)).
				Tooltip(locales.ViewCloseTab(title)).Size(16, 16).Margin(0, 6, 0, 0).Center().Radius(4).Shrink(0)
			if closeBtn.Hovered() {
				closeBtn.Background(colors.Hover)
			}
			closeBtn.Children(func() {
				ui.Icon(c, design.Icon("x")).FontSize(12).TextColor(colors.TextFaint)
			}).OnClick(func() { a.closeTab(t) })
		}
	})
}

func (a *app) drawerBudget(c *ui.Context, hasFiles, hasAI bool) (filesW, filesMaxW, aiW, aiMaxW float32) {
	if a.filesWidth <= 0 {
		a.filesWidth = 560
	}
	if a.aiWidth <= 0 {
		a.aiWidth = 460
	}
	windowW, _ := c.Size()
	var sideW float32
	if a.sidebarOpen {
		sideW = a.effectiveSidebarWidth()
	}
	const minPaneW float32 = 260
	const gapOverhead float32 = 24
	totalBudget := max(float32(320), windowW-sideW-minPaneW-gapOverhead)

	if hasFiles && hasAI {
		const minFiles float32 = 300
		const minAI float32 = 320
		if totalBudget < minFiles+minAI {
			if a.lastDrawer == "ai" {
				a.setFilesOpen(a.current(), false)
				hasFiles = false
			} else {
				a.ai.open = false
				hasAI = false
			}
		} else {
			desiredFiles := max(minFiles, a.filesWidth)
			desiredAI := max(minAI, a.aiWidth)
			if desiredFiles+desiredAI <= totalBudget {
				filesW, aiW = desiredFiles, desiredAI
			} else {
				extra := totalBudget - (minFiles + minAI)
				wantExtra := (desiredFiles - minFiles) + (desiredAI - minAI)
				if wantExtra <= 0 {
					filesW, aiW = minFiles+extra/2, minAI+extra/2
				} else {
					filesW = minFiles + extra*((desiredFiles-minFiles)/wantExtra)
					aiW = minAI + extra*((desiredAI-minAI)/wantExtra)
				}
			}
			filesMaxW = max(minFiles, totalBudget-aiW)
			aiMaxW = max(minAI, totalBudget-filesW)
			return filesW, filesMaxW, aiW, aiMaxW
		}
	}
	if hasFiles {
		filesMaxW = max(float32(320), totalBudget)
		filesW = min(max(a.filesWidth, 320), filesMaxW)
	}
	if hasAI {
		aiMaxW = max(float32(340), totalBudget)
		aiW = min(max(a.aiWidth, 340), aiMaxW)
	}
	return filesW, filesMaxW, aiW, aiMaxW
}

func (a *app) effectiveSidebarWidth() float32 {
	if a.sidebarWidth <= 0 {
		return 240
	}
	return min(max(a.sidebarWidth, 200), 360)
}

func (a *app) workspace(c *ui.Context, colors design.Colors) {
	t := a.current()
	left := float32(0)
	if !a.sidebarOpen {
		left = design.Gap / 2
	}
	hasFiles := t != nil && a.filesOpen
	hasAI := a.ai.open
	filesW, filesMaxW, aiW, aiMaxW := a.drawerBudget(c, hasFiles, hasAI)
	hasFiles = t != nil && a.filesOpen
	hasAI = a.ai.open
	// 每张卡片自带一半的间距（Margin），这里再补一半，卡片之间和到窗口边缘都是一样的空隙。
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Padding(0, design.Gap/2, design.Gap/2, left).Children(func() {
		if t == nil {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				if !a.sidebarOpen && (a.hostsError != "" || a.notice != "") {
					ui.Column(c).Padding(design.Gap/2, design.Gap/2, 0, design.Gap/2).Children(func() {
						noticeBanner(c, colors, "hostsError", "dismissHostsError", "关闭系统错误", a.hostsError, func() { a.hostsError = "" })
						noticeBanner(c, colors, "notice", "dismissNotice", "关闭通知", a.notice, func() { a.notice = "" })
					})
				}
				a.emptyState(c, colors)
			})
		} else {
			ui.Column(c.Key(t.id)).Grow(1).MinWidth(0).Children(func() {
				if !a.sidebarOpen && (a.hostsError != "" || a.notice != "") {
					ui.Column(c).Padding(design.Gap/2, design.Gap/2, 0, design.Gap/2).Children(func() {
						noticeBanner(c, colors, "hostsError", "dismissHostsError", "关闭系统错误", a.hostsError, func() { a.hostsError = "" })
						noticeBanner(c, colors, "notice", "dismissNotice", "关闭通知", a.notice, func() { a.notice = "" })
					})
				}
				a.paneView(c, colors, t, t.root)
				if !hasFiles && t.floatingQueue && len(t.transfers) > 0 {
					a.floatingTransferQueue(c, colors, t, aiW, hasAI)
				}
			})
			if hasFiles {
				a.filesDrawer(c, colors, t, filesW, filesMaxW)
			}
		}
		if hasAI {
			a.aiDrawer(c, colors, aiW, aiMaxW)
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
				h, ok := a.connectingHost[hostID]
				if !ok {
					h, ok = a.hostByID(hostID)
				}
				if ok {
					name := h.Name
					if name == "" {
						name = h.Address
					}
					phase := a.connectingPhase[hostID]
					if phase == "" {
						phase = locales.ViewConnecting()
					}
					ui.Row(c).Padding(8, 12).Gap(10).AlignItems(ui.Center).Radius(design.RadiusRow).
						Background(colors.Card).Border(1, colors.CardBorder).Children(func() {
						design.DistroCoatView(c, h.ID, h.Distro, h.Production, 18)
						ui.Text(c, name).FontWeight(500)
						loading.Arc(c, loading.Options{Size: 12, Color: t.Accent}).Label("正在连接").Shrink(0)
						ui.Text(c, phase).FontSize(design.TextSmall).TextColor(t.TextMuted)
						ui.Button(c.Key("cancelConnect:"+hostID), "取消连接").Height(24).Padding(0, 8).OnClick(cancel)
					})
				}
			}
		}
		if len(a.hosts) == 0 {
			ui.PrimaryButton(c.Key("addFirst"), locales.AddHost()).OnClick(func() { a.openAdd() })
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
					btn := ui.ButtonBase(c.Key("recent:"+host.ID)).Label("连接 "+host.Name).
						Height(32).Padding(0, 8).Gap(8).Justify(ui.Start).AlignItems(ui.Center).
						Radius(design.RadiusRow).Transition(design.Hover)
					if btn.Hovered() {
						btn.Background(colors.Hover)
					}
					btn.Children(func() {
						design.DistroCoatView(c, host.ID, host.Distro, host.Production, 18)
						ui.Text(c, host.Name).FontWeight(500).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
						ui.Text(c, host.Target()).FontSize(design.TextSmall).TextColor(colors.TextFaint).SingleLine().Shrink(0)
					}).OnClick(func() { a.open(host) })
				}
			})
		}
		ui.Row(c).Gap(14).Wrap().Justify(ui.Center).AlignItems(ui.Center).Children(func() {
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
		ui.Themed(c.Key(p.id), &quiet).Background(ui.Transparent).Grow(1).MinWidth(0).MinHeight(0).Children(func() {
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
	card := raised(ui.Column(c.Key(p.id)).Grow(1).MinWidth(0).MinHeight(0).Margin(design.Gap/2).Clip(), colors, design.RadiusCard)
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
			closeText := locales.PaneCloseTab()
			if t.root.leaves() > 1 {
				closeText = "关闭窗格"
			}
			ui.Row(c.Key(fmt.Sprintf("endedBanner:%d", p.id))).Height(32).Shrink(0).Padding(0, 12).Gap(8).
				AlignItems(ui.Center).Background(colors.DangerSoft).BorderWidth(0, 0, 1, 0).BorderColor(colors.Hairline).Children(func() {
				ui.Icon(c, design.Icon("cloud-off")).FontSize(14).TextColor(colors.Danger)
				ui.Text(c, locales.PaneDisconnected()).FontSize(design.TextSmall).FontWeight(500).TextColor(c.Theme().Text).Grow(1)
				ui.Button(c.Key(fmt.Sprintf("bannerReconnect:%d", p.id)), locales.Reconnect()).Height(24).Padding(0, 8).OnClick(func() { a.reconnect(t) })
				ui.Button(c.Key(fmt.Sprintf("bannerClose:%d", p.id)), closeText).Height(24).Padding(0, 8).OnClick(func() { a.closePane(t, p) })
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
		view := terminal.View(c, p.term).Bind(&p.termHandle).Grow(1).MinHeight(0)
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
	// 按终端列数估计窗格宽度，窄的时候逐级收起次要信息，而不是让文字互相挤压重叠。
	cols, rows := p.term.Size()
	if cols != p.lastCols || rows != p.lastRows {
		if p.lastCols != 0 || p.lastRows != 0 {
			p.sizeChangedAt = time.Now()
		}
		p.lastCols, p.lastRows = cols, rows
	}
	compact, tiny := cols < 70, cols < 50
	headerGap := float32(8)
	if compact {
		headerGap = 6
	}

	ui.Row(c).Height(32).Shrink(0).Padding(0, 12).Gap(headerGap).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(colors.Hairline).FontSize(design.TextSmall).Children(func() {
		multiPane := t.root.leaves() > 1
		if multiPane {
			design.DistroCoatView(c, t.host.ID, t.host.Distro, t.host.Production, 14)
			ui.Text(c, tabLabel(t)).FontWeight(600).FontSize(design.TextBody).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
		} else {
			// 单窗格时主机名已在上方标签页清晰显示，此处仅展示主机专属斑纹，避免信息冗余
			design.DistroCoatView(c, t.host.ID, t.host.Distro, t.host.Production, 14)
		}
		if multiPane || a.broadcastOn {
			chip(c.Key(fmt.Sprintf("paneOrd:%d", p.id)), fmt.Sprintf("#%d", paneOrdinal(t.root, p)), theme.TextMuted, colors.Chip)
		}
		user, dir := paneUserAndDir(t, p)
		if user != "" {
			if user == "root" {
				ui.Row(c.Key(fmt.Sprintf("paneUser:%d", p.id))).Height(20).Padding(0, 7).Gap(4).
					Radius(design.RadiusChip).AlignItems(ui.Center).Shrink(0).Background(colors.DangerSoft).Children(func() {
					ui.Box(c).Size(5, 5).Radius(3).Background(colors.Danger)
					ui.Text(c, user).TextColor(colors.Danger).FontWeight(700).FontSize(design.TextSmall - 0.5).NoWrap()
				})
			} else if !(multiPane && compact) {
				chip(c.Key(fmt.Sprintf("paneUser:%d", p.id)), user, theme.TextMuted, colors.Chip)
			}
		}
		// 路径放在一个会裁剪的容器里：容器宽度只取剩余空间，文字超出时用省略号截断。
		ui.Row(c).Basis(0).Grow(1).MinWidth(0).Clip().Children(func() {
			if dir != "" {
				ui.Text(c, shortPath(dir)).TextColor(theme.TextMuted).SingleLine().TruncateStart().Ellipsis("…").MinWidth(0).Shrink(1)
			}
		})
		switch {
		case t.reconnecting:
			chip(c.Key(fmt.Sprintf("status:%d", p.id)), locales.ViewReconnecting(t.reconnectAttempt), theme.Warning, theme.Warning.Alpha(0.14))
			ui.ButtonBase(c.Key(fmt.Sprintf("cancelReconnect:%d", p.id))).Label(locales.Cancel()).Shrink(0).Children(func() {
				ui.Text(c, locales.Cancel()).TextColor(theme.Accent).FontWeight(600).NoWrap()
			}).OnClick(func() { a.cancelReconnect(t) })
		case p.ended:
			ui.Text(c.Key(fmt.Sprintf("status:%d", p.id)), locales.ViewSessionEnded()).TextColor(theme.TextMuted).NoWrap().Shrink(0)
			ui.ButtonBase(c.Key(fmt.Sprintf("reconnect:%d", p.id))).Label(locales.Reconnect()).Shrink(0).Children(func() {
				ui.Text(c, locales.Reconnect()).TextColor(theme.Accent).FontWeight(600).NoWrap()
			}).OnClick(func() { a.reconnect(t) })
		case t.local:
			ui.Row(c.Key(fmt.Sprintf("status:%d", p.id))).Gap(6).AlignItems(ui.Center).Shrink(0).Label(locales.ViewLocal()).Children(func() {
				dot := ui.Box(c).Size(6, 6).Radius(3).Background(colors.Live)
				s := dot.Keyframes("pulse", design.Pulse)
				dot.Scale(s, s)
				if !tiny {
					ui.Text(c, locales.ViewLocal()).TextColor(colors.TextFaint).NoWrap()
				}
			})
		case t.stalled:
			chip(c.Key(fmt.Sprintf("status:%d", p.id)), locales.ViewServerUnresponsive(), theme.Warning, theme.Warning.Alpha(0.14))
		default:
			if t.host.Production {
				label := locales.ViewProduction()
				if compact {
					label = locales.ViewProductionShort()
				}
				chip(c.Key(fmt.Sprintf("paneProd:%d", p.id)), label, colors.Danger, colors.DangerSoft)
			}
			statusRow := ui.Row(c.Key(fmt.Sprintf("status:%d", p.id))).Gap(6).AlignItems(ui.Center).Shrink(0).Label(locales.ViewConnected())
			if t.latency > 0 {
				statusRow.Tooltip(locales.ViewConnected() + " · " + formatLatency(t.latency))
			}
			statusRow.Children(func() {
				// 圆点第一次出现时（连上、或从“无响应”恢复）放大回弹一次，提示状态变好了。
				dot := ui.Box(c).Size(6, 6).Radius(3).Background(colors.Live)
				s := dot.Keyframes("pulse", design.Pulse)
				dot.Scale(s, s)
				if t.latency > 0 && !compact {
					ui.Text(c, formatLatency(t.latency)).TextColor(colors.TextFaint).FontFeatures("tnum").NoWrap()
				}
			})
		}
		if !compact && !p.ended && !t.reconnecting && !p.sizeChangedAt.IsZero() && time.Since(p.sizeChangedAt) < 1500*time.Millisecond {
			ui.Textf(c, "%d × %d", cols, rows).TextColor(colors.TextFaint).FontFeatures("tnum").NoWrap().Shrink(0)
		}
		if !t.local && t.client != nil && !tiny {
			if n := a.countTunnelsForTab(t.id); n > 0 {
				lbl := locales.ViewPaneTunnelsBadge(n)
				accessLbl := lbl
				if t.root.leaves() > 1 {
					accessLbl = locales.ViewPaneTunnelsLabel(paneOrdinal(t.root, p), lbl)
				}
				tbBtn := ui.ButtonBase(c.Key(fmt.Sprintf("paneTunnels:%d", p.id))).Label(accessLbl).Tooltip(locales.ViewPaneTunnelsTooltip()).
					Height(20).Padding(0, 7).Gap(4).Radius(design.RadiusChip).AlignItems(ui.Center).Shrink(0).
					Background(colors.Chip).Transition(design.Hover)
				if tbBtn.Hovered() {
					tbBtn.Background(colors.Hover)
				}
				tbBtn.Children(func() {
					ui.Box(c).Size(5, 5).Radius(3).Background(colors.Live)
					ui.Text(c, lbl).FontSize(design.TextSmall - 0.5).TextColor(theme.TextMuted).FontFeatures("tnum").NoWrap()
				}).OnClick(a.openForwards)
			}
		}
		if t.monitorOpen && !t.local && t.client != nil && !p.ended && !tiny {
			a.paneMonitorMetrics(c, colors, t, p)
		}
		if !t.local && t.client != nil {
			monitorLabel := locales.MonitorToggleOpen()
			if t.monitorOpen {
				monitorLabel = locales.MonitorToggleClose()
			}
			btn := iconButton(c.Key(fmt.Sprintf("paneMonitorToggle:%d", p.id)), colors, "activity", monitorLabel, func() { a.toggleMonitor(t) }).Disabled(p.ended)
			if t.monitorOpen {
				btn.Background(theme.Accent.Alpha(0.12))
			}
		}
		if a.broadcast[p.id] {
			fg, bg := theme.Accent, theme.Accent.Alpha(0.14)
			if t.host.Production {
				fg, bg = colors.Danger, colors.DangerSoft
			}
			b := ui.ButtonBase(c.Key(fmt.Sprintf("paneBroadcast:%d", p.id))).
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
		} else if a.broadcastOn && t.focus == p && !p.ended {
			bcastText := "未加入广播"
			if compact {
				bcastText = "未广播"
			}
			chip(c.Key(fmt.Sprintf("paneNotInBcast:%d", p.id)), bcastText, colors.TextFaint, colors.Chip).
				Tooltip("当前焦点窗格未加入广播")
		}
		moreLabel := locales.PaneMoreActions()
		if t.root.leaves() > 1 {
			moreLabel = fmt.Sprintf("%s %d", locales.PaneMoreActions(), paneOrdinal(t.root, p))
		}
		moreBtn := ui.ButtonBase(c.Key(fmt.Sprintf("paneMore:%d", p.id))).Label(moreLabel).Tooltip(locales.PaneMoreActions()).
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
			m.Separator()
			canSplit := t.local || t.client != nil
			if m.Item("向右拆分").Disabled(!canSplit).Chosen() {
				t.focus = p
				a.split(t, false)
			}
			if m.Item("向下拆分").Disabled(!canSplit).Chosen() {
				t.focus = p
				a.split(t, true)
			}
			if !t.local && m.Item("重新连接会话").Chosen() {
				a.reconnect(t)
			}
			m.Separator()
			if m.Item("关闭窗格").Danger().Chosen() {
				t.focus = p
				a.closeFocused(t)
			}
		}, menuButtonOptions(colors))
	})
}

// chip 是一枚小标签。
func chip(c *ui.Context, text string, fg, bg ui.Color) ui.Element {
	return ui.Row(c).Height(20).Padding(0, 8).Radius(design.RadiusChip).AlignItems(ui.Center).Shrink(0).Background(bg).Children(func() {
		ui.Text(c, text).TextColor(fg).FontWeight(600).FontSize(design.TextSmall - 0.5).NoWrap()
	})
}

func (a *app) filesDrawer(c *ui.Context, colors design.Colors, t *tab, width, maxW float32) {
	a.ensureFiles(t)
	a.followCwd(t)
	if a.filesWidth <= 0 {
		a.filesWidth = 560
	}
	if width <= 0 {
		width = a.filesWidth
	}
	if maxW < 300 {
		maxW = 300
	}
	if a.filesSplit <= 0 {
		a.filesSplit = 50
	}
	// 终端与文件抽屉之间的拖动分隔条：负外边距贴在两张卡片之间的空隙里，不改变卡片间距；
	// 拖动改变抽屉宽度，双击恢复默认宽度。
	grip := ui.Box(c.Key("drawerResize")).Width(8).Shrink(0).Margin(design.Gap/2, -4).
		Cursor(ui.CursorResizeEW).Focusable().Label("调整文件抽屉宽度")
	dx, _, held := grip.Dragged()
	changed := false
	switch {
	case grip.DoubleClicked():
		a.filesWidth = 560
		changed = true
	case held && dx != 0:
		a.filesWidth = min(max(width-dx, 300), maxW)
		a.filesResizing = true
	case a.filesResizing && !held:
		a.filesResizing = false
		changed = true
	case grip.Shortcut(0, ui.KeyLeft):
		a.filesWidth = min(width+20, maxW)
		changed = true
	case grip.Shortcut(0, ui.KeyRight):
		a.filesWidth = max(width-20, 300)
		changed = true
	}
	if changed && a.devicePath != "" {
		next := a.local
		next.FilesWidth = a.filesWidth
		_ = a.saveLocal(next)
	}
	if held || grip.Hovered() {
		accent := c.Theme().Accent
		grip.Draw(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X + r.W/2 - 0.5, Y: r.Y + 8, W: 1, H: max(0, r.H-16)}, accent, 0)
		})
	}
	// 两栏并排，支持拖动调整抽屉总宽和内部左右栏比例；不做宽度动画，避免终端连续收到几十次 SIGWINCH。
	drawer := raised(ui.Column(c.Key("drawer")).Width(width).Shrink(0).Margin(design.Gap/2).Clip(), colors, design.RadiusCard)
	// 抽屉的宽度一步到位，终端只改一次尺寸；内容从右侧滑入。
	// 之前用 Collapse 让宽度从 0 长大，会让终端逐帧改尺寸，已改掉。
	drawer.Transition(design.SlideIn(18)).Children(func() {
		ui.Row(c.Key("filesHeader")).Height(28).Shrink(0).Padding(0, 6, 0, 10).Gap(6).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(colors.Hairline).Children(func() {
			theme := c.Theme()
			ui.Icon(c, design.Icon("folder")).FontSize(13).TextColor(theme.Accent)
			ui.Text(c, "文件管理").FontSize(design.TextSmall).FontWeight(600).SingleLine()
			if target := t.host.Target(); target != "" {
				ui.Box(c).Padding(1, 6).Radius(design.RadiusChip).Background(colors.Chip).Shrink(1).MinWidth(0).Children(func() {
					ui.Text(c, target).FontSize(design.TextSmall - 1).TextColor(theme.TextMuted).SingleLine().Ellipsis("…")
				})
			}
			ui.Spacer(c)
			iconButton(c.Key("filesClose"), colors, "x", "关闭文件抽屉", func() {
				a.setFilesOpen(t, false)
			}).
				Tooltip("关闭文件抽屉（" + shortcutText("J") + "）")
		})
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

// floatingTransferQueue 在文件抽屉未展开时，于终端右下角悬浮展示当前传输队列。
func (a *app) floatingTransferQueue(c *ui.Context, colors design.Colors, t *tab, aiW float32, hasAI bool) {
	windowW, _ := c.Size()
	var sideW float32
	if a.sidebarOpen {
		sideW = a.effectiveSidebarWidth()
	}
	var aiUsed float32
	if hasAI {
		aiUsed = aiW
	}
	availW := max(float32(240), windowW-sideW-aiUsed-24)
	queueW := min(float32(340), max(float32(220), availW-24))
	card := ui.Column(c.Key("floatingTransfers")).Absolute().Right(14).Bottom(14).Width(queueW).
		Radius(design.RadiusCard).Material(glass.Glass{}).Border(1, colors.CardBorder).
		Shadow(0, 14, 36, -10, colors.CardShadow).Clip()
	card.Transition(design.SlideIn(18)).Children(func() {
		a.transferQueueView(c, colors, t)
	})
}

// transferQueueView 展示传输队列面板。
func (a *app) transferQueueView(c *ui.Context, colors design.Colors, t *tab) {
	theme := c.Theme()
	var activeCount, failedCount int
	hasFinished := false
	for _, tr := range t.transfers {
		if tr.done.Load() {
			hasFinished = true
			if tr.err != "" && tr.err != locales.FileCancelled() && tr.err != "已取消" {
				failedCount++
			}
		} else {
			activeCount++
		}
	}
	ui.Column(c.Key("transfers")).Padding(6, 10, 8, 10).Gap(6).Children(func() {
		ui.Row(c).Height(22).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.ViewTransferQueue()).FontSize(design.TextSmall).FontWeight(600).TextColor(theme.TextMuted)
			if activeCount > 0 {
				chip(c, fmt.Sprintf("%d 进行中", activeCount), theme.Accent, theme.Accent.Alpha(0.14))
			}
			if failedCount > 0 {
				chip(c, fmt.Sprintf("%d 失败", failedCount), colors.Danger, colors.DangerSoft)
			}
			ui.Spacer(c)
			if activeCount > 0 {
				cancelBtn := ui.ButtonBase(c.Key("cancelAllTransfers")).Label("全部取消").Height(20).Padding(0, 6).
					Radius(design.RadiusRow - 2).AlignItems(ui.Center).Transition(design.Hover)
				if cancelBtn.Hovered() {
					cancelBtn.Background(colors.Hover)
				}
				cancelBtn.Children(func() {
					ui.Text(c, "全部取消").FontSize(design.TextSmall - 1).TextColor(colors.Danger).NoWrap()
				}).OnClick(func() { a.cancelAllTransfers(t) })
			}
			if hasFinished {
				clearBtn := ui.ButtonBase(c.Key("clearTransfers")).Label("清除已完成").Height(20).Padding(0, 6).
					Radius(design.RadiusRow - 2).AlignItems(ui.Center).Transition(design.Hover)
				if clearBtn.Hovered() {
					clearBtn.Background(colors.Hover)
				}
				clearBtn.Children(func() {
					ui.Text(c, "清除已完成").FontSize(design.TextSmall - 1).TextColor(theme.TextMuted).NoWrap()
				}).OnClick(func() { a.clearFinishedTransfers(t) })
			}
		})
		ui.Scroll(c.Key("transfersScroll")).MaxHeight(140).Children(func() {
			ui.Column(c).Gap(4).Children(func() {
				for i := len(t.transfers) - 1; i >= 0; i-- {
					item := t.transfers[i]
					done := item.done.Load()
					ui.Column(c.Key(fmt.Sprintf("tr:%d", item.id))).Gap(3).Padding(5, 6).
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
							ui.Text(c, item.fileName).FontSize(design.TextSmall).FontWeight(500).Grow(1).MinWidth(0).SingleLine().Ellipsis("…").Tooltip(item.fileName)
							if !done {
								iconButton(c.Key(fmt.Sprintf("trCancel:%d", item.id)), colors, "x", locales.ViewCancelTransfer(), func() { a.cancelTransfer(item) })
							} else {
								if item.err != "" {
									retryBtn := ui.ButtonBase(c.Key(fmt.Sprintf("trRetry:%d", item.id))).
										Label("重试 "+item.fileName).Height(20).Padding(0, 6).Radius(design.RadiusChip).
										AlignItems(ui.Center).Background(colors.Card).Border(1, colors.CardBorder).
										Disabled(t.files == nil).Transition(design.Hover)
									if retryBtn.Hovered() {
										retryBtn.Background(theme.Accent.Alpha(0.14))
									}
									retryBtn.Children(func() {
										ui.Text(c, "重试").FontSize(design.TextSmall - 1).FontWeight(500).TextColor(theme.Accent).NoWrap()
									}).OnClick(func() { a.retryTransfer(t, item) })
								}
								iconButton(c.Key(fmt.Sprintf("trDismiss:%d", item.id)), colors, "x", "移除传输记录 "+item.fileName, func() { a.dismissTransfer(t, item.id) }).
									Tooltip("移除记录")
							}
						})
						if !done {
							total := item.total.Load()
							pct := float64(-1)
							if total > 0 {
								pct = min(max(float64(item.transferred.Load())/float64(total), 0), 1)
							} else if item.folder {
								if filesTotal := item.filesTotal.Load(); filesTotal > 0 {
									pct = min(max(float64(item.filesDone.Load())/float64(filesTotal), 0), 1)
								}
							}
							ui.Progress(c, pct).Height(3)
						}
						ui.Row(c).Children(func() {
							var info string
							if done {
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
							ui.Text(c, info).FontSize(design.TextSmall - 1).TextColor(color).FontFeatures("tnum").SingleLine().Ellipsis("…").Tooltip(info)
						})
					})
				}
			})
		})
	})
}

// fileRow 显示一项文件。
func (a *app) fileRow(c *ui.Context, colors design.Colors, t *tab, entry sshconn.Entry) {
	theme := c.Theme()
	menu := func(m *ui.Menu) {
		if entry.IsDir {
			if m.Item(locales.ViewOpen()).Chosen() {
				a.openDir(t, entry.Path)
			}
			if m.Item("下载文件夹").Chosen() {
				a.startDownloadFolder(t, entry.Path, entry.Name)
			}
			if m.Item("下载到当前本地目录").Disabled(a.localDir == "").Chosen() {
				a.acceptPaneDrop(t, true, a.localDir, paneDrag{local: false, path: entry.Path, name: entry.Name, dir: true})
			}
		} else {
			if m.Item("编辑").Chosen() {
				a.openEditor(t, entry.Path, entry.Name)
			}
			if m.Item(locales.Download()).Chosen() {
				a.startDownload(t, entry.Path, entry.Name)
			}
			if m.Item("下载到当前本地目录").Disabled(a.localDir == "").Chosen() {
				a.acceptPaneDrop(t, true, a.localDir, paneDrag{local: false, path: entry.Path, name: entry.Name, dir: false})
			}
		}
		m.Separator()
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
		m.Separator()
		if m.Item(locales.Delete()).Danger().Chosen() {
			a.askDeleteFile(t, entry.Path, entry.Name, entry.IsDir)
		}
	}
	line := ui.Row(c.Key("remoteLine:"+entry.Path)).AlignItems(ui.Center).Radius(design.RadiusRow-2).Padding(0, 2, 0, 0)
	hovered := line.Hovered()
	if hovered {
		line.Background(colors.Hover)
	}
	line.Children(func() {
		row := ui.ButtonBase(c.Key(entry.Path)).Label(entry.Name).Tooltip(entry.Name).Height(28).Grow(1).MinWidth(0).
			Padding(0, 6, 0, 8).Gap(6).Justify(ui.Start).AlignItems(ui.Center).Radius(design.RadiusRow-2).PopoverMenu(menu, menuOptions(colors))
		// 窗口内拖动交给另一栏。拖到桌面要等副本下好：准备好之后才声明系统拖放，
		// 否则这次手势会变成系统拖动，本机栏收不到。松手在窗口外才开始下那份副本。
		row.Drag(paneDrag{path: entry.Path, name: entry.Name, dir: entry.IsDir})
		if entry.IsDir {
			if item, ok := ui.Drop[paneDrag](row); ok {
				a.acceptPaneDrop(t, false, entry.Path, item)
			}
			if over, ok := ui.DragOver[paneDrag](row); ok && over.path != entry.Path {
				row.Background(theme.Accent.Alpha(0.14))
			}
		}
		if at, ok := row.DragMissed(); ok {
			w, h := c.Size()
			if at.X < 0 || at.Y < 0 || at.X > w || at.Y > h {
				a.prepareDragOut(t, entry)
			}
		}
		_, dragPrepared := a.dragReady(t, entry)
		if dragPrepared {
			a.bindDragOut(row, t, entry)
		}
		if entry.IsDir {
			row.OnClick(func() { a.openDir(t, entry.Path) })
		} else if row.DoubleClicked() {
			a.openEditor(t, entry.Path, entry.Name)
		}
		row.Children(func() {
			icon := "file"
			color := colors.TextFaint
			if entry.IsDir {
				icon, color = "folder", theme.Accent
			}
			ui.Icon(c, design.Icon(icon)).FontSize(14).TextColor(color).Shrink(0)
			ui.Text(c, entry.Name).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			if dragPrepared {
				chip(c, "可拖出", theme.Accent, theme.Accent.Alpha(0.14))
			}
			if !entry.IsDir {
				ui.Text(c, formatSize(entry.Size)).FontSize(design.TextSmall - 1).TextColor(colors.TextFaint).FontFeatures("tnum").Shrink(0)
			}
			if a.paneWidth(false) >= 240 && !entry.ModTime.IsZero() {
				ui.Text(c, formatShortTime(entry.ModTime)).FontSize(design.TextSmall - 1).TextColor(colors.TextFaint).FontFeatures("tnum").Shrink(0)
			}
		})
		if hovered {
			more := ui.ButtonBase(c.Key("remoteFileMore:"+entry.Path)).Label("更多远程操作 "+entry.Name).
				Size(22, 22).Center().Radius(4).Shrink(0)
			if more.Hovered() {
				more.Background(colors.Hover)
			}
			more.Children(func() {
				ui.Icon(c, design.Icon("ellipsis")).FontSize(13).TextColor(theme.TextMuted)
			}).PopoverMenuButton(menu, menuButtonOptions(colors))
		}
	})
}

// searchBar 是终端窗格顶部的查找工具条。
func (a *app) searchBar(c *ui.Context, colors design.Colors, t *tab, p *pane) {
	theme := c.Theme()
	ui.Row(c).Height(34).Shrink(0).Padding(0, 10).Gap(6).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(colors.Hairline).Background(colors.Hover).Children(func() {
		ui.Icon(c, design.Icon("search")).FontSize(14).TextColor(theme.TextMuted)
		c.Key(fmt.Sprintf("searchInput:%d", p.id))
		input := ui.TextInput(c, &p.searchQuery).Bind(&p.searchHandle).Placeholder(locales.ViewFindPlaceholder()).Label(locales.ViewFindField()).
			Grow(1).FontSize(design.TextSmall).Border(0, ui.Transparent).Background(ui.Transparent)
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
			ui.Text(c.Key(fmt.Sprintf("searchStatus:%d", p.id)), countText).FontSize(design.TextSmall).TextColor(colors.TextFaint).FontFeatures("tnum").Shrink(0)
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
		layer := ui.Box(c.Key(fmt.Sprintf("dropLayer:%d", p.id))).Absolute().Top(0).Right(0).Bottom(0).Left(0).
			Radius(design.RadiusCard).Background(theme.Accent.Alpha(0.10)).Border(1.5, theme.Accent.Alpha(0.6)).
			Justify(ui.Center).AlignItems(ui.Center).Label(locales.ViewDropUpload())
		if files := layer.DroppedFiles(); len(files) > 0 {
			a.uploadDropped(t, p, files)
		}
		over = over || layer.FileDragOver()
		target := locales.ViewTerminalDirectory()
		user, dir := paneUserAndDir(t, p)
		if resolved := a.previewRemoteDir(t, user, dir); resolved != "" {
			target = shortPath(resolved)
		}
		layer.Children(func() {
			ui.Column(c).Gap(4).AlignItems(ui.Center).Padding(12, 18).Radius(design.RadiusRow).
				Material(glass.Glass{}).Border(1, colors.CardBorder).Shadow(0, 8, 24, -8, colors.CardShadow).Children(func() {
				ui.Text(c, locales.ViewReleaseToUpload()).FontWeight(600)
				ui.Text(c, locales.ViewUploadTo(target)).FontSize(design.TextSmall).TextColor(theme.TextMuted).SingleLine().TruncateStart().Ellipsis("…").MaxWidth(360)
			})
		})
	}
	p.fileDrag = over
}

// paneMonitorMetrics 在窗格顶栏显示紧凑的中文监控摘要（CPU / 内存），点击弹出完整监控浮层。
func (a *app) paneMonitorMetrics(c *ui.Context, colors design.Colors, t *tab, p *pane) {
	s := t.stats
	if s.UpdatedAt.IsZero() {
		if s.LastErr != "" {
			retryBtn := ui.ButtonBase(c.Key(fmt.Sprintf("monitorRetry:%d", p.id))).Label("重试服务器监控").
				Tooltip(s.LastErr+"（点击重试）").Height(22).Padding(0, 6).Gap(4).Radius(design.RadiusChip).
				AlignItems(ui.Center).Background(colors.DangerSoft).Shrink(0)
			if retryBtn.Hovered() {
				retryBtn.Background(colors.Danger.Alpha(0.2))
			}
			retryBtn.Children(func() {
				ui.Text(c, s.LastErr).TextColor(colors.Danger).FontSize(design.TextSmall - 1).NoWrap()
				ui.Icon(c, design.Icon("refresh-cw")).FontSize(11).TextColor(colors.Danger)
			}).OnClick(func() {
				a.stopTabMonitor(t)
				t.monitorOpen = true
				a.startTabMonitor(t)
			})
			return
		}
		ui.Row(c).Gap(5).AlignItems(ui.Center).Shrink(0).Children(func() {
			loading.BouncingDots(c, loading.Options{Size: 10, Color: colors.TextFaint}).Label(locales.MonitorProbing()).Shrink(0)
			ui.Text(c, locales.MonitorProbing()).TextColor(colors.TextFaint).FontSize(design.TextSmall).NoWrap()
		})
		return
	}
	tTheme := c.Theme()
	cpuText := "—"
	cpuColor := colors.TextFaint
	if s.HasCPU {
		cpuText = fmt.Sprintf("%3.0f%%", s.CPUPercent)
		if s.CPUPercent >= 85 {
			cpuColor = colors.Danger
		}
	}
	var memPercent float32
	if s.MemTotalBytes > 0 {
		memPercent = float32(s.MemUsedBytes) * 100.0 / float32(s.MemTotalBytes)
	}
	memColor := colors.TextFaint
	if memPercent >= 85 {
		memColor = colors.Danger
	}

	anchor := ui.ButtonBase(c.Key(fmt.Sprintf("monitorBtn:%d", p.id))).Label(locales.MonitorTitle()).Tooltip(locales.MonitorTitle()).
		Height(24).Padding(0, 6).Gap(8).AlignItems(ui.Center).Radius(design.RadiusRow).Shrink(0).Transition(design.Hover)
	if anchor.Hovered() || p.monitorPopoverOpen {
		anchor.Background(colors.Hover)
	}
	anchor.Children(func() {
		ui.Row(c).Gap(3).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.MonitorCPU()).TextColor(colors.TextFaint).FontSize(design.TextSmall - 1).FontWeight(600)
			ui.Text(c, cpuText).TextColor(cpuColor).FontFeatures("tnum")
		})
		if s.MemTotalBytes > 0 {
			ui.Row(c).Gap(3).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.MonitorMemory()).TextColor(colors.TextFaint).FontSize(design.TextSmall - 1).FontWeight(600)
				ui.Textf(c, "%3.0f%%", memPercent).TextColor(memColor).FontFeatures("tnum")
			})
		}
	}).OnClick(func() { p.monitorPopoverOpen = !p.monitorPopoverOpen })

	ui.Popover(c, anchor, &p.monitorPopoverOpen, func() {
		ui.Column(c).Width(240).Padding(12).Gap(8).Children(func() {
			ui.Text(c, locales.MonitorTitle()).FontWeight(600).FontSize(design.TextBody)
			clampRatio := func(pct float32) float64 {
				return min(max(float64(pct)/100.0, 0), 1)
			}
			metricRow := func(label, val string, valColor ui.Color) {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, label).FontSize(design.TextSmall).TextColor(tTheme.TextMuted).Grow(1)
					ui.Text(c, val).FontSize(design.TextSmall).TextColor(valColor).FontWeight(600).FontFeatures("tnum")
				})
			}
			if s.HasCPU {
				ui.Column(c).Gap(4).Children(func() {
					metricRow(locales.MonitorCPU(), fmt.Sprintf("%.1f%%", s.CPUPercent), cpuColor)
					ui.Progress(c, clampRatio(s.CPUPercent)).Height(3)
				})
			} else {
				metricRow(locales.MonitorCPU(), "—", colors.TextFaint)
			}
			metricRow(locales.MonitorLoad(), fmt.Sprintf("%.2f / %.2f", s.Load1, s.Load5), tTheme.Text)
			if s.MemTotalBytes > 0 {
				ui.Column(c).Gap(4).Children(func() {
					metricRow(locales.MonitorMemory(), fmt.Sprintf("%s / %s (%.0f%%)", formatBytes(s.MemUsedBytes), formatBytes(s.MemTotalBytes), memPercent), memColor)
					ui.Progress(c, clampRatio(memPercent)).Height(3)
				})
			}
			if s.DiskTotalBytes > 0 {
				diskPercent := float32(s.DiskUsedBytes) * 100.0 / float32(s.DiskTotalBytes)
				diskColor := tTheme.Text
				if diskPercent >= 90 {
					diskColor = colors.Danger
				}
				ui.Column(c).Gap(4).Children(func() {
					metricRow(locales.MonitorDisk(), fmt.Sprintf("%s / %s (%.0f%%)", formatBytes(s.DiskUsedBytes), formatBytes(s.DiskTotalBytes), diskPercent), diskColor)
					ui.Progress(c, clampRatio(diskPercent)).Height(3)
				})
			}
			netUp, netDown := "—", "—"
			netColor := colors.TextFaint
			if s.HasNet {
				netUp = formatRate(s.NetTxBytesPerSec)
				netDown = formatRate(s.NetRxBytesPerSec)
				netColor = tTheme.Accent
			}
			metricRow(locales.MonitorNetUp(), netUp, netColor)
			metricRow(locales.MonitorNetDown(), netDown, netColor)
			if s.LastErr != "" {
				ui.Text(c, "最近探测失败，正在等待下次重试").FontSize(design.TextSmall - 1).TextColor(colors.Danger)
			}
		})
	})
}
