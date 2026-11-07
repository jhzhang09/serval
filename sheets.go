package main

import (
	"fmt"

	"serval/core/vault"
	"serval/design"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

// sheet 是 Serval 的浮层面板：一块玻璃浮在压暗的窗口上，打开时从略小、半透明弹出。
//
// 玻璃会透出背后被压暗、模糊的界面，让浮层和窗口是一个整体，而不是一张不透明的卡片。
// 系统开启“减少透明度”时，玻璃插件会自动改画成不透明的面板。
// fresh 指向“刚打开”的标记：为 true 时这一帧从起始姿态开始，并把它清掉。
func sheet(c *ui.Context, colors design.Colors, open, fresh *bool, width, padding float32, fn func()) ui.Element {
	return ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		backdrop.Background(colors.Scrim)
		panel.Width(width).Padding(padding).Radius(18).Material(glass.Glass{}).
			Shadow(0, 24, 60, -16, colors.CardShadow)
		pose := ui.Pose{}
		if fresh != nil && *fresh {
			pose = ui.Pose{Scale: 0.96, Fade: 0.6}
			*fresh = false
			c.Invalidate()
		}
		panel.AnimatePose(pose, design.Pop)
		fn()
	})
}

func (a *app) hostKeyDialog(c *ui.Context) {
	if len(a.prompts) == 0 {
		a.asking = false
		return
	}
	prompt := a.prompts[0]
	a.asking = true
	message := fmt.Sprintf("这是第一次连接 %s。请核对下面的指纹和服务器上的一致，再继续。\n\n%s\n%s",
		prompt.info.Host, prompt.info.KeyType, prompt.info.Fingerprint)
	// 安全确认保留系统风格的警告框，不做成玻璃：它需要最直接、最不会被误读的样子。
	// 最后一个按钮是默认按钮，放“取消”，避免按回车就信任了未知主机。
	choice := ui.AlertDialog(c, &a.asking, "信任这台主机吗？", message, "信任并连接", "取消")
	if choice == -1 {
		return
	}
	prompt.answer <- choice == 0
	a.prompts = a.prompts[1:]
}

// authDialog 询问密码或私钥口令。勾选“记住”时，连接成功后才存进保险库。
func (a *app) authDialog(c *ui.Context, colors design.Colors) {
	if !a.auth.open || a.setup.open {
		return
	}
	t := c.Theme()
	sheet(c, colors, &a.auth.open, &a.auth.fresh, 380, 22, func() {
		ui.Column(c).Gap(16).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				design.CoatView(c, a.auth.host.ID, a.auth.host.Production, 36)
				ui.Column(c).Gap(2).MinWidth(0).Children(func() {
					ui.Text(c, a.auth.host.Name).FontSize(design.TextHeading).FontWeight(600)
					ui.Text(c, a.auth.host.Target()).TextColor(t.TextMuted).FontSize(design.TextSmall)
				})
			})
			label := "密码"
			if a.auth.passphrase {
				label = "私钥口令"
			}
			var input ui.Element
			field := ui.Field(c, label, func() {
				c.Key("secret")
				input = ui.TextInput(c, &a.auth.secret).Password().AutoFocus()
			}).Error(a.auth.message)
			// 为跳板机询问时要说清楚：用户点的是另一台主机，别把目标主机的密码填到这里。
			if a.auth.via {
				field.Description(fmt.Sprintf("这是连接 %s 途中的跳板机。", a.auth.target.Name))
			}
			remember := "记住密码"
			if a.auth.passphrase {
				remember = "记住口令"
			}
			c.Key("authRemember")
			ui.Checkbox(c, &a.auth.remember, remember+"（加密保存在保险库里）")
			submit := input.Submitted()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				ui.Button(c, "取消").Key("authCancel").OnClick(func() {
					a.auth = authSheet{}
				})
				if ui.PrimaryButton(c, "连接").Key("authSubmit").Clicked() || submit {
					a.submitSecret()
				}
			})
		})
	})
	// 点击外部或按 Esc 关闭时，同样不能把已输入的内容留在内存里。
	if !a.auth.open {
		a.auth.secret = ""
	}
}

