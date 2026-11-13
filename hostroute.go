package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"serval/core/hosts"
	"serval/design"
	"serval/locales"

	"github.com/egoist/mygo/ui"
)

// pickHost 是新加的一跳还没选主机时下拉框显示的文字。
// 和 noKey 一样，文字本身就是尚未选择的哨兵值。
func pickHost() string { return locales.RoutePickHost() }

// pickProxy 是引用的代理已删除、等待用户重新选择时下拉框显示的占位文字。
const pickProxy = "选择代理…"

// legacyProxyLabel 是旧记录里直接填写的代理地址在下拉框里的显示文字。
// 旧版本允许手填代理，这里原样保留，用户改选独立管理的代理后才替换。
func legacyProxyLabel(spec string) string { return locales.RouteLegacyProxy(spec) }

// jumpChoices 返回第 i 跳的规范值切片与展示标签切片：除了正在编辑的主机自己，
// 其余已保存的主机都展示为“名称 (user@host:port)”，表单字段仍保留规范名称。
func (a *app) jumpChoices(f *hostForm, i int) (values, labels []string) {
	values = []string{pickHost()}
	labels = []string{pickHost()}
	for _, h := range a.hosts {
		if f.editing != "" && h.ID == f.editing {
			continue
		}
		values = append(values, h.Name)
		labels = append(labels, fmt.Sprintf("%s (%s)", h.Name, h.Target()))
	}
	if cur := f.jumps[i]; !slices.Contains(values, cur) {
		values = append(values, cur)
		labels = append(labels, cur)
	}
	return values, labels
}

// jumpOptions 返回第 i 跳的规范值列表。
func (a *app) jumpOptions(f *hostForm, i int) []string {
	values, _ := a.jumpChoices(f, i)
	return values
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

// proxyChoices 返回代理下拉框的规范名称与丰富展示标签（名称 + 协议与地址端口）。
func (a *app) proxyChoices(f *hostForm) (values, labels []string) {
	if f.proxyName == pickProxy {
		values = append(values, pickProxy)
		labels = append(labels, pickProxy)
	}
	for _, p := range a.proxies {
		values = append(values, p.Name)
		kind := strings.ToUpper(p.Kind)
		if kind == "" {
			kind = "SOCKS5"
		}
		labels = append(labels, fmt.Sprintf("%s (%s %s:%d)", p.Name, kind, p.Address, p.Port))
	}
	if f.legacyProxy != "" {
		legacy := legacyProxyLabel(f.legacyProxy)
		values = append(values, legacy)
		labels = append(labels, legacy)
	}
	return values, labels
}

// proxyOptions 返回代理下拉框的规范名称列表。
func (a *app) proxyOptions(f *hostForm) []string {
	values, _ := a.proxyChoices(f)
	return values
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
					values, labels := a.jumpChoices(f, i)
					curIdx := indexOr(values, f.jumps[i], 0)
					selectedLabel := labels[curIdx]
					ui.Row(c).Key(fmt.Sprintf("jumpRow:%d", i)).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, fmt.Sprintf("%d", i+1)).FontSize(design.TextSmall).TextColor(colors.TextFaint).Width(14)
						ui.Column(c).Grow(1).MinWidth(0).Children(func() {
							c.Key(fmt.Sprintf("addJump:%d", i))
							if ui.Select(c, &selectedLabel, labels).Changed() {
								if idx := slices.Index(labels, selectedLabel); idx >= 0 {
									f.jumps[i] = values[idx]
								}
							}
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
				targetLabel := strings.TrimSpace(f.name)
				if targetLabel == "" {
					targetLabel = strings.TrimSpace(f.address)
				}
				if targetLabel == "" {
					targetLabel = "目标主机"
				}
				chainNodes := []string{"本机"}
				for _, j := range f.jumps {
					if trimmed := strings.TrimSpace(j); trimmed != "" && trimmed != pickHost() {
						chainNodes = append(chainNodes, trimmed)
					}
				}
				chainNodes = append(chainNodes, targetLabel)
				ui.Row(c).Key("jumpChainPreview").Gap(4).Wrap().AlignItems(ui.Center).
					Padding(4, 8).Radius(design.RadiusRow).Background(colors.Chip).Children(func() {
					for i, node := range chainNodes {
						if i > 0 {
							ui.Text(c, "→").FontSize(design.TextSmall - 1).TextColor(colors.TextFaint)
						}
						color := t.TextMuted
						if i == len(chainNodes)-1 {
							color = t.Accent
						}
						ui.Text(c, node).FontSize(design.TextSmall - 1).FontWeight(500).TextColor(color)
					}
				})
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
				if values, labels := a.proxyChoices(f); len(values) > 0 {
					if !slices.Contains(values, f.proxyName) {
						f.proxyName = values[0]
					}
					curIdx := indexOr(values, f.proxyName, 0)
					selectedLabel := labels[curIdx]
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						c.Key("addProxySelect")
						if ui.Select(c, &selectedLabel, labels).Changed() {
							if idx := slices.Index(labels, selectedLabel); idx >= 0 {
								f.proxyName = values[idx]
							}
						}
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
