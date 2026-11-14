package main

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"serval/core/vault"
	"serval/design"

	"github.com/egoist/mygo/ui"
)

// snippet 是一条代码片段。正文里的 {{name}} 在发送时填写，值不入库。
// name 只认英文字母、数字和下划线，避免把普通文字里的花括号当成变量。
type snippet struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Body string `json:"body"`
}

// snippetVarPattern 匹配 {{name}}，花括号内侧允许空白。
var snippetVarPattern = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// snippetVarNames 按出现顺序返回变量名，同名只留第一次。
func snippetVarNames(body string) []string {
	seen := map[string]bool{}
	var names []string
	for _, m := range snippetVarPattern.FindAllStringSubmatch(body, -1) {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// expandSnippet 把 {{name}} 换成 values 里的值。缺任何一个变量就失败，不发送半截内容。
// 替换只做一轮：填进去的文字里再出现 {{…}} 也原样保留。对不上的花括号原样保留。
func expandSnippet(body string, values map[string]string) (string, error) {
	missing := ""
	out := snippetVarPattern.ReplaceAllStringFunc(body, func(match string) string {
		name := snippetVarPattern.FindStringSubmatch(match)[1]
		value, ok := values[name]
		if !ok {
			if missing == "" {
				missing = name
			}
			return match
		}
		return value
	})
	if missing != "" {
		return "", fmt.Errorf("请先填写变量 %s。", missing)
	}
	return out, nil
}

func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// snippetLineCount 是正文的行数。末尾的换行不算额外的空行。
func snippetLineCount(body string) int {
	if body == "" {
		return 0
	}
	n := strings.Count(body, "\n") + 1
	if strings.HasSuffix(body, "\n") {
		n--
	}
	if n < 1 {
		return 1
	}
	return n
}

func snippetIsMultiline(body string) bool { return snippetLineCount(body) > 1 }

// snippetSubtitle 取第一行，超过 42 个字时截断，给命令面板和列表做副标题。
func snippetSubtitle(body string) string {
	line, _, _ := strings.Cut(body, "\n")
	line = strings.TrimSpace(line)
	const max = 42
	if utf8.RuneCountInString(line) <= max {
		return line
	}
	r := []rune(line)
	return string(r[:max-1]) + "…"
}

func snippetPreview(body string, max int) string {
	r := []rune(body)
	if len(r) <= max {
		return body
	}
	return string(r[:max]) + "…"
}

// snippetPanel 是代码片段的管理面板，也暂存“填写变量”和“多行确认”这两步。
type snippetPanel struct {
	open, fresh bool
	editing     bool
	id          string
	name, body  string
	err         string
	deleting    *snippet

	// filling 为真时面板改成填写变量。fillOnly 表示面板是为了填写才打开的，取消后关掉。
	filling  bool
	fillOnly bool
	vars     []string
	values   []string
	pending  string

	// confirming 为真时弹出多行确认。确认可以发生在面板已经关上之后，所以和 open 分开。
	confirming  bool
	confirmBody string
}

func (a *app) openSnippets() { a.snippetPanel = snippetPanel{open: true, fresh: true} }

func (a *app) startAddSnippet() {
	p := &a.snippetPanel
	p.editing, p.filling = true, false
	p.id, p.name, p.body, p.err = "", "", "", ""
}

func (a *app) editSnippet(s snippet) {
	p := &a.snippetPanel
	p.editing, p.filling = true, false
	p.id, p.name, p.body, p.err = s.ID, s.Name, s.Body, ""
}

func (a *app) closeSnippetEdit() {
	p := &a.snippetPanel
	p.editing, p.filling = false, false
	p.id, p.name, p.body, p.err = "", "", "", ""
	p.vars, p.values, p.pending, p.fillOnly = nil, nil, "", false
}

func (a *app) sortedSnippets() []snippet {
	out := slices.Clone(a.snippets)
	slices.SortFunc(out, func(x, y snippet) int {
		return cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	return out
}

func (a *app) snippetByID(id string) (snippet, bool) {
	i := slices.IndexFunc(a.snippets, func(s snippet) bool { return s.ID == id })
	if i < 0 || id == "" {
		return snippet{}, false
	}
	return a.snippets[i], true
}

func (a *app) snippetNameTaken(name, exceptID string) bool {
	return slices.ContainsFunc(a.snippets, func(s snippet) bool {
		return s.Name == name && s.ID != exceptID
	})
}

// saveSnippet 校验并保存片段。正文里经常有口令或令牌，没法可靠区分，所以一律先要求保险库已加密。
func (a *app) saveSnippet() {
	p := &a.snippetPanel
	name := strings.TrimSpace(p.name)
	body := normalizeNewlines(p.body)
	if name == "" {
		p.err = "请填写名称。"
		return
	}
	if strings.TrimSpace(body) == "" {
		p.err = "请填写要发送的内容。"
		return
	}
	if a.snippetNameTaken(name, p.id) {
		p.err = fmt.Sprintf("已经有一个叫 %s 的代码片段，请换一个名称。", name)
		return
	}
	s := snippet{ID: p.id, Name: name, Body: body}
	if s.ID == "" {
		s.ID = vault.NewID()
		p.id = s.ID // 设置主密码后重试时沿用同一个 ID
	}
	if a.vault.State() == vault.Unprotected {
		a.requireProtection(a.saveSnippet)
		return
	}
	if err := a.apply(snippetChange(s)); err != nil {
		p.err = err.Error()
		return
	}
	a.toast(ui.Toast{Title: "已保存片段 " + name})
	a.closeSnippetEdit()
}

func (a *app) deleteSnippet(s snippet) {
	if err := a.apply(vault.Change{ID: s.ID, Delete: true}); err != nil {
		a.snippetPanel.err = err.Error()
		return
	}
	if a.snippetPanel.id == s.ID {
		a.closeSnippetEdit()
	}
	a.toast(ui.Toast{Title: "已删除片段 " + s.Name})
}

// beginSend 开始把正文发到当前终端：有变量先填写，多行再确认，单行直接发送。
func (a *app) beginSend(body string) {
	body = normalizeNewlines(body)
	if strings.TrimSpace(body) == "" {
		a.toast(ui.Toast{Title: "这段代码是空的。"})
		return
	}
	names := snippetVarNames(body)
	if len(names) == 0 {
		a.confirmOrSend(body)
		return
	}
	p := &a.snippetPanel
	fillOnly := !p.open
	p.open = true
	if fillOnly {
		p.fresh = true
	}
	p.filling = true
	p.fillOnly = fillOnly
	p.vars = names
	p.values = make([]string, len(names))
	p.pending = body
	p.err = ""
}

func (a *app) sendSnippet(s snippet) { a.beginSend(s.Body) }

func (a *app) cancelSnippetFill() {
	p := &a.snippetPanel
	fillOnly := p.fillOnly
	p.filling, p.fillOnly = false, false
	p.vars, p.values, p.pending, p.err = nil, nil, "", ""
	if fillOnly {
		p.open = false
	}
}

func (a *app) submitSnippetFill() {
	p := &a.snippetPanel
	values := make(map[string]string, len(p.vars))
	for i, name := range p.vars {
		values[name] = p.values[i]
	}
	body, err := expandSnippet(p.pending, values)
	if err != nil {
		p.err = err.Error()
		return
	}
	fillOnly := p.fillOnly
	p.filling, p.fillOnly = false, false
	p.vars, p.values, p.pending, p.err = nil, nil, "", ""
	if fillOnly {
		p.open = false
	}
	a.confirmOrSend(body)
}

// confirmOrSend 在多行时先确认。默认按钮是取消，避免回车就把一整段打进终端。
func (a *app) confirmOrSend(body string) {
	if snippetIsMultiline(body) {
		a.snippetPanel.confirming = true
		a.snippetPanel.confirmBody = body
		return
	}
	a.sendToFocused(body)
}

// sendToFocused 把正文写进当前窗格，末尾没有换行就补一个，让最后一行被执行。
// 没有终端时只说明原因，不把正文放进提示。
func (a *app) sendToFocused(body string) {
	body = normalizeNewlines(body)
	t := a.current()
	if t == nil || t.focus == nil || t.focus.term == nil || t.focus.ended {
		a.toast(ui.Toast{Title: "先打开一个终端，再发送代码片段。"})
		return
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	t.focus.term.Send([]byte(body))
	a.toast(ui.Toast{Title: "已发送到当前终端"})
}

// snippetsDialog 渲染片段面板、删除确认和多行确认。设置主密码时面板让位，正在编辑的内容保留。
func (a *app) snippetsDialog(c *ui.Context, colors design.Colors) {
	p := &a.snippetPanel
	if p.open && !a.setup.open {
		sheet(c, colors, &p.open, &p.fresh, 520, 22, func() {
			switch {
			case p.filling:
				a.snippetFillPage(c)
			case p.editing:
				a.snippetEditPage(c)
			default:
				a.snippetListPage(c, colors)
			}
		})
		if !p.open {
			// 关掉面板时丢掉正在编辑的正文。多行确认可能还开着，单独留下。
			a.snippetPanel = snippetPanel{confirming: p.confirming, confirmBody: p.confirmBody}
		}
	}
	a.snippetDeleteDialog(c)
	a.snippetConfirmDialog(c)
}

func (a *app) snippetListPage(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	_, windowHeight := c.Size()
	maxListH := max(min(windowHeight-240, 340), 120)
	ui.Column(c).Gap(14).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "代码片段").FontSize(design.TextHeading).FontWeight(600).Grow(1)
			ui.PrimaryButton(c, "新建片段").Key("snippetAdd").OnClick(a.startAddSnippet)
		})
		if len(a.snippets) == 0 {
			ui.Column(c).Gap(6).Padding(20, 0).Children(func() {
				ui.Text(c, "还没有代码片段").FontWeight(600)
				ui.Text(c, "把常用命令存下来，之后在命令面板里搜索，发到当前终端。").
					FontSize(design.TextSmall).TextColor(t.TextMuted)
			})
		} else {
			ui.Scroll(c).Key("snippetsListScroll").MaxHeight(maxListH).Children(func() {
				ui.Column(c).Gap(2).Padding(0, 4, 0, 0).Children(func() {
					for _, s := range a.sortedSnippets() {
						a.snippetRow(c, colors, s)
					}
				})
			})
		}
		if p := &a.snippetPanel; p.err != "" {
			ui.Text(c, p.err).TextColor(t.Danger)
		}
		ui.Row(c).Justify(ui.End).Children(func() {
			ui.Button(c, "完成").Key("snippetClose").OnClick(func() { a.snippetPanel = snippetPanel{} })
		})
	})
}

