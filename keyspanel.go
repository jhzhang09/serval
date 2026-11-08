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
				err = fmt.Errorf("%s 太大了，不像是私钥文件", filepath.Base(path))
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
		base = "私钥"
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
		p.err = fmt.Sprintf("已经有一把叫 %s 的私钥了，请换一个名称。", name)
		return
	}
	if !p.generate {
		k, err := keys.Parse(name, []byte(p.pem), p.passphrase)
		switch {
		case errors.Is(err, keys.ErrPassphraseNeeded):
			p.err = "这把私钥加了口令，请在下面输入口令。"
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
	a.toast(ui.Toast{Title: "已添加私钥 " + k.Name})
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
		p.err = "请填写名称。"
		return
	}
	if a.keyNameTaken(name, k.ID) {
		p.err = fmt.Sprintf("已经有一把叫 %s 的私钥了。", name)
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
	a.toast(ui.Toast{Title: "已删除私钥 " + k.Name})
}

func (a *app) copyPublicKey(c *ui.Context, k keys.Key) {
	c.WriteClipboard(k.PublicKey)
	a.toast(ui.Toast{ID: "copy", Title: "已复制 " + k.Name + " 的公钥"})
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
	ui.Column(c).Gap(14).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "密钥").FontSize(design.TextHeading).FontWeight(600).Grow(1)
			ui.PrimaryButton(c, "添加私钥").Key("keysAdd").OnClick(a.startAddKey)
		})
		if len(a.keys) == 0 {
			ui.Column(c).Gap(6).Padding(20, 0).Children(func() {
				ui.Text(c, "还没有私钥").FontWeight(600)
				ui.Text(c, "添加已有的私钥文件，或者生成一把新的。私钥加密保存在 Serval 的保险库里，"+
					"之后在主机里选它就能登录。").FontSize(design.TextSmall).TextColor(t.TextMuted)
			})
		}
		ui.Column(c).Gap(2).Children(func() {
			for _, k := range a.keys {
				a.keyRow(c, colors, k)
			}
		})
		if p := &a.keyPanel; p.err != "" {
			ui.Text(c, p.err).TextColor(t.Danger)
		}
		ui.Row(c).Justify(ui.End).Children(func() {
			ui.Button(c, "完成").Key("keysClose").OnClick(func() { a.keyPanel = keyPanel{} })
		})
	})
}

