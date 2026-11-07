package main

import (
	"slices"

	"serval/design"

	"github.com/egoist/mygo/ui"
)

// 外观设置。默认（空值）跟随系统。
const (
	appearanceSystem = ""
	appearanceLight  = "light"
	appearanceDark   = "dark"
)

var appearanceModes = []string{appearanceSystem, appearanceLight, appearanceDark}

// settings 是随保险库同步的偏好设置（记录类型 settings，固定 ID settings）。
//
// 外观以及后续的终端字体与主题、代码片段默认值等跨设备一致的偏好都放在这里；
// 写入时通过 JSONChange 保留其他版本客户端增加的字段。
type settings struct {
	Appearance string `json:"appearance,omitempty"`
}

// deviceSettings 是只属于当前这台设备的设置，保存在配置目录下的 device.json，
// 不写进 vault.json，也不随保险库同步。
type deviceSettings struct {
	// RecentHosts 是本机最近连接过的主机 ID，按时间从近到远排列。
	RecentHosts []string `json:"recentHosts,omitempty"`
}

const maxRecentHosts = 20

// settingsPanel 是“设置”浮层面板的状态。
type settingsPanel struct {
	open, fresh bool
	err         string
}

// openSettings 打开设置面板。
func (a *app) openSettings() {
	a.settingsPanel = settingsPanel{open: true, fresh: true}
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
	_ = a.saveLocal(next)
}

// removeRecentHost 从本机最近使用列表里移除已删除的主机。
func (a *app) removeRecentHost(id string) {
	if !slices.Contains(a.local.RecentHosts, id) {
		return
	}
	next := a.local
	next.RecentHosts = slices.DeleteFunc(slices.Clone(a.local.RecentHosts), func(x string) bool { return x == id })
	_ = a.saveLocal(next)
}

// settingsDialog 是设置面板。设置主密码面板打开时让位。
func (a *app) settingsDialog(c *ui.Context, colors design.Colors) {
	p := &a.settingsPanel
	if !p.open || a.setup.open {
		return
	}
	t := c.Theme()
	mode := max(slices.Index(appearanceModes, a.settings.Appearance), 0)
	sheet(c, colors, &p.open, &p.fresh, 440, 22, func() {
		ui.Column(c).Gap(14).Children(func() {
			ui.Text(c, "设置").FontSize(design.TextHeading).FontWeight(600)
			ui.Field(c, "外观", func() {
				c.Key("settingsAppearance")
				ui.Segmented(c, &mode, "跟随系统", "浅色", "深色").OnChange(func() {
					p.err = ""
					a.setAppearance(appearanceModes[mode])
				})
			}).Description("外观设置保存在保险库里，之后在多台设备间同步时会一起生效。")
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
