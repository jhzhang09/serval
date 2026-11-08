package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"serval/core/hosts"
	"serval/core/vault"
	"serval/design"

	"github.com/egoist/mygo/ui"
)

// proxyPanel 是“代理”面板：独立管理代理配置，主机表单里只做选择。
// 一个代理通常给很多台主机用，改地址、改密码只需改一处。
type proxyPanel struct {
	open, fresh bool
	// editing 为真时显示编辑页；id 为空表示新建。
	editing bool
	id      string
	// fromForm 表示从主机表单里打开的“新建代理”：保存后回到表单并选中它。
	fromForm bool

	name, address, port, user, password string
	kind                                int // 0 SOCKS5，1 HTTP
	passwordSaved, clearPassword        bool
	err                                 string

	deleting *hosts.Proxy
}

var proxyKinds = []string{hosts.ProxySOCKS5, hosts.ProxyHTTP}

func (a *app) openProxies() { a.proxyPanel = proxyPanel{open: true, fresh: true} }

// openProxyAdd 打开新建代理页。fromForm 为真时保存或返回后回到主机表单。
func (a *app) openProxyAdd(fromForm bool) {
	a.proxyPanel = proxyPanel{open: true, fresh: true, editing: true, fromForm: fromForm}
}

func (a *app) editProxy(p hosts.Proxy) {
	pp := &a.proxyPanel
	pp.editing, pp.id, pp.err = true, p.ID, ""
	pp.name, pp.address, pp.user, pp.password = p.Name, p.Address, p.User, ""
	pp.port = strconv.Itoa(p.Port)
	pp.kind = max(slices.Index(proxyKinds, p.Kind), 0)
	pp.passwordSaved, pp.clearPassword = p.Password != "", false
}

// proxyUsers 返回选用了这个代理的主机。
func (a *app) proxyUsers(id string) []hosts.Host {
	var out []hosts.Host
	for _, h := range a.hosts {
		if h.ProxyID == id {
			out = append(out, h)
		}
	}
	return out
}

func hostNames(list []hosts.Host) string {
	names := make([]string, len(list))
	for i, h := range list {
		names[i] = h.Name
	}
	return strings.Join(names, "、")
}

// closeProxyEdit 离开编辑页：从表单打开的回到表单，否则回到列表。
func (a *app) closeProxyEdit() {
	if a.proxyPanel.fromForm {
		a.proxyPanel = proxyPanel{}
		return
	}
	p := &a.proxyPanel
	p.editing, p.id, p.err, p.password = false, "", "", ""
}

// saveProxy 校验并保存代理。填了密码而保险库还没有主密码时，先设置主密码。
func (a *app) saveProxy() {
	p := &a.proxyPanel
	name := strings.TrimSpace(p.name)
	address := strings.TrimSpace(p.address)
	if address == "" {
		p.err = "请填写代理地址，例如 127.0.0.1。"
		return
	}
	if strings.Contains(address, "://") || strings.Contains(address, "/") {
		p.err = "地址只填主机名或 IP，协议在上面选择，端口填在右边。"
		return
	}
	port, err := strconv.Atoi(strings.TrimSpace(p.port))
	if err != nil || port < 1 || port > 65535 {
		p.err = "端口必须是 1 到 65535 之间的数字。"
		return
	}
	if name == "" {
		name = address + ":" + strconv.Itoa(port)
	}
	if slices.ContainsFunc(a.proxies, func(x hosts.Proxy) bool { return x.Name == name && x.ID != p.id }) {
		p.err = fmt.Sprintf("已经有一个叫 %s 的代理，请换一个名称。", name)
		return
	}
	proxy := hosts.Proxy{ID: p.id, Name: name, Kind: proxyKinds[p.kind], Address: address, Port: port,
		User: strings.TrimSpace(p.user)}
	previous, _ := a.proxyByID(p.id)
	switch {
	case proxy.User == "" || p.clearPassword:
		proxy.Password = ""
	case p.password != "":
		proxy.Password = p.password
	default:
		proxy.Password = previous.Password
	}
	if proxy.ID == "" {
		proxy.ID = vault.NewID()
		p.id = proxy.ID // 设置主密码后重试时沿用同一个 ID
	}
	if proxy.Password != "" && a.vault.State() == vault.Unprotected {
		a.requireProtection(a.saveProxy)
		return
	}
	if err := a.apply(proxyChange(proxy)); err != nil {
		p.err = err.Error()
		return
	}
	a.toast(ui.Toast{Title: "已保存代理 " + name})
	if p.fromForm {
		a.form.showProxy, a.form.proxyName = true, name
	}
	a.closeProxyEdit()
}

