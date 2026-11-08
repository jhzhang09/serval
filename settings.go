package main

import (
	"errors"
	"fmt"
	"slices"

	"serval/core/vault"
	"serval/design"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// 外观设置。默认（空值）跟随系统。
const (
	appearanceSystem = ""
	appearanceLight  = "light"
	appearanceDark   = "dark"
)

var appearanceModes = []string{appearanceSystem, appearanceLight, appearanceDark}

var autoLockChoices = []int{0, 5, 15, 30, 60}

var fontSizes = []float32{11, 12, 13, 14, 15, 16}
var fontSizeLabels = []string{"11", "12", "13", "14", "15", "16"}

var lineHeights = []float32{1.0, 1.15, 1.3, 1.5}
var lineHeightLabels = []string{"1.0", "1.15", "1.3", "1.5"}

var cursorStyles = []string{"block", "bar", "underline"}
var cursorStyleLabels = []string{"方块", "竖线", "下划线"}

// 回滚缓冲区按容量设置：终端插件的 Scrollback 单位是字节，一行占多少字节取决于内容，
// 按“行数”标注会误导。0 表示插件默认的 10 MB。
var scrollbackMB = []int{5, 10, 50, 100}
var scrollbackLabels = []string{"5 MB", "10 MB", "50 MB", "100 MB"}

// 预设配色必须是 terminal.GhosttyThemes() 里存在的名字（TestTerminalThemePresetsExist 保证），
// 找不到时终端会退回默认配色。空字符串表示 Serval 自己的默认配色。
var popularDarkThemes = []string{"", "Dracula", "Nord", "TokyoNight", "Catppuccin Mocha"}
var popularDarkThemeLabels = []string{"默认深色", "Dracula", "Nord", "Tokyo Night", "Catppuccin Mocha"}

var popularLightThemes = []string{"", "Catppuccin Latte", "GitHub Light Default", "Atom One Light"}
var popularLightThemeLabels = []string{"默认浅色", "Catppuccin Latte", "GitHub", "One Light"}

// indexOr 返回 v 在 list 里的下标，找不到时返回 def。
// 不能写成 max(slices.Index(...), def)：那样会把排在默认项前面的选项也抬成默认项。
func indexOr[T comparable](list []T, v T, def int) int {
	if i := slices.Index(list, v); i >= 0 {
		return i
	}
	return def
}

// settings 是随保险库同步的偏好设置（记录类型 settings，固定 ID settings）。
//
// 外观以及终端字体与主题、代码片段默认值等跨设备一致的偏好都放在这里；
// 写入时通过 JSONChange 保留其他版本客户端增加的字段。
type settings struct {
	Appearance string `json:"appearance,omitempty"`

	// 终端设置（随同步）
	TerminalFontFamily string  `json:"terminalFontFamily,omitempty"`
	TerminalFontSize   float32 `json:"terminalFontSize,omitempty"`
	TerminalLineHeight float32 `json:"terminalLineHeight,omitempty"`
	TerminalCursor     string  `json:"terminalCursor,omitempty"`
	// TerminalScrollbackMB 是回滚缓冲区容量（MB），0 表示终端插件默认的 10 MB。
	TerminalScrollbackMB int    `json:"terminalScrollbackMB,omitempty"`
	TerminalThemeLight   string `json:"terminalThemeLight,omitempty"`
	TerminalThemeDark    string `json:"terminalThemeDark,omitempty"`
}

// deviceSettings 是只属于当前这台设备的设置，保存在配置目录下的 device.json，
// 不写进 vault.json，也不随保险库同步。
type deviceSettings struct {
	// RecentHosts 是本机最近连接过的主机 ID，按时间从近到远排列。
	RecentHosts []string `json:"recentHosts,omitempty"`
	// AutoLockMinutes 是闲置指定分钟后自动锁定的时间，0 表示不自动锁定。
	AutoLockMinutes int `json:"autoLockMinutes,omitempty"`
	// DisableRestoreSession 为真时不在启动时恢复上次会话。
	DisableRestoreSession bool `json:"disableRestoreSession,omitempty"`
	// SavedSession 是本机退出时保存的标签与窗格布局。
	SavedSession *savedSession `json:"savedSession,omitempty"`
}

// savedPane 保存一个窗格或窗格拆分。叶子窗格（终端）时 First 和 Second 为空。
type savedPane struct {
	Vertical bool       `json:"vertical,omitempty"`
	Percent  float32    `json:"percent,omitempty"`
	First    *savedPane `json:"first,omitempty"`
	Second   *savedPane `json:"second,omitempty"`
}

func (p *savedPane) leaves() int {
	if p == nil || (p.First == nil && p.Second == nil) {
		return 1
	}
	n := 0
	if p.First != nil {
		n += p.First.leaves()
	}
	if p.Second != nil {
		n += p.Second.leaves()
	}
	if n == 0 {
		return 1
	}
	return n
}

// savedTab 保存一个标签页的主机与窗格布局。
type savedTab struct {
	HostID string     `json:"hostID"`
	Root   *savedPane `json:"root,omitempty"`
}

// savedSession 保存退出时的全部标签和激活位置。只保存在本机设置 device.json。
type savedSession struct {
	Tabs   []savedTab `json:"tabs,omitempty"`
	Active int        `json:"active,omitempty"`
}

const maxRecentHosts = 20

type settingsTab int

const (
	settingsTabGeneral settingsTab = iota
	settingsTabTerminal
)

// settingsPanel 是“设置”浮层面板的状态。
type settingsPanel struct {
	open, fresh bool
	tab         settingsTab
	err         string

	rememberDevice bool

	// 修改主密码表单
	changePwOpen    bool
	currentPassword string
	newPassword     string
	confirmPassword string
	changePwErr     string
	// changePwBusy 表示正在后台派生密钥（两次 Argon2id），期间按钮禁用。
	changePwBusy bool
	// revealChangePw 让刚展开的修改主密码表单滚动到可见位置（设置项在滚动区域里，表单可能在下方）。
	revealChangePw bool
	restoreSession bool
}

// openSettings 打开设置面板。
func (a *app) openSettings() {
	a.settingsPanel = settingsPanel{
		open:           true,
		fresh:          true,
		rememberDevice: a.isRememberedOnDevice(),
		restoreSession: !a.local.DisableRestoreSession,
	}
}

// setAppearance 切换外观并写入保险库，让所有已打开的终端立即跟着更新。
func (a *app) setAppearance(mode string) {
	if a.settings.Appearance == mode {
		return
	}
	next := a.settings
	next.Appearance = mode
	if err := a.saveSettings(next); err != nil {
		a.settingsPanel.err = err.Error()
		a.toast(ui.Toast{Title: "没能保存设置", Description: err.Error(), Type: "error"})
	}
}

func (a *app) setTerminalFontSize(size float32) {
	if a.settings.TerminalFontSize == size {
		return
	}
	next := a.settings
	next.TerminalFontSize = size
	if err := a.saveSettings(next); err != nil {
		a.settingsPanel.err = err.Error()
	}
}

func (a *app) setTerminalLineHeight(lh float32) {
	if a.settings.TerminalLineHeight == lh {
		return
	}
	next := a.settings
	next.TerminalLineHeight = lh
	if err := a.saveSettings(next); err != nil {
		a.settingsPanel.err = err.Error()
	}
}

func (a *app) setTerminalCursor(cursor string) {
	if a.settings.TerminalCursor == cursor {
		return
	}
	next := a.settings
	next.TerminalCursor = cursor
	if err := a.saveSettings(next); err != nil {
		a.settingsPanel.err = err.Error()
	}
}

func (a *app) setTerminalScrollbackMB(mb int) {
	if a.settings.TerminalScrollbackMB == mb {
		return
	}
	next := a.settings
	next.TerminalScrollbackMB = mb
	if err := a.saveSettings(next); err != nil {
		a.settingsPanel.err = err.Error()
	}
}

func (a *app) setTerminalThemeDark(theme string) {
	if a.settings.TerminalThemeDark == theme {
		return
	}
	next := a.settings
	next.TerminalThemeDark = theme
	if err := a.saveSettings(next); err != nil {
		a.settingsPanel.err = err.Error()
	}
}

func (a *app) setTerminalThemeLight(theme string) {
	if a.settings.TerminalThemeLight == theme {
		return
	}
	next := a.settings
	next.TerminalThemeLight = theme
	if err := a.saveSettings(next); err != nil {
		a.settingsPanel.err = err.Error()
	}
}

// recordRecentHost 在本机设置里记下最近打开的主机。
func (a *app) recordRecentHost(id string) {
	if id == "" {
		return
	}
	recent := slices.DeleteFunc(slices.Clone(a.local.RecentHosts), func(x string) bool { return x == id })
	recent = append([]string{id}, recent...)
	if len(recent) > maxRecentHosts {
		recent = recent[:maxRecentHosts]
	}
	if slices.Equal(recent, a.local.RecentHosts) {
		return
	}
	next := a.local
	next.RecentHosts = recent
	if err := a.saveLocal(next); err != nil {
		a.notice = "保存最近使用的主机失败：" + err.Error()
	}
}

// removeRecentHost 从本机最近使用列表里移除已删除的主机。
func (a *app) removeRecentHost(id string) {
	if !slices.Contains(a.local.RecentHosts, id) {
		return
	}
	next := a.local
	next.RecentHosts = slices.DeleteFunc(slices.Clone(a.local.RecentHosts), func(x string) bool { return x == id })
	if err := a.saveLocal(next); err != nil {
		a.notice = "保存最近使用的主机失败：" + err.Error()
	}
}

// submitChangePassword 在后台修改主密码：要做两次 Argon2id，不能在主线程上执行。
func (a *app) submitChangePassword() {
	p := &a.settingsPanel
	if p.changePwBusy {
		return
	}
	if p.newPassword != p.confirmPassword {
		p.changePwErr = "两次输入的新主密码不一致。"
		return
	}
	current, next := p.currentPassword, p.newPassword
	p.changePwBusy, p.changePwErr = true, ""
	go func() {
		err := a.vault.ChangePassword(current, next)
		a.post(func() {
			p := &a.settingsPanel
			p.changePwBusy = false
			switch {
			case errors.Is(err, vault.ErrWrongPassword):
				p.changePwErr = "当前主密码不正确。"
			case err != nil:
				p.changePwErr = err.Error()
			default:
				p.changePwOpen = false
				p.currentPassword, p.newPassword, p.confirmPassword, p.changePwErr = "", "", "", ""
				a.toast(ui.Toast{Title: "已修改主密码"})
			}
		})
	}()
}

// settingsDialog 是设置面板。设置主密码面板打开时让位。
func (a *app) settingsDialog(c *ui.Context, colors design.Colors) {
	p := &a.settingsPanel
	if !p.open || a.setup.open {
		return
	}
	t := c.Theme()
	mode := max(slices.Index(appearanceModes, a.settings.Appearance), 0)
	// 标题和“完成”固定，中间的设置项放进滚动区域：设置项越来越多，窗口矮时放不下。
	// 220 是标题、按钮、面板内边距和面板与窗口边缘的距离之和。
	_, windowHeight := c.Size()

	curSize := a.settings.TerminalFontSize
	if curSize <= 0 {
		curSize = 13
	}
	sizeIdx := indexOr(fontSizes, curSize, 2)
	curLH := a.settings.TerminalLineHeight
	if curLH <= 0 {
		curLH = 1.15
	}
	lhIdx := indexOr(lineHeights, curLH, 1)
	cursorIdx := indexOr(cursorStyles, a.settings.TerminalCursor, 0)
	curScroll := a.settings.TerminalScrollbackMB
	if curScroll <= 0 {
		curScroll = 10
	}
	scrollIdx := indexOr(scrollbackMB, curScroll, 1)
	darkThemeIdx := indexOr(popularDarkThemes, a.settings.TerminalThemeDark, 0)
	lightThemeIdx := indexOr(popularLightThemes, a.settings.TerminalThemeLight, 0)

	sheet(c, colors, &p.open, &p.fresh, 560, 22, func() {
		ui.Column(c).Gap(14).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "设置").FontSize(design.TextHeading).FontWeight(600).Grow(1)
				tabIdx := int(p.tab)
				c.Key("settingsTab")
				ui.Segmented(c, &tabIdx, "通用", "终端").OnChange(func() {
					p.tab = settingsTab(tabIdx)
				})
			})
			ui.Scroll(c).Key("settingsScroll").MaxHeight(max(windowHeight-220, 120)).Children(func() {
				ui.Column(c).Gap(14).Padding(0, 4, 0, 0).Children(func() {
					if p.tab == settingsTabGeneral {
						ui.Field(c, "外观", func() {
							c.Key("settingsAppearance")
							ui.Segmented(c, &mode, "跟随系统", "浅色", "深色").OnChange(func() {
								p.err = ""
								a.setAppearance(appearanceModes[mode])
							})
						}).Description("外观设置保存在保险库里，之后在多台设备间同步时会一起生效。")

						autoLockIdx := slices.Index(autoLockChoices, a.local.AutoLockMinutes)
						if autoLockIdx < 0 {
							autoLockIdx = 0
						}
						ui.Field(c, "自动锁定", func() {
							c.Key("settingsAutoLock")
							ui.Segmented(c, &autoLockIdx, "从不", "5分钟", "15分钟", "30分钟", "1小时").OnChange(func() {
								next := a.local
								next.AutoLockMinutes = autoLockChoices[autoLockIdx]
								if err := a.saveLocal(next); err != nil {
									p.err = err.Error()
								}
							})
						}).Description("闲置超过指定时间后自动关闭会话并锁定保险库。")

						ui.Field(c, "会话恢复", func() {
							c.Key("settingsRestoreSession")
							before := p.restoreSession
							ui.Checkbox(c, &p.restoreSession, "启动时恢复上次打开的标签和窗格")
							if p.restoreSession != before {
								next := a.local
								next.DisableRestoreSession = !p.restoreSession
								if next.DisableRestoreSession {
									next.SavedSession = nil
								}
								if err := a.saveLocal(next); err != nil {
									p.err = err.Error()
								}
							}
						}).Description("退出时记住当前打开的主机和窗格拆分，下次启动时自动恢复（仅保存在本机）。")

						if a.device != nil && a.vault != nil && a.vault.State() != vault.Unprotected {
							ui.Field(c, "记住主密码", func() {
								c.Key("settingsRemember")
								ui.Checkbox(c, &p.rememberDevice, "在这台设备上记住主密码").OnChange(func() {
									a.rememberOnDevice(p.rememberDevice)
									if p.rememberDevice {
										a.toast(ui.Toast{Title: "已在这台设备上记住主密码"})
									} else {
										a.toast(ui.Toast{Title: "已清除在这台设备上记住的主密码"})
									}
								})
							}).Description("关闭后，每次启动 Serval 都需要重新输入主密码。")
						}

						if a.vault != nil && a.vault.State() != vault.Unprotected {
							if !p.changePwOpen {
								ui.Row(c).Children(func() {
									ui.Button(c, "修改主密码…").Key("settingsOpenChangePw").OnClick(func() {
										p.changePwOpen = true
										p.revealChangePw = true
										p.currentPassword = ""
										p.newPassword = ""
										p.confirmPassword = ""
										p.changePwErr = ""
									})
								})
							} else {
								form := ui.Column(c).Key("settingsChangePwForm").Gap(10).Padding(12).Radius(design.RadiusRow).Background(colors.Hover)
								if p.revealChangePw {
									p.revealChangePw = false
									form.ScrollIntoView()
								}
								form.Children(func() {
									ui.Text(c, "修改主密码").FontWeight(600)
									ui.Field(c, "当前主密码", func() {
										c.Key("settingsCurrentPw")
										ui.TextInput(c, &p.currentPassword).Password().AutoFocus()
									})
									ui.Field(c, "新主密码", func() {
										c.Key("settingsNewPw")
										ui.TextInput(c, &p.newPassword).Password()
									}).Description(fmt.Sprintf("至少 %d 个字符。", vault.MinPasswordLength))
									ui.Field(c, "确认新主密码", func() {
										c.Key("settingsConfirmPw")
										ui.TextInput(c, &p.confirmPassword).Password()
									})
									if p.changePwErr != "" {
										ui.Text(c, p.changePwErr).Key("settingsChangePwError").TextColor(colors.Danger).FontSize(design.TextSmall)
									}
									ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
										ui.Button(c, "取消").Key("settingsCancelPw").OnClick(func() {
											p.changePwOpen = false
											p.changePwErr = ""
										})
										label := "保存新密码"
										if p.changePwBusy {
											label = "正在保存…"
										}
										ui.PrimaryButton(c, label).Key("settingsSavePw").Disabled(p.changePwBusy).OnClick(func() {
											a.submitChangePassword()
										})
									})
								})
							}
						}

						ui.Divider(c)
						a.knownHostsSection(c, colors)
						return
					}
					ui.Field(c, "终端字号", func() {
						c.Key("settingsFontSize")
						ui.Segmented(c, &sizeIdx, fontSizeLabels...).OnChange(func() {
							p.err = ""
							a.setTerminalFontSize(fontSizes[sizeIdx])
						})
					})
					ui.Field(c, "终端行高", func() {
						c.Key("settingsLineHeight")
						ui.Segmented(c, &lhIdx, lineHeightLabels...).OnChange(func() {
							p.err = ""
							a.setTerminalLineHeight(lineHeights[lhIdx])
						})
					})
					ui.Field(c, "光标样式", func() {
						c.Key("settingsCursor")
						ui.Segmented(c, &cursorIdx, cursorStyleLabels...).OnChange(func() {
							p.err = ""
							a.setTerminalCursor(cursorStyles[cursorIdx])
						})
					})
					ui.Field(c, "回滚缓冲区", func() {
						c.Key("settingsScrollback")
						ui.Segmented(c, &scrollIdx, scrollbackLabels...).OnChange(func() {
							p.err = ""
							a.setTerminalScrollbackMB(scrollbackMB[scrollIdx])
						})
					}).Description("终端在屏幕上方保留的历史输出。对新打开的终端生效。")
					ui.Field(c, "深色终端配色", func() {
						c.Key("settingsDarkTheme")
						ui.Segmented(c, &darkThemeIdx, popularDarkThemeLabels...).OnChange(func() {
							p.err = ""
							a.setTerminalThemeDark(popularDarkThemes[darkThemeIdx])
						})
					}).Description(themeNote(a.settings.TerminalThemeDark))
					ui.Field(c, "浅色终端配色", func() {
						c.Key("settingsLightTheme")
						ui.Segmented(c, &lightThemeIdx, popularLightThemeLabels...).OnChange(func() {
							p.err = ""
							a.setTerminalThemeLight(popularLightThemes[lightThemeIdx])
						})
					}).Description(themeNote(a.settings.TerminalThemeLight))
				})
			})
			if p.err != "" {
				ui.Text(c, p.err).Key("settingsError").TextColor(t.Danger)
			}
			ui.Row(c).Justify(ui.End).Children(func() {
				ui.Button(c, "完成").Key("settingsClose").OnClick(func() { a.settingsPanel = settingsPanel{} })
			})
		})
	})
	if !p.open {
		a.settingsPanel = settingsPanel{}
	}
}

