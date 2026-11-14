package main

import (
	"fmt"

	"serval/core/vault"
	"serval/design"
	"serval/locales"

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
	message := locales.SheetTrustMessage(prompt.info.Host, prompt.info.KeyType, prompt.info.Fingerprint)
	// 安全确认保留系统风格的警告框，不做成玻璃：它需要最直接、最不会被误读的样子。
	// 最后一个按钮是默认按钮，放“取消”，避免按回车就信任了未知主机。
	choice := ui.AlertDialog(c, &a.asking, locales.SheetTrustHost(), message, locales.SheetTrustConnect(), locales.Cancel())
	if choice == -1 {
		return
	}
	prompt.answer <- choice == 0
	a.prompts = a.prompts[1:]
}

// passwordInput 构建带“显示/隐藏”眼睛切换按钮的密码输入框。
// reveal 为 nil 时始终密文显示。关闭对应弹窗或锁定保险库时由状态结构体清零。
func passwordInput(c *ui.Context, colors design.Colors, key, fieldLabel string, value *string, reveal *bool, configure func(ui.Element)) ui.Element {
	if reveal == nil {
		c.Key(key)
		el := ui.TextInput(c, value).Password()
		if configure != nil {
			configure(el)
		}
		return el
	}
	t := c.Theme()
	var el ui.Element
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			c.Key(key)
			el = ui.TextInput(c, value)
			if !*reveal {
				el.Password()
			}
			if configure != nil {
				configure(el)
			}
		})
		iconName := "eye"
		btnLabel := "显示" + fieldLabel
		if *reveal {
			iconName = "eye-off"
			btnLabel = "隐藏" + fieldLabel
		}
		btn := ui.ButtonBase(c).Key(key+"Reveal").Label(btnLabel).Tooltip(btnLabel).
			Size(28, 28).Center().Radius(6).Shrink(0)
		if btn.Hovered() {
			btn.Background(colors.Hover)
		}
		btn.Children(func() {
			ui.Icon(c, design.Icon(iconName)).FontSize(14).TextColor(t.TextMuted)
		}).OnClick(func() {
			*reveal = !*reveal
		})
	})
	return el
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
				design.DistroCoatView(c, a.auth.host.ID, a.auth.host.Distro, a.auth.host.Production, 36)
				ui.Column(c).Gap(2).MinWidth(0).Children(func() {
					ui.Text(c, a.auth.host.Name).FontSize(design.TextHeading).FontWeight(600)
					ui.Text(c, a.auth.host.Target()).TextColor(t.TextMuted).FontSize(design.TextSmall)
				})
			})
			label := locales.Password()
			if a.auth.passphrase {
				label = locales.SheetKeyPassphrase()
			}
			var input ui.Element
			field := ui.Field(c, label, func() {
				input = passwordInput(c, colors, "secret", label, &a.auth.secret, &a.auth.showSecret, func(el ui.Element) {
					el.AutoFocus()
				})
			}).Error(a.auth.message)
			// 为跳板机询问时要说清楚：用户点的是另一台主机，别把目标主机的密码填到这里。
			if a.auth.via {
				field.Description(locales.SheetJumpHost(a.auth.target.Name))
			}
			remember := locales.SheetRememberPassword()
			if a.auth.passphrase {
				remember = locales.SheetRememberPassphrase()
			}
			// 临时连接的主机（快速连接、直接写在跳板机列表里的地址）不在保险库里，没有地方保存密码，
			// 不显示“记住”，免得用户以为记住了。
			if _, saved := a.hostByID(a.auth.host.ID); saved {
				c.Key("authRemember")
				ui.Checkbox(c, &a.auth.remember, remember)
			}
			submit := input.Submitted()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				ui.Button(c, locales.Cancel()).Key("authCancel").OnClick(func() {
					a.auth = authSheet{}
				})
				if ui.PrimaryButton(c, locales.Connect()).Key("authSubmit").Clicked() || submit {
					a.submitSecret()
				}
			})
		})
	})
	// 点击外部或按 Esc 关闭时，同样不能把已输入的内容和可见开关留在内存里。
	if !a.auth.open {
		a.auth.secret = ""
		a.auth.showSecret = false
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
			ui.Text(c, locales.SheetSetMasterPassword()).FontSize(design.TextHeading).FontWeight(600)
			ui.Text(c, locales.SheetMasterPasswordIntro()).
				TextColor(t.TextMuted)
			ui.Field(c, locales.MasterPassword(), func() {
				passwordInput(c, colors, "setupPassword", locales.MasterPassword(), &s.password, &s.showPassword, func(el ui.Element) {
					el.AutoFocus()
				})
			}).Description(locales.SheetMinLength(vault.MinPasswordLength))
			var confirm ui.Element
			ui.Field(c, locales.SheetEnterAgain(), func() {
				confirm = passwordInput(c, colors, "setupConfirm", locales.SheetEnterAgain(), &s.confirm, &s.showConfirm, nil)
			})
			if a.deviceAvailable() {
				c.Key("setupRemember")
				ui.Checkbox(c, &s.remember, locales.SheetRememberDeviceUnlock())
			}
			ui.Text(c, locales.SheetMasterPasswordWarning()).FontSize(design.TextSmall).TextColor(colors.Danger)
			if s.err != "" {
				ui.Text(c, s.err).Key("setupError").TextColor(t.Danger)
			}
			submit := confirm.Submitted()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				ui.Button(c, locales.Cancel()).Key("setupCancel").Disabled(s.busy).OnClick(func() { a.setup = setupSheet{} })
				label := locales.SheetSetAndContinue()
				if s.busy {
					label = locales.SheetSetting()
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

// fileOpDialog 是文件操作（新建目录、重命名、修改权限）的输入面板。
func (a *app) fileOpDialog(c *ui.Context, colors design.Colors) {
	op := &a.fileOp
	if !op.open || a.setup.open {
		return
	}
	var title, label, btnText string
	switch op.kind {
	case fileOpNewFolder:
		title = locales.NewFolder()
		label = locales.SheetFolderName()
		btnText = locales.SheetCreate()
	case fileOpRename:
		// 标题带上文件名，按钮只写动作：同一界面里可点击的文字不能和标题重复。
		title = locales.SheetRenameTitle(op.name)
		label = locales.SheetNewName()
		btnText = locales.Rename()
	case fileOpPermissions:
		title = locales.SheetChangeModeTitle(op.name)
		label = locales.SheetOctalMode()
		btnText = locales.ChangeMode()
	}
	if op.busy {
		btnText = locales.SheetWorking()
	}

	sheet(c, colors, &op.open, &op.fresh, 360, 22, func() {
		ui.Column(c).Gap(14).Children(func() {
			ui.Text(c, title).FontSize(design.TextHeading).FontWeight(600)
			var input ui.Element
			f := ui.Field(c, label, func() {
				c.Key("fileOpInput")
				input = ui.TextInput(c, &op.input).AutoFocus()
			})
			if op.err != "" {
				f.Error(op.err)
			}
			submit := input.Submitted()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				ui.Button(c, locales.Cancel()).Key("fileOpCancel").Disabled(op.busy).OnClick(func() {
					a.fileOp = fileOpState{}
				})
				if ui.PrimaryButton(c, btnText).Key("fileOpSubmit").Disabled(op.busy).Clicked() || (submit && !op.busy) {
					a.submitFileOp()
				}
			})
		})
	})
}