// deleteProxy 删除代理，选用它的主机同一次写入改为不使用代理，不会留下指向不存在代理的主机。
func (a *app) deleteProxy(p hosts.Proxy) {
	changes := []vault.Change{{ID: p.ID, Delete: true}}
	for _, h := range a.proxyUsers(p.ID) {
		h.ProxyID = ""
		changes = append(changes, hostChange(h))
	}
	if err := a.apply(changes...); err != nil {
		a.proxyPanel.err = err.Error()
		return
	}
	a.toast(ui.Toast{Title: "已删除代理 " + p.Name})
}

// proxiesDialog 是代理面板。设置主密码的面板打开时让位，内容保留。
func (a *app) proxiesDialog(c *ui.Context, colors design.Colors) {
	p := &a.proxyPanel
	if !p.open || a.setup.open {
		return
	}
	sheet(c, colors, &p.open, &p.fresh, 480, 22, func() {
		if p.editing {
			a.proxyEditPage(c)
		} else {
			a.proxyListPage(c, colors)
		}
	})
	if !p.open {
		a.proxyPanel = proxyPanel{} // 关闭时丢掉输入的密码
	}
	a.proxyDeleteDialog(c)
}

func (a *app) proxyListPage(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	ui.Column(c).Gap(14).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "代理").FontSize(design.TextHeading).FontWeight(600).Grow(1)
			ui.PrimaryButton(c, "添加代理").Key("proxiesAdd").OnClick(func() { a.openProxyAdd(false) })
		})
		if len(a.proxies) == 0 {
			ui.Column(c).Gap(6).Padding(20, 0).Children(func() {
				ui.Text(c, "还没有代理").FontWeight(600)
				ui.Text(c, "需要经 SOCKS5 或 HTTP 代理才能连上服务器时，在这里添加，然后在主机里选择它。").
					FontSize(design.TextSmall).TextColor(t.TextMuted)
			})
		}
		ui.Column(c).Gap(2).Children(func() {
			for _, px := range a.proxies {
				a.proxyRow(c, colors, px)
			}
		})
		if p := &a.proxyPanel; p.err != "" {
			ui.Text(c, p.err).TextColor(t.Danger)
		}
		ui.Row(c).Justify(ui.End).Children(func() {
			ui.Button(c, "完成").Key("proxiesClose").OnClick(func() { a.proxyPanel = proxyPanel{} })
		})
	})
}

func (a *app) proxyRow(c *ui.Context, colors design.Colors, px hosts.Proxy) {
	t := c.Theme()
	menu := func(m *ui.Menu) {
		if m.Item("编辑").Chosen() {
			a.editProxy(px)
		}
		m.Separator()
		if m.Item("删除").Danger().Chosen() {
			a.proxyPanel.deleting = &px
		}
	}
	line := ui.Row(c).Key("proxyline:"+px.ID).AlignItems(ui.Center).Gap(6).Padding(0, 4, 0, 0).Radius(design.RadiusRow).Transition(design.Hover)
	if line.Hovered() {
		line.Background(colors.Hover)
	}
	line.Children(func() {
		row := ui.ButtonBase(c).Key("proxy:"+px.ID).Label(px.Name).Grow(1).MinWidth(0).Padding(8, 10).Gap(10).
			Justify(ui.Start).AlignItems(ui.Center)
		row.Children(func() {
			ui.Icon(c, design.Icon("globe")).FontSize(18).TextColor(t.TextMuted)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, px.Name).FontWeight(500).SingleLine()
				sub := px.Target()
				if n := len(a.proxyUsers(px.ID)); n > 0 {
					sub = fmt.Sprintf("%s  %d 台主机在用", sub, n)
				}
				ui.Text(c, sub).FontSize(design.TextSmall).TextColor(t.TextMuted).SingleLine()
			})
		}).OnClick(func() { a.editProxy(px) })
		row.PopoverMenu(menu)
		more := ui.ButtonBase(c).Key("proxyMore:"+px.ID).Label("更多操作").Size(26, 26).Radius(6).Shrink(0)
		if more.Hovered() {
			more.Background(colors.Hover)
		}
		more.Children(func() {
			ui.Icon(c, design.Icon("ellipsis")).FontSize(14).TextColor(t.TextMuted)
		}).PopoverMenuButton(menu)
	})
}