func (a *app) snippetRow(c *ui.Context, colors design.Colors, s snippet) {
	t := c.Theme()
	cur := a.current()
	canSend := cur != nil && cur.focus != nil && cur.focus.term != nil && !cur.focus.ended
	menu := func(m *ui.Menu) {
		if m.Item("编辑").Chosen() {
			a.editSnippet(s)
		}
		if m.Item("发送到当前终端").Disabled(!canSend).Chosen() {
			a.sendSnippet(s)
		}
		m.Separator()
		if m.Item("删除").Danger().Chosen() {
			a.snippetPanel.deleting = &s
		}
	}
	line := ui.Row(c).Key("snippetline:"+s.ID).AlignItems(ui.Center).Gap(6).Padding(0, 4, 0, 0).Radius(design.RadiusRow).Transition(design.Hover)
	if line.Hovered() {
		line.Background(colors.Hover)
	}
	line.Children(func() {
		row := ui.ButtonBase(c).Key("snippet:"+s.ID).Label(s.Name).Grow(1).MinWidth(0).Padding(8, 10).Gap(10).
			Justify(ui.Start).AlignItems(ui.Center)
		row.Children(func() {
			ui.Icon(c, design.Icon("code")).FontSize(16).TextColor(t.TextMuted).Shrink(0)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, s.Name).FontWeight(500).SingleLine().Ellipsis("…")
				if sub := snippetSubtitle(s.Body); sub != "" {
					ui.Text(c, sub).FontSize(design.TextSmall).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
				}
			})
		}).OnClick(func() { a.editSnippet(s) })
		row.PopoverMenu(menu)
		ui.Button(c, "执行").Key("snippetSendRow:" + s.ID).Label("发送片段 " + s.Name).
			Tooltip("发送到当前终端").Disabled(!canSend).OnClick(func() { a.sendSnippet(s) })
		more := ui.ButtonBase(c).Key("snippetMore:"+s.ID).Label("片段更多 "+s.Name).Tooltip("更多操作").
			Size(26, 26).Center().Radius(6).Shrink(0)
		if more.Hovered() {
			more.Background(colors.Hover)
		}
		more.Children(func() {
			ui.Icon(c, design.Icon("ellipsis")).FontSize(14).TextColor(t.TextMuted)
		}).PopoverMenuButton(menu)
	})
}

