package main

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"

	"serval/core/hosts"
	"serval/core/keys"
	"serval/core/vault"
	"serval/design"
	"serval/locales"

	"github.com/egoist/mygo/ui"
)

// 密钥面板的页面。
const (
	keyList = iota
	keyAdd
	keyDetail
)

// maxKeyFile 是私钥文件的大小上限。正常的私钥只有几 KB，选错了大文件时直接拒绝。
const maxKeyFile = 64 << 10

// keyPanel 是“密钥”面板：管理保险库里的私钥。
type keyPanel struct {
	open, fresh bool
	page        int

	// 添加页
	generate       bool // false 导入已有私钥，true 生成新密钥
	genIndex       int  // 生成类型：0 Ed25519，1 RSA 4096
	name, pem      string
	passphrase     string
	savePassphrase bool
	busy           bool // 正在生成 RSA 密钥（需要一两秒）
	err            string

	// 详情页
	detail     string // 正在查看的私钥 ID
	rename     string
	deleting   *keys.Key
	addingMode int // Segmented 的选中项，和 generate 对应
	// fromHostForm 表示从主机表单行内发起导入，保存后自动选中并返回主机表单。
	fromHostForm bool
}

// openKeys 打开密钥面板。
func (a *app) openKeys() {
	a.keyPanel = keyPanel{open: true, fresh: true}
}

func (a *app) startAddKey() {
	p := &a.keyPanel
	p.page, p.err = keyAdd, ""
	p.name, p.pem, p.passphrase, p.savePassphrase, p.generate, p.addingMode, p.genIndex = "", "", "", true, false, 0, 0
}

func (a *app) showKey(id string) {
	p := &a.keyPanel
	k, _ := a.keyByID(id)
	p.page, p.detail, p.rename, p.err = keyDetail, id, k.Name, ""
}

// pickKeyFile 弹出系统的文件选择框（默认在 ~/.ssh），把选中的私钥读进输入框。
// 选择框会阻塞，放在后台，结果回到主线程。
func (a *app) pickKeyFile() {
	pick := a.chooseKeyFile
	go func() {
		path, err := pick()
		a.post(func() {
			p := &a.keyPanel
			if err != nil {
				p.err = err.Error()
				return
			}
			if path == "" {
				return // 用户取消
			}
			info, err := os.Stat(path)
			if err == nil && info.Size() > maxKeyFile {
				err = errors.New(locales.KeyFileTooLarge(filepath.Base(path)))
			}
			var data []byte
			if err == nil {
				data, err = os.ReadFile(path)
			}
			if err != nil {
				p.err = err.Error()
				return
			}
			p.pem, p.err = string(data), ""
			if strings.TrimSpace(p.name) == "" {
				p.name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			}
		})
	}()
}

// defaultPickFile 用系统文件选择框选私钥，默认打开 ~/.ssh 并显示隐藏文件。
func defaultPickFile(open func(dir string) ([]string, error)) func() (string, error) {
	return func() (string, error) {
		dir, _ := expandHome("~/.ssh")
		paths, err := open(dir)
		if err != nil || len(paths) == 0 {
			return "", err
		}
		return paths[0], nil
	}
}

func (a *app) keyNameTaken(name, exceptID string) bool {
	return slices.ContainsFunc(a.keys, func(k keys.Key) bool { return k.Name == name && k.ID != exceptID })
}

// defaultKeyName 是没填名称时的名称。
func (a *app) defaultKeyName(base string) string {
	if base == "" {
		base = locales.PrivateKey()
	}
	name := base
	for i := 2; a.keyNameTaken(name, ""); i++ {
		name = fmt.Sprintf("%s %d", base, i)
	}
	return name
}