func (a *app) knownHostsSection(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	known, err := a.knownHostsList()
	ui.Field(c, "已知主机", func() {
		if err != nil {
			ui.Text(c, "读取已知主机失败："+err.Error()).FontSize(design.TextSmall).TextColor(colors.Danger)
			return
		}
		if len(known) == 0 {
			ui.Text(c, "还没有已信任的主机密钥。首次连接服务器并确认指纹后，记录会显示在这里。").
				FontSize(design.TextSmall).TextColor(t.TextMuted)
			return
		}
		ui.Scroll(c).MaxHeight(200).Children(func() {
			ui.Column(c).Gap(6).Children(func() {
				for _, k := range known {
					a.knownHostRow(c, colors, k)
				}
			})
		})
	}).Description("已核对过指纹的服务器保存在加密保险库里。删除后，下次连接会重新核对指纹。")
}

func (a *app) knownHostRow(c *ui.Context, colors design.Colors, k knownHost) {
	t := c.Theme()
	host := k.Host
	ui.Row(c).Key("known:"+host).Padding(8, 10).Gap(10).AlignItems(ui.Center).
		Radius(design.RadiusRow).Background(colors.Chip).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, host).FontWeight(500).SingleLine().MinWidth(0).Shrink(1)
				if !k.Added.IsZero() {
					ui.Text(c, k.Added.Local().Format("2006-01-02 15:04")).
						FontSize(design.TextSmall).TextColor(colors.TextFaint).FontFeatures("tnum").Shrink(0)
				}
			})
			for _, sum := range k.keySummaries() {
				ui.Text(c, sum).Font(design.FontTerminal).FontSize(design.TextSmall).
					TextColor(t.TextMuted).SingleLine()
			}
		})
		ui.Button(c, "删除").Key("knownDelete:" + host).OnClick(func() {
			a.deleteKnownHost(host)
		})
	})
}

// themeNote 是终端配色字段的说明。保存的配色在本机找不到时（例如另一台设备上的新版本选了
// 这个版本没有的主题）要说清楚正在用默认配色，而不是静默地没有效果。
func themeNote(name string) string {
	if name == "" {
		return ""
	}
	if _, err := terminal.GhosttyTheme(name); err != nil {
		return fmt.Sprintf("找不到配色 %s，正在使用默认配色。", name)
	}
	return ""
}
