package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"serval/core/hosts"
	"serval/core/keys"
	"serval/core/sshconn"
	"serval/core/vault"
	"serval/design"

	"github.com/egoist/mygo/ui"
)

// defaultGroup 是没填分组的主机所在的分组。
const defaultGroup = "我的主机"

// noKey 是私钥下拉框里“不用私钥”的选项。
const noKey = "不使用"

// 表单里认证方式的顺序，和 Segmented 的选项一一对应。
var authModes = []string{hosts.AuthAuto, hosts.AuthPassword, hosts.AuthKey}

// hostForm 是添加、编辑和复制主机共用的表单。
type hostForm struct {
	open, fresh bool
	// editing 是正在编辑的主机 ID，新建（包括复制）时为空。
	editing string
	// copyOf 是复制来源的 ID：没填新密码时，沿用来源保存的密码。
	copyOf string

	name, address, port, user, group string
	auth                             int    // authModes 的下标
	keyName                          string // 选中的私钥名称，noKey 表示不用
	jumps, proxy                     string
	production                       bool

	// password 是新输入的密码，留空表示保持保存的密码不变。
	password string
	// passwordSaved 表示已经保存了密码；clearPassword 表示用户要求清除。
	passwordSaved, clearPassword bool

	err string
}

// openAdd 打开空白的添加主机表单。
func (a *app) openAdd() {
	a.form = hostForm{open: true, fresh: true, keyName: noKey}
	if len(a.keys) == 1 {
		a.form.keyName = a.keys[0].Name // 只有一把私钥时直接选上，添加完私钥就能用
	}
}

// formFrom 用已有主机的信息填好表单。
func (a *app) formFrom(h hosts.Host) hostForm {
	f := hostForm{
		open: true, fresh: true,
		name: h.Name, address: h.Address, user: h.User, group: h.Group,
		jumps: h.ProxyJump, proxy: h.Proxy, production: h.Production,
		passwordSaved: h.Password != "", keyName: noKey,
	}
	if k, ok := a.keyByID(h.KeyID); ok {
		f.keyName = k.Name
	}
	if h.Port != 0 && h.Port != 22 {
		f.port = strconv.Itoa(h.Port)
	}
	f.auth = max(slices.Index(authModes, h.Auth), 0)
	return f
}

// openEdit 编辑主机。
func (a *app) openEdit(h hosts.Host) {
	a.form = a.formFrom(h)
	a.form.editing = h.ID
}

// openDuplicate 以 h 为模板新建主机。
func (a *app) openDuplicate(h hosts.Host) {
	a.form = a.formFrom(h)
	a.form.copyOf = h.ID
	a.form.name = a.uniqueName(h.Name + " 副本")
}

// uniqueName 在 name 已被占用时加上序号。跳板机按名称查找主机，名称必须唯一。
func (a *app) uniqueName(name string) string {
	candidate := name
	for i := 2; a.nameTaken(candidate, ""); i++ {
		candidate = fmt.Sprintf("%s %d", name, i)
	}
	return candidate
}

func (a *app) nameTaken(name, exceptID string) bool {
	return slices.ContainsFunc(a.hosts, func(h hosts.Host) bool { return h.Name == name && h.ID != exceptID })
}

