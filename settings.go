package main

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"
	"time"

	"serval/core/ai"
	"serval/core/syncer"
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

	// AI 助手设置（随加密保险库同步）
	AIBaseURL string `json:"aiBaseURL,omitempty"`
	AIModel   string `json:"aiModel,omitempty"`
	AIKey     string `json:"aiKey,omitempty"`
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
	// DefaultMonitor 为真时，新建 SSH 会话默认开启顶栏服务器监控。
	DefaultMonitor bool `json:"defaultMonitor,omitempty"`
	// HostMonitor 记录各台主机上次的监控开关状态。
	HostMonitor map[string]bool `json:"hostMonitor,omitempty"`
	// DisableDistroProbe 为真时，不在首次连接时执行发行版探测命令。
	DisableDistroProbe bool `json:"disableDistroProbe,omitempty"`
	// SyncKind 为同步提供商类型："webdav"、"s3"、空（根据 SyncURL 自动识别）
	SyncKind string `json:"syncKind,omitempty"`
	// SyncURL 是 WebDAV 或 S3 上 vault.json 的统一地址。密码/私钥在系统安全存储。
	SyncURL string `json:"syncURL,omitempty"`
	// SyncUser 是 WebDAV 账号或 S3 的 AccessKeyId。密码在系统安全存储的 sync-password。
	SyncUser string `json:"syncUser,omitempty"`
	// SyncSubDir 是远程根目录（如 serval、cc-switch-sync 等，默认为 serval）。
	SyncSubDir string `json:"syncSubDir,omitempty"`
	// SyncVendor 是预设服务商名称（如 "坚果云"、"AWS S3"、"Cloudflare R2" 等）。
	SyncVendor string `json:"syncVendor,omitempty"`
	// SyncOn 为真时，解锁后、本地修改后和每隔几分钟会同步。
	SyncOn bool `json:"syncOn,omitempty"`
	// SyncDir 是本机的同步文件夹（iCloud Drive、Dropbox、Syncthing）。只存路径，不存秘密。
	SyncDir string `json:"syncDir,omitempty"`
	// LegacyAIKey 仅用于将旧版本明文保存在 device.json 的密钥迁移进加密保险库，迁移后即清空。
	LegacyAIKey string `json:"aiKey,omitempty"`
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
	settingsTabAI
)

