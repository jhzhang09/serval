package main

import (
	"errors"
	"fmt"
	"slices"

	"serval/core/hosts"
	"serval/design"
	"serval/locales"

	"github.com/egoist/mygo/ui"
)

// pickHost 是新加的一跳还没选主机时下拉框显示的文字。
// 和 noKey 一样，文字本身就是尚未选择的哨兵值。
func pickHost() string { return locales.RoutePickHost() }

// legacyProxyLabel 是旧记录里直接填写的代理地址在下拉框里的显示文字。
// 旧版本允许手填代理，这里原样保留，用户改选独立管理的代理后才替换。
func legacyProxyLabel(spec string) string { return locales.RouteLegacyProxy(spec) }

// jumpOptions 返回第 i 跳可选的主机：除了正在编辑的主机自己，其余已保存的主机都可选。
// 旧记录里直接填写的地址（不是主机名称）也放进去，编辑时不会被悄悄丢掉。
func (a *app) jumpOptions(f *hostForm, i int) []string {
	options := []string{pickHost()}
	for _, h := range a.hosts {
		if f.editing != "" && h.ID == f.editing {
			continue
		}
		options = append(options, h.Name)
	}
	if cur := f.jumps[i]; !slices.Contains(options, cur) {
		options = append(options, cur)
	}
	return options
}

// formProxy 把表单里的代理选择转成主机记录的字段：引用的代理 ID，或旧版直接填写的地址。
func (a *app) formProxy(f *hostForm) (proxyID, legacy string, err error) {
	if !f.showProxy {
		return "", "", nil
	}
	if f.legacyProxy != "" && f.proxyName == legacyProxyLabel(f.legacyProxy) {
		return "", f.legacyProxy, nil
	}
	i := slices.IndexFunc(a.proxies, func(p hosts.Proxy) bool { return p.Name == f.proxyName })
	if i < 0 {
		return "", "", errors.New(locales.RouteProxyRequired())
	}
	return a.proxies[i].ID, "", nil
}

// proxyOptions 返回代理下拉框的选项。
func (a *app) proxyOptions(f *hostForm) []string {
	var options []string
	for _, p := range a.proxies {
		options = append(options, p.Name)
	}
	if f.legacyProxy != "" {
		options = append(options, legacyProxyLabel(f.legacyProxy))
	}
	return options
}

// routeFields 是表单里的跳板机和代理。两者都默认收起，只显示两个“添加”按钮：
// 大多数主机直连，不该一打开表单就看到用不上的输入项。
func (a *app) routeFields(c *ui.Context, colors design.Colors, f *hostForm) {
	t := c.Theme()
	if !f.showJumps || !f.showProxy {
		ui.Row(c).Gap(8).Children(func() {
			if !f.showJumps {
				ui.Button(c, locales.RouteAddJump()).Key("addJumps").OnClick(func() {
					f.showJumps, f.jumps = true, []string{pickHost()}
				})
			}
			if !f.showProxy {
				ui.Button(c, locales.RouteUseProxy()).Key("addProxy").OnClick(func() {
					f.showProxy = true
					if f.proxyName == "" && len(a.proxies) > 0 {
						f.proxyName = a.proxies[0].Name
					}
				})
			}
		})
	}

	if f.showJumps {
		ui.Field(c, locales.RouteJump(), func() {
			ui.Column(c).Gap(6).Children(func() {
				for i := range f.jumps {
					ui.Row(c).Key(fmt.Sprintf("jumpRow:%d", i)).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, fmt.Sprintf("%d", i+1)).FontSize(design.TextSmall).TextColor(colors.TextFaint).Width(14)
						ui.Column(c).Grow(1).MinWidth(0).Children(func() {
							c.Key(fmt.Sprintf("addJump:%d", i))
							ui.Select(c, &f.jumps[i], a.jumpOptions(f, i))
						})
						label := locales.RouteRemoveJump(i + 1)
						c.Key(fmt.Sprintf("removeJump:%d", i))
						iconButton(c, colors, "x", label, func() {
							f.jumps = slices.Delete(f.jumps, i, i+1)
							if len(f.jumps) == 0 {
								f.showJumps = false
							}
						})
					})
				}
				ui.Row(c).Children(func() {
					ui.Button(c, locales.RouteAddAnother()).Key("addJumpMore").OnClick(func() {
						f.jumps = append(f.jumps, pickHost())
					})
				})
			})
		}).Description(locales.RouteJumpHint())
	}

	if f.showProxy {
		ui.Field(c, locales.Proxy(), func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				if options := a.proxyOptions(f); len(options) > 0 {
					if !slices.Contains(options, f.proxyName) {
						f.proxyName = options[0]
					}
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						c.Key("addProxySelect")
						ui.Select(c, &f.proxyName, options)
					})
				} else {
					ui.Text(c, locales.RouteNoProxyYet()).FontSize(design.TextSmall).TextColor(t.TextMuted).Grow(1)
				}
				ui.Button(c, locales.RouteNewProxy()).Key("addProxyNew").OnClick(func() { a.openProxyAdd(true) })
				ui.Button(c, locales.RouteDontUseProxy()).Key("addProxyRemove").OnClick(func() {
					f.showProxy, f.proxyName, f.legacyProxy = false, "", ""
				})
			})
		}).Description(locales.RouteProxyHint())
	}
}