// saveForm 校验表单并保存主机。表单里填了密码而保险库还没有主密码时，先设置主密码。
func (a *app) saveForm() {
	f := &a.form
	address := strings.TrimSpace(f.address)
	if address == "" {
		f.err = "请填写地址。"
		return
	}
	port := 22
	if text := strings.TrimSpace(f.port); text != "" {
		parsed, err := strconv.Atoi(text)
		if err != nil || parsed < 1 || parsed > 65535 {
			f.err = "端口必须是 1 到 65535 之间的数字。"
			return
		}
		port = parsed
	}
	user := strings.TrimSpace(f.user)
	if user == "" {
		f.err = "请填写用户名。"
		return
	}
	auth := authModes[f.auth]
	keyID := ""
	if auth != hosts.AuthPassword && f.keyName != noKey {
		i := slices.IndexFunc(a.keys, func(k keys.Key) bool { return k.Name == f.keyName })
		if i < 0 {
			f.err = "选中的私钥已经不存在，请重新选择。"
			return
		}
		keyID = a.keys[i].ID
	}
	if auth == hosts.AuthKey && keyID == "" {
		f.err = "选择私钥登录时，请选一把私钥。还没有私钥的话，先在侧栏的“密钥”里添加。"
		return
	}
	name := strings.TrimSpace(f.name)
	if name == "" {
		name = address
	}
	if a.nameTaken(name, f.editing) {
		f.err = fmt.Sprintf("已经有一台叫 %s 的主机。跳板机按名称查找主机，请换一个名称。", name)
		return
	}
	group := strings.TrimSpace(f.group)
	if group == "" {
		group = defaultGroup
	}
	proxyText := strings.TrimSpace(f.proxy)
	proxy, err := sshconn.ParseProxy(proxyText)
	if err != nil {
		f.err = "代理：" + err.Error()
		return
	}
	if proxy != nil && proxy.Password != "" {
		f.err = "代理地址里不能带密码。"
		return
	}

	h := hosts.Host{
		Name: name, Address: address, Port: port, User: user, Auth: auth, KeyID: keyID,
		Group: group, Production: f.production,
		ProxyJump: strings.Join(hosts.SplitJumps(f.jumps), ","), Proxy: proxyText,
	}
	// 密码：新输入的覆盖；要求清除的清除；密码以外的登录方式也保留已有密码（自动模式会用到），
	// 只有“私钥”模式用不上密码，清掉。复制来的主机沿用来源的密码。
	var previous hosts.Host
	if f.editing != "" {
		previous, _ = a.hostByID(f.editing)
	} else if f.copyOf != "" {
		previous, _ = a.hostByID(f.copyOf)
	}
	switch {
	case auth == hosts.AuthKey || f.clearPassword:
		h.Password = ""
	case f.password != "":
		h.Password = f.password
	default:
		h.Password = previous.Password
	}
	if f.editing != "" {
		h.ID = f.editing
	} else {
		h.ID = vault.NewID()
	}
	all := slices.DeleteFunc(slices.Clone(a.hosts), func(x hosts.Host) bool { return x.ID == h.ID })
	changes := []vault.Change{hostChange(h)}
	if f.editing != "" && previous.Name != "" && previous.Name != h.Name {
		for i := range all {
			if updated, ok := hosts.RenameJump(all[i].ProxyJump, previous.Name, h.Name); ok {
				all[i].ProxyJump = updated
				changes = append(changes, hostChange(all[i]))
			}
		}
	}
	// 保存前就把链路完整解析一遍：写错的地址、互相引用成环，在这里说清楚，
	// 而不是等到连接时才失败。
	if _, err := hosts.ResolveRoute(h, append(all, h), a.localUser); err != nil {
		f.err = "跳板机：" + err.Error()
		return
	}
	if h.Password != "" && a.vault.State() == vault.Unprotected {
		a.requireProtection(a.saveForm)
		return
	}
	if err := a.apply(changes...); err != nil {
		f.err = err.Error()
		return
	}
	verb := "已添加 "
	if f.editing != "" {
		verb = "已保存 "
	}
	a.form = hostForm{}
	a.toast(ui.Toast{Title: verb + name})
}

// askDelete 请用户确认删除主机。
func (a *app) askDelete(h hosts.Host) { a.deleting = &h }

// dependents 返回经 h 跳转的主机名称。
func (a *app) dependents(h hosts.Host) []string {
	var names []string
	for _, other := range a.hosts {
		if other.ID != h.ID && slices.Contains(hosts.SplitJumps(other.ProxyJump), h.Name) {
			names = append(names, other.Name)
		}
	}
	return names
}

