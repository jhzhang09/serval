package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"serval/core/ai"
	"serval/core/hosts"
	"serval/core/keys"
	"serval/core/syncer"
	"serval/core/vault"
	"serval/design"
	"serval/locales"

	"github.com/egoist/mygo/plugins/loading"
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

var rightClickModes = []string{"", "paste", "copy_or_paste"}

func rightClickModeLabels() []string {
	return []string{"右键菜单", "直接粘贴", "有选中则复制/否则粘贴"}
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
	TerminalCopyOnSelect bool   `json:"terminalCopyOnSelect,omitempty"`
	TerminalRightClick   string `json:"terminalRightClick,omitempty"`

	// AI 助手设置（随加密保险库同步）
	AIBaseURL       string      `json:"aiBaseURL,omitempty"`
	AIModel         string      `json:"aiModel,omitempty"`
	AIKey           string      `json:"aiKey,omitempty"`
	AIProfiles      []aiProfile `json:"aiProfiles,omitempty"`
	ActiveAIProfile string      `json:"activeAIProfile,omitempty"`

	// 侧栏空自定义分组（随保险库同步）
	CustomGroups []string `json:"customGroups,omitempty"`
}

// aiProfile 保存单个 AI 服务商与模型配置，密钥随保险库加密存储。
type aiProfile struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	BaseURL  string   `json:"baseURL"`
	Model    string   `json:"model"`
	APIKey   string   `json:"apiKey,omitempty"`
	Models   []string `json:"models,omitempty"`
	Disabled bool     `json:"disabled,omitempty"`
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
	// SidebarWidth 是本机侧栏宽度（DIP），0 表示使用默认值 240。
	SidebarWidth float32 `json:"sidebarWidth,omitempty"`
	// FilesWidth 是本机文件抽屉宽度（DIP），0 表示使用默认值 560。
	FilesWidth float32 `json:"filesWidth,omitempty"`
	// AIWidth 是本机 AI 助理侧栏宽度（DIP），0 表示使用默认值 460。
	AIWidth float32 `json:"aiWidth,omitempty"`
	// AIMode 是本机记住的 AI Agent 权限模式（0 自动执行、1 确认执行、2 只读观察）。
	AIMode uint8 `json:"aiMode,omitempty"`
	// DisableAIRedact 为真时关闭发送给 AI 前的私钥/Token/密码自动脱敏。默认开启脱敏。
	DisableAIRedact bool `json:"disableAIRedact,omitempty"`
	// SyncAIChats 为真时将独立的 AI 会话加密分片（aichat.vault.json）纳入云同步与同步文件夹。默认关闭（仅本机加密保存）。
	SyncAIChats bool `json:"syncAIChats,omitempty"`
	// DisableDefaultMonitor 为真时，新建 SSH 会话不默认开启顶栏服务器监控。
	DisableDefaultMonitor bool `json:"disableDefaultMonitor,omitempty"`
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
	// DisableAutoCheckUpdate 为真时启动时不自动检查更新。默认自动检查（值为 false）。
	DisableAutoCheckUpdate bool `json:"disableAutoCheckUpdate,omitempty"`
	// SkippedVersion 记录用户选择“跳过此版本”的更新版本号（不阻止手动检查更新）。
	SkippedVersion string `json:"skippedVersion,omitempty"`
	// LegacyAIKey 仅用于将旧版本明文保存在 device.json 的密钥迁移进加密保险库，迁移后即清空。
	LegacyAIKey string `json:"aiKey,omitempty"`
	// AIKeyMigrated 标记已完成一次性旧版 AI Key 迁移检查，避免每次启动都读取系统钥匙串。
	AIKeyMigrated bool `json:"aiKeyMigrated,omitempty"`
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
	settingsTabSecurity
	settingsTabTerminal
	settingsTabSync
	settingsTabAI
	settingsTabAbout
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
	showCurrentPw   bool
	showNewPw       bool
	showConfirmPw   bool
	changePwErr     string
	// changePwBusy 表示正在后台派生密钥（两次 Argon2id），期间按钮禁用。
	changePwBusy bool
	// revealChangePw 让刚展开的修改主密码表单滚动到可见位置（设置项在滚动区域里，表单可能在下方）。
	revealChangePw bool
	restoreSession bool

	// 同步页的草稿。密码只在保存或离开同步页/关闭设置时写入系统安全存储，空着表示保留已有密码。
	syncKind         string // "WebDAV" 或 "S3 兼容存储"
	syncVendor       string // 厂商预设
	syncSubDir       string // 远程根目录，如 serval
	syncOn           bool
	probeBusy        bool
	probeOK          bool
	probeMsg         string
	askSyncMerge     bool
	allowMerge       bool
	showSyncPassword bool
	showS3Secret     bool
	showSyncGuide    bool

	// AI 助手页草稿
	editingProfileID string
	aiProfileName    string
	aiBaseURL        string
	aiModel          string
	aiKey            string
	showAIKey        bool
	aiModels         []string
	aiManualModel    bool
	aiFetchBusy      bool
	aiTestBusy       bool
	aiTestOK         bool
	aiTestMsg        string

	// WebDAV 专用字段
	webdavServer   string
	webdavUser     string
	webdavPassword string

	// S3 专用字段
	s3Region       string
	s3Bucket       string
	s3AccessID     string
	s3Secret       string
	s3Endpoint     string
	lastS3Vendor   string
	lastS3Endpoint string

	// 兼容老字段
	syncURL      string
	syncUser     string
	syncPassword string

	// 加密备份与恢复（跨库解锁合并状态）
	backupPromptOpen   bool
	revealBackupPrompt bool
	backupPassword     string
	showBackupPw       bool
	backupBusy         bool
	backupErr          string
	backupVault        *vault.Vault
}

// restorePanel 是空设备“从同步恢复”的面板。
type restorePanel struct {
	open, fresh    bool
	syncKind       string
	syncVendor     string
	lastS3Vendor   string
	lastS3Endpoint string
	s3Region       string
	s3Bucket       string
	s3Endpoint     string
	syncSubDir     string
	url            string
	user           string
	password       string
	showPassword   bool
	showSyncGuide  bool
	busy           bool
	err            string
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
	p.lastS3Vendor = p.syncVendor
	p.lastS3Endpoint = p.s3Endpoint
	profiles, activeID := dedupAIProfiles(a.settings.AIProfiles, a.settings.ActiveAIProfile)
	if len(profiles) > 0 {
		for _, prof := range profiles {
			if prof.ID == activeID {
				norm := normalizeAIProfile(prof)
				if !isAIProfileConfigured(norm) || (len(norm.Models) == 0 && norm.Model == "") {
					p.editingProfileID = norm.ID
					p.aiProfileName = norm.Name
					p.aiBaseURL = norm.BaseURL
					p.aiModel = norm.Model
					p.aiKey = norm.APIKey
					p.aiModels = mergeModelLists(norm.Models)
					p.aiManualModel = len(p.aiModels) == 0
				}
				break
			}
		}
	} else if a.settings.AIBaseURL != "" || a.settings.AIModel != "" || a.settings.AIKey != "" {
		cfg := a.activeAIConfig()
		p.editingProfileID = cfg.ID
		p.aiProfileName = cfg.Name
		p.aiBaseURL = cfg.BaseURL
		p.aiModel = cfg.Model
		p.aiKey = cfg.APIKey
		p.aiModels = mergeModelLists(cfg.Models)
		p.aiManualModel = len(p.aiModels) == 0
	}
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

func (a *app) setTerminalCopyOnSelect(enabled bool) {
	if a.settings.TerminalCopyOnSelect == enabled {
		return
	}
	next := a.settings
	next.TerminalCopyOnSelect = enabled
	if err := a.saveSettings(next); err != nil {
		a.settingsPanel.err = err.Error()
	}
}

func (a *app) setTerminalRightClick(mode string) {
	if a.settings.TerminalRightClick == mode {
		return
	}
	next := a.settings
	next.TerminalRightClick = mode
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
				p.showCurrentPw, p.showNewPw, p.showConfirmPw = false, false, false
				a.noteVaultChanged()
				a.toast(ui.Toast{Title: locales.SetPasswordChanged()})
			}
		})
	}()
}

func (a *app) switchSettingsTab(nextTab settingsTab) {
	p := &a.settingsPanel
	if p.tab == nextTab {
		return
	}
	if p.tab == settingsTabAI {
		a.commitAISettings()
	}
	if p.tab == settingsTabSync {
		a.commitSyncDraft()
	}
	p.tab = nextTab
	if p.tab == settingsTabAI && p.editingProfileID != "" && len(p.aiModels) == 0 {
		a.fetchAIModels(false)
	}
}

