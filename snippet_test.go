package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"serval/core/hosts"
	"serval/core/vault"
)

func TestSnippetExpand(t *testing.T) {
	body := "ssh {{ host }} {{user}}\n{{user}}\nkeep {{1bad}} and {{"
	names := snippetVarNames(body)
	if len(names) != 2 || names[0] != "host" || names[1] != "user" {
		t.Fatalf("变量名 = %v，期望 [host user]", names)
	}
	got, err := expandSnippet(body, map[string]string{"host": "web", "user": "root"})
	if err != nil || got != "ssh web root\nroot\nkeep {{1bad}} and {{" {
		t.Fatalf("替换结果 = %q, %v", got, err)
	}
	if _, err := expandSnippet(body, map[string]string{"host": "web"}); err == nil || !strings.Contains(err.Error(), "user") {
		t.Fatalf("缺少变量应失败并指出名字: %v", err)
	}
	plain, err := expandSnippet("echo hi", nil)
	if err != nil || plain != "echo hi" {
		t.Fatalf("没有变量时应原样返回: %q %v", plain, err)
	}
	empty, err := expandSnippet("a{{x}}b", map[string]string{"x": ""})
	if err != nil || empty != "ab" {
		t.Fatalf("空值应替换成空: %q %v", empty, err)
	}
	once, err := expandSnippet("{{x}}", map[string]string{"x": "{{y}}"})
	if err != nil || once != "{{y}}" {
		t.Fatalf("填进去的变量不应再展开: %q %v", once, err)
	}
	if !snippetIsMultiline("a\nb") || snippetLineCount("a\nb") != 2 {
		t.Fatal("两行应算多行")
	}
	if snippetIsMultiline("a\n") || snippetLineCount("a\n") != 1 || snippetIsMultiline("abc") {
		t.Fatal("末尾换行的单行不是多行")
	}
	long := strings.Repeat("测", 50)
	sub := snippetSubtitle(long + "\nsecond")
	if utf8.RuneCountInString(sub) != 42 || !strings.HasSuffix(sub, "…") || strings.Contains(sub, "second") {
		t.Fatalf("副标题 = %q（%d 字）", sub, utf8.RuneCountInString(sub))
	}
}

func TestSnippetPaletteOrder(t *testing.T) {
	a := newApp()
	a.hosts = []hosts.Host{{ID: "h", Name: "echo-box"}}
	a.snippets = []snippet{{ID: "s", Name: "other", Body: "echo hi"}}
	hostAt, snipAt, actionAt := -1, -1, -1
	for i, it := range a.paletteItems("") {
		switch {
		case it.host != nil:
			hostAt = i
		case strings.HasPrefix(it.key, "snippet:"):
			snipAt = i
		case it.key == "action:代码片段":
			actionAt = i
		}
	}
	if hostAt < 0 || snipAt < 0 || actionAt < 0 || hostAt > snipAt || snipAt > actionAt {
		t.Fatalf("顺序 host=%d snippet=%d action=%d", hostAt, snipAt, actionAt)
	}
	matched := a.paletteItems("echo")
	if len(matched) == 0 || matched[0].title != "echo-box" {
		t.Fatalf("匹配的主机应排在片段前面: %+v", matched)
	}
	found := a.paletteItems("echo hi")
	if len(found) == 0 || found[0].title != "other" {
		t.Fatalf("应按正文搜到片段: %+v", found)
	}
}

func TestSnippetSaveRequiresProtection(t *testing.T) {
	h := newBareHarness(t)
	h.openSnippetPanel()
	h.click("新建片段")
	h.tt.Frame()
	p := &h.a.snippetPanel
	p.name, p.body = "片段甲", "token-snippet-secret"
	h.tt.Frame()
	h.click("保存片段")
	h.tt.Frame()
	if !h.a.setup.open || len(h.a.snippets) != 0 || h.vaultFileContains("token-snippet-secret") {
		t.Fatalf("保存片段前应先设置主密码: setup=%v n=%d", h.a.setup.open, len(h.a.snippets))
	}
	h.click("取消")
	h.tt.Frame()
	if h.a.setup.open || !h.a.snippetPanel.editing || h.a.snippetPanel.body != "token-snippet-secret" {
		t.Fatalf("取消设置主密码后应回到片段表单: setup=%v editing=%v body=%q",
			h.a.setup.open, h.a.snippetPanel.editing, h.a.snippetPanel.body)
	}

	h.click("保存片段")
	h.tt.Frame()
	h.a.setup.password, h.a.setup.confirm = "master pw 1", "master pw 1"
	h.tt.Frame()
	h.click("设置并继续")
	h.pump("设置主密码并保存片段", func() bool { return !h.a.setup.open && len(h.a.snippets) == 1 })
	if h.a.snippets[0].Body != "token-snippet-secret" || h.a.snippetPanel.editing {
		t.Fatalf("设置主密码后应保存片段并回到列表: %+v editing=%v err=%q",
			h.a.snippets, h.a.snippetPanel.editing, h.a.snippetPanel.err)
	}
	if h.vaultFileContains("token-snippet-secret") || h.vaultFileContains("片段甲") {
		t.Fatal("保险库文件里不应出现片段名称或正文")
	}

	h.click("片段甲")
	h.tt.Frame()
	h.click("删除片段")
	h.tt.Frame()
	h.click("删除")
	h.tt.Frame()
	if len(h.a.snippets) != 0 {
		t.Fatalf("删除后不应留下片段: %+v", h.a.snippets)
	}

	h.a.snippetPanel.body = "still-in-memory"
	h.a.snippetPanel.confirmBody = "still-in-memory"
	h.a.lockVault()
	if h.a.snippets != nil || h.a.snippetPanel.body != "" || h.a.snippetPanel.confirmBody != "" || h.a.vault.State() != vault.Locked {
		t.Fatalf("锁定后应清掉片段和未发送的正文: state=%v panel=%+v", h.a.vault.State(), h.a.snippetPanel)
	}
}