// submitAddKey 导入或生成私钥。私钥是秘密，保险库还没有主密码时先设置。
func (a *app) submitAddKey() {
	p := &a.keyPanel
	name := strings.TrimSpace(p.name)
	if name != "" && a.keyNameTaken(name, "") {
		p.err = locales.KeyNameTaken(name)
		return
	}
	if !p.generate {
		k, err := keys.Parse(name, []byte(p.pem), p.passphrase)
		switch {
		case errors.Is(err, keys.ErrPassphraseNeeded):
			p.err = locales.KeyPassphraseNeeded()
			return
		case err != nil:
			p.err = err.Error()
			return
		}
		if k.Name == "" {
			k.Name = a.defaultKeyName(strings.ToLower(k.Label()))
		}
		if !p.savePassphrase {
			k.Passphrase = "" // 不保存口令：连接时再询问
		}
		k.ID = vault.NewID()
		a.requireProtection(func() { a.storeNewKey(k, false) })
		return
	}

	kind := []string{keys.Ed25519, keys.RSA4096}[p.genIndex]
	if name == "" {
		name = a.defaultKeyName(map[string]string{keys.Ed25519: "ed25519", keys.RSA4096: "rsa"}[kind])
	}
	passphrase := p.passphrase
	comment := a.keyComment()
	// 先确保有主密码再生成，免得生成完了用户取消设置，白等一场。
	a.requireProtection(func() {
		a.keyPanel.busy, a.keyPanel.err = true, ""
		go func() {
			k, err := keys.Generate(kind, name, comment, passphrase)
			a.post(func() {
				a.keyPanel.busy = false
				if err != nil {
					a.keyPanel.err = err.Error()
					return
				}
				k.ID = vault.NewID()
				a.storeNewKey(k, true)
			})
		}()
	})
}

// keyComment 是写在新公钥末尾的说明，和 ssh-keygen 一样是“用户@主机名”，
// 方便在服务器的 authorized_keys 里认出这把钥匙。
func (a *app) keyComment() string {
	name := a.localUser
	if u, err := user.Current(); name == "" && err == nil {
		name = u.Username
	}
	if host, err := os.Hostname(); err == nil {
		return name + "@" + strings.TrimSuffix(host, ".local")
	}
	return name
}

func (a *app) storeNewKey(k keys.Key, generated bool) {
	if err := a.apply(keyChange(k)); err != nil {
		a.keyPanel.err = err.Error()
		return
	}
	a.toast(ui.Toast{Title: locales.KeyAdded(k.Name)})
	if a.form.open {
		a.form.keyName = k.Name
		if a.form.auth == 1 {
			a.form.auth = 0
		}
		if a.keyPanel.fromHostForm {
			a.keyPanel = keyPanel{}
			return
		}
	}
	if generated {
		a.showKey(k.ID) // 新生成的钥匙要先把公钥放到服务器上，直接打开详情方便复制
		return
	}
	a.keyPanel.page = keyList
}

// usedBy 返回选用了这把私钥的主机。
func (a *app) usedBy(id string) []hosts.Host {
	var out []hosts.Host
	for _, h := range a.hosts {
		if h.KeyID == id {
			out = append(out, h)
		}
	}
	return out
}

func (a *app) saveKeyDetail() {
	p := &a.keyPanel
	k, ok := a.keyByID(p.detail)
	if !ok {
		p.page = keyList
		return
	}
	name := strings.TrimSpace(p.rename)
	if name == "" {
		p.err = locales.KeyNameRequired()
		return
	}
	if a.keyNameTaken(name, k.ID) {
		p.err = locales.KeyNameTakenShort(name)
		return
	}
	if name != k.Name {
		k.Name = name
		if err := a.apply(keyChange(k)); err != nil {
			p.err = err.Error()
			return
		}
	}
	p.page, p.err = keyList, ""
}

func (a *app) clearPassphrase(id string) {
	if k, ok := a.keyByID(id); ok {
		k.Passphrase = ""
		if err := a.apply(keyChange(k)); err != nil {
			a.keyPanel.err = err.Error()
		}
	}
}

// deleteKey 删除私钥。选用它的主机一并改回“自动”认证，同一次写入，不会出现主机指向不存在的私钥。
func (a *app) deleteKey(k keys.Key) {
	changes := []vault.Change{{ID: k.ID, Delete: true}}
	for _, h := range a.usedBy(k.ID) {
		h.KeyID = ""
		if h.Auth == hosts.AuthKey {
			h.Auth = hosts.AuthAuto
		}
		changes = append(changes, hostChange(h))
	}
	if err := a.apply(changes...); err != nil {
		a.keyPanel.err = err.Error()
		return
	}
	a.keyPanel.page = keyList
	a.toast(ui.Toast{Title: locales.KeyDeleted(k.Name)})
}

