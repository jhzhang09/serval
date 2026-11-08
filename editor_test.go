package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestEditorSavesConflictAndDiscard(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	path := openEditorOn(t, h, tb, "note.txt", "hello")
	h.tt.Frame()
	if h.a.editor.dirty || h.a.editor.buf.String() != "hello" {
		t.Fatalf("刚打开不应标成未保存: dirty=%v text=%q", h.a.editor.dirty, h.a.editor.buf.String())
	}

	h.a.editor.buf.Set("edited")
	h.tt.Frame()
	h.click("保存")
	h.pump("保存完成", func() bool {
		if h.a.editor.busy {
			return false
		}
		got, err := os.ReadFile(path)
		return err == nil && string(got) == "edited" && !h.a.editor.dirty
	})

	// 打开之后远端被改过：先取消，文件保持服务器上的新内容；确认后才覆盖。
	future := time.Now().Add(5 * time.Second)
	if err := os.WriteFile(path, []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	h.a.editor.buf.Set("local")
	h.tt.Frame()
	h.click("保存")
	h.pump("覆盖确认", func() bool { return h.a.editor.askOverwrite })
	h.tt.Frame()
	h.click("取消")
	h.tt.Frame()
	if h.a.editor.askOverwrite || !h.a.editor.open {
		t.Fatalf("取消后应回到编辑器: overwrite=%v open=%v", h.a.editor.askOverwrite, h.a.editor.open)
	}
	if got, _ := os.ReadFile(path); string(got) != "remote" {
		t.Fatalf("取消后远端应保持新内容，= %q", got)
	}

	h.click("保存")
	h.pump("再次确认", func() bool { return h.a.editor.askOverwrite })
	h.tt.Frame()
	h.click("仍然保存")
	h.pump("覆盖完成", func() bool {
		if h.a.editor.busy || h.a.editor.askOverwrite {
			return false
		}
		got, err := os.ReadFile(path)
		return err == nil && string(got) == "local"
	})

	h.a.editor.buf.Set("discard-me")
	h.tt.Frame()
	h.click("关闭")
	h.tt.Frame()
	if !h.a.editor.askDiscard || !h.tt.HasText("放弃修改？") {
		t.Fatalf("有修改时应先确认: discard=%v texts=%q", h.a.editor.askDiscard, h.tt.Texts())
	}
	h.click("取消")
	h.tt.Frame()
	if !h.a.editor.open || h.a.editor.buf == nil || h.a.editor.buf.String() != "discard-me" {
		t.Fatal("取消放弃后应继续编辑")
	}
	h.click("关闭")
	h.tt.Frame()
	h.click("放弃修改")
	h.tt.Frame()
	if h.a.editor.open {
		t.Fatal("放弃修改后编辑器应关闭")
	}
	if got, _ := os.ReadFile(path); string(got) != "local" {
		t.Fatalf("放弃不应写回远端，= %q", got)
	}

	// 快捷键和“保存”按钮同一条路径。
	openEditorOn(t, h, tb, "note.txt", "local")
	h.a.editor.buf.Set("via-key")
	h.tt.Frame()
	h.tt.Key(chromeShortcut(), ui.KeyS)
	h.pump("快捷键保存", func() bool {
		got, err := os.ReadFile(path)
		return err == nil && string(got) == "via-key" && !h.a.editor.busy && !h.a.editor.dirty
	})
}

func TestEditorRejectsBinaryAndHuge(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	h.click("文件（" + shortcutText("J") + "）")
	h.pump("文件列表", func() bool { return tb.dir != "" })

	bin := filepath.Join(tb.dir, "bin.dat")
	if err := os.WriteFile(bin, []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	refreshUntil(t, h, tb, "bin.dat")
	chooseEdit(t, h, "bin.dat")
	h.pump("拒绝二进制", func() bool {
		return !h.a.editor.loading && !h.a.editor.open && h.tt.HasText("不是 UTF-8")
	})

	big := filepath.Join(tb.dir, "big.txt")
	if err := os.WriteFile(big, make([]byte, maxEditorBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	refreshUntil(t, h, tb, "big.txt")
	chooseEdit(t, h, "big.txt")
	h.pump("拒绝大文件", func() bool {
		return !h.a.editor.loading && !h.a.editor.open && h.tt.HasText("超过 1 MB")
	})
}

// 回归：关闭标签时，标签下的编辑器里还有未保存的修改要先确认，
// 不能不问一声就把内容丢掉。
func TestEditorCloseTabConfirmsUnsaved(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	path := openEditorOn(t, h, tb, "note.txt", "hello")
	h.a.editor.buf.Set("unsaved")
	h.tt.Frame()
	if !h.a.editor.dirty {
		t.Fatal("应有未保存修改")
	}

	h.a.closeTab(tb)
	h.tt.Frame()
	if !h.tt.HasText("放弃修改？") {
		t.Fatal("关标签时应先确认放弃修改")
	}
	if len(h.a.tabs) != 1 {
		t.Fatal("确认前不应关闭标签")
	}
	h.click("取消")
	h.tt.Frame()
	if len(h.a.tabs) != 1 || !h.a.editor.open {
		t.Fatal("取消后标签和编辑器都应保留")
	}
	if h.a.editor.buf == nil || h.a.editor.buf.String() != "unsaved" {
		t.Fatal("取消后内容不应丢失")
	}

	h.a.closeTab(tb)
	h.pump("再次确认", func() bool { return h.tt.HasText("放弃修改？") })
	h.click("放弃修改")
	h.pump("关闭标签", func() bool { return len(h.a.tabs) == 0 })
	if h.a.editor.open {
		t.Fatal("放弃后编辑器应随标签一起关闭")
	}
	if got, _ := os.ReadFile(path); string(got) != "hello" {
		t.Fatalf("放弃不应写回远端，= %q", got)
	}
}

func openEditorOn(t *testing.T, h *harness, tb *tab, name, content string) string {
	t.Helper()
	if tb.dir == "" {
		h.click("文件（" + shortcutText("J") + "）")
		h.pump("文件列表", func() bool { return tb.dir != "" })
	}
	path := filepath.Join(tb.dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	refreshUntil(t, h, tb, name)
	chooseEdit(t, h, name)
	h.pump("打开编辑器", func() bool {
		return h.a.editor.open && h.a.editor.buf != nil && !h.a.editor.loading && h.a.editor.name == name
	})
	return path
}

func refreshUntil(t *testing.T, h *harness, tb *tab, name string) {
	t.Helper()
	h.click("刷新")
	h.pump("列表出现 "+name, func() bool {
		for _, e := range tb.entries {
			if e.Name == name && !e.IsDir {
				return true
			}
		}
		return false
	})
}

func chooseEdit(t *testing.T, h *harness, name string) {
	t.Helper()
	if err := h.tt.RightClick(name); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("编辑"); err != nil {
		t.Fatalf("选择编辑失败: %v menu=%q", err, h.tt.Menu())
	}
}