// settingsDialog 是设置面板。设置主密码面板打开时让位。
func (a *app) settingsDialog(c *ui.Context, colors design.Colors) {
	p := &a.settingsPanel
	if a.setup.open || a.restore.open || a.update.sheetOpen {
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
	rightClickIdx := indexOr(rightClickModes, a.settings.TerminalRightClick, 0)

	sheet(c, colors, &p.open, &p.fresh, 600, 22, func() {
		ui.Column(c).Gap(14).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
				ui.Text(c, locales.Settings()).FontSize(design.TextHeading).FontWeight(600).Shrink(0)
				ui.Spacer(c)
				tabIdx := int(p.tab)
				c.Key("settingsTab")
				ui.Segmented(c, &tabIdx, locales.SetGeneral(), "安全", locales.SetTerminal(), "同步", "AI", "关于").OnChange(func() {
					a.switchSettingsTab(settingsTab(tabIdx))
				})
				if int(p.tab) != tabIdx {
					a.switchSettingsTab(settingsTab(tabIdx))
				}
			})
			ui.Scroll(c.Key(fmt.Sprintf("settingsScroll:%d", p.tab))).MaxHeight(max(windowHeight-165, 120)).Children(func() {
				ui.Column(c).Gap(11).Padding(0, 4, 0, 0).Children(func() {
					if p.tab == settingsTabGeneral {
						ui.Field(c, locales.SetAppearance(), func() {
							c.Key("settingsAppearance")
							ui.Segmented(c, &mode, locales.FollowSystem(), locales.AppearanceLight(), locales.AppearanceDark()).OnChange(func() {
								p.err = ""
								a.setAppearance(appearanceModes[mode])
							})
						}).Description(locales.SetAppearanceHint())

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
								ui.Button(c.Key("settingsLogDir"), "选择日志文件夹").OnClick(func() {
									a.pickSessionLogDir(false)
								})
								pathText := a.local.SessionLogDir
								if pathText == "" {
									pathText = "还没有选择"
								}
								ui.Text(c, pathText).TextColor(t.TextMuted).FontSize(design.TextSmall).SingleLine().TruncateStart().Ellipsis("…").Grow(1).MinWidth(0)
							})
						}).Description("默认关闭。只记录远程会话的输出，存成纯文本并去掉颜色和控制字符。文件在所选文件夹里，不进保险库。本机终端不记录。")

						ui.Field(c, "服务器监控", func() {
							defMon := !a.local.DisableDefaultMonitor
							c.Key("settingsDefaultMonitor")
							ui.Checkbox(c, &defMon, "新建 SSH 会话时默认开启顶栏监控").OnChange(func() {
								next := a.local
								next.DisableDefaultMonitor = !defMon
								if err := a.saveLocal(next); err != nil {
									p.err = err.Error()
								}
							})
						}).Description("默认开启。每次连接 SSH 服务器都会自动探测并展示 CPU、内存等轻量状态。仅保存在本机。")

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
						return
					}
					if p.tab == settingsTabSecurity {
						a.settingsSecuritySection(c, colors)
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
					if p.tab == settingsTabAbout {
						a.settingsAboutSection(c, colors)
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
					ui.Field(c, "鼠标选中", func() {
						copyOnSelect := a.settings.TerminalCopyOnSelect
						c.Key("settingsCopyOnSelect")
						ui.Checkbox(c, &copyOnSelect, "选中文本时自动复制到剪贴板").OnChange(func() {
							p.err = ""
							a.setTerminalCopyOnSelect(copyOnSelect)
						})
					}).Description("开启后，在终端里鼠标拖选文字松开即自动复制；同时也会更新 AI 的“最近复制”上下文。")
					ui.Field(c, "右键点击行为", func() {
						c.Key("settingsRightClick")
						ui.Segmented(c, &rightClickIdx, rightClickModeLabels()...).OnChange(func() {
							p.err = ""
							a.setTerminalRightClick(rightClickModes[rightClickIdx])
						})
					}).Description("选择“右键菜单”可进行复制、粘贴等操作；更改将在新打开的标签页生效。")
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
				ui.Text(c.Key("settingsError"), p.err).TextColor(t.Danger)
			}
			ui.Row(c).Justify(ui.End).Children(func() {
				ui.Button(c.Key("settingsClose"), locales.Done()).OnClick(a.closeSettings)
			})
		})
	})
	if !p.open {
		a.closeSettings()
	}
}

func (a *app) closeSettings() {
	p := &a.settingsPanel
	if a.aiFetchTimer != nil {
		a.aiFetchTimer.Stop()
		a.aiFetchTimer = nil
	}
	a.commitAISettings()
	a.commitSyncDraft()
	if a.setup.open {
		p.open = true
		p.active = true
		prevThen := a.setup.then
		a.setup.then = func() {
			if prevThen != nil {
				prevThen()
			}
			if a.settingsPanel.err == "" {
				a.aiFetchSeq++
				a.clearBackupState()
				a.settingsPanel = settingsPanel{}
			}
		}
		return
	}
	a.aiFetchSeq++
	a.clearBackupState()
	a.settingsPanel = settingsPanel{}
}

func (a *app) clearBackupState() {
	p := &a.settingsPanel
	p.backupPromptOpen = false
	p.revealBackupPrompt = false
	p.backupPassword = ""
	p.showBackupPw = false
	p.backupBusy = false
	p.backupErr = ""
	p.backupVault = nil
}

// settingsSecuritySection 是设置里的“安全”页：自动锁定、设备记住密钥、修改主密码、已知主机、加密备份与恢复。
func (a *app) settingsSecuritySection(c *ui.Context, colors design.Colors) {
	p := &a.settingsPanel
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

	if a.deviceAvailable() && a.vault != nil && a.vault.State() != vault.Unprotected {
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
				ui.Button(c.Key("settingsOpenChangePw"), locales.SetChangeMasterPassword()).OnClick(func() {
					p.changePwOpen = true
					p.revealChangePw = true
					p.currentPassword = ""
					p.newPassword = ""
					p.confirmPassword = ""
					p.showCurrentPw, p.showNewPw, p.showConfirmPw = false, false, false
					p.changePwErr = ""
				})
			})
		} else {
			form := ui.Column(c.Key("settingsChangePwForm")).Gap(10).Padding(12).Radius(design.RadiusRow).Background(colors.Hover)
			if p.revealChangePw {
				p.revealChangePw = false
				form.ScrollIntoView()
			}
			form.Children(func() {
				ui.Text(c, locales.SetChangeMasterPasswordTitle()).FontWeight(600)
				ui.Field(c, locales.SetCurrentMasterPassword(), func() {
					passwordInput(c, colors, "settingsCurrentPw", locales.SetCurrentMasterPassword(), &p.currentPassword, &p.showCurrentPw, func(el ui.Element) {
						el.AutoFocus().OnSubmit(a.submitChangePassword)
					})
				})
				ui.Field(c, locales.SetNewMasterPassword(), func() {
					passwordInput(c, colors, "settingsNewPw", locales.SetNewMasterPassword(), &p.newPassword, &p.showNewPw, func(el ui.Element) {
						el.OnSubmit(a.submitChangePassword)
					})
				}).Description(locales.PasswordMin(vault.MinPasswordLength))
				ui.Field(c, locales.SetConfirmMasterPassword(), func() {
					passwordInput(c, colors, "settingsConfirmPw", locales.SetConfirmMasterPassword(), &p.confirmPassword, &p.showConfirmPw, func(el ui.Element) {
						el.OnSubmit(a.submitChangePassword)
					})
				})
				if p.changePwErr != "" {
					ui.Text(c.Key("settingsChangePwError"), p.changePwErr).TextColor(colors.Danger).FontSize(design.TextSmall)
				}
				ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
					ui.Button(c.Key("settingsCancelPw"), locales.Cancel()).OnClick(func() {
						p.changePwOpen = false
						p.currentPassword, p.newPassword, p.confirmPassword = "", "", ""
						p.showCurrentPw, p.showNewPw, p.showConfirmPw = false, false, false
						p.changePwErr = ""
					})
					label := locales.SetSaveNewPassword()
					if p.changePwBusy {
						label = locales.SetSaving()
					}
					loadingButton(c, "settingsSavePw", label, true, p.changePwBusy, func() {
						a.submitChangePassword()
					})
				})
			})
		}
	}

	ui.Divider(c)
	a.knownHostsSection(c, colors)
	ui.Divider(c)
	a.vaultBackupSection(c, colors)
}

func (a *app) vaultBackupSection(c *ui.Context, colors design.Colors) {
	p := &a.settingsPanel
	t := c.Theme()
	ui.Field(c, locales.SetBackupTitle(), func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c.Key("settingsBackupExport"), locales.SetBackupExport()).Disabled(p.backupBusy).OnClick(a.exportVaultBackup)
			ui.Button(c.Key("settingsBackupRestore"), locales.SetBackupRestore()).Disabled(p.backupBusy).OnClick(a.startRestoreBackup)
		})
	}).Description(locales.SetBackupDesc())

	if !p.backupPromptOpen {
		return
	}
	form := ui.Column(c.Key("settingsBackupPrompt")).Gap(10).Padding(12).Radius(design.RadiusRow).Background(colors.Hover)
	if p.revealBackupPrompt {
		p.revealBackupPrompt = false
		form.ScrollIntoView()
	}
	form.Children(func() {
		ui.Text(c, locales.SetBackupEnterPassword()).FontWeight(600)
		ui.Text(c, locales.SetBackupOtherVaultHint()).
			FontSize(design.TextSmall).TextColor(t.TextMuted)
		ui.Field(c, locales.SetBackupPassword(), func() {
			passwordInput(c, colors, "settingsBackupPw", locales.SetBackupPassword(), &p.backupPassword, &p.showBackupPw, func(el ui.Element) {
				el.AutoFocus().OnSubmit(a.submitBackupPassword)
			})
		})
		if p.backupErr != "" {
			ui.Text(c.Key("settingsBackupError"), p.backupErr).TextColor(colors.Danger).FontSize(design.TextSmall)
		}
		ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
			ui.Button(c.Key("settingsBackupCancel"), locales.Cancel()).Disabled(p.backupBusy).OnClick(a.clearBackupState)
			label := locales.SetBackupUnlockMerge()
			if p.backupBusy {
				label = locales.SheetUnlocking()
			}
			loadingButton(c, "settingsBackupSubmit", label, true, p.backupBusy, a.submitBackupPassword)
		})
	})
}