func (a *app) copyPublicKey(c *ui.Context, k keys.Key) {
	c.WriteClipboard(k.PublicKey)
	a.toast(ui.Toast{ID: "copy", Title: locales.KeyCopied(k.Name)})
}

// keysDialog 是密钥面板。设置主密码的面板打开时让位，内容保留。
func (a *app) keysDialog(c *ui.Context, colors design.Colors) {
	p := &a.keyPanel
	if !p.open || a.setup.open {
		return
	}
	sheet(c, colors, &p.open, &p.fresh, 520, 22, func() {
		switch p.page {
		case keyAdd:
			a.keyAddPage(c)
		case keyDetail:
			a.keyDetailPage(c, colors)
		default:
			a.keyListPage(c, colors)
		}
	})
	if !p.open {
		a.keyPanel = keyPanel{} // 关闭时丢掉输入框里的私钥和口令
	}
	a.keyDeleteDialog(c)
}

func (a *app) keyListPage(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	_, windowHeight := c.Size()
	maxListH := max(min(windowHeight-240, 340), 120)
	ui.Column(c).Gap(14).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.KeyTitle()).FontSize(design.TextHeading).FontWeight(600).Grow(1)
			ui.PrimaryButton(c, locales.AddPrivateKey()).Key("keysAdd").OnClick(a.startAddKey)
		})
		if len(a.keys) == 0 {
			ui.Column(c).Gap(6).Padding(20, 0).Children(func() {
				ui.Text(c, locales.KeyEmptyTitle()).FontWeight(600)
				ui.Text(c, locales.KeyEmptyBody()).FontSize(design.TextSmall).TextColor(t.TextMuted)
			})
		} else {
			ui.Scroll(c).Key("keysListScroll").MaxHeight(maxListH).Children(func() {
				ui.Column(c).Gap(2).Padding(0, 4, 0, 0).Children(func() {
					for _, k := range a.keys {
						a.keyRow(c, colors, k)
					}
				})
			})
		}
		if p := &a.keyPanel; p.err != "" {
			ui.Text(c, p.err).TextColor(t.Danger)
		}
		ui.Row(c).Justify(ui.End).Children(func() {
			ui.Button(c, locales.Done()).Key("keysClose").OnClick(func() { a.keyPanel = keyPanel{} })
		})
	})
}

func (a *app) sshHosts() []hosts.Host {
	var out []hosts.Host
	for _, h := range a.hosts {
		if h.EffectiveProtocol() == hosts.ProtocolSSH {
			out = append(out, h)
		}
	}
	return out
}

func (a *app) keyRow(c *ui.Context, colors design.Colors, k keys.Key) {
	t := c.Theme()
	menu := func(m *ui.Menu) {
		if m.Item(locales.KeyDetails()).Chosen() {
			a.showKey(k.ID)
		}
		if m.Item(locales.CopyPublicKey()).Chosen() {
			a.copyPublicKey(c, k)
		}
		if k.PublicKey != "" {
			if sshHosts := a.sshHosts(); len(sshHosts) > 0 {
				m.Submenu("部署到主机", func(sub *ui.Menu) {
					for _, h := range sshHosts {
						host := h
						if sub.Item(host.Name).Chosen() {
							a.deployKeyToHost(host, k)
						}
					}
				})
			}
		}
		m.Separator()
		if m.Item(locales.Delete()).Danger().Chosen() {
			a.keyPanel.deleting = &k
		}
	}
	line := ui.Row(c).Key("keyline:"+k.ID).AlignItems(ui.Center).Gap(6).Padding(0, 4, 0, 0).Radius(design.RadiusRow).Transition(design.Hover)
	if line.Hovered() {
		line.Background(colors.Hover)
	}
	line.Children(func() {
		row := ui.ButtonBase(c).Key("key:"+k.ID).Label(k.Name).Grow(1).MinWidth(0).Padding(8, 10).Gap(10).
			Justify(ui.Start).AlignItems(ui.Center)
		row.Children(func() {
			ui.Icon(c, design.Icon("key-round")).FontSize(18).TextColor(t.TextMuted)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, k.Name).FontWeight(500).SingleLine()
				sub := k.Label() + "  " + k.Fingerprint
				if n := len(a.usedBy(k.ID)); n > 0 {
					sub = locales.KeyHostsUsing(k.Label(), n)
				}
				ui.Text(c, sub).FontSize(design.TextSmall).TextColor(t.TextMuted).SingleLine()
			})
		}).OnClick(func() { a.showKey(k.ID) })
		row.PopoverMenu(menu)
		ui.Button(c, locales.CopyPublicKey()).Key("keyCopy:" + k.ID).OnClick(func() { a.copyPublicKey(c, k) })
		more := ui.ButtonBase(c).Key("keyMore:"+k.ID).Label(locales.MoreActions()).Size(26, 26).Radius(6).Shrink(0)
		if more.Hovered() {
			more.Background(colors.Hover)
		}
		more.Children(func() {
			ui.Icon(c, design.Icon("ellipsis")).FontSize(14).TextColor(t.TextMuted)
		}).PopoverMenuButton(menu)
	})
}

