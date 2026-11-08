package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"serval/core/hosts"
	"serval/core/keys"
	"serval/core/vault"
	"serval/design"
	"serval/locales"

	"github.com/egoist/mygo/ui"
)

// defaultGroup 是没填分组的主机所在的分组。
const defaultGroup = "我的主机"

// noKey 是私钥下拉框里“不用私钥”的选项。
// 选项文字同时是表单里的哨兵值，渲染和比较都走这里，不能在 init 时钉死语言。
func noKey() string { return locales.HostNoKey() }

// 表单里认证方式的顺序，和 Segmented 的选项一一对应。
var authModes = []string{hosts.AuthAuto, hosts.AuthPassword, hosts.AuthKey}

// hostForm 是添加、编辑和复制主机共用的表单。
type hostForm struct {
	open, fresh bool
	// editing 是正在编辑的主机 ID，新建（包括复制）时为空。
	editing string
	// copyOf 是复制来源的 ID：没填新密码时，沿用来源保存的密码。
	copyOf string
	// tempHostID 是当前正在保存的临时主机 ID。
	tempHostID string

	name, address, port, user, group string
	auth                             int    // authModes 的下标
	keyName                          string // 选中的私钥名称，noKey 表示不用
	production                       bool

	// jumps 是按顺序选择的跳板机（主机名称；旧记录里直接填写的地址原样保留）。
	// 默认不显示，点“添加跳板机”后才出现。
	jumps     []string
	showJumps bool
	// proxyName 是选中的代理名称；legacyProxy 是旧记录里直接填写的代理地址。
	// 代理同样默认不显示，点“使用代理”后才出现。
	proxyName   string
	legacyProxy string
	showProxy   bool

	// password 是新输入的密码，留空表示保持保存的密码不变。
	password string
	// passwordSaved 表示已经保存了密码；clearPassword 表示用户要求清除。
	passwordSaved, clearPassword bool

	err string
}

// openAdd 打开空白的添加主机表单。
func (a *app) openAdd() {
	a.form = hostForm{open: true, fresh: true, keyName: noKey()}
	if len(a.keys) == 1 {
		a.form.keyName = a.keys[0].Name // 只有一把私钥时直接选上，添加完私钥就能用
	}
}