func TestSnippetCreateSearchAndSend(t *testing.T) {
	h := newHarness(t)
	h.addSnippet("标记", "echo snippet-ok")
	if h.vaultFileContains("echo snippet-ok") || h.vaultFileContains("标记") {
		t.Fatal("保险库文件里不应出现片段明文")
	}
	h.sendSnippetNamed("标记")
	if len(h.a.tabs) != 0 || !h.tt.HasText("先打开一个终端，再发送代码片段。") {
		t.Fatalf("没有终端时不应发送: tabs=%d texts=%q", len(h.a.tabs), h.tt.Texts())
	}

	tb := h.connectDev()
	h.sendSnippetNamed("标记")
	h.pump("片段输出", func() bool { return strings.Contains(tb.focus.term.Text(), "snippet-ok") })

	h.addSnippet("多行", "echo multi-marker-a\necho multi-marker-b")
	h.sendSnippetNamed("多行")
	if !h.tt.HasText("发送多行内容？") {
		t.Fatalf("多行发送前应确认: %q", h.tt.Texts())
	}
	h.click("取消")
	h.tt.Frame()
	if strings.Contains(tb.focus.term.Text(), "multi-marker") {
		t.Fatal("取消后不应把多行内容发进终端")
	}
	h.sendSnippetNamed("多行")
	h.click("发送")
	h.pump("多行输出", func() bool {
		text := tb.focus.term.Text()
		return strings.Contains(text, "multi-marker-a") && strings.Contains(text, "multi-marker-b")
	})

	h.addSnippet("招呼", "echo hello-{{who}}-marker")
	h.sendSnippetNamed("招呼")
	if !h.a.snippetPanel.filling || !h.tt.HasText("填写变量") {
		t.Fatalf("有变量时应先填写: filling=%v texts=%q", h.a.snippetPanel.filling, h.tt.Texts())
	}
	h.a.snippetPanel.values[0] = "zz"
	h.tt.Frame()
	h.click("填好并发送")
	h.pump("变量输出", func() bool { return strings.Contains(tb.focus.term.Text(), "hello-zz-marker") })
	if strings.Contains(tb.focus.term.Text(), "{{who}}") {
		t.Fatal("变量应已替换")
	}
}

func (h *harness) openSnippetPanel() {
	h.t.Helper()
	h.click("搜索主机和操作")
	h.a.pal.query = "代码片段"
	h.tt.Frame()
	h.click("代码片段")
	h.tt.Frame()
	if !h.a.snippetPanel.open || h.a.snippetPanel.editing {
		h.t.Fatalf("代码片段面板没有打开: open=%v editing=%v texts=%q",
			h.a.snippetPanel.open, h.a.snippetPanel.editing, h.tt.Texts())
	}
}

func (h *harness) addSnippet(name, body string) {
	h.t.Helper()
	h.openSnippetPanel()
	h.click("新建片段")
	h.tt.Frame()
	h.a.snippetPanel.name = name
	h.a.snippetPanel.body = body
	h.tt.Frame()
	h.click("保存片段")
	h.tt.Frame()
	if h.a.snippetPanel.err != "" || h.snippetNamed(name).ID == "" {
		h.t.Fatalf("保存 %s 失败: %s snippets=%+v", name, h.a.snippetPanel.err, h.a.snippets)
	}
	h.click("完成")
	h.tt.Frame()
	if h.a.snippetPanel.open {
		h.t.Fatal("完成后面板应关闭")
	}
}

func (h *harness) sendSnippetNamed(name string) {
	h.t.Helper()
	h.click("搜索主机和操作")
	h.a.pal.query = name
	h.tt.Frame()
	items := h.a.paletteItems(name)
	if len(items) == 0 || items[0].title != name {
		h.t.Fatalf("搜索 %s 应把该片段排在最前: %+v", name, items)
	}
	h.click(name)
	h.tt.Frame()
}

func (h *harness) snippetNamed(name string) snippet {
	for _, s := range h.a.snippets {
		if s.Name == name {
			return s
		}
	}
	return snippet{}
}