// fileDeleteDialog 确认删除远程文件或目录。
func (a *app) fileDeleteDialog(c *ui.Context) {
	if a.fileDelete == nil {
		return
	}
	del := *a.fileDelete
	message := locales.SheetDeleteFile()
	if del.isDir {
		message = locales.SheetDeleteDirectory()
	}
	open := true
	choice := ui.AlertDialog(c, &open, locales.SheetDeleteNamed(del.name), message, locales.Delete(), locales.Cancel())
	if choice == -1 && open {
		return
	}
	if choice == 0 {
		a.confirmDeleteFile()
	} else {
		a.fileDelete = nil
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
				ui.Text(c, locales.SheetLocked()).FontSize(design.TextHeading).FontWeight(600)
				ui.Text(c, locales.SheetUnlockPrompt()).TextColor(t.TextMuted)
			})
			var input ui.Element
			ui.Field(c, locales.MasterPassword(), func() {
				input = passwordInput(c, colors, "unlockPassword", locales.MasterPassword(), &u.password, &u.showPassword, func(el ui.Element) {
					el.AutoFocus()
				})
			}).Error(u.err)
			if a.deviceAvailable() {
				c.Key("unlockRemember")
				ui.Checkbox(c, &u.remember, locales.SheetRememberDevice())
			}
			label := locales.SheetUnlock()
			if u.busy {
				label = locales.SheetUnlocking()
			}
			submit := input.Submitted()
			b := ui.PrimaryButton(c, label).Key("unlockSubmit").Disabled(u.busy)
			if (b.Clicked() || submit) && !u.busy {
				a.submitUnlock()
			}
		})
	})
}

