package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestDrawerDropUploadsIntoListedDir(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	remote, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	setTerminalDir(t, h, tb, remote)
	h.click("文件（⌘J）")
	h.pump("抽屉跟随终端目录", func() bool { return tb.dir == remote })

	inbox := filepath.Join(remote, "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	h.click("刷新")
	h.pump("列出 inbox", func() bool {
		for _, e := range tb.entries {
			if e.Name == "inbox" && e.IsDir {
				return true
			}
		}
		return false
	})
	h.click("inbox")
	h.pump("进入 inbox", func() bool { return tb.dir == inbox })

	local := filepath.Join(t.TempDir(), "from-finder.txt")
	if err := os.WriteFile(local, []byte("drawer"), 0o644); err != nil {
		t.Fatal(err)
	}
	box, ok := h.tt.Find("上传文件")
	if !ok {
		t.Fatalf("找不到上传按钮: %q", h.tt.Texts())
	}
	if !h.tt.DropFiles(box.X+4, box.Y+4, local) {
		t.Fatal("拖进文件抽屉的文件应被接住")
	}
	h.pump("上传到抽屉当前目录", func() bool {
		got, err := os.ReadFile(filepath.Join(inbox, "from-finder.txt"))
		return err == nil && string(got) == "drawer"
	})
	if _, err := os.Stat(filepath.Join(remote, "from-finder.txt")); err == nil {
		t.Fatal("文件不应上传到终端目录，抽屉当时列的是 inbox")
	}
}

func TestUploadFolderFromButton(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	h.click("文件（⌘J）")
	h.pump("打开文件列表", func() bool { return tb.dir != "" })

	local := filepath.Join(t.TempDir(), "bundle")
	makeTree(t, local, map[string]string{"note.txt": "hello", "sub/a.txt": "world"})
	h.a.chooseUploadFolder = func() (string, error) { return local, nil }
	h.click("上传文件夹")
	h.pump("文件夹上传完成", func() bool {
		return len(tb.transfers) == 1 && tb.transfers[0].done.Load() && tb.transfers[0].err == ""
	})
	dst := filepath.Join(tb.dir, "bundle")
	t.Cleanup(func() { os.RemoveAll(dst) })
	if got := readFile(t, filepath.Join(dst, "note.txt")); got != "hello" {
		t.Fatalf("note.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(dst, "sub", "a.txt")); got != "world" {
		t.Fatalf("a.txt = %q", got)
	}
}

func TestDownloadFolderToChosenDir(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	h.click("文件（⌘J）")
	h.pump("打开文件列表", func() bool { return tb.dir != "" })

	remote := filepath.Join(tb.dir, "pack-dl")
	makeTree(t, remote, map[string]string{"one.txt": "1", "nested/two.txt": "2"})
	t.Cleanup(func() { os.RemoveAll(remote) })
	if err := os.Symlink(filepath.Join(remote, "one.txt"), filepath.Join(remote, "skip.txt")); err != nil {
		t.Fatal(err)
	}
	h.click("刷新")
	h.pump("列出 pack-dl", func() bool {
		for _, e := range tb.entries {
			if e.Name == "pack-dl" {
				return true
			}
		}
		return false
	})
	h.tt.Frame()
	if err := h.tt.RightClick("pack-dl"); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	h.a.chooseDownloadFolder = func() (string, error) { return parent, nil }
	if err := h.tt.ChooseMenuItem("下载文件夹"); err != nil {
		t.Fatalf("选择下载文件夹失败: %v，菜单 %q", err, h.tt.Menu())
	}
	h.pump("文件夹下载完成", func() bool {
		return len(tb.transfers) == 1 && tb.transfers[0].done.Load() && tb.transfers[0].err == ""
	})
	if got := readFile(t, filepath.Join(parent, "pack-dl", "one.txt")); got != "1" {
		t.Fatalf("one.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(parent, "pack-dl", "nested", "two.txt")); got != "2" {
		t.Fatalf("two.txt = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(parent, "pack-dl", "skip.txt")); err == nil {
		t.Fatal("符号链接不应下载")
	}
	if !h.tt.HasText("跳过了 1 个符号链接或特殊文件") {
		t.Fatalf("应说明跳过了符号链接: %q", h.tt.Texts())
	}
}

func TestDragOutOffersFileAfterDownload(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	h.click("文件（⌘J）")
	h.pump("打开文件列表", func() bool { return tb.dir != "" })

	name := "drag-me.txt"
	remote := filepath.Join(tb.dir, name)
	if err := os.WriteFile(remote, []byte("carry"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(remote) })
	h.click("刷新")
	h.pump("列出要拖出的文件", func() bool {
		for _, e := range tb.entries {
			if e.Name == name {
				return true
			}
		}
		return false
	})

	dragRow(t, h, name)
	if !h.tt.HasText("正在下载 " + name) {
		t.Fatalf("第一次拖动应开始下载: %q", h.tt.Texts())
	}
	var path string
	h.pump("临时文件下好", func() bool {
		for _, e := range tb.entries {
			if e.Name != name {
				continue
			}
			c := h.a.dragCopies[e.Path]
			if c != nil && c.local != "" && !c.preparing {
				path = c.local
				return true
			}
		}
		return false
	})
	if got := readFile(t, path); got != "carry" {
		t.Fatalf("临时文件内容 = %q", got)
	}
	for _, c := range h.a.dragCopies {
		if c.offered {
			t.Fatal("文件还没准备好时不应把路径交给拖放")
		}
	}

	dragRow(t, h, name)
	offered := false
	for _, c := range h.a.dragCopies {
		if c.local == path && c.offered {
			offered = true
		}
	}
	if !offered {
		t.Fatal("下载完成后再次拖动应交出本地路径")
	}
}

// dragRow 在文件行上按下并移动，超过拖动阈值后再松开。
// 测试里的拖放后端不校验空数据，系统拖动会留着，所以用 Escape 结束它。
func dragRow(t *testing.T, h *harness, label string) {
	t.Helper()
	box, ok := h.tt.Find(label)
	if !ok {
		t.Fatalf("找不到 %s: %q", label, h.tt.Texts())
	}
	x, y := box.X+box.W/2, box.Y+min(box.H/2, 8)
	h.tt.Press(x, y)
	h.tt.Move(x+24, y)
	// 松手在窗口外：窗口里的另一栏不接这次拖动，才会开始准备拖到桌面的副本。
	h.tt.Release(-20, -20)
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
}