func (a *app) keyAddPage(c *ui.Context) {
	t := c.Theme()
	p := &a.keyPanel
	ui.Column(c).Gap(12).Children(func() {
		ui.Text(c, locales.AddPrivateKey()).FontSize(design.TextHeading).FontWeight(600)
		c.Key("keyMode")
		ui.Segmented(c, &p.addingMode, locales.KeyImport(), locales.KeyGenerateOption())
		p.generate = p.addingMode == 1
		ui.Field(c, locales.Name(), func() {
			c.Key("keyName")
			placeholder := locales.KeyNameExample()
			if !p.generate {
				placeholder = locales.KeyNameFromFile()
			}
			ui.TextInput(c, &p.name).Placeholder(placeholder).AutoFocus()
		})
		if p.generate {
			ui.Field(c, locales.KeyType(), func() {
				c.Key("keyType")
				ui.Segmented(c, &p.genIndex, "Ed25519", "RSA 4096")
			}).Description(locales.KeyTypeHint())
			ui.Field(c, locales.KeyPassphraseOptional(), func() {
				c.Key("keyPassphrase")
				ui.TextInput(c, &p.passphrase).Password()
			}).Description(locales.KeyPassphraseHint())
		} else {
			ui.Field(c, locales.PrivateKey(), func() {
				ui.Column(c).Gap(8).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Button(c, locales.KeyChooseFile()).Key("keyPick").OnClick(a.pickKeyFile)
						ui.Text(c, locales.KeyPasteHint()).FontSize(design.TextSmall).TextColor(t.TextMuted)
					})
					c.Key("keyPem")
					ui.TextArea(c, &p.pem).Height(120).Font(design.FontTerminal).FontSize(design.TextSmall).
						Placeholder("-----BEGIN OPENSSH PRIVATE KEY-----")
				})
			}).Description(locales.KeyPemHint())
			if keys.Encrypted([]byte(strings.TrimSpace(p.pem) + "\n")) {
				ui.Field(c, locales.Passphrase(), func() {
					c.Key("keyPassphrase")
					ui.TextInput(c, &p.passphrase).Password()
				})
				c.Key("keySavePassphrase")
				ui.Checkbox(c, &p.savePassphrase, locales.KeySavePassphrase())
			}
		}
		if p.err != "" {
			ui.Text(c, p.err).Key("keyError").TextColor(t.Danger)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			ui.Button(c, locales.Back()).Key("keyBack").OnClick(func() {
				if p.fromHostForm {
					a.keyPanel = keyPanel{}
					return
				}
				p.page, p.err = keyList, ""
			})
			label := locales.Add()
			switch {
			case p.busy:
				label = locales.KeyGenerating()
			case p.generate:
				label = locales.KeyGenerate()
			}
			b := ui.PrimaryButton(c, label).Key("keySubmit").Disabled(p.busy)
			if b.Clicked() && !p.busy {
				a.submitAddKey()
			}
		})
	})
}

