package main

import (
	"errors"
	"slices"

	"serval/core/vault"
	"serval/design"
	"serval/locales"

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

// cursorStyleLabels 在渲染时取文案，避免包初始化时钉死语言。
func cursorStyleLabels() []string {
	return []string{locales.SetCursorBlock(), locales.SetCursorBar(), locales.SetCursorUnderline()}
}

// 回滚缓冲区按容量设置：终端插件的 Scrollback 单位是字节，一行占多少字节取决于内容，
// 按“行数”标注会误导。0 表示插件默认的 10 MB。
var scrollbackMB = []int{5, 10, 50, 100}
var scrollbackLabels = []string{"5 MB", "10 MB", "50 MB", "100 MB"}

// 预设配色必须是 terminal.GhosttyThemes() 里存在的名字（TestTerminalThemePresetsExist 保证），
// 找不到时终端会退回默认配色。空字符串表示 Serval 自己的默认配色。
var popularDarkThemes = []string{"", "Dracula", "Nord", "TokyoNight", "Catppuccin Mocha"}

// popularDarkThemeLabels 在渲染时取文案。主题专名不翻译。
func popularDarkThemeLabels() []string {
	return []string{locales.SetThemeDarkDefault(), "Dracula", "Nord", "Tokyo Night", "Catppuccin Mocha"}
}

var popularLightThemes = []string{"", "Catppuccin Latte", "GitHub Light Default", "Atom One Light"}

// popularLightThemeLabels 在渲染时取文案。主题专名不翻译。
func popularLightThemeLabels() []string {
	return []string{locales.SetThemeLightDefault(), "Catppuccin Latte", "GitHub", "One Light"}
}

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
	// SessionLog 为真时把远程终端输出写到 SessionLogDir。默认关闭。路径只属于这台机器。
	SessionLog    bool   `json:"sessionLog,omitempty"`
	SessionLogDir string `json:"sessionLogDir,omitempty"`
	// SavedSession 是本机退出时保存的标签与窗格布局。
	SavedSession *savedSession `json:"savedSession,omitempty"`
	// CollapsedGroups 是侧栏里折叠起来的分组名。只记在本机，不随保险库同步。
	CollapsedGroups []string `json:"collapsedGroups,omitempty"`
	// SyncURL 是 WebDAV 上 vault.json 的地址，或它所在的目录。密码不在这里。
	SyncURL string `json:"syncURL,omitempty"`
	// SyncUser 是 WebDAV 账号。密码在系统安全存储的 sync-password。
	SyncUser string `json:"syncUser,omitempty"`
	// SyncOn 为真时，解锁后、本地修改后和每隔几分钟会同步。
	SyncOn bool `json:"syncOn,omitempty"`
	// SyncDir 是本机的同步文件夹（iCloud Drive、Dropbox、Syncthing）。只存路径，不存秘密。
	SyncDir string `json:"syncDir,omitempty"`
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
	settingsTabSync
)

// settingsPanel 是“设置”浮层面板的状态。
type settingsPanel struct {
	open, fresh bool
	tab         settingsTab
	err         string

	rememberDevice bool
	sessionLog     bool

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

	// 同步页的草稿。密码只在点保存时写入系统安全存储，空着表示保留已有密码。
	syncURL, syncUser, syncPassword string
	syncOn                          bool
	probeBusy                       bool
	probeOK                         bool
	probeMsg                        string
	// askSyncMerge 在已有数据的设备第一次开启同步时弹出合并说明。allowMerge 是用户确认之后。
	askSyncMerge bool
	allowMerge   bool
}

// restorePanel 是空设备“从同步恢复”的面板。
type restorePanel struct {
	open, fresh bool
	url         string
	user        string
	password    string
	busy        bool
	err         string
}

// openSettings 打开设置面板。
func (a *app) openSettings() {
	a.settingsPanel = settingsPanel{
		open:           true,
		fresh:          true,
		rememberDevice: a.isRememberedOnDevice(),
		restoreSession: !a.local.DisableRestoreSession,
		sessionLog:     a.local.SessionLog,
		syncURL:        a.local.SyncURL,
		syncUser:       a.local.SyncUser,
		syncOn:         a.local.SyncOn,
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
		a.toast(ui.Toast{Title: locales.SetSaveFailed(), Description: err.Error(), Type: "error"})
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
		a.notice = locales.SetRecentSaveFailed(err.Error())
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
		a.notice = locales.SetRecentSaveFailed(err.Error())
	}
}