func (a *app) exportVaultBackup() {
	if a.vault == nil {
		return
	}
	if a.vault.State() == vault.Unprotected {
		a.requireProtection(a.exportVaultBackup)
		return
	}
	if a.vault.State() == vault.Locked || a.chooseSavePath == nil {
		return
	}
	p := &a.settingsPanel
	p.err = ""
	defaultName := fmt.Sprintf("serval-backup-%s.json", time.Now().Format("20060102"))
	savePath, err := a.chooseSavePath(defaultName)
	if err != nil {
		p.err = "无法打开保存对话框：" + err.Error()
		return
	}
	if savePath == "" {
		return
	}
	data, err := a.vault.Bytes()
	if err != nil {
		p.err = "导出备份失败：" + err.Error()
		return
	}
	if err := os.MkdirAll(filepath.Dir(savePath), 0o700); err != nil {
		p.err = "导出备份失败：" + err.Error()
		return
	}
	if err := writePrivateFile(savePath, data); err != nil {
		p.err = "导出备份失败：" + err.Error()
		return
	}
	a.toast(ui.Toast{Title: locales.SetBackupExported(), Description: filepath.Base(savePath)})
}

func (a *app) startRestoreBackup() {
	p := &a.settingsPanel
	if p.backupBusy || a.vault == nil || a.chooseBackupFile == nil {
		return
	}
	p.err = ""
	backupPath, err := a.chooseBackupFile()
	if err != nil {
		p.err = "选择备份文件失败：" + err.Error()
		return
	}
	if backupPath == "" {
		return
	}
	raw, err := os.ReadFile(backupPath)
	if err != nil {
		p.err = "读取备份文件失败：" + err.Error()
		return
	}
	bv, err := openVaultBytes(raw)
	if err != nil {
		p.err = err.Error()
		return
	}

	if a.vault.Empty() {
		if err := a.vault.Adopt(raw); err != nil {
			p.err = err.Error()
			return
		}
		a.clearBackupState()
		a.settingsPanel = settingsPanel{}
		a.hosts, a.keys, a.proxies, a.snippets, a.forwards = nil, nil, nil, nil, nil
		a.settings = settings{}
		if a.vault.State() == vault.Locked {
			a.unlock = unlockState{remember: a.deviceAvailable()}
			a.toast(ui.Toast{Title: locales.SetBackupImported(), Description: locales.SetBackupUnlockHint()})
			return
		}
		a.reload()
		a.noteVaultChanged()
		a.toast(ui.Toast{Title: locales.SetBackupRestored()})
		return
	}

	if bv.ID() == a.vault.ID() {
		changed, err := a.vault.Merge(raw)
		if err == nil {
			a.clearBackupState()
			if changed {
				a.reload()
				a.noteVaultChanged()
				a.toast(ui.Toast{Title: locales.SetBackupMergedSame()})
			} else {
				a.toast(ui.Toast{Title: locales.SetBackupAlreadyUpToDate()})
			}
			return
		}
		if !strings.Contains(err.Error(), "不是同一把保险库密钥") {
			p.err = err.Error()
			return
		}
	}

	if bv.State() == vault.Unlocked {
		a.applyBackupRecords(bv)
		return
	}

	p.backupPromptOpen = true
	p.revealBackupPrompt = true
	p.backupPassword = ""
	p.showBackupPw = false
	p.backupBusy = false
	p.backupErr = ""
	p.backupVault = bv
}

// submitBackupPassword 在后台用 Argon2id 解锁另一个保险库的备份文件，避免阻塞主线程。
func (a *app) submitBackupPassword() {
	p := &a.settingsPanel
	if p.backupBusy || p.backupVault == nil {
		return
	}
	pw := p.backupPassword
	bv := p.backupVault
	p.backupBusy = true
	p.backupErr = ""
	go func() {
		err := bv.Unlock(pw)
		a.post(func() {
			p := &a.settingsPanel
			if !p.open || !p.backupPromptOpen || p.backupVault != bv {
				return
			}
			p.backupBusy = false
			if err != nil {
				if errors.Is(err, vault.ErrWrongPassword) {
					p.backupErr = locales.SetBackupWrongPassword()
				} else {
					p.backupErr = err.Error()
				}
				return
			}
			p.backupPassword = ""
			p.showBackupPw = false
			a.applyBackupRecords(bv)
		})
	}()
}

func (a *app) applyBackupRecords(bv *vault.Vault) {
	if bv == nil || a.vault == nil {
		return
	}
	var (
		bHosts      []hosts.Host
		bKeys       []keys.Key
		bProxies    []hosts.Proxy
		bForwards   []hosts.Forward
		bSnippets   []snippet
		bKnownHosts []knownHost
		bAIChats    []aiChatRecord
	)
	for _, fn := range []func() error{
		func() error { return decodeAll(bv, kindHost, &bHosts) },
		func() error { return decodeAll(bv, kindKey, &bKeys) },
		func() error { return decodeAll(bv, kindProxy, &bProxies) },
		func() error { return decodeAll(bv, kindForward, &bForwards) },
		func() error { return decodeAll(bv, kindSnippet, &bSnippets) },
		func() error { return decodeAll(bv, kindKnownHost, &bKnownHosts) },
		func() error { return decodeAll(bv, kindAIChat, &bAIChats) },
	} {
		if err := fn(); err != nil {
			a.settingsPanel.backupErr = err.Error()
			a.settingsPanel.err = err.Error()
			return
		}
	}

	var changes []vault.Change
	hasSecrets := false

	usedKeyNames := make(map[string]bool, len(a.keys))
	for _, k := range a.keys {
		usedKeyNames[k.Name] = true
	}
	for _, k := range bKeys {
		if _, exists := a.keyByID(k.ID); exists {
			continue
		}
		k.Name = uniqueImportedName(k.Name, usedKeyNames)
		if k.PrivateKey != "" || k.Passphrase != "" {
			hasSecrets = true
		}
		changes = append(changes, keyChange(k))
	}

	usedProxyNames := make(map[string]bool, len(a.proxies))
	for _, px := range a.proxies {
		usedProxyNames[px.Name] = true
	}
	for _, px := range bProxies {
		if _, exists := a.proxyByID(px.ID); exists {
			continue
		}
		px.Name = uniqueImportedName(px.Name, usedProxyNames)
		if px.Password != "" {
			hasSecrets = true
		}
		changes = append(changes, proxyChange(px))
	}

	usedHostNames := make(map[string]bool, len(a.hosts))
	for _, h := range a.hosts {
		usedHostNames[h.Name] = true
	}
	for _, h := range bHosts {
		if _, exists := a.hostByID(h.ID); exists {
			continue
		}
		h.Name = uniqueImportedName(h.Name, usedHostNames)
		if h.Password != "" || h.TOTPSecret != "" {
			hasSecrets = true
		}
		changes = append(changes, hostChange(h))
	}

	for _, fw := range bForwards {
		if slices.ContainsFunc(a.forwards, func(x hosts.Forward) bool { return x.ID == fw.ID }) {
			continue
		}
		changes = append(changes, forwardChange(fw))
	}

	usedSnippetNames := make(map[string]bool, len(a.snippets))
	for _, sn := range a.snippets {
		usedSnippetNames[sn.Name] = true
	}
	for _, sn := range bSnippets {
		if _, exists := a.snippetByID(sn.ID); exists {
			continue
		}
		sn.Name = uniqueImportedName(sn.Name, usedSnippetNames)
		hasSecrets = true
		changes = append(changes, snippetChange(sn))
	}

	for _, kh := range bKnownHosts {
		id := knownHostID(kh.Host)
		if _, ok, _ := a.vault.Get(id); ok {
			continue
		}
		ch, err := vault.JSONChange(kindKnownHost, id, kh)
		if err != nil {
			a.settingsPanel.backupErr = err.Error()
			a.settingsPanel.err = err.Error()
			return
		}
		changes = append(changes, ch)
	}

	var aiChatChanges []vault.Change
	for _, ch := range bAIChats {
		if ch.ID == "" || slices.ContainsFunc(a.aiChats, func(x aiChatRecord) bool { return x.ID == ch.ID }) {
			continue
		}
		hasSecrets = true
		aiChatChanges = append(aiChatChanges, aiChatChange(ch))
	}

	if _, hasSettings, _ := a.vault.Get(settingsID); !hasSettings {
		var bs settings
		if err := decodeSettings(bv, &bs); err == nil && hasNonEmptySettings(bs) {
			if bs.AIKey != "" || slices.ContainsFunc(bs.AIProfiles, func(pr aiProfile) bool { return pr.APIKey != "" }) {
				hasSecrets = true
			}
			changes = append(changes, settingsChange(bs))
		}
	}

	totalNew := len(changes) + len(aiChatChanges)
	if totalNew == 0 {
		a.clearBackupState()
		a.toast(ui.Toast{Title: locales.SetBackupNothingToAdd()})
		return
	}

	if hasSecrets && a.vault.State() == vault.Unprotected {
		a.requireProtection(func() { a.applyBackupRecords(bv) })
		return
	}

	if len(aiChatChanges) > 0 {
		a.syncAIVaultState()
		if a.aiVault != nil && a.aiVault.State() == vault.Unlocked {
			if err := a.aiVault.Apply(aiChatChanges); err != nil {
				a.settingsPanel.backupErr = err.Error()
				a.settingsPanel.err = err.Error()
				return
			}
			a.noteAIVaultChanged()
		}
	}
	if len(changes) > 0 {
		if err := a.apply(changes...); err != nil {
			a.settingsPanel.backupErr = err.Error()
			a.settingsPanel.err = err.Error()
			return
		}
	} else {
		a.reload()
	}
	a.clearBackupState()
	a.toast(ui.Toast{Title: locales.SetBackupMergedCount(totalNew)})
}