// renamingJumpCount 返回编辑表单改名时将同步更新跳板机引用的主机数量。
func (a *app) renamingJumpCount(f *hostForm) int {
	if f.editing == "" {
		return 0
	}
	previous, ok := a.hostByID(f.editing)
	if !ok || previous.Name == "" {
		return 0
	}
	next := strings.TrimSpace(f.name)
	if next == "" {
		next = strings.TrimSpace(f.address)
	}
	if next == "" || next == previous.Name {
		return 0
	}
	return len(a.dependents(previous))
}

// deleteHost 删除主机（连同保存的密码）。已打开的会话不受影响。
func (a *app) deleteHost(h hosts.Host) {
	if err := a.apply(vault.Change{ID: h.ID, Delete: true}); err != nil {
		a.notice = err.Error()
		return
	}
	a.toast(ui.Toast{Title: "已删除 " + h.Name})
}

// hostMenu 是主机的右键菜单，侧栏行尾的“更多”按钮打开同一个菜单。
func (a *app) hostMenu(h hosts.Host) func(m *ui.Menu) {
	return func(m *ui.Menu) {
		if m.Item("连接").Chosen() {
			a.open(h)
		}
		if m.Item("编辑").Chosen() {
			a.openEdit(h)
		}
		if m.Item("复制").Chosen() {
			a.openDuplicate(h)
		}
		// 服务器重装、换了密钥时用。没有信任记录时没什么可重置的。
		if m.Item("重置主机密钥").Disabled(!a.hasTrustRecord(h)).Chosen() {
			a.resetHostKey(h)
		}
		m.Separator()
		if m.Item("删除").Danger().Chosen() {
			a.askDelete(h)
		}
	}
}

// deleteDialog 确认删除。和主机指纹确认一样用系统风格的警告框，默认按钮是“取消”。
func (a *app) deleteDialog(c *ui.Context) {
	if a.deleting == nil {
		return
	}
	h := *a.deleting
	message := "删除后无法恢复，为它保存的密码也会一并删除。已打开的会话不受影响。"
	if names := a.dependents(h); len(names) > 0 {
		message += fmt.Sprintf("\n\n%s 经它跳转，删除后将无法连接。", strings.Join(names, "、"))
	}
	open := true
	choice := ui.AlertDialog(c, &open, "删除 "+h.Name+"？", message, "删除", "取消")
	if choice == -1 && open {
		return
	}
	a.deleting = nil
	if choice == 0 {
		a.deleteHost(h)
	}
}