// submitChangePassword 在后台修改主密码：要做两次 Argon2id，不能在主线程上执行。
func (a *app) submitChangePassword() {
	p := &a.settingsPanel
	if p.changePwBusy {
		return
	}
	if p.newPassword != p.confirmPassword {
		p.changePwErr = locales.SetPasswordMismatch()
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
				p.changePwErr = locales.SetPasswordWrong()
			case err != nil:
				p.changePwErr = err.Error()
			default:
				p.changePwOpen = false
				p.currentPassword, p.newPassword, p.confirmPassword, p.changePwErr = "", "", "", ""
				a.noteVaultChanged()
				a.toast(ui.Toast{Title: locales.SetPasswordChanged()})
			}
		})
	}()
}

// settingsDialog 是设置面板。设置主密码面板打开时让位。
func (a *app) settingsDialog(c *ui.Context, colors design.Colors) {
	p := &a.settingsPanel
	if !p.open || a.setup.open || a.restore.open {
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
				ui.Text(c, locales.Settings()).FontSize(design.TextHeading).FontWeight(600).Grow(1)
				tabIdx := int(p.tab)
				c.Key("settingsTab")
				ui.Segmented(c, &tabIdx, locales.SetGeneral(), locales.SetTerminal(), "同步").OnChange(func() {
					p.tab = settingsTab(tabIdx)
				})
			})
			ui.Scroll(c).Key("settingsScroll").MaxHeight(max(windowHeight-220, 120)).Children(func() {
				ui.Column(c).Gap(14).Padding(0, 4, 0, 0).Children(func() {
					if p.tab == settingsTabGeneral {
						ui.Field(c, locales.SetAppearance(), func() {
							c.Key("settingsAppearance")
							ui.Segmented(c, &mode, locales.FollowSystem(), locales.AppearanceLight(), locales.AppearanceDark()).OnChange(func() {
								p.err = ""
								a.setAppearance(appearanceModes[mode])
							})
						}).Description(locales.SetAppearanceHint())

						autoLockIdx := slices.Index(autoLockChoices, a.local.AutoLockMinutes)
						if autoLockIdx < 0 {
							autoLockIdx = 0
						}
						ui.Field(c, locales.SetAutoLock(), func() {
							c.Key("settingsAutoLock")
							ui.Segmented(c, &autoLockIdx, locales.SetNever(), locales.SetLock5Min(), locales.SetLock15Min(), locales.SetLock30Min(), locales.SetLock1Hour()).OnChange(func() {
								next := a.local
								next.AutoLockMinutes = autoLockChoices[autoLockIdx]
								if err := a.saveLocal(next); err != nil {
									p.err = err.Error()
								}
							})
						}).Description(locales.SetAutoLockHint())

						ui.Field(c, locales.SetSessionRestore(), func() {
							c.Key("settingsRestoreSession")
							before := p.restoreSession
							ui.Checkbox(c, &p.restoreSession, locales.SetRestoreOnLaunch())
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
						}).Description(locales.SetRestoreHint())

						ui.Field(c, "会话日志", func() {
							c.Key("settingsSessionLog")
							ui.Checkbox(c, &p.sessionLog, "记录终端输出").OnChange(func() {
								a.setSessionLog(p.sessionLog)
							})
							ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
								ui.Button(c, "选择日志文件夹").Key("settingsLogDir").OnClick(func() {
									a.pickSessionLogDir(false)
								})
								pathText := a.local.SessionLogDir
								if pathText == "" {
									pathText = "还没有选择"
								}
								ui.Text(c, pathText).TextColor(t.TextMuted).FontSize(design.TextSmall).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
							})
						}).Description("默认关闭。只记录远程会话的输出，存成纯文本并去掉颜色和控制字符。文件在所选文件夹里，不进保险库。本机终端不记录。")

						if a.device != nil && a.vault != nil && a.vault.State() != vault.Unprotected {
							ui.Field(c, locales.SetRememberMasterPassword(), func() {
								c.Key("settingsRemember")
								ui.Checkbox(c, &p.rememberDevice, locales.SetRememberOnDevice()).OnChange(func() {
									a.rememberOnDevice(p.rememberDevice)
									if p.rememberDevice {
										a.toast(ui.Toast{Title: locales.SetRemembered()})
									} else {
										a.toast(ui.Toast{Title: locales.SetRememberCleared()})
									}
								})
							}).Description(locales.SetRememberHint())
						}

						if a.vault != nil && a.vault.State() != vault.Unprotected {
							if !p.changePwOpen {
								ui.Row(c).Children(func() {
									ui.Button(c, locales.SetChangeMasterPassword()).Key("settingsOpenChangePw").OnClick(func() {
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
									ui.Text(c, locales.SetChangeMasterPasswordTitle()).FontWeight(600)
									ui.Field(c, locales.SetCurrentMasterPassword(), func() {
										c.Key("settingsCurrentPw")
										ui.TextInput(c, &p.currentPassword).Password().AutoFocus()
									})
									ui.Field(c, locales.SetNewMasterPassword(), func() {
										c.Key("settingsNewPw")
										ui.TextInput(c, &p.newPassword).Password()
									}).Description(locales.PasswordMin(vault.MinPasswordLength))
									ui.Field(c, locales.SetConfirmMasterPassword(), func() {
										c.Key("settingsConfirmPw")
										ui.TextInput(c, &p.confirmPassword).Password()
									})
									if p.changePwErr != "" {
										ui.Text(c, p.changePwErr).Key("settingsChangePwError").TextColor(colors.Danger).FontSize(design.TextSmall)
									}
									ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
										ui.Button(c, locales.Cancel()).Key("settingsCancelPw").OnClick(func() {
											p.changePwOpen = false
											p.changePwErr = ""
										})
										label := locales.SetSaveNewPassword()
										if p.changePwBusy {
											label = locales.SetSaving()
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
					if p.tab == settingsTabSync {
						a.syncSettingsSection(c, colors)
						return
					}
					ui.Field(c, locales.SetFontSize(), func() {
						c.Key("settingsFontSize")
						ui.Segmented(c, &sizeIdx, fontSizeLabels...).OnChange(func() {
							p.err = ""
							a.setTerminalFontSize(fontSizes[sizeIdx])
						})
					})
					ui.Field(c, locales.SetLineHeight(), func() {
						c.Key("settingsLineHeight")
						ui.Segmented(c, &lhIdx, lineHeightLabels...).OnChange(func() {
							p.err = ""
							a.setTerminalLineHeight(lineHeights[lhIdx])
						})
					})
					ui.Field(c, locales.SetCursor(), func() {
						c.Key("settingsCursor")
						ui.Segmented(c, &cursorIdx, cursorStyleLabels()...).OnChange(func() {
							p.err = ""
							a.setTerminalCursor(cursorStyles[cursorIdx])
						})
					})
					ui.Field(c, locales.SetScrollback(), func() {
						c.Key("settingsScrollback")
						ui.Segmented(c, &scrollIdx, scrollbackLabels...).OnChange(func() {
							p.err = ""
							a.setTerminalScrollbackMB(scrollbackMB[scrollIdx])
						})
					}).Description(locales.SetScrollbackHint())
					ui.Field(c, locales.SetDarkTheme(), func() {
						c.Key("settingsDarkTheme")
						ui.Segmented(c, &darkThemeIdx, popularDarkThemeLabels()...).OnChange(func() {
							p.err = ""
							a.setTerminalThemeDark(popularDarkThemes[darkThemeIdx])
						})
					}).Description(themeNote(a.settings.TerminalThemeDark))
					ui.Field(c, locales.SetLightTheme(), func() {
						c.Key("settingsLightTheme")
						ui.Segmented(c, &lightThemeIdx, popularLightThemeLabels()...).OnChange(func() {
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
				ui.Button(c, locales.Done()).Key("settingsClose").OnClick(func() { a.settingsPanel = settingsPanel{} })
			})
		})
	})
	if !p.open {
		a.settingsPanel = settingsPanel{}
	}
}

// syncSettingsSection 是设置里的“同步”页。地址和账号写在本机，密码只在保存时进系统安全存储。
func (a *app) syncSettingsSection(c *ui.Context, colors design.Colors) {
	p := &a.settingsPanel
	ui.Field(c, "服务地址", func() {
		c.Key("syncURL")
		ui.TextInput(c, &p.syncURL).Placeholder("https://example.com/remote/")
	}).Description("填目录或 vault.json 的地址。账号和密码不要写在地址里。")
	ui.Field(c, "账号", func() {
		c.Key("syncUser")
		ui.TextInput(c, &p.syncUser).Placeholder("可选")
	})
	ui.Field(c, "同步密码", func() {
		c.Key("syncPassword")
		ui.TextInput(c, &p.syncPassword).Password().Placeholder("留空则保留已保存的密码")
	}).Description("密码只放在这台设备的系统安全存储里，不会写进保险库或本机设置文件。")
	c.Key("syncEnabled")
	ui.Checkbox(c, &p.syncOn, "启用同步")
	hint := "开启后，解锁、本地修改和每隔几分钟都会与服务器对齐。"
	if a.vault != nil && a.vault.Empty() {
		hint = "这台设备还没有数据。要下载另一台设备上的保险库，用侧栏的“从同步恢复”。"
	}
	ui.Text(c, hint).FontSize(design.TextSmall).TextColor(c.Theme().TextMuted)
	ui.Divider(c)
	dirLabel := "未选择文件夹"
	if a.local.SyncDir != "" {
		dirLabel = a.local.SyncDir
	}
	ui.Field(c, "同步文件夹", func() {
		ui.Text(c, dirLabel).Key("syncDirPath").FontSize(design.TextSmall).TextColor(c.Theme().TextMuted).SingleLine()
	}).Description("把保险库副本放进 iCloud Drive、Dropbox 或 Syncthing 的文件夹。文件仍然是加密的。")
	ui.Row(c).Gap(8).Children(func() {
		ui.Button(c, "选择同步文件夹").Key("syncDirChoose").OnClick(a.chooseSyncDir)
		if a.local.SyncDir != "" {
			ui.Button(c, "停止使用此文件夹").Key("syncDirStop").OnClick(func() {
				if err := a.setSyncDir(""); err != nil {
					p.err = err.Error()
				}
			})
		}
	})
	ui.Row(c).Gap(8).Children(func() {
		label := "测试连接"
		if p.probeBusy {
			label = "正在测试连接"
		}
		ui.Button(c, label).Key("syncTest").Disabled(p.probeBusy).OnClick(a.testSyncFromPanel)
		ui.PrimaryButton(c, "保存同步设置").Key("syncSave").OnClick(a.saveSyncSettings)
	})
	if p.probeMsg != "" {
		col := c.Theme().TextMuted
		if !p.probeOK {
			col = colors.Danger
		}
		ui.Text(c, p.probeMsg).Key("syncProbe").FontSize(design.TextSmall).TextColor(col).Selectable()
	}
	switch {
	case a.syncing:
		ui.Text(c, "正在同步…").Key("syncProgress").FontSize(design.TextSmall).TextColor(c.Theme().TextMuted)
	case !a.lastSync.IsZero():
		ui.Text(c, "上次同步 "+formatSyncTime(a.lastSync)).Key("syncLast").FontSize(design.TextSmall).TextColor(c.Theme().TextMuted)
	}
	if a.syncErr != "" {
		ui.Text(c, a.syncErr).Key("syncError").FontSize(design.TextSmall).TextColor(colors.Danger).Selectable()
	}
}

func (a *app) knownHostsSection(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	known, err := a.knownHostsList()
	ui.Field(c, locales.KnownHosts(), func() {
		if err != nil {
			ui.Text(c, locales.SetKnownHostsLoadFailed(err.Error())).FontSize(design.TextSmall).TextColor(colors.Danger)
			return
		}
		if len(known) == 0 {
			ui.Text(c, locales.SetKnownHostsEmpty()).
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
	}).Description(locales.SetKnownHostsHint())
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
		ui.Button(c, locales.Delete()).Key("knownDelete:" + host).OnClick(func() {
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
		return locales.SetThemeMissing(name)
	}
	return ""
}