// settingsPanel 是“设置”浮层面板的状态。
type settingsPanel struct {
	open, fresh, active bool
	tab                 settingsTab
	err                 string

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
	syncKind     string // "WebDAV" 或 "S3 兼容存储"
	syncVendor   string // 厂商预设
	syncSubDir   string // 远程根目录，如 serval
	syncOn       bool
	probeBusy    bool
	probeOK      bool
	probeMsg     string
	askSyncMerge bool
	allowMerge   bool

	// AI 助手页草稿
	aiBaseURL  string
	aiModel    string
	aiKey      string
	aiTestBusy bool
	aiTestOK   bool
	aiTestMsg  string

	// WebDAV 专用字段
	webdavServer   string
	webdavUser     string
	webdavPassword string

	// S3 专用字段
	s3Region   string
	s3Bucket   string
	s3AccessID string
	s3Secret   string
	s3Endpoint string

	// 兼容老字段
	syncURL      string
	syncUser     string
	syncPassword string
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
	p := settingsPanel{
		open:           true,
		fresh:          true,
		rememberDevice: a.isRememberedOnDevice(),
		restoreSession: !a.local.DisableRestoreSession,
		sessionLog:     a.local.SessionLog,
		syncURL:        a.local.SyncURL,
		syncUser:       a.local.SyncUser,
		syncOn:         a.local.SyncOn,
		syncSubDir:     a.local.SyncSubDir,
		syncVendor:     a.local.SyncVendor,
	}
	if p.syncSubDir == "" {
		p.syncSubDir = "serval"
	}
	if syncer.IsS3URL(p.syncURL) {
		p.syncKind = "S3 兼容存储"
		target, err := syncer.ParseS3URL(p.syncURL)
		if err == nil {
			p.s3Bucket = target.Bucket
			p.s3Region = target.Region
			p.s3Endpoint = target.Endpoint
			p.s3AccessID = a.local.SyncUser
			keyDir := path.Dir(target.Key)
			if keyDir != "." && keyDir != "/" && keyDir != "" {
				p.syncSubDir = keyDir
			}
		}
		if p.syncVendor == "" {
			p.syncVendor = "AWS S3"
		}
	} else {
		p.syncKind = "WebDAV"
		if p.syncURL != "" {
			if a.local.SyncSubDir != "" {
				p.syncSubDir = a.local.SyncSubDir
				server := strings.TrimSuffix(p.syncURL, "/"+p.syncSubDir+"/vault.json")
				p.webdavServer = strings.TrimSuffix(server, "/vault.json")
			} else {
				p.syncSubDir = ""
				p.webdavServer = strings.TrimSuffix(p.syncURL, "/vault.json")
			}
		}
		p.webdavUser = a.local.SyncUser
		if p.syncVendor == "" {
			p.syncVendor = "自定义"
		}
	}
	p.aiBaseURL = a.settings.AIBaseURL
	if p.aiBaseURL == "" {
		p.aiBaseURL = "https://api.openai.com/v1"
	}
	p.aiModel = a.settings.AIModel
	if p.aiModel == "" {
		p.aiModel = "gpt-4o-mini"
	}
	p.aiKey = a.getAIKey()
	a.settingsPanel = p
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
	if a.setup.open || a.restore.open {
		return
	}
	if !p.open {
		if p.active {
			a.closeSettings()
		}
		return
	}
	p.active = true
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
				ui.Segmented(c, &tabIdx, locales.SetGeneral(), locales.SetTerminal(), "同步", "AI").OnChange(func() {
					if p.tab == settingsTabAI && settingsTab(tabIdx) != settingsTabAI {
						a.commitAISettings()
					}
					p.tab = settingsTab(tabIdx)
				})
				if int(p.tab) != tabIdx {
					if p.tab == settingsTabAI {
						a.commitAISettings()
					}
					p.tab = settingsTab(tabIdx)
				}
			})
			ui.Scroll(c).Key("settingsScroll").MaxHeight(max(windowHeight-165, 120)).Children(func() {
				ui.Column(c).Gap(11).Padding(0, 4, 0, 0).Children(func() {
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
							ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
								c.Key("settingsSessionLog")
								ui.Checkbox(c, &p.sessionLog, "记录终端输出").OnChange(func() {
									a.setSessionLog(p.sessionLog)
								})
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

						ui.Field(c, "服务器监控", func() {
							defMon := a.local.DefaultMonitor
							c.Key("settingsDefaultMonitor")
							ui.Checkbox(c, &defMon, "新建 SSH 会话时默认开启顶栏监控").OnChange(func() {
								next := a.local
								next.DefaultMonitor = defMon
								if err := a.saveLocal(next); err != nil {
									p.err = err.Error()
								}
							})
						}).Description("开启后，每次连接 SSH 服务器都会自动探测并展示 CPU、内存等轻量状态。仅保存在本机。")

						ui.Field(c, "系统识别", func() {
							probeOn := !a.local.DisableDistroProbe
							c.Key("settingsDistroProbe")
							ui.Checkbox(c, &probeOn, "首次连接时自动识别操作系统发行版").OnChange(func() {
								next := a.local
								next.DisableDistroProbe = !probeOn
								if err := a.saveLocal(next); err != nil {
									p.err = err.Error()
								}
							})
						}).Description("连接后会在远端执行一次只读命令读取 /etc/os-release。对审计严格的服务器可关闭此项，改在主机编辑里手动指定。")

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
					if p.tab == settingsTabAI {
						a.aiSettingsSection(c, colors)
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
				ui.Button(c, locales.Done()).Key("settingsClose").OnClick(a.closeSettings)
			})
		})
	})
	if !p.open {
		a.closeSettings()
	}
}