func uniqueImportedName(name string, used map[string]bool) string {
	if name == "" || !used[name] {
		used[name] = true
		return name
	}
	candidate := name + " (备份)"
	for i := 2; used[candidate]; i++ {
		candidate = fmt.Sprintf("%s (备份 %d)", name, i)
	}
	used[candidate] = true
	return candidate
}

func hasNonEmptySettings(s settings) bool {
	return s.Appearance != "" || s.TerminalFontFamily != "" || s.TerminalFontSize != 0 ||
		s.TerminalLineHeight != 0 || s.TerminalCursor != "" || s.TerminalScrollbackMB != 0 ||
		s.TerminalThemeLight != "" || s.TerminalThemeDark != "" || s.TerminalCopyOnSelect ||
		s.TerminalRightClick != "" || s.AIBaseURL != "" || s.AIModel != "" || s.AIKey != "" ||
		len(s.AIProfiles) > 0 || len(s.CustomGroups) > 0
}

func openVaultBytes(raw []byte) (*vault.Vault, error) {
	dir, err := os.MkdirTemp("", "serval-backup-*")
	if err != nil {
		return nil, fmt.Errorf("无法读取备份文件: %w", err)
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "vault.json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		return nil, fmt.Errorf("无法读取备份文件: %w", err)
	}
	bv, err := vault.Open(p, "backup")
	if err != nil {
		return nil, err
	}
	if bv.ID() == "" {
		return nil, errors.New("备份文件格式无效或已损坏")
	}
	return bv, nil
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

	prevKind, prevVendor, prevSubDir := p.syncKind, p.syncVendor, p.syncSubDir
	prevWDServer, prevWDUser, prevWDPw := p.webdavServer, p.webdavUser, p.webdavPassword
	prevS3Region, prevS3Bucket, prevS3ID, prevS3Secret, prevS3Ep := p.s3Region, p.s3Bucket, p.s3AccessID, p.s3Secret, p.s3Endpoint

	ui.Field(c, "同步方式", func() {
		c.Key("syncKind")
		ui.Select(c, &p.syncKind, syncKindOptions)
	})

	if p.syncKind == "WebDAV" {
		a.webdavConfigFields(c, colors, webdavFieldKeys{
			vendor:   "webdavVendor",
			server:   "webdavServer",
			user:     "webdavUser",
			password: "webdavPassword",
			subDir:   "syncSubDir",
		}, &p.syncVendor, &p.webdavServer, &p.webdavUser, &p.webdavPassword, &p.showSyncPassword, &p.syncSubDir,
			&p.showSyncGuide, "应用密码（坚果云请使用“第三方应用密码”）", "默认 serval", true)
	} else {
		a.s3ConfigFields(c, colors, s3FieldKeys{
			vendor:   "s3Vendor",
			region:   "s3Region",
			bucket:   "s3Bucket",
			accessID: "s3AccessID",
			secret:   "s3Secret",
			endpoint: "s3Endpoint",
			subDir:   "syncSubDir",
		}, &p.syncVendor, &p.lastS3Vendor, &p.lastS3Endpoint,
			&p.s3Region, &p.s3Bucket, &p.s3AccessID, &p.s3Secret, &p.showS3Secret, &p.s3Endpoint, &p.syncSubDir,
			&p.showSyncGuide, "留空则保持现有密钥")
	}

	if p.syncKind != prevKind || p.syncVendor != prevVendor || p.syncSubDir != prevSubDir ||
		p.webdavServer != prevWDServer || p.webdavUser != prevWDUser || p.webdavPassword != prevWDPw ||
		p.s3Region != prevS3Region || p.s3Bucket != prevS3Bucket || p.s3AccessID != prevS3ID ||
		p.s3Secret != prevS3Secret || p.s3Endpoint != prevS3Ep {
		p.probeMsg = ""
		p.probeOK = false
	}

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
		loadingButton(c, "syncTest", label, false, p.probeBusy, a.testSyncFromPanel)
		ui.PrimaryButton(c.Key("syncSave"), "保存配置").OnClick(a.saveSyncSettings)
		if a.local.SyncOn || a.local.SyncURL != "" {
			ui.Button(c.Key("syncClear"), "清除配置").OnClick(func() {
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
				p.showSyncPassword = false
				p.showS3Secret = false
				p.probeMsg = ""
				p.probeOK = false
				a.configureSync("", "", "", false)
				a.toast(ui.Toast{Title: "已清除同步配置"})
			})
		}
	})

	if p.probeMsg != "" {
		col := colors.Danger
		if p.probeOK {
			col = colors.Live
		}
		ui.Text(c.Key("syncProbe"), p.probeMsg).FontSize(design.TextSmall).TextColor(col).Selectable()
	}
	switch {
	case a.syncing:
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			loading.Arc(c, loading.Options{Size: 12, Color: c.Theme().Accent}).Label("正在同步").Shrink(0)
			ui.Text(c.Key("syncProgress"), "正在同步…").FontSize(design.TextSmall).TextColor(c.Theme().TextMuted)
		})
	case !a.lastSync.IsZero():
		ui.Text(c.Key("syncLast"), "上次同步 "+formatSyncTime(a.lastSync)).FontSize(design.TextSmall).TextColor(c.Theme().TextMuted)
	}
	if a.syncErr != "" {
		ui.Text(c.Key("syncError"), a.syncErr).FontSize(design.TextSmall).TextColor(colors.Danger).Selectable()
		if a.syncDifferentVault && !a.vault.Empty() {
			ui.Button(c.Key("syncOverwriteBtn"), "用本机覆盖远端").OnClick(func() {
				a.syncOverwriteAsk = true
			})
		}
	}

	if a.syncConfigured() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.PrimaryButton(c.Key("syncNowBtn"), "立即同步").Disabled(a.syncing).OnClick(func() {
				a.syncNow(true)
			})
			ui.Text(c, "基于记录版本自动与远端双向合并同步").FontSize(design.TextSmall).TextColor(c.Theme().TextMuted)
		})
	}

	ui.Column(c).Padding(10, 12).Gap(4).Radius(design.RadiusRow).Background(colors.Hover).Border(1, colors.Hairline).Children(func() {
		syncAI := a.local.SyncAIChats
		c.Key("syncAIChatsToggle")
		if ui.Checkbox(c, &syncAI, "同步 AI 会话历史（独立加密分片）").Changed() {
			next := a.local
			next.SyncAIChats = syncAI
			_ = a.saveLocal(next)
			if syncAI && a.vaultOpen() {
				a.exportSyncDir()
				if a.syncConfigured() {
					a.syncNow(false)
				}
			}
		}
		ui.Text(c, "AI 对话加密保存在独立的 aichat.vault.json 中；开启后随云同步与同步文件夹双向合并，关闭时仅在本机加密保存。").
			FontSize(design.TextSmall).TextColor(c.Theme().TextMuted)
	})

	ui.Divider(c)

	// 本地同步文件夹（独立解耦，用于本地离线/云盘同步备份）
	dirLabel := "未选择文件夹"
	if a.local.SyncDir != "" {
		dirLabel = a.local.SyncDir
	}
	ui.Field(c, "本地同步文件夹", func() {
		ui.Text(c.Key("syncDirPath"), dirLabel).FontSize(design.TextSmall).TextColor(c.Theme().TextMuted).SingleLine().TruncateStart().Ellipsis("…")
	}).Description("将加密的保险库镜像保存至 iCloud Drive、Dropbox 或 Syncthing 文件夹。与云同步独立运作。")
	ui.Row(c).Gap(8).Children(func() {
		ui.Button(c.Key("syncDirChoose"), "选择同步文件夹").OnClick(a.chooseSyncDir)
		if a.local.SyncDir != "" {
			ui.Button(c.Key("syncDirStop"), "停止使用此文件夹").OnClick(func() {
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
		ui.Column(c).Gap(6).Children(func() {
			for _, k := range known {
				a.knownHostRow(c, colors, k)
			}
		})
	}).Description(locales.SetKnownHostsHint())
}

func (a *app) knownHostRow(c *ui.Context, colors design.Colors, k knownHost) {
	t := c.Theme()
	host := k.Host
	ui.Row(c.Key("known:"+host)).Padding(8, 10).Gap(10).AlignItems(ui.Center).
		Radius(design.RadiusRow).Background(colors.Chip).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, host).FontWeight(500).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
				if !k.Added.IsZero() {
					ui.Text(c, k.Added.Local().Format("2006-01-02 15:04")).
						FontSize(design.TextSmall).TextColor(colors.TextFaint).FontFeatures("tnum").Shrink(0)
				}
			})
			for _, sum := range k.keySummaries() {
				ui.Text(c, sum).Font(design.FontTerminal).FontSize(design.TextSmall).
					TextColor(t.TextMuted).SingleLine().Ellipsis("…")
			}
		})
		ui.Button(c.Key("knownDelete:"+host), locales.Delete()).OnClick(func() {
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

type aiPreset struct {
	ID      string
	Name    string
	Short   string
	BaseURL string
}

var aiPresets = []aiPreset{
	{ID: "openai", Name: "OpenAI", Short: "OA", BaseURL: "https://api.openai.com/v1"},
	{ID: "anthropic", Name: "Claude (Anthropic)", Short: "CL", BaseURL: "https://api.anthropic.com/v1"},
	{ID: "deepseek", Name: "DeepSeek", Short: "DS", BaseURL: "https://api.deepseek.com/v1"},
	{ID: "qwen", Name: "通义千问", Short: "QW", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1"},
	{ID: "kimi", Name: "Kimi", Short: "KM", BaseURL: "https://api.moonshot.cn/v1"},
	{ID: "google", Name: "Google", Short: "GG", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai"},
	{ID: "minimax", Name: "MiniMax", Short: "MM", BaseURL: "https://api.minimaxi.com/v1"},
	{ID: "ollama", Name: "Ollama", Short: "OL", BaseURL: "http://localhost:11434/v1"},
	{ID: "openrouter", Name: "OpenRouter", Short: "OR", BaseURL: "https://openrouter.ai/api/v1"},
	{ID: "zhipu", Name: "智谱 GLM", Short: "ZP", BaseURL: "https://open.bigmodel.cn/api/paas/v4"},
	{ID: "doubao", Name: "豆包", Short: "DB", BaseURL: "https://ark.cn-beijing.volces.com/api/v3"},
	{ID: "mimo", Name: "小米 MiMo", Short: "MI", BaseURL: "https://api.xiaomimimo.com/v1"},
	{ID: "custom", Name: "自定义", Short: "AI", BaseURL: ""},
}

func aiPresetByID(id string) (aiPreset, bool) {
	for _, pr := range aiPresets {
		if pr.ID == id {
			return pr, true
		}
	}
	return aiPreset{}, false
}

func providerIDFor(name, baseURL string) string {
	lowURL := strings.ToLower(strings.TrimSpace(baseURL))
	switch {
	case strings.Contains(lowURL, "anthropic"):
		return "anthropic"
	case strings.Contains(lowURL, "deepseek"):
		return "deepseek"
	case strings.Contains(lowURL, "dashscope") || strings.Contains(lowURL, "aliyuncs"):
		return "qwen"
	case strings.Contains(lowURL, "moonshot"):
		return "kimi"
	case strings.Contains(lowURL, "generativelanguage.googleapis.com"):
		return "google"
	case strings.Contains(lowURL, "minimax"):
		return "minimax"
	case strings.Contains(lowURL, ":11434") || strings.Contains(lowURL, "ollama"):
		return "ollama"
	case strings.Contains(lowURL, "openrouter"):
		return "openrouter"
	case strings.Contains(lowURL, "bigmodel"):
		return "zhipu"
	case strings.Contains(lowURL, "volces"):
		return "doubao"
	case strings.Contains(lowURL, "xiaomimimo"):
		return "mimo"
	case strings.Contains(lowURL, "openai"):
		return "openai"
	}
	lowName := strings.ToLower(strings.TrimSpace(name))
	switch lowName {
	case "deepseek":
		return "deepseek"
	case "anthropic", "claude", "claude (anthropic)":
		return "anthropic"
	case "openai":
		return "openai"
	case "通义千问", "qwen":
		return "qwen"
	case "kimi", "moonshot":
		return "kimi"
	case "google", "google ai", "google gemini", "gemini":
		return "google"
	case "minimax":
		return "minimax"
	case "ollama":
		return "ollama"
	case "openrouter":
		return "openrouter"
	case "智谱 glm", "智谱", "zhipu":
		return "zhipu"
	case "豆包", "doubao":
		return "doubao"
	case "小米 mimo", "mimo":
		return "mimo"
	default:
		return "custom"
	}
}

func inferAIProviderName(baseURL string) string {
	if id := providerIDFor("", baseURL); id != "custom" {
		if pr, ok := aiPresetByID(id); ok {
			return pr.Name
		}
	}
	return "自定义"
}

func providerShortLabel(prof aiProfile) string {
	if pr, ok := aiPresetByID(prof.ID); ok && pr.Short != "" {
		return pr.Short
	}
	pid := providerIDFor(prof.Name, prof.BaseURL)
	if pr, ok := aiPresetByID(pid); ok && pr.Short != "" {
		return pr.Short
	}
	return "AI"
}

func mergeModelLists(lists ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range lists {
		for _, item := range list {
			m := strings.TrimSpace(item)
			if m == "" || seen[m] {
				continue
			}
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

func normalizeAIProfile(prof aiProfile) aiProfile {
	baseURL := strings.TrimSpace(prof.BaseURL)
	if baseURL == "" {
		if pr, ok := aiPresetByID(prof.ID); ok && pr.BaseURL != "" {
			baseURL = pr.BaseURL
		}
	}
	name := strings.TrimSpace(prof.Name)
	if name == "" {
		if pr, ok := aiPresetByID(prof.ID); ok && prof.ID != "custom" {
			name = pr.Name
		} else if baseURL != "" {
			name = inferAIProviderName(baseURL)
		} else {
			name = "自定义"
		}
	}
	id := strings.TrimSpace(prof.ID)
	if id == "" {
		id = providerIDFor(name, baseURL)
	} else if !strings.HasPrefix(id, "custom-") {
		if _, ok := aiPresetByID(id); !ok {
			if inferred := providerIDFor(name, baseURL); inferred != "custom" {
				id = inferred
			}
		}
	}
	return aiProfile{
		ID:       id,
		Name:     name,
		BaseURL:  baseURL,
		Model:    strings.TrimSpace(prof.Model),
		APIKey:   strings.TrimSpace(prof.APIKey),
		Models:   mergeModelLists(prof.Models),
		Disabled: prof.Disabled,
	}
}

func isAIProfileConfigured(prof aiProfile) bool {
	if strings.TrimSpace(prof.APIKey) != "" {
		return true
	}
	baseURL := strings.TrimSpace(prof.BaseURL)
	return baseURL != "" && ai.IsLocalEndpoint(baseURL)
}

func isAIProfileActive(prof aiProfile) bool {
	return !prof.Disabled && isAIProfileConfigured(prof)
}

func dedupAIProfiles(profiles []aiProfile, activeID string) ([]aiProfile, string) {
	if len(profiles) == 0 {
		return nil, ""
	}
	type slot struct {
		profile aiProfile
		active  bool
	}
	slots := make(map[string]*slot, len(profiles))
	var order []string
	var resolvedActive string

	for _, raw := range profiles {
		isActive := activeID != "" && raw.ID == activeID
		pid := strings.TrimSpace(raw.ID)
		if !strings.HasPrefix(pid, "custom-") {
			if _, ok := aiPresetByID(pid); !ok {
				if inferred := providerIDFor(raw.Name, raw.BaseURL); inferred != "custom" {
					pid = inferred
				} else if pid == "" {
					pid = "custom"
				}
			}
		}
		if isActive {
			resolvedActive = pid
		}
		cur, exists := slots[pid]
		if !exists {
			s := &slot{
				profile: aiProfile{
					ID:       pid,
					Name:     strings.TrimSpace(raw.Name),
					BaseURL:  strings.TrimSpace(raw.BaseURL),
					Model:    strings.TrimSpace(raw.Model),
					APIKey:   strings.TrimSpace(raw.APIKey),
					Models:   mergeModelLists(raw.Models),
					Disabled: raw.Disabled,
				},
				active: isActive,
			}
			if s.profile.Name == "" {
				if pr, ok := aiPresetByID(pid); ok && pid != "custom" {
					s.profile.Name = pr.Name
				} else {
					s.profile.Name = inferAIProviderName(s.profile.BaseURL)
				}
			}
			slots[pid] = s
			order = append(order, pid)
			continue
		}

		mergedModels := mergeModelLists(cur.profile.Models, raw.Models)
		newKey := strings.TrimSpace(raw.APIKey)
		replace := false
		switch {
		case isActive:
			replace = true
		case !cur.active && (newKey != "" || cur.profile.APIKey == ""):
			replace = true
		}
		if replace {
			fallbackKey := cur.profile.APIKey
			cur.profile = aiProfile{
				ID:       pid,
				Name:     strings.TrimSpace(raw.Name),
				BaseURL:  strings.TrimSpace(raw.BaseURL),
				Model:    strings.TrimSpace(raw.Model),
				APIKey:   newKey,
				Models:   mergedModels,
				Disabled: raw.Disabled,
			}
			if cur.profile.APIKey == "" {
				cur.profile.APIKey = fallbackKey
			}
			if cur.profile.Name == "" {
				if pr, ok := aiPresetByID(pid); ok && pid != "custom" {
					cur.profile.Name = pr.Name
				} else {
					cur.profile.Name = inferAIProviderName(cur.profile.BaseURL)
				}
			}
			if isActive {
				cur.active = true
			}
		} else {
			cur.profile.Models = mergedModels
			if cur.profile.APIKey == "" && newKey != "" {
				cur.profile.APIKey = newKey
			}
		}
	}

	out := make([]aiProfile, 0, len(order))
	for _, pid := range order {
		out = append(out, slots[pid].profile)
	}
	if resolvedActive == "" && activeID != "" {
		if _, ok := slots[activeID]; ok {
			resolvedActive = activeID
		}
	}
	return out, resolvedActive
}

func (a *app) startEditingAIProfile(prof aiProfile) {
	p := &a.settingsPanel
	norm := normalizeAIProfile(prof)
	a.aiFetchSeq++
	p.aiFetchBusy = false
	p.editingProfileID = norm.ID
	p.aiProfileName = norm.Name
	p.aiBaseURL = norm.BaseURL
	p.aiModel = norm.Model
	p.aiKey = norm.APIKey
	p.aiModels = mergeModelLists(norm.Models)
	p.aiManualModel = len(p.aiModels) == 0
	p.aiTestMsg = ""
	if len(p.aiModels) == 0 && (norm.APIKey != "" || (norm.BaseURL != "" && ai.IsLocalEndpoint(norm.BaseURL))) {
		a.fetchAIModels(false)
	}
}

func (a *app) toggleEditAIProfile(id string) {
	p := &a.settingsPanel
	if p.editingProfileID == id {
		a.commitAISettings()
		p.editingProfileID = ""
		p.aiTestMsg = ""
		return
	}
	if p.editingProfileID != "" {
		a.commitAISettings()
		if p.err != "" {
			return
		}
	}
	profiles, _ := dedupAIProfiles(a.settings.AIProfiles, a.settings.ActiveAIProfile)
	for _, prof := range profiles {
		if prof.ID == id {
			a.startEditingAIProfile(prof)
			return
		}
	}
}

func (a *app) addOrEditAIProvider(preset aiPreset) {
	p := &a.settingsPanel
	if p.editingProfileID != "" && strings.TrimSpace(p.aiKey) != "" && a.vault != nil && a.vault.State() == vault.Unprotected {
		a.requireProtection(func() { a.addOrEditAIProvider(preset) })
		return
	}
	if p.editingProfileID != "" {
		a.commitAISettings()
		if p.err != "" {
			return
		}
	}

	profiles, activeID := dedupAIProfiles(a.settings.AIProfiles, a.settings.ActiveAIProfile)
	if preset.ID != "custom" {
		for _, prof := range profiles {
			if prof.ID == preset.ID {
				a.startEditingAIProfile(prof)
				return
			}
		}
	}

	id := preset.ID
	if id == "custom" {
		id = fmt.Sprintf("custom-%d", time.Now().UnixNano())
	}
	newProf := aiProfile{
		ID:      id,
		Name:    preset.Name,
		BaseURL: preset.BaseURL,
	}
	profiles = append(profiles, newProf)

	next := a.settings
	next.AIProfiles = profiles
	if activeID == "" && isAIProfileConfigured(newProf) {
		activeID = newProf.ID
		next.AIBaseURL = newProf.BaseURL
		next.AIModel = newProf.Model
		next.AIKey = newProf.APIKey
	}
	next.ActiveAIProfile = activeID
	if err := a.saveSettings(next); err != nil {
		p.err = err.Error()
		return
	}
	a.startEditingAIProfile(newProf)
}

func (a *app) toggleAIProfileInSettings(id string, on bool) {
	p := &a.settingsPanel
	if p.editingProfileID == id {
		a.commitAISettings()
		if p.err != "" {
			return
		}
	}
	profiles, activeID := dedupAIProfiles(a.settings.AIProfiles, a.settings.ActiveAIProfile)
	idx := slices.IndexFunc(profiles, func(prof aiProfile) bool { return prof.ID == id })
	if idx < 0 {
		return
	}
	if on && !isAIProfileConfigured(profiles[idx]) {
		profiles[idx].Disabled = false
		next := a.settings
		next.AIProfiles = profiles
		_ = a.saveSettings(next)
		a.startEditingAIProfile(profiles[idx])
		p.aiTestOK = false
		p.aiTestMsg = "请先填写 API Key，保存后即可在 AI 面板中使用"
		return
	}

	profiles[idx].Disabled = !on
	if !on && activeID == id {
		activeID = ""
		for _, pr := range profiles {
			if isAIProfileActive(pr) {
				activeID = pr.ID
				break
			}
		}
	} else if on {
		curActiveIdx := slices.IndexFunc(profiles, func(pr aiProfile) bool { return pr.ID == activeID })
		if activeID == "" || curActiveIdx < 0 || !isAIProfileActive(profiles[curActiveIdx]) {
			activeID = id
		}
	}

	next := a.settings
	next.AIProfiles = profiles
	next.ActiveAIProfile = activeID
	next.AIBaseURL = ""
	next.AIModel = ""
	next.AIKey = ""
	for _, pr := range profiles {
		if pr.ID == activeID {
			norm := normalizeAIProfile(pr)
			next.AIBaseURL = norm.BaseURL
			next.AIModel = norm.Model
			next.AIKey = norm.APIKey
			break
		}
	}
	if err := a.saveSettings(next); err != nil {
		p.err = err.Error()
	}
}

func (a *app) deleteAIProfileInSettings(id string) {
	p := &a.settingsPanel
	profiles, activeID := dedupAIProfiles(a.settings.AIProfiles, a.settings.ActiveAIProfile)
	var kept []aiProfile
	deletedName := ""
	for _, prof := range profiles {
		if prof.ID != id {
			kept = append(kept, prof)
		} else {
			deletedName = normalizeAIProfile(prof).Name
		}
	}

	next := a.settings
	next.AIProfiles = kept
	if activeID == id {
		next.ActiveAIProfile = ""
		next.AIBaseURL = ""
		next.AIModel = ""
		next.AIKey = ""
		for _, prof := range kept {
			if isAIProfileActive(prof) {
				norm := normalizeAIProfile(prof)
				next.ActiveAIProfile = norm.ID
				next.AIBaseURL = norm.BaseURL
				next.AIModel = norm.Model
				next.AIKey = norm.APIKey
				break
			}
		}
	}
	if err := a.saveSettings(next); err != nil {
		p.err = err.Error()
		return
	}
	if deletedName != "" {
		a.toast(ui.Toast{Title: "已删除 AI 配置 " + deletedName})
	} else {
		a.toast(ui.Toast{Title: "已删除 AI 配置"})
	}

	if p.editingProfileID == id || len(kept) == 0 {
		if a.aiFetchTimer != nil {
			a.aiFetchTimer.Stop()
			a.aiFetchTimer = nil
		}
		a.aiFetchSeq++
		p.aiFetchBusy = false
		p.editingProfileID = ""
		p.aiProfileName = ""
		p.aiBaseURL = ""
		p.aiModel = ""
		p.aiKey = ""
		p.aiModels = nil
		p.aiManualModel = false
		p.aiTestMsg = ""
	}
}

func (a *app) commitAISettings() {
	p := &a.settingsPanel
	if !p.open && !p.active {
		return
	}
	baseURL := strings.TrimSpace(p.aiBaseURL)
	model := strings.TrimSpace(p.aiModel)
	key := strings.TrimSpace(p.aiKey)
	name := strings.TrimSpace(p.aiProfileName)

	profiles, activeID := dedupAIProfiles(a.settings.AIProfiles, a.settings.ActiveAIProfile)

	// 若未展开任何卡片，仅当测试或外部直接向草稿字段写入了值时才落盘。
	if p.editingProfileID == "" {
		if baseURL == "" && model == "" && key == "" && name == "" {
			return
		}
		if len(profiles) > 0 && activeID != "" {
			p.editingProfileID = activeID
		} else {
			if baseURL == "" {
				baseURL = ai.DefaultBaseURL
			}
			if name == "" {
				name = inferAIProviderName(baseURL)
			}
			p.editingProfileID = providerIDFor(name, baseURL)
			p.aiProfileName = name
			p.aiBaseURL = baseURL
		}
	}

	idx := slices.IndexFunc(profiles, func(prof aiProfile) bool { return prof.ID == p.editingProfileID })
	if idx < 0 && baseURL == "" && model == "" && key == "" {
		return
	}

	if key != "" && a.vault != nil && a.vault.State() == vault.Unprotected {
		a.requireProtection(func() { a.commitAISettings() })
		return
	}

	if idx >= 0 {
		if baseURL == "" {
			baseURL = profiles[idx].BaseURL
		}
		if name == "" {
			name = profiles[idx].Name
		}
	}
	if baseURL == "" {
		if pr, ok := aiPresetByID(p.editingProfileID); ok && pr.BaseURL != "" {
			baseURL = pr.BaseURL
		} else {
			baseURL = ai.DefaultBaseURL
		}
	}
	if name == "" {
		if pr, ok := aiPresetByID(p.editingProfileID); ok && p.editingProfileID != "custom" {
			name = pr.Name
		} else {
			name = inferAIProviderName(baseURL)
		}
	}

	disabled := false
	if idx >= 0 {
		disabled = profiles[idx].Disabled
	}
	updated := aiProfile{
		ID:       p.editingProfileID,
		Name:     name,
		BaseURL:  baseURL,
		Model:    model,
		APIKey:   key,
		Models:   mergeModelLists(p.aiModels),
		Disabled: disabled,
	}

	wasActive := activeID == updated.ID
	if activeID == "" && isAIProfileActive(updated) {
		activeID = updated.ID
		wasActive = true
	}

	if idx >= 0 {
		old := profiles[idx]
		if old.Name == updated.Name &&
			old.BaseURL == updated.BaseURL &&
			old.Model == updated.Model &&
			old.APIKey == updated.APIKey &&
			old.Disabled == updated.Disabled &&
			slices.Equal(old.Models, updated.Models) &&
			a.settings.ActiveAIProfile == activeID &&
			len(profiles) == len(a.settings.AIProfiles) &&
			(!wasActive || (a.settings.AIBaseURL == updated.BaseURL && a.settings.AIModel == updated.Model && a.settings.AIKey == updated.APIKey)) {
			return
		}
		profiles[idx] = updated
	} else {
		profiles = append(profiles, updated)
	}

	next := a.settings
	next.AIProfiles = profiles
	next.ActiveAIProfile = activeID
	if wasActive {
		next.AIBaseURL = updated.BaseURL
		next.AIModel = updated.Model
		next.AIKey = updated.APIKey
	}
	if err := a.saveSettings(next); err != nil {
		p.err = err.Error()
	}
}

func (a *app) scheduleAIKeyModelRefresh() {
	if a.aiFetchTimer != nil {
		a.aiFetchTimer.Stop()
	}
	a.aiFetchTimer = time.AfterFunc(450*time.Millisecond, func() {
		a.post(func() {
			p := &a.settingsPanel
			if !p.open || p.editingProfileID == "" || strings.TrimSpace(p.aiKey) == "" {
				return
			}
			a.commitAISettings()
			a.fetchAIModels(false)
		})
	})
}

func (a *app) fetchAIModels(manual bool) {
	p := &a.settingsPanel
	baseURL := strings.TrimSpace(p.aiBaseURL)
	if baseURL == "" && p.editingProfileID != "" {
		if pr, ok := aiPresetByID(p.editingProfileID); ok {
			baseURL = pr.BaseURL
		}
	}
	if baseURL == "" {
		if manual {
			p.aiTestOK = false
			p.aiTestMsg = "请先填写 Base URL"
		}
		return
	}
	key := strings.TrimSpace(p.aiKey)
	if key == "" && !ai.IsLocalEndpoint(baseURL) {
		if manual {
			p.aiTestOK = false
			p.aiTestMsg = "请先填写 API Key 后再获取模型列表"
		}
		return
	}

	a.aiFetchSeq++
	seq := a.aiFetchSeq
	targetID := p.editingProfileID
	p.aiFetchBusy = true
	if manual {
		p.aiTestMsg = ""
	}

	client := ai.NewClient(ai.Config{
		BaseURL: baseURL,
		APIKey:  key,
		Timeout: 8 * time.Second,
	})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		models, err := client.ListModels(ctx)
		cancel()
		a.post(func() {
			p := &a.settingsPanel
			if !p.open || seq != a.aiFetchSeq {
				return
			}
			p.aiFetchBusy = false
			if err != nil {
				if manual {
					p.aiTestOK = false
					p.aiTestMsg = "获取模型失败：" + friendlyAIError(err)
				}
				return
			}
			ordered := mergeModelLists(models)
			p.aiModels = ordered
			p.aiManualModel = false
			curModel := strings.TrimSpace(p.aiModel)
			if curModel == "" || !slices.Contains(ordered, curModel) {
				p.aiModel = ai.ChoosePreferredModel(ordered)
			}
			if targetID != "" && p.editingProfileID == targetID {
				a.commitAISettings()
			} else if targetID != "" {
				a.syncFetchedAIModels(targetID, ordered)
			}
			if manual {
				p.aiTestOK = true
				p.aiTestMsg = fmt.Sprintf("已从接口获取 %d 个可用模型", len(ordered))
			}
		})
	}()
}

func (a *app) syncFetchedAIModels(providerID string, models []string) {
	if a.vault == nil || a.vault.State() == vault.Locked {
		return
	}
	profiles, activeID := dedupAIProfiles(a.settings.AIProfiles, a.settings.ActiveAIProfile)
	idx := slices.IndexFunc(profiles, func(prof aiProfile) bool { return prof.ID == providerID })
	if idx < 0 || slices.Equal(profiles[idx].Models, models) {
		return
	}
	profiles[idx].Models = append([]string(nil), models...)
	if (profiles[idx].Model == "" || !slices.Contains(models, profiles[idx].Model)) && len(models) > 0 {
		profiles[idx].Model = ai.ChoosePreferredModel(models)
	}
	next := a.settings
	next.AIProfiles = profiles
	if activeID != "" {
		next.ActiveAIProfile = activeID
		if activeID == providerID {
			next.AIModel = profiles[idx].Model
		}
	}
	_ = a.saveSettings(next)
}

func (a *app) aiSettingsSection(c *ui.Context, colors design.Colors) {
	p := &a.settingsPanel
	t := c.Theme()

	profiles, _ := dedupAIProfiles(a.settings.AIProfiles, a.settings.ActiveAIProfile)
	if len(profiles) == 0 && (a.settings.AIBaseURL != "" || a.settings.AIModel != "" || a.settings.AIKey != "") {
		cfg := a.activeAIConfig()
		if cfg.BaseURL != "" || cfg.Model != "" || cfg.APIKey != "" {
			profiles = []aiProfile{cfg}
		}
	}

	// 顶栏：左“提供商”，右“+ 添加提供商”下拉菜单
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "提供商").FontSize(design.TextBody).FontWeight(600).Grow(1)
		c.Key("aiAddProviderBtn")
		ui.PopoverMenuButton(c, "+ 添加提供商", func(m *ui.Menu) {
			for _, pr := range aiPresets {
				preset := pr
				if m.Item(preset.Name).ID("addProvider:" + preset.ID).Chosen() {
					a.addOrEditAIProvider(preset)
				}
			}
		}, menuButtonOptions(colors)).Height(28).Padding(0, 12)
	})

	draftOnly := len(profiles) == 0 && (p.editingProfileID != "" || strings.TrimSpace(p.aiBaseURL) != "" || strings.TrimSpace(p.aiModel) != "" || strings.TrimSpace(p.aiKey) != "")
	if len(profiles) == 0 && !draftOnly {
		ui.Column(c).Padding(18, 14).Gap(6).Radius(design.RadiusRow).Background(colors.Hover).Border(1, colors.Hairline).Children(func() {
			ui.Text(c, "尚未添加 AI 提供商").FontWeight(600)
			ui.Text(c, "点击右上角「+ 添加提供商」选择 OpenAI、DeepSeek、通义千问、Kimi、Ollama 或自定义兼容接口，填入 API Key 后即可自动获取模型列表。").
				FontSize(design.TextSmall).TextColor(t.TextMuted)
		})
		return
	}

	if draftOnly {
		ui.Column(c).Padding(12).Gap(10).Radius(design.RadiusRow).Background(colors.Hover).Border(1, colors.CardBorder).Children(func() {
			a.renderAIProfileEditor(c, colors)
		})
		return
	}

	ui.Column(c).Gap(10).Children(func() {
		for _, prof := range profiles {
			item := normalizeAIProfile(prof)
			isEditing := p.editingProfileID == item.ID
			if isEditing {
				if strings.TrimSpace(p.aiProfileName) != "" {
					item.Name = strings.TrimSpace(p.aiProfileName)
				}
				item.BaseURL = strings.TrimSpace(p.aiBaseURL)
				item.Model = strings.TrimSpace(p.aiModel)
				item.APIKey = strings.TrimSpace(p.aiKey)
				if len(p.aiModels) > 0 {
					item.Models = mergeModelLists(p.aiModels)
				}
			}
			isActive := isAIProfileActive(item)

			cardBg := colors.Hover
			borderCol := colors.CardBorder
			if isActive {
				cardBg = t.Accent.Alpha(0.08)
				borderCol = t.Accent.Alpha(0.35)
			}

			ui.Column(c.Key("aiCard:"+item.ID)).Padding(12, 14).Gap(12).Radius(design.RadiusRow).Background(cardBg).Border(1, borderCol).Children(func() {
				// 卡片头：左侧图标与状态摘要，右侧编辑、删除、启用开关
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					badgeBg := colors.Chip
					badgeFg := t.TextMuted
					if isActive {
						badgeBg = t.Accent.Alpha(0.18)
						badgeFg = t.Accent
					}
					ui.Box(c).Size(34, 34).Radius(8).Background(badgeBg).Center().Shrink(0).Children(func() {
						ui.Text(c, providerShortLabel(item)).FontSize(design.TextSmall).FontWeight(700).TextColor(badgeFg)
					})

					ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							ui.Text(c, item.Name).FontSize(design.TextBody).FontWeight(600).SingleLine().Ellipsis("…")
							if isActive {
								chip(c, "活跃", t.Accent, t.Accent.Alpha(0.15))
							}
						})

						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							switch {
							case item.APIKey != "":
								ui.Text(c, "API Key 已配置").FontSize(design.TextSmall).TextColor(colors.Live).Shrink(0)
							case item.BaseURL != "" && ai.IsLocalEndpoint(item.BaseURL):
								ui.Text(c, "本地服务").FontSize(design.TextSmall).TextColor(colors.Live).Shrink(0)
							default:
								ui.Text(c, "未设置 API Key").FontSize(design.TextSmall).TextColor(t.TextMuted).Shrink(0)
							}
							switch {
							case len(item.Models) > 0:
								ui.Text(c, fmt.Sprintf("|  已获取 %d 个模型", len(item.Models))).FontSize(design.TextSmall).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
							case item.Model != "":
								ui.Text(c, "|  "+item.Model).FontSize(design.TextSmall).Font(design.FontTerminal).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
							case isAIProfileConfigured(item):
								ui.Text(c, "|  待获取模型列表").FontSize(design.TextSmall).TextColor(colors.TextFaint).Shrink(0)
							}
						})
					})

					iconButton(c.Key("aiEdit:"+item.ID), colors, "pencil", "编辑 "+item.Name, func() {
						a.toggleEditAIProfile(item.ID)
					})

					iconButton(c.Key("aiProfileDel:"+item.ID), colors, "trash-2", "删除 "+item.Name, func() {
						a.deleteAIProfileInSettings(item.ID)
					})

					sw := isActive
					c.Key("aiSwitch:" + item.ID)
					if ui.Switch(c, &sw).Label("启用 " + item.Name).Changed() {
						a.toggleAIProfileInSettings(item.ID, sw)
					}
				})

				if isEditing {
					ui.Divider(c)
					a.renderAIProfileEditor(c, colors)
				}
			})
		}
	})

	ui.Column(c).Padding(10, 12).Gap(4).Radius(design.RadiusRow).Background(colors.Hover).Border(1, colors.Hairline).Children(func() {
		redactOn := !a.local.DisableAIRedact
		c.Key("aiRedactToggle")
		if ui.Checkbox(c, &redactOn, "发送给 AI 前自动脱敏私钥、Token 与密码").Changed() {
			next := a.local
			next.DisableAIRedact = !redactOn
			_ = a.saveLocal(next)
		}
		ui.Text(c, "读取终端输出、日志文件和剪贴板内容时自动遮蔽常见私钥与凭据（保存在本机设置）。").
			FontSize(design.TextSmall).TextColor(t.TextMuted)
	})
}