func (a *app) snippetEditPage(c *ui.Context) {
	t := c.Theme()
	p := &a.snippetPanel
	_, saved := a.snippetByID(p.id)
	title := "新建片段"
	if saved {
		title = "编辑片段"
	}
	ui.Column(c).Gap(12).Children(func() {
		ui.Text(c, title).FontSize(design.TextHeading).FontWeight(600)
		ui.Field(c, "名称", func() {
			c.Key("snippetName")
			ui.TextInput(c, &p.name).Placeholder("例如 查看磁盘").AutoFocus()
		})
		ui.Field(c, "内容", func() {
			c.Key("snippetBody")
			ui.TextArea(c, &p.body).Height(120).Font(design.FontTerminal).FontSize(design.TextSmall).
				Placeholder("要发送的命令。变量写成 {{name}}，发送时再填写。")
		}).Description("变量名用英文字母、数字和下划线。多行内容发送前会再确认一次。")
		if p.err != "" {
			ui.Text(c, p.err).Key("snippetError").TextColor(t.Danger)
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if saved {
				ui.Button(c, "删除片段").Key("snippetDelete").OnClick(func() {
					s, ok := a.snippetByID(p.id)
					if ok {
						p.deleting = &s
					}
				})
			}
			ui.Spacer(c)
			ui.Button(c, "返回").Key("snippetBack").OnClick(a.closeSnippetEdit)
			ui.Button(c, "发送到当前窗格").Key("snippetSend").OnClick(func() { a.beginSend(p.body) })
			ui.PrimaryButton(c, "保存片段").Key("snippetSave").OnClick(a.saveSnippet)
		})
	})
}