// lockConfirmDialog 在有打开的会话时提示用户锁定将关闭所有会话。
func (a *app) lockConfirmDialog(c *ui.Context) {
	if !a.askingLock {
		return
	}
	message := locales.SheetLockConfirm(len(a.tabs))
	open := true
	choice := ui.AlertDialog(c, &open, locales.SheetLockVault(), message, locales.SheetLock(), locales.Cancel())
	if choice == -1 && open {
		return
	}
	a.askingLock = false
	if choice == 0 {
		a.lockVault()
	}
}

// challengeDialog 弹出多步验证（keyboard-interactive）对话框。
func (a *app) challengeDialog(c *ui.Context, colors design.Colors) {
	ch := a.currentChallenge()
	if ch == nil || !ch.open {
		return
	}
	for len(ch.showAnswers) < len(ch.questions) {
		ch.showAnswers = append(ch.showAnswers, false)
	}
	title := locales.SheetChallenge()
	if ch.name != "" {
		title = ch.name
	}
	subtitle := fmt.Sprintf("%s · %s", ch.host.Name, ch.host.Target())
	if ch.instruction != "" {
		subtitle = ch.instruction
	}
	closeSheet := func() { a.cancelChallenge() }
	sheet(c, colors, &ch.open, &ch.fresh, 400, 20, func() {
		ui.Column(c).Key("challengeBody").Gap(16).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				design.DistroCoatView(c, ch.host.ID, ch.host.Distro, ch.host.Production, 28)
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, title).FontSize(design.TextHeading).FontWeight(600)
					ui.Text(c, subtitle).FontSize(design.TextSmall).TextColor(c.Theme().TextMuted)
				})
			})
			ui.Divider(c)
			var lastSubmitted bool
			ui.Column(c).Gap(10).Children(func() {
				for i, q := range ch.questions {
					idx := i
					label := q.Text
					if label == "" {
						label = locales.SheetQuestion(idx + 1)
					}
					var input ui.Element
					ui.Field(c, label, func() {
						key := fmt.Sprintf("challengeInput:%d", idx)
						if !q.Echo {
							input = passwordInput(c, colors, key, label, &ch.answers[idx], &ch.showAnswers[idx], func(el ui.Element) {
								if idx == 0 {
									el.AutoFocus()
								}
							})
						} else {
							c.Key(key)
							input = ui.TextInput(c, &ch.answers[idx])
							if idx == 0 {
								input.AutoFocus()
							}
						}
					})
					if idx == len(ch.questions)-1 && input.Submitted() {
						lastSubmitted = true
					}
				}
			})
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				ui.Button(c, locales.Cancel()).OnClick(closeSheet)
				if ui.PrimaryButton(c, locales.SheetVerify()).Key("submitChallenge").Clicked() || lastSubmitted {
					a.submitChallenge()
				}
			})
		})
	})
	if !ch.open && a.currentChallenge() == ch {
		a.cancelChallenge()
	}
}