// formFrom 用已有主机的信息填好表单。
func (a *app) formFrom(h hosts.Host) hostForm {
	f := hostForm{
		open: true, fresh: true,
		name: h.Name, address: h.Address, user: h.User, group: h.Group,
		production:    h.Production,
		passwordSaved: h.Password != "", keyName: noKey(),
		jumps: hosts.SplitJumps(h.ProxyJump),
	}
	f.showJumps = len(f.jumps) > 0
	if p, ok := a.proxyByID(h.ProxyID); ok {
		f.proxyName, f.showProxy = p.Name, true
	} else if h.ProxyID != "" {
		f.showProxy = true // 引用的代理已删除：展开让用户重新选择
	} else if h.Proxy != "" {
		f.legacyProxy, f.showProxy = h.Proxy, true
		f.proxyName = legacyProxyLabel(h.Proxy)
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
	a.form.name = a.uniqueName(locales.HostCopyName(h.Name))
}

// openSaveHost 从临时快速连接的主机打开添加主机表单。
func (a *app) openSaveHost(h hosts.Host) {
	a.form = a.formFrom(h)
	a.form.editing = ""
	a.form.copyOf = ""
	a.form.tempHostID = h.ID
	a.form.name = a.uniqueName(h.Name)
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
		f.err = locales.HostAddressRequired()
		return
	}
	port := 22
	if text := strings.TrimSpace(f.port); text != "" {
		parsed, err := strconv.Atoi(text)
		if err != nil || parsed < 1 || parsed > 65535 {
			f.err = locales.HostPortInvalid()
			return
		}
		port = parsed
	}
	user := strings.TrimSpace(f.user)
	if user == "" {
		f.err = locales.HostUserRequired()
		return
	}
	auth := authModes[f.auth]
	keyID := ""
	if auth != hosts.AuthPassword && f.keyName != noKey() {
		i := slices.IndexFunc(a.keys, func(k keys.Key) bool { return k.Name == f.keyName })
		if i < 0 {
			f.err = locales.HostKeyGone()
			return
		}
		keyID = a.keys[i].ID
	}
	if auth == hosts.AuthKey && keyID == "" {
		f.err = locales.HostKeyRequired()
		return
	}
	name := strings.TrimSpace(f.name)
	if name == "" {
		name = address
	}
	if a.nameTaken(name, f.editing) {
		f.err = locales.HostNameTaken(name)
		return
	}
	group := strings.TrimSpace(f.group)
	if group == "" {
		group = defaultGroup
	}
	proxyID, legacyProxy, err := a.formProxy(f)
	if err != nil {
		f.err = err.Error()
		return
	}
	var jumps []string
	if f.showJumps {
		for i, j := range f.jumps {
			if j == pickHost() || strings.TrimSpace(j) == "" {
				f.err = locales.RouteJumpMissing(i + 1)
				return
			}
			jumps = append(jumps, j)
		}
	}

	h := hosts.Host{
		Name: name, Address: address, Port: port, User: user, Auth: auth, KeyID: keyID,
		Group: group, Production: f.production,
		ProxyJump: strings.Join(jumps, ","), ProxyID: proxyID, Proxy: legacyProxy,
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
		f.err = locales.RouteJumpError(err.Error())
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
	if f.tempHostID != "" {
		for _, tab := range a.tabs {
			if tab.host.ID == f.tempHostID {
				tab.host = h
			}
		}
	}
	title := locales.HostAdded(name)
	if f.editing != "" {
		title = locales.HostSaved(name)
	}
	a.form = hostForm{}
	a.toast(ui.Toast{Title: title})
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
	a.removeRecentHost(h.ID)
	a.toast(ui.Toast{Title: locales.HostDeleted(h.Name)})
}

// hostMenu 是主机的右键菜单，侧栏行尾的“更多”按钮打开同一个菜单。
func (a *app) hostMenu(h hosts.Host) func(m *ui.Menu) {
	return func(m *ui.Menu) {
		if m.Item(locales.Connect()).Chosen() {
			a.open(h)
		}
		if m.Item(locales.Edit()).Chosen() {
			a.openEdit(h)
		}
		if m.Item(locales.HostDuplicate()).Chosen() {
			a.openDuplicate(h)
		}
		// 服务器重装、换了密钥时用。没有信任记录时没什么可重置的。
		if m.Item(locales.HostResetKey()).Disabled(!a.hasTrustRecord(h)).Chosen() {
			a.resetHostKey(h)
		}
		m.Separator()
		if m.Item(locales.Delete()).Danger().Chosen() {
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
	message := locales.HostDeleteBody()
	if names := a.dependents(h); len(names) > 0 {
		message += locales.HostDeleteJumps(strings.Join(names, locales.NameSeparator()))
	}
	open := true
	choice := ui.AlertDialog(c, &open, locales.HostDeleteTitle(h.Name), message, locales.Delete(), locales.Cancel())
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
	// 从表单里新建代理时代理面板盖在上面，表单让位，内容保留。
	if !a.form.open || a.setup.open || a.proxyPanel.open {
		return
	}
	t := c.Theme()
	f := &a.form
	title, submit := locales.AddHost(), locales.Add()
	switch {
	case f.editing != "":
		title, submit = locales.HostEdit(), locales.Save()
	case f.copyOf != "":
		title = locales.HostDuplicateTitle()
	}
	half := func(fn func()) { ui.Column(c).Grow(1).Basis(0).MinWidth(0).Children(fn) }
	sheet(c, colors, &f.open, &f.fresh, 440, 22, func() {
		ui.Column(c).Gap(12).Children(func() {
			ui.Text(c, title).FontSize(design.TextHeading).FontWeight(600)
			ui.Row(c).Gap(10).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					ui.Field(c, locales.Address(), func() {
						c.Key("addAddress")
						ui.TextInput(c, &f.address).Placeholder("example.com").AutoFocus()
					})
				})
				ui.Column(c).Width(76).Children(func() {
					ui.Field(c, locales.Port(), func() {
						c.Key("addPort")
						ui.TextInput(c, &f.port).Placeholder("22")
					})
				})
			})
			ui.Field(c, locales.HostUsername(), func() {
				c.Key("addUser")
				ui.TextInput(c, &f.user).Placeholder("deploy")
			})
			ui.Row(c).Gap(10).Children(func() {
				half(func() {
					nameField := ui.Field(c, locales.Name(), func() {
						c.Key("addName")
						ui.TextInput(c, &f.name).Placeholder(locales.HostNamePlaceholder())
					})
					if n := a.renamingJumpCount(f); n > 0 {
						nameField.Description(locales.HostRenameJumps(n))
					}
				})
				half(func() {
					ui.Field(c, locales.HostGroup(), func() {
						c.Key("addGroup")
						ui.TextInput(c, &f.group).Placeholder(defaultGroup)
					})
				})
			})

			descriptions := []string{
				locales.HostAuthAutoHint(),
				locales.HostAuthPasswordHint(),
				locales.HostAuthKeyHint(),
			}
			ui.Field(c, locales.HostAuth(), func() {
				c.Key("addAuth")
				ui.Segmented(c, &f.auth, locales.HostAuthAuto(), locales.Password(), locales.PrivateKey())
			}).Description(descriptions[f.auth])
			if f.auth == 1 {
				a.passwordField(c, f)
			} else {
				a.keyField(c, f)
			}

			a.routeFields(c, colors, f)
			c.Key("addProduction")
			ui.Checkbox(c, &f.production, locales.HostProduction())
			if f.err != "" {
				ui.Text(c, f.err).Key("addError").TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				ui.Button(c, locales.Cancel()).Key("addCancel").OnClick(func() { a.form = hostForm{} })
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
		ui.Field(c, locales.PrivateKey(), func() {
			ui.Text(c, locales.HostNoPrivateKey()).
				FontSize(design.TextSmall).TextColor(t.TextMuted)
		})
		return
	}
	options := []string{noKey()}
	for _, k := range a.keys {
		options = append(options, k.Name)
	}
	if !slices.Contains(options, f.keyName) {
		f.keyName = noKey()
	}
	ui.Field(c, locales.PrivateKey(), func() {
		c.Key("addKey")
		ui.Select(c, &f.keyName, options)
	})
}

// passwordField 是密码输入框。已保存密码时，留空表示保持不变，并可以清除。
func (a *app) passwordField(c *ui.Context, f *hostForm) {
	placeholder := locales.HostPasswordPrompt()
	switch {
	case f.passwordSaved && f.clearPassword:
		placeholder = locales.HostClearPasswordPending()
	case f.passwordSaved:
		placeholder = locales.HostPasswordUnchanged()
	}
	ui.Row(c).Gap(8).AlignItems(ui.End).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			ui.Field(c, locales.Password(), func() {
				c.Key("addPassword")
				ui.TextInput(c, &f.password).Password().Placeholder(placeholder)
			})
		})
		if f.passwordSaved {
			text := locales.Clear()
			if f.clearPassword {
				text = locales.HostUndoClear()
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