func (a *app) snippetFillPage(c *ui.Context) {
	t := c.Theme()
	p := &a.snippetPanel
	ui.Column(c).Gap(12).Children(func() {
		ui.Text(c, "填写变量").FontSize(design.TextHeading).FontWeight(600)
		ui.Text(c, "填好后发到当前终端。这些值只用于这一次发送，不会保存。").
			FontSize(design.TextSmall).TextColor(t.TextMuted)
		for i := range p.vars {
			ui.Field(c, p.vars[i], func() {
				c.Key("snippetVar:" + p.vars[i])
				input := ui.TextInput(c, &p.values[i])
				if i == 0 {
					input.AutoFocus()
				}
			})
		}
		if p.err != "" {
			ui.Text(c, p.err).Key("snippetError").TextColor(t.Danger)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			ui.Button(c, "返回").Key("snippetFillBack").OnClick(a.cancelSnippetFill)
			ui.PrimaryButton(c, "填好并发送").Key("snippetFillSubmit").OnClick(a.submitSnippetFill)
		})
	})
}

func (a *app) snippetDeleteDialog(c *ui.Context) {
	p := &a.snippetPanel
	if p.deleting == nil {
		return
	}
	s := *p.deleting
	open := true
	choice := ui.AlertDialog(c, &open, "删除片段 "+s.Name+"？",
		"删除后无法恢复。其他设备同步后也会删掉。", "删除", "取消")
	if choice == -1 && open {
		return
	}
	p.deleting = nil
	if choice == 0 {
		a.deleteSnippet(s)
	}
}

func (a *app) snippetConfirmDialog(c *ui.Context) {
	p := &a.snippetPanel
	if !p.confirming {
		return
	}
	body := p.confirmBody
	open := true
	message := fmt.Sprintf("将把 %d 行输入到当前终端，就像亲手键入。\n\n%s",
		snippetLineCount(body), snippetPreview(body, 400))
	// 最后一个按钮是默认按钮，放“取消”，避免按回车就把多行命令打进终端。
	choice := ui.AlertDialog(c, &open, "发送多行内容？", message, "发送", "取消")
	if choice == -1 && open {
		return
	}
	p.confirming, p.confirmBody = false, ""
	if choice == 0 {
		a.sendToFocused(body)
	}
}
