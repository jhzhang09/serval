package main

import (
	"errors"
	"fmt"
	"slices"

	"serval/core/vault"
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

var autoLockChoices = []int{0, 5, 15, 30, 60}

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
	// AutoLockMinutes 是闲置指定分钟后自动锁定的时间，0 表示不自动锁定。
	AutoLockMinutes int `json:"autoLockMinutes,omitempty"`
}

const maxRecentHosts = 20

// settingsPanel 是“设置”浮层面板的状态。
type settingsPanel struct {
	open, fresh bool
	err         string

	rememberDevice bool

	// 修改主密码表单
	changePwOpen    bool
	currentPassword string
	newPassword     string
	confirmPassword string
	changePwErr     string
}

// openSettings 打开设置面板。
func (a *app) openSettings() {
	a.settingsPanel = settingsPanel{
		open:           true,
		fresh:          true,
		rememberDevice: a.isRememberedOnDevice(),
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

func (a *app) submitChangePassword() {
	p := &a.settingsPanel
	if p.newPassword != p.confirmPassword {
		p.changePwErr = "两次输入的新主密码不一致。"
		return
	}
	if err := a.vault.ChangePassword(p.currentPassword, p.newPassword); err != nil {
		if errors.Is(err, vault.ErrWrongPassword) {
			p.changePwErr = "当前主密码不正确。"
			return
		}
		p.changePwErr = err.Error()
		return
	}
	p.changePwOpen = false
	p.currentPassword = ""
	p.newPassword = ""
	p.confirmPassword = ""
	p.changePwErr = ""
	a.toast(ui.Toast{Title: "已修改主密码"})
}

// settingsDialog 是设置面板。设置主密码面板打开时让位。
func (a *app) settingsDialog(c *ui.Context, colors design.Colors) {
	p := &a.settingsPanel
	if !p.open || a.setup.open {
		return
	}
	t := c.Theme()
	mode := max(slices.Index(appearanceModes, a.settings.Appearance), 0)
	sheet(c, colors, &p.open, &p.fresh, 560, 22, func() {
		ui.Column(c).Gap(14).Children(func() {
			ui.Text(c, "设置").FontSize(design.TextHeading).FontWeight(600)
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
					_ = a.saveLocal(next)
				})
			}).Description("闲置超过指定时间后自动关闭会话并锁定保险库。")

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
							p.currentPassword = ""
							p.newPassword = ""
							p.confirmPassword = ""
							p.changePwErr = ""
						})
					})
				} else {
					ui.Column(c).Gap(10).Padding(12).Radius(design.RadiusRow).Background(colors.Hover).Children(func() {
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
							ui.PrimaryButton(c, "保存新密码").Key("settingsSavePw").OnClick(func() {
								a.submitChangePassword()
							})
						})
					})
				}
			}

			ui.Divider(c)
			a.knownHostsSection(c, colors)
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
	known := a.knownHostsList()
	ui.Field(c, "已知主机", func() {
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
				ui.Text(c, host).FontWeight(500).NoWrap().Ellipsis("…").MinWidth(0).Shrink(1)
				if !k.Added.IsZero() {
					ui.Text(c, k.Added.Local().Format("2006-01-02 15:04")).
						FontSize(design.TextSmall).TextColor(colors.TextFaint).FontFeatures("tnum").Shrink(0)
				}
			})
			for _, sum := range k.keySummaries() {
				ui.Text(c, sum).Font(design.FontTerminal).FontSize(design.TextSmall).
					TextColor(t.TextMuted).NoWrap().Ellipsis("…")
			}
		})
		ui.Button(c, "删除").Key("knownDelete:" + host).OnClick(func() {
			a.deleteKnownHost(host)
		})
	})
}