func (a *app) closeSettings() {
	p := &a.settingsPanel
	a.commitAISettings()
	if a.setup.open {
		p.open = true
		p.active = true
		prevThen := a.setup.then
		a.setup.then = func() {
			if prevThen != nil {
				prevThen()
			}
			if a.settingsPanel.err == "" {
				a.settingsPanel = settingsPanel{}
			}
		}
		return
	}
	a.settingsPanel = settingsPanel{}
}

var (
	syncKindOptions = []string{"WebDAV", "S3 兼容存储"}
	webdavVendors   = []string{"自定义", "坚果云", "Nextcloud", "群晖 NAS"}
	s3Vendors       = []string{"AWS S3", "MinIO", "Cloudflare R2", "阿里云 OSS", "腾讯云 COS", "华为云 OBS", "S3 兼容服务（自定义）"}
)

// syncSettingsSection 是设置里的“同步”页。
func (a *app) syncSettingsSection(c *ui.Context, colors design.Colors) {
	p := &a.settingsPanel
	if p.syncKind == "" {
		p.syncKind = "WebDAV"
	}

	ui.Field(c, "同步方式", func() {
		c.Key("syncKind")
		ui.Select(c, &p.syncKind, syncKindOptions)
	})

	if p.syncKind == "WebDAV" {
		if !slices.Contains(webdavVendors, p.syncVendor) {
			p.syncVendor = "自定义"
		}
		ui.Field(c, "服务商", func() {
			c.Key("webdavVendor")
			ui.Select(c, &p.syncVendor, webdavVendors)
		})
		// 切换服务商自动辅助填入提示或常用地址前缀
		placeholder := "https://example.com/dav/"
		switch p.syncVendor {
		case "坚果云":
			placeholder = "https://dav.jianguoyun.com/dav/"
			if p.webdavServer == "" {
				p.webdavServer = "https://dav.jianguoyun.com/dav/"
			}
		case "Nextcloud":
			placeholder = "https://your-domain.com/remote.php/dav/files/username/"
		case "群晖 NAS":
			placeholder = "https://nas-ip:5006/home/"
		}

		ui.Field(c, "WebDAV 服务器地址", func() {
			c.Key("webdavServer")
			ui.TextInput(c, &p.webdavServer).Placeholder(placeholder)
		}).Description("填服务器基础地址，无需手动写 /vault.json。")

		ui.Field(c, "WebDAV 账户", func() {
			c.Key("webdavUser")
			ui.TextInput(c, &p.webdavUser).Placeholder("账号或邮箱")
		})

		ui.Field(c, "WebDAV 密码", func() {
			c.Key("webdavPassword")
			ui.TextInput(c, &p.webdavPassword).Password().Placeholder("应用密码（坚果云请使用“第三方应用密码”）")
		}).Description("密码保存在系统安全存储中，不会以明文保存。")

	} else {
		// S3 兼容存储
		if !slices.Contains(s3Vendors, p.syncVendor) {
			p.syncVendor = "AWS S3"
		}
		ui.Field(c, "服务商", func() {
			c.Key("s3Vendor")
			ui.Select(c, &p.syncVendor, s3Vendors)
		})

		ui.Text(c, "使用 IAM Access Key，需要 s3:PutObject/GetObject/HeadObject 权限。").
			FontSize(design.TextSmall).TextColor(c.Theme().TextMuted)

		endpointPlaceholder := "例如 https://s3-compatible.example.com"
		regionPlaceholder := "例如 us-east-1"
		switch p.syncVendor {
		case "AWS S3":
			endpointPlaceholder = "AWS 可留空"
			if p.s3Region == "" {
				p.s3Region = "us-east-1"
			}
		case "Cloudflare R2":
			endpointPlaceholder = "https://<account_id>.r2.cloudflarestorage.com"
			if p.s3Region == "" {
				p.s3Region = "auto"
			}
		case "MinIO":
			endpointPlaceholder = "例如 http://127.0.0.1:9000"
		case "阿里云 OSS":
			endpointPlaceholder = "例如 https://oss-cn-hangzhou.aliyuncs.com"
		case "腾讯云 COS":
			endpointPlaceholder = "例如 https://cos.ap-guangzhou.myqcloud.com"
		case "华为云 OBS":
			endpointPlaceholder = "例如 https://obs.cn-north-4.myhuaweicloud.com"
		}

		ui.Field(c, "区域 (Region)", func() {
			c.Key("s3Region")
			ui.TextInput(c, &p.s3Region).Placeholder(regionPlaceholder)
		})

		ui.Field(c, "存储桶 (Bucket)", func() {
			c.Key("s3Bucket")
			ui.TextInput(c, &p.s3Bucket).Placeholder("存储桶名称")
		})

		ui.Field(c, "Access Key ID", func() {
			c.Key("s3AccessID")
			ui.TextInput(c, &p.s3AccessID).Placeholder("访问密钥 ID")
		})

		ui.Field(c, "Secret Access Key", func() {
			c.Key("s3Secret")
			ui.TextInput(c, &p.s3Secret).Password().Placeholder("留空则保持现有密钥")
		}).Description("密钥保存在系统安全存储中，不会以明文保存。")

		ui.Field(c, "Endpoint 终端节点", func() {
			c.Key("s3Endpoint")
			ui.TextInput(c, &p.s3Endpoint).Placeholder(endpointPlaceholder)
		}).Description("多数 S3 兼容服务需填写（AWS S3 可留空）。")
	}

	ui.Field(c, "远程根目录", func() {
		c.Key("syncSubDir")
		ui.TextInput(c, &p.syncSubDir).Placeholder("默认 serval")
	}).Description("数据将保存在该目录下（例如 serval/vault.json），便于多应用分类管理。")

	c.Key("syncEnabled")
	ui.Checkbox(c, &p.syncOn, "自动同步")
	hint := "开启后，每次修改或启动解锁时都会自动合并与同步。"
	if a.vault != nil && a.vault.Empty() {
		hint = "这台设备还没有数据。要下载另一台设备上的保险库，用侧栏的“从同步恢复”。"
	}
	ui.Text(c, hint).FontSize(design.TextSmall).TextColor(c.Theme().TextMuted)

	ui.Row(c).Gap(8).Children(func() {
		label := "测试连接"
		if p.probeBusy {
			label = "正在测试连接"
		}
		ui.Button(c, label).Key("syncTest").Disabled(p.probeBusy).OnClick(a.testSyncFromPanel)
		ui.PrimaryButton(c, "保存配置").Key("syncSave").OnClick(a.saveSyncSettings)
		if a.local.SyncOn || a.local.SyncURL != "" {
			ui.Button(c, "清除配置").Key("syncClear").OnClick(func() {
				p.syncOn = false
				p.syncURL = ""
				p.syncUser = ""
				p.syncPassword = ""
				p.webdavServer = ""
				p.webdavUser = ""
				p.webdavPassword = ""
				p.s3Bucket = ""
				p.s3AccessID = ""
				p.s3Secret = ""
				p.s3Endpoint = ""
				a.configureSync("", "", "", false)
				a.toast(ui.Toast{Title: "已清除同步配置"})
			})
		}
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
		if a.syncDifferentVault && !a.vault.Empty() {
			ui.Button(c, "用本机覆盖远端").Key("syncOverwriteBtn").OnClick(func() {
				a.syncOverwriteAsk = true
			})
		}
	}

	if a.syncConfigured() {
		ui.Row(c).Gap(10).Children(func() {
			ui.PrimaryButton(c, "上传到云端").Key("syncPush").Disabled(a.syncing).OnClick(func() {
				a.syncNow(true)
			})
			ui.Button(c, "从云端下载").Key("syncPull").Disabled(a.syncing).OnClick(func() {
				a.syncNow(true)
			})
		})
	}

	ui.Divider(c)

	// 本地同步文件夹（独立解耦，用于本地离线/云盘同步备份）
	dirLabel := "未选择文件夹"
	if a.local.SyncDir != "" {
		dirLabel = a.local.SyncDir
	}
	ui.Field(c, "本地同步文件夹", func() {
		ui.Text(c, dirLabel).Key("syncDirPath").FontSize(design.TextSmall).TextColor(c.Theme().TextMuted).SingleLine()
	}).Description("将加密的保险库镜像保存至 iCloud Drive、Dropbox 或 Syncthing 文件夹。与云同步独立运作。")
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

func (a *app) commitAISettings() {
	p := &a.settingsPanel
	baseURL := strings.TrimSpace(p.aiBaseURL)
	if baseURL == "" {
		baseURL = ai.DefaultBaseURL
	}
	model := strings.TrimSpace(p.aiModel)
	if model == "" {
		model = ai.DefaultModel
	}
	key := strings.TrimSpace(p.aiKey)

	curBaseURL := a.settings.AIBaseURL
	if curBaseURL == "" {
		curBaseURL = ai.DefaultBaseURL
	}
	curModel := a.settings.AIModel
	if curModel == "" {
		curModel = ai.DefaultModel
	}

	if baseURL != curBaseURL || model != curModel {
		next := a.settings
		next.AIBaseURL = baseURL
		next.AIModel = model
		if err := a.saveSettings(next); err != nil {
			p.err = err.Error()
		}
	}
	if key != a.getAIKey() {
		a.setAIKey(key)
	}
}

func (a *app) aiSettingsSection(c *ui.Context, colors design.Colors) {
	p := &a.settingsPanel
	t := c.Theme()

	ui.Field(c, "API 端点 (Base URL)", func() {
		c.Key("setAIBaseURL")
		ui.TextInput(c, &p.aiBaseURL).Placeholder(ai.DefaultBaseURL).OnSubmit(func() {
			a.commitAISettings()
		})
	}).Description("兼容 OpenAI 标准端点，如 DeepSeek、Kimi、通义千问或本地 Ollama (http://localhost:11434/v1)")

	ui.Field(c, "模型名称 (Model)", func() {
		c.Key("setAIModel")
		ui.TextInput(c, &p.aiModel).Placeholder(ai.DefaultModel).OnSubmit(func() {
			a.commitAISettings()
		})
	}).Description("例如：gpt-4o-mini, deepseek-chat, qwen-turbo 等")

	ui.Field(c, "API Key (访问密钥)", func() {
		c.Key("setAIKey")
		ui.TextInput(c, &p.aiKey).Password().Placeholder("sk-...").OnSubmit(func() {
			a.commitAISettings()
		})
	}).Description("密钥会加密保存进保险库（随同步），修改后在关闭设置或回车时自动保存。")

	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		label := "测试连接"
		if p.aiTestBusy {
			label = "正在测试连接"
		}
		ui.Button(c, label).Key("aiTestBtn").Disabled(p.aiTestBusy).OnClick(func() {
			a.commitAISettings()
			p.aiTestBusy = true
			p.aiTestMsg = ""
			client := ai.NewClient(ai.Config{
				BaseURL: p.aiBaseURL,
				Model:   p.aiModel,
				APIKey:  p.aiKey,
				Timeout: 10 * time.Second,
			})
			go func() {
				err := client.Probe(context.Background())
				a.post(func() {
					p.aiTestBusy = false
					if err != nil {
						p.aiTestOK = false
						p.aiTestMsg = "连接测试失败：" + friendlyAIError(err)
					} else {
						p.aiTestOK = true
						p.aiTestMsg = "连接测试成功，AI 服务响应正常。"
					}
				})
			}()
		})
	})

	if p.aiTestMsg != "" {
		col := t.Accent
		if !p.aiTestOK {
			col = colors.Danger
		}
		ui.Text(c, p.aiTestMsg).Key("aiTestResult").FontSize(design.TextSmall).TextColor(col)
	}
}