func (a *app) keyRow(c *ui.Context, colors design.Colors, k keys.Key) {
	t := c.Theme()
	menu := func(m *ui.Menu) {
		if m.Item("详情").Chosen() {
			a.showKey(k.ID)
		}
		if m.Item("复制公钥").Chosen() {
			a.copyPublicKey(c, k)
		}
		m.Separator()
		if m.Item("删除").Danger().Chosen() {
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
					sub = fmt.Sprintf("%s  %d 台主机在用", k.Label(), n)
				}
				ui.Text(c, sub).FontSize(design.TextSmall).TextColor(t.TextMuted).SingleLine()
			})
		}).OnClick(func() { a.showKey(k.ID) })
		row.PopoverMenu(menu)
		ui.Button(c, "复制公钥").Key("keyCopy:" + k.ID).OnClick(func() { a.copyPublicKey(c, k) })
		more := ui.ButtonBase(c).Key("keyMore:"+k.ID).Label("更多操作").Size(26, 26).Radius(6).Shrink(0)
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
		ui.Text(c, "添加私钥").FontSize(design.TextHeading).FontWeight(600)
		c.Key("keyMode")
		ui.Segmented(c, &p.addingMode, "导入已有私钥", "生成新密钥")
		p.generate = p.addingMode == 1
		ui.Field(c, "名称", func() {
			c.Key("keyName")
			placeholder := "例如 工作笔记本"
			if !p.generate {
				placeholder = "留空时使用文件名"
			}
			ui.TextInput(c, &p.name).Placeholder(placeholder).AutoFocus()
		})
		if p.generate {
			ui.Field(c, "类型", func() {
				c.Key("keyType")
				ui.Segmented(c, &p.genIndex, "Ed25519", "RSA 4096")
			}).Description("Ed25519 更短更快，绝大多数服务器都支持；只有很老的系统才需要 RSA。")
			ui.Field(c, "口令（可选）", func() {
				c.Key("keyPassphrase")
				ui.TextInput(c, &p.passphrase).Password()
			}).Description("给私钥本身再加一道锁，导出到别处时仍受保护。口令也保存在保险库里，连接时不用输入。")
		} else {
			ui.Field(c, "私钥", func() {
				ui.Column(c).Gap(8).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Button(c, "选择文件…").Key("keyPick").OnClick(a.pickKeyFile)
						ui.Text(c, "或者把私钥内容粘贴到下面").FontSize(design.TextSmall).TextColor(t.TextMuted)
					})
					c.Key("keyPem")
					ui.TextArea(c, &p.pem).Height(120).Font(design.FontTerminal).FontSize(design.TextSmall).
						Placeholder("-----BEGIN OPENSSH PRIVATE KEY-----")
				})
			}).Description("私钥内容加密保存在 Serval 的保险库里，原文件删掉或移动都不影响使用。")
			if keys.Encrypted([]byte(strings.TrimSpace(p.pem) + "\n")) {
				ui.Field(c, "口令", func() {
					c.Key("keyPassphrase")
					ui.TextInput(c, &p.passphrase).Password()
				})
				c.Key("keySavePassphrase")
				ui.Checkbox(c, &p.savePassphrase, "保存口令（不保存时，每次连接都询问）")
			}
		}
		if p.err != "" {
			ui.Text(c, p.err).Key("keyError").TextColor(t.Danger)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			ui.Button(c, "返回").Key("keyBack").OnClick(func() { p.page, p.err = keyList, "" })
			label := "添加"
			switch {
			case p.busy:
				label = "正在生成…"
			case p.generate:
				label = "生成"
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
		ui.Field(c, "名称", func() {
			c.Key("keyRename")
			ui.TextInput(c, &p.rename)
		})
		ui.Field(c, "公钥", func() {
			ui.Column(c).Gap(8).Children(func() {
				ui.Text(c, k.PublicKey).Key("keyPublic").Font(design.FontTerminal).FontSize(design.TextSmall).
					Padding(8, 10).Radius(design.RadiusRow).Background(colors.Chip).Selectable()
				ui.Row(c).Children(func() {
					ui.Button(c, "复制公钥").Key("keyCopyDetail").OnClick(func() { a.copyPublicKey(c, k) })
				})
			})
		}).Description("把公钥加到服务器的 ~/.ssh/authorized_keys 里，就能用这把私钥登录。")
		ui.Field(c, "指纹", func() {
			ui.Text(c, k.Fingerprint).Font(design.FontTerminal).FontSize(design.TextSmall).Selectable()
		})
		if keys.Encrypted([]byte(k.PrivateKey)) {
			ui.Field(c, "口令", func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if k.Passphrase != "" {
						ui.Text(c, "已保存，连接时不用输入").Grow(1)
						ui.Button(c, "清除").Key("keyClearPassphrase").OnClick(func() { a.clearPassphrase(k.ID) })
					} else {
						ui.Text(c, "未保存，连接时询问（可以在询问时勾选记住）").TextColor(t.TextMuted)
					}
				})
			})
		}
		if used := a.usedBy(k.ID); len(used) > 0 {
			names := make([]string, len(used))
			for i, h := range used {
				names[i] = h.Name
			}
			ui.Field(c, "使用它的主机", func() {
				ui.Text(c, strings.Join(names, "、"))
			})
		}
		if p.err != "" {
			ui.Text(c, p.err).TextColor(t.Danger)
		}
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c, "删除私钥").Key("keyDelete").OnClick(func() { p.deleting = &k })
			ui.Box(c).Grow(1)
			ui.Button(c, "返回").Key("keyBack").OnClick(func() { p.page, p.err = keyList, "" })
			ui.PrimaryButton(c, "保存").Key("keySave").OnClick(a.saveKeyDetail)
		})
	})
}

func (a *app) keyDeleteDialog(c *ui.Context) {
	p := &a.keyPanel
	if p.deleting == nil {
		return
	}
	k := *p.deleting
	message := "删除后无法恢复。如果服务器上只登记了这把钥匙，删除后就登录不了，请先确认有别的登录方式。"
	if used := a.usedBy(k.ID); len(used) > 0 {
		names := make([]string, len(used))
		for i, h := range used {
			names[i] = h.Name
		}
		message += fmt.Sprintf("\n\n%s 正在使用它，删除后这些主机改为“自动”认证。", strings.Join(names, "、"))
	}
	open := true
	choice := ui.AlertDialog(c, &open, "删除私钥 "+k.Name+"？", message, "删除", "取消")
	if choice == -1 && open {
		return
	}
	p.deleting = nil
	if choice == 0 {
		a.deleteKey(k)
	}
}