func (a *app) proxyEditPage(c *ui.Context) {
	t := c.Theme()
	p := &a.proxyPanel
	title, submit := "添加代理", "添加"
	if p.id != "" {
		if _, ok := a.proxyByID(p.id); ok {
			title, submit = "编辑代理", "保存"
		}
	}
	ui.Column(c).Gap(12).Children(func() {
		ui.Text(c, title).FontSize(design.TextHeading).FontWeight(600)
		ui.Field(c, "协议", func() {
			c.Key("proxyKind")
			ui.Segmented(c, &p.kind, "SOCKS5", "HTTP")
		})
		ui.Row(c).Gap(10).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				ui.Field(c, "地址", func() {
					c.Key("proxyAddress")
					ui.TextInput(c, &p.address).Placeholder("127.0.0.1").AutoFocus()
				})
			})
			ui.Column(c).Width(90).Children(func() {
				ui.Field(c, "端口", func() {
					c.Key("proxyPort")
					ui.TextInput(c, &p.port).Placeholder("1080")
				})
			})
		})
		ui.Field(c, "名称", func() {
			c.Key("proxyName")
			ui.TextInput(c, &p.name).Placeholder("留空时使用地址和端口")
		})
		ui.Field(c, "用户名（可选）", func() {
			c.Key("proxyUser")
			ui.TextInput(c, &p.user)
		})
		if strings.TrimSpace(p.user) != "" {
			placeholder := ""
			switch {
			case p.passwordSaved && p.clearPassword:
				placeholder = "保存后清除已保存的密码"
			case p.passwordSaved:
				placeholder = "已保存，留空保持不变"
			}
			ui.Row(c).Gap(8).AlignItems(ui.End).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					ui.Field(c, "密码", func() {
						c.Key("proxyPassword")
						ui.TextInput(c, &p.password).Password().Placeholder(placeholder)
					})
				})
				if p.passwordSaved {
					text := "清除密码"
					if p.clearPassword {
						text = "撤销清除"
					}
					ui.Button(c, text).Key("proxyPasswordClear").OnClick(func() {
						p.clearPassword = !p.clearPassword
						if p.clearPassword {
							p.password = ""
						}
					})
				}
			})
		}
		if p.err != "" {
			ui.Text(c, p.err).Key("proxyError").TextColor(t.Danger)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			ui.Button(c, "返回").Key("proxyBack").OnClick(a.closeProxyEdit)
			ui.PrimaryButton(c, submit).Key("proxySubmit").OnClick(a.saveProxy)
		})
	})
}

func (a *app) proxyDeleteDialog(c *ui.Context) {
	p := &a.proxyPanel
	if p.deleting == nil {
		return
	}
	px := *p.deleting
	message := "删除后无法恢复，为它保存的密码也会一并删除。"
	if used := a.proxyUsers(px.ID); len(used) > 0 {
		message += fmt.Sprintf("\n\n%s 正在使用它，删除后这些主机改为直连。", hostNames(used))
	}
	open := true
	choice := ui.AlertDialog(c, &open, "删除代理 "+px.Name+"？", message, "删除", "取消")
	if choice == -1 && open {
		return
	}
	p.deleting = nil
	if choice == 0 {
		a.deleteProxy(px)
	}
}
