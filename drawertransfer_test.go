package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"serval/core/hosts"
	"serval/internal/devsshd"

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
			c := h.a.dragCopies[dragKey{tb.id, e.Path}]
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

// 回归：两个标签连到不同主机、远端路径（和大小、修改时间）相同时，
// 拖出副本必须分开记录，否则会把 A 主机下载的内容当成 B 主机的交给用户。
func TestDragOutSeparatesTabs(t *testing.T) {
	dir2 := t.TempDir()
	second, err := devsshd.Start(devsshd.Options{Password: testPassword, Dir: dir2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	h := newHarness(t, hosts.Host{ID: "second", Name: "second", Address: "127.0.0.1", Port: second.Addr().Port, User: "tester"})

	// 第二台主机上先放好文件；第一台主机的文件等它的目录列出来再放。
	stamp := time.Now().Add(-time.Hour)
	secondFile := filepath.Join(dir2, "same.txt")
	if err := os.WriteFile(secondFile, []byte("AAAA-secnd"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(secondFile, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	tb1 := h.connectDev()
	h.click("文件（⌘J）")
	h.pump("打开第一个标签的文件列表", func() bool { return tb1.dir != "" })
	first := filepath.Join(tb1.dir, "same.txt")
	if err := os.WriteFile(first, []byte("AAAA-first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(first, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	h.click("刷新")
	h.pump("列出第一个标签的文件", func() bool {
		for _, e := range tb1.entries {
			if e.Name == "same.txt" {
				return true
			}
		}
		return false
	})
	dragRow(t, h, "same.txt")
	h.pump("第一个标签的副本下好", func() bool {
		for _, e := range tb1.entries {
			if e.Name != "same.txt" {
				continue
			}
			c := h.a.dragCopies[dragKey{tb1.id, e.Path}]
			return c != nil && c.local != "" && !c.preparing
		}
		return false
	})

	// 连上第二台主机，新标签成为当前标签。
	h.click("second")
	h.pump("密码询问", func() bool { return h.a.auth.open })
	h.a.auth.secret = testPassword
	h.tt.Frame()
	h.click("连接")
	h.pump("第二台主机指纹", func() bool { return len(h.a.prompts) > 0 })
	h.click("信任并连接")
	h.pump("第二个会话建立", func() bool { return len(h.a.tabs) == 2 })
	tb2 := h.a.tabs[1]
	// 抽屉是全局开关，第二台主机连上后已经对着新标签列出目录，直接刷新即可。
	h.pump("打开第二个标签的文件列表", func() bool { return tb2.dir != "" })
	h.click("刷新")
	h.pump("列出第二个标签的文件", func() bool {
		for _, e := range tb2.entries {
			if e.Name == "same.txt" {
				return true
			}
		}
		return false
	})

	// 第一次拖动必须重新下载第二台主机上的文件，不能复用第一台的副本。
	dragRow(t, h, "same.txt")
	h.pump("第二个标签的副本下好", func() bool {
		for _, e := range tb2.entries {
			if e.Name != "same.txt" {
				continue
			}
			c := h.a.dragCopies[dragKey{tb2.id, e.Path}]
			return c != nil && c.local != "" && !c.preparing
		}
		return false
	})
	dragRow(t, h, "same.txt")
	var offered string
	for _, c := range h.a.dragCopies {
		if c.offered {
			offered = c.local
		}
	}
	if offered == "" {
		t.Fatal("下载完成后再次拖动应交出本地路径")
	}
	if got := readFile(t, offered); got != "AAAA-secnd" {
		t.Fatalf("交给系统的应是第二台主机的内容，得到 %q", got)
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