func (a *app) renderAIProfileEditor(c *ui.Context, colors design.Colors) {
	p := &a.settingsPanel
	t := c.Theme()

	ui.Field(c, "显示名称", func() {
		c.Key("setAIProfileName")
		ui.TextInput(c, &p.aiProfileName).Placeholder("例如：OpenAI、DeepSeek").OnSubmit(func() {
			a.commitAISettings()
		})
	})

	keyPlaceholder := "输入 API Key"
	if p.editingProfileID == "ollama" || (p.aiBaseURL != "" && ai.IsLocalEndpoint(p.aiBaseURL)) {
		keyPlaceholder = "本地服务可留空"
	}
	ui.Field(c, "API Key", func() {
		in := passwordInput(c, colors, "setAIKey", "API Key", &p.aiKey, &p.showAIKey, func(el ui.Element) {
			el.Placeholder(keyPlaceholder).OnSubmit(func() {
				a.commitAISettings()
				a.fetchAIModels(false)
			})
		})
		if in.Changed() {
			a.scheduleAIKeyModelRefresh()
		}
	}).Description("密钥会加密保存进保险库（随同步），填入后将自动通过接口获取模型列表。")

	ui.Field(c, "Base URL", func() {
		c.Key("setAIBaseURL")
		ui.TextInput(c, &p.aiBaseURL).Placeholder(ai.DefaultBaseURL).OnSubmit(func() {
			a.commitAISettings()
		})
	})

	hasFetchedModels := len(p.aiModels) > 0
	fetchLabel := "获取模型"
	if hasFetchedModels {
		fetchLabel = "刷新模型"
	}
	if p.aiFetchBusy {
		fetchLabel = "获取中…"
	}

	if hasFetchedModels && !p.aiManualModel {
		ui.Field(c, "可用模型", func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprintf("已自动获取 %d 个可用模型，可在 AI 面板底部菜单直接选择", len(p.aiModels))).
					FontSize(design.TextSmall).TextColor(colors.Live).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
				loadingButton(c, "aiFetchModelsBtn", fetchLabel, false, p.aiFetchBusy, func() {
					a.fetchAIModels(true)
				}).Shrink(0)
				ui.Button(c.Key("aiToggleManualModel"), "手动输入").Shrink(0).OnClick(func() {
					p.aiManualModel = true
				})
			})
		})
	} else {
		modelDesc := "填入 API Key 后自动拉取模型列表；仅在接口不支持自动获取时手动输入。"
		ui.Field(c, "模型名称（可选）", func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				c.Key("setAIModel")
				ui.TextInput(c, &p.aiModel).Placeholder("自动获取失败或不支持列表时可手动填写模型名").Grow(1).MinWidth(0).OnSubmit(func() {
					a.commitAISettings()
				})
				loadingButton(c, "aiFetchModelsBtn", fetchLabel, false, p.aiFetchBusy, func() {
					a.fetchAIModels(true)
				}).Shrink(0)
				if hasFetchedModels {
					ui.Button(c.Key("aiToggleManualModel"), "使用自动列表").Shrink(0).OnClick(func() {
						p.aiManualModel = false
					})
				}
			})
		}).Description(modelDesc)
	}

	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		label := "测试连接"
		if p.aiTestBusy {
			label = "正在测试连接"
		}
		loadingButton(c, "aiTestBtn", label, false, p.aiTestBusy, func() {
			a.commitAISettings()
			p.aiTestBusy = true
			p.aiTestMsg = ""
			targetID := p.editingProfileID
			baseURL := p.aiBaseURL
			model := p.aiModel
			key := p.aiKey
			manualModel := p.aiManualModel
			cachedModels := append([]string(nil), p.aiModels...)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				probeClient := ai.NewClient(ai.Config{
					BaseURL: baseURL,
					Model:   model,
					APIKey:  key,
					Timeout: 10 * time.Second,
				})
				var fetchedModels []string
				models, listErr := probeClient.ListModels(ctx)
				if listErr == nil && len(models) > 0 {
					fetchedModels = models
				}
				candidates := fetchedModels
				if len(candidates) == 0 {
					candidates = cachedModels
				}

				testModel := strings.TrimSpace(model)
				if !manualModel && len(candidates) > 0 {
					if testModel == "" || !slices.Contains(candidates, testModel) {
						testModel = ai.ChoosePreferredModel(candidates)
					}
				}

				var err error
				if testModel != "" {
					chatClient := ai.NewClient(ai.Config{
						BaseURL: baseURL,
						Model:   testModel,
						APIKey:  key,
						Timeout: 12 * time.Second,
					})
					_, err = chatClient.Chat(ctx, []ai.Message{{Role: "user", Content: "hi"}})
					if err != nil && !manualModel && len(candidates) > 1 {
						var apiErr *ai.APIError
						if errors.As(err, &apiErr) && (apiErr.StatusCode == 400 || apiErr.StatusCode == 404 || apiErr.StatusCode == 422) {
							tries := 0
							for _, alt := range candidates {
								if alt == "" || alt == testModel {
									continue
								}
								tries++
								altClient := ai.NewClient(ai.Config{
									BaseURL: baseURL,
									Model:   alt,
									APIKey:  key,
									Timeout: 10 * time.Second,
								})
								if _, altErr := altClient.Chat(ctx, []ai.Message{{Role: "user", Content: "hi"}}); altErr == nil {
									testModel = alt
									err = nil
									break
								}
								if tries >= 2 {
									break
								}
							}
						}
					}
				} else {
					err = probeClient.Probe(ctx)
				}

				a.post(func() {
					p := &a.settingsPanel
					p.aiTestBusy = false
					if len(fetchedModels) > 0 && p.open && p.editingProfileID == targetID {
						p.aiModels = mergeModelLists(fetchedModels)
					}
					if testModel != "" && p.open && p.editingProfileID == targetID && !p.aiManualModel {
						p.aiModel = testModel
						a.commitAISettings()
					} else if len(fetchedModels) > 0 && targetID != "" {
						a.syncFetchedAIModels(targetID, mergeModelLists(fetchedModels))
					}
					if err != nil {
						p.aiTestOK = false
						p.aiTestMsg = "连接测试失败：" + friendlyAIError(err)
					} else {
						p.aiTestOK = true
						if testModel != "" {
							p.aiTestMsg = fmt.Sprintf("连接测试成功，模型 %s 响应正常。", testModel)
						} else {
							p.aiTestMsg = "连接测试成功，AI 服务响应正常。"
						}
					}
				})
			}()
		})
		if p.editingProfileID != "" {
			ui.Button(c.Key("aiCollapseEditBtn"), "收起").OnClick(func() {
				a.commitAISettings()
				p.editingProfileID = ""
				p.aiTestMsg = ""
			})
		}
	})

	if p.aiTestMsg != "" {
		col := t.Accent
		if !p.aiTestOK {
			col = colors.Danger
		}
		ui.Text(c.Key("aiTestResult"), p.aiTestMsg).FontSize(design.TextSmall).TextColor(col)
	}
}