func (a *app) keyDetailPage(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	p := &a.keyPanel
	k, ok := a.keyByID(p.detail)
	if !ok {
		p.page = keyList
		return
	}
	ui.Column(c).Gap(12).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, design.Icon("key-round")).FontSize(20).TextColor(t.TextMuted)
			ui.Text(c, k.Name).FontSize(design.TextHeading).FontWeight(600).Grow(1).MinWidth(0).SingleLine()
			ui.Text(c, k.Label()).FontSize(design.TextSmall).TextColor(t.TextMuted)
		})
		ui.Field(c, locales.Name(), func() {
			c.Key("keyRename")
			ui.TextInput(c, &p.rename)
		})
		ui.Field(c, locales.KeyPublicKey(), func() {
			ui.Column(c).Gap(8).Children(func() {
				ui.Text(c, k.PublicKey).Key("keyPublic").Font(design.FontTerminal).FontSize(design.TextSmall).
					Padding(8, 10).Radius(design.RadiusRow).Background(colors.Chip).Selectable()
				ui.Row(c).Gap(8).Children(func() {
					ui.Button(c, locales.CopyPublicKey()).Key("keyCopyDetail").OnClick(func() { a.copyPublicKey(c, k) })
					if k.PublicKey != "" {
						if sshHosts := a.sshHosts(); len(sshHosts) > 0 {
							ui.Button(c, "部署到主机").Key("keyDeployDetail").PopoverMenuButton(func(m *ui.Menu) {
								for _, h := range sshHosts {
									host := h
									if m.Item(host.Name).Chosen() {
										a.deployKeyToHost(host, k)
									}
								}
							})
						}
					}
				})
			})
		}).Description(locales.KeyPublicKeyHint())
		ui.Field(c, locales.KeyFingerprint(), func() {
			ui.Text(c, k.Fingerprint).Font(design.FontTerminal).FontSize(design.TextSmall).Selectable()
		})
		if keys.Encrypted([]byte(k.PrivateKey)) {
			ui.Field(c, locales.Passphrase(), func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if k.Passphrase != "" {
						ui.Text(c, locales.KeyPassphraseSaved()).Grow(1)
						ui.Button(c, locales.Clear()).Key("keyClearPassphrase").OnClick(func() { a.clearPassphrase(k.ID) })
					} else {
						ui.Text(c, locales.KeyPassphraseUnsaved()).TextColor(t.TextMuted)
					}
				})
			})
		}
		if used := a.usedBy(k.ID); len(used) > 0 {
			names := make([]string, len(used))
			for i, h := range used {
				names[i] = h.Name
			}
			ui.Field(c, locales.KeyUsedBy(), func() {
				ui.Text(c, strings.Join(names, locales.NameSeparator()))
			})
		}
		if p.err != "" {
			ui.Text(c, p.err).TextColor(t.Danger)
		}
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c, locales.KeyDelete()).Key("keyDelete").OnClick(func() { p.deleting = &k })
			ui.Box(c).Grow(1)
			ui.Button(c, locales.Back()).Key("keyBack").OnClick(func() { p.page, p.err = keyList, "" })
			ui.PrimaryButton(c, locales.Save()).Key("keySave").OnClick(a.saveKeyDetail)
		})
	})
}

func (a *app) keyDeleteDialog(c *ui.Context) {
	p := &a.keyPanel
	if p.deleting == nil {
		return
	}
	k := *p.deleting
	message := locales.KeyDeleteBody()
	if used := a.usedBy(k.ID); len(used) > 0 {
		names := make([]string, len(used))
		for i, h := range used {
			names[i] = h.Name
		}
		message += locales.KeyDeleteInUse(strings.Join(names, locales.NameSeparator()))
	}
	open := true
	choice := ui.AlertDialog(c, &open, locales.KeyDeleteTitle(k.Name), message, locales.Delete(), locales.Cancel())
	if choice == -1 && open {
		return
	}
	p.deleting = nil
	if choice == 0 {
		a.deleteKey(k)
	}
}