// setupDialog 是“设置主密码”面板。它在第一次保存密码或私钥时出现，打开期间其他面板让位。
func (a *app) setupDialog(c *ui.Context, colors design.Colors) {
	s := &a.setup
	if !s.open {
		return
	}
	t := c.Theme()
	sheet(c, colors, &s.open, &s.fresh, 400, 22, func() {
		ui.Column(c).Gap(14).Children(func() {
			ui.Text(c, "设置主密码").FontSize(design.TextHeading).FontWeight(600)
			ui.Text(c, "Serval 用主密码加密你保存的密码和私钥。以后在其他设备上同步数据时，也用它解锁。").
				TextColor(t.TextMuted)
			ui.Field(c, "主密码", func() {
				c.Key("setupPassword")
				ui.TextInput(c, &s.password).Password().AutoFocus()
			}).Description(fmt.Sprintf("至少 %d 个字符。", vault.MinPasswordLength))
			var confirm ui.Element
			ui.Field(c, "再输入一次", func() {
				c.Key("setupConfirm")
				confirm = ui.TextInput(c, &s.confirm).Password()
			})
			if a.device != nil {
				c.Key("setupRemember")
				ui.Checkbox(c, &s.remember, "在这台设备上记住，打开 Serval 时自动解锁")
			}
			ui.Text(c, "主密码无法找回。忘记后，保存的密码和私钥都无法解开。").FontSize(design.TextSmall).TextColor(colors.Danger)
			if s.err != "" {
				ui.Text(c, s.err).Key("setupError").TextColor(t.Danger)
			}
			submit := confirm.Submitted()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				ui.Button(c, "取消").Key("setupCancel").Disabled(s.busy).OnClick(func() { a.setup = setupSheet{} })
				label := "设置并继续"
				if s.busy {
					label = "正在设置…"
				}
				b := ui.PrimaryButton(c, label).Key("setupSubmit").Disabled(s.busy)
				if (b.Clicked() || submit) && !s.busy {
					a.submitSetup()
				}
			})
		})
	})
	// 点击外部或按 Esc 取消：放弃这次要保存的秘密，原来的面板重新出现。派生进行中不允许中途关闭。
	if !a.setup.open {
		if a.setup.busy {
			a.setup.open = true
			return
		}
		a.setup = setupSheet{}
	}
}

// lockScreen 是保险库锁定时的全窗口页面。解锁前不显示任何主机信息。
func (a *app) lockScreen(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	u := &a.unlock
	ui.Column(c).Fill().Center().DragWindow().Children(func() {
		card := ui.Column(c).Width(360).Padding(28).Gap(16).Radius(18).Material(glass.Glass{}).
			Shadow(0, 24, 60, -16, colors.CardShadow)
		card.Children(func() {
			design.BrandView(c, 56)
			ui.Column(c).Gap(4).Children(func() {
				ui.Text(c, "Serval 已锁定").FontSize(design.TextHeading).FontWeight(600)
				ui.Text(c, "输入主密码解锁保险库。").TextColor(t.TextMuted)
			})
			var input ui.Element
			ui.Field(c, "主密码", func() {
				c.Key("unlockPassword")
				input = ui.TextInput(c, &u.password).Password().AutoFocus()
			}).Error(u.err)
			if a.device != nil {
				c.Key("unlockRemember")
				ui.Checkbox(c, &u.remember, "在这台设备上记住")
			}
			label := "解锁"
			if u.busy {
				label = "正在解锁…"
			}
			submit := input.Submitted()
			b := ui.PrimaryButton(c, label).Key("unlockSubmit").Disabled(u.busy)
			if (b.Clicked() || submit) && !u.busy {
				a.submitUnlock()
			}
		})
	})
}