// hostDialog 是添加、编辑、复制主机的表单面板。设置主密码的面板打开时让位，表单内容保留。
func (a *app) hostDialog(c *ui.Context, colors design.Colors) {
	if !a.form.open || a.setup.open {
		return
	}
	t := c.Theme()
	f := &a.form
	title, submit := "添加主机", "添加"
	switch {
	case f.editing != "":
		title, submit = "编辑主机", "保存"
	case f.copyOf != "":
		title = "复制主机"
	}
	half := func(fn func()) { ui.Column(c).Grow(1).Basis(0).MinWidth(0).Children(fn) }
	sheet(c, colors, &f.open, &f.fresh, 440, 22, func() {
		ui.Column(c).Gap(12).Children(func() {
			ui.Text(c, title).FontSize(design.TextHeading).FontWeight(600)
			ui.Row(c).Gap(10).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					ui.Field(c, "地址", func() {
						c.Key("addAddress")
						ui.TextInput(c, &f.address).Placeholder("example.com").AutoFocus()
					})
				})
				ui.Column(c).Width(76).Children(func() {
					ui.Field(c, "端口", func() {
						c.Key("addPort")
						ui.TextInput(c, &f.port).Placeholder("22")
					})
				})
			})
			ui.Field(c, "用户名", func() {
				c.Key("addUser")
				ui.TextInput(c, &f.user).Placeholder("deploy")
			})
			ui.Row(c).Gap(10).Children(func() {
				half(func() {
					nameField := ui.Field(c, "名称", func() {
						c.Key("addName")
						ui.TextInput(c, &f.name).Placeholder("留空时使用地址")
					})
					if n := a.renamingJumpCount(f); n > 0 {
						nameField.Description(fmt.Sprintf("将同时更新 %d 台主机的跳板机设置。", n))
					}
				})
				half(func() {
					ui.Field(c, "分组", func() {
						c.Key("addGroup")
						ui.TextInput(c, &f.group).Placeholder(defaultGroup)
					})
				})
			})

			descriptions := []string{
				"依次尝试选定的私钥、ssh-agent，都不行时再询问密码。",
				"只用密码登录。",
				"只用选定的私钥登录。",
			}
			ui.Field(c, "认证方式", func() {
				c.Key("addAuth")
				ui.Segmented(c, &f.auth, "自动", "密码", "私钥")
			}).Description(descriptions[f.auth])
			if f.auth == 1 {
				a.passwordField(c, f)
			} else {
				a.keyField(c, f)
			}

			ui.Row(c).Gap(10).Children(func() {
				half(func() {
					ui.Field(c, "跳板机", func() {
						c.Key("addJumps")
						ui.TextInput(c, &f.jumps).Placeholder("bastion")
					}).Description("多台用逗号分隔。")
				})
				half(func() {
					ui.Field(c, "代理", func() {
						c.Key("addProxy")
						ui.TextInput(c, &f.proxy).Placeholder("socks5://127.0.0.1:1080")
					}).Description("socks5 或 http，仅第一跳。")
				})
			})
			c.Key("addProduction")
			ui.Checkbox(c, &f.production, "这是生产环境")
			if f.err != "" {
				ui.Text(c, f.err).Key("addError").TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				ui.Button(c, "取消").Key("addCancel").OnClick(func() { a.form = hostForm{} })
				ui.PrimaryButton(c, submit).Key("addSubmit").OnClick(a.saveForm)
			})
		})
	})
	// 点击外部或按 Esc 关闭时，不把输入的密码留在内存里。
	if !a.form.open {
		a.form = hostForm{}
	}
}

// keyField 是从密钥库里选私钥的下拉框。
func (a *app) keyField(c *ui.Context, f *hostForm) {
	t := c.Theme()
	if len(a.keys) == 0 {
		ui.Field(c, "私钥", func() {
			ui.Text(c, "还没有私钥。在侧栏的“密钥”里添加私钥文件或生成新密钥，就能在这里选择。").
				FontSize(design.TextSmall).TextColor(t.TextMuted)
		})
		return
	}
	options := []string{noKey}
	for _, k := range a.keys {
		options = append(options, k.Name)
	}
	if !slices.Contains(options, f.keyName) {
		f.keyName = noKey
	}
	ui.Field(c, "私钥", func() {
		c.Key("addKey")
		ui.Select(c, &f.keyName, options)
	})
}

// passwordField 是密码输入框。已保存密码时，留空表示保持不变，并可以清除。
func (a *app) passwordField(c *ui.Context, f *hostForm) {
	placeholder := "留空时连接时询问"
	switch {
	case f.passwordSaved && f.clearPassword:
		placeholder = "保存后清除已保存的密码"
	case f.passwordSaved:
		placeholder = "已保存，留空保持不变"
	}
	ui.Row(c).Gap(8).AlignItems(ui.End).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			ui.Field(c, "密码", func() {
				c.Key("addPassword")
				ui.TextInput(c, &f.password).Password().Placeholder(placeholder)
			})
		})
		if f.passwordSaved {
			text := "清除"
			if f.clearPassword {
				text = "撤销清除"
			}
			ui.Button(c, text).Key("addPasswordClear").OnClick(func() {
				f.clearPassword = !f.clearPassword
				if f.clearPassword {
					f.password = ""
				}
			})
		}
	})
}
