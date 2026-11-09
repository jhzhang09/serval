package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDualPaneDragsBothWays(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	localFile := filepath.Join(h.a.localDir, "from-local.txt")
	if err := os.WriteFile(localFile, []byte("from-local"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.click("文件（" + shortcutText("J") + "）")
	h.pump("两栏都列出文件", func() bool {
		return tb.dir != "" && h.tt.HasText("from-local.txt") && h.tt.HasText("go.mod")
	})

	dragOnto(t, h, "本地 from-local.txt", "上传文件")
	remote := filepath.Join(tb.dir, "from-local.txt")
	h.pump("拖到远端", func() bool {
		got, err := os.ReadFile(remote)
		return err == nil && string(got) == "from-local"
	})

	name := "from-remote.txt"
	src := filepath.Join(tb.dir, name)
	if err := os.WriteFile(src, []byte("from-remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.click("刷新")
	h.pump("列出远端文件", func() bool {
		for _, e := range tb.entries {
			if e.Name == name {
				return true
			}
		}
		return false
	})
	dragOnto(t, h, name, "刷新本地")
	h.pump("拖到本机", func() bool {
		got, err := os.ReadFile(filepath.Join(h.a.localDir, name))
		return err == nil && string(got) == "from-remote"
	})

	// 本机已有同名文件时不覆盖。
	dragOnto(t, h, name, "刷新本地")
	h.tt.Frame()
	if !h.tt.HasText("本地已经有同名的文件或文件夹") {
		t.Fatalf("应拒绝覆盖: %q", h.tt.Texts())
	}
}

// 回归：拖放下载承诺“本地已有同名文件不覆盖”。目标在下载开始之后才出现
// （检查与落盘之间的窗口）也不能覆盖：走带保护的下载路径，收到 ErrLocalExists。
func TestPaneDownloadGuardsLateLocalFile(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	h.click("文件（" + shortcutText("J") + "）")
	h.pump("打开文件抽屉", func() bool { return tb.dir != "" })
	remote := filepath.Join(tb.dir, "late.txt")
	if err := os.WriteFile(remote, []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "late.txt")
	if err := os.WriteFile(local, []byte("keep-me"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.a.runTransfer(tb, transferDownload, remote, local, "late.txt", true)
	h.pump("下载结束", func() bool { return h.tt.HasText("没有下载 late.txt") })
	if got := readFile(t, local); got != "keep-me" {
		t.Fatalf("不应覆盖本地内容，= %q", got)
	}
}

// 回归：本地文件少、远端文件多时，左右两栏都必须顶天立地撑满抽屉高度，
// 标题对齐在顶部，远端列表在抽屉内可滚动。
func TestDualPaneColumnsStretchAndScroll(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	if err := os.WriteFile(filepath.Join(h.a.localDir, "only-one.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.click("文件（" + shortcutText("J") + "）")
	h.pump("打开文件抽屉", func() bool {
		return tb.dir != "" && h.tt.HasText("only-one.txt")
	})

	remoteDir := filepath.Join(tb.dir, "many")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		name := fmt.Sprintf("item-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(remoteDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.click("刷新")
	h.pump("列出远端目录", func() bool {
		return h.tt.HasText("many")
	})
	h.click("many")
	h.pump("进入多文件目录", func() bool {
		return filepath.Base(tb.dir) == "many" && h.tt.HasText("item-00.txt")
	})

	localHeader, ok := h.tt.Find("本地")
	if !ok {
		t.Fatal("找不到本地标题")
	}
	remoteHeader, ok := h.tt.Find("远程")
	if !ok {
		t.Fatal("找不到远程标题")
	}
	if localHeader.Y != remoteHeader.Y {
		t.Fatalf("两栏顶部标签未水平对齐: local.Y=%v remote.Y=%v", localHeader.Y, remoteHeader.Y)
	}
	if localHeader.Y < 0 || localHeader.Y > 120 {
		t.Fatalf("两栏顶部标签位置异常（未贴齐抽屉顶部）: Y=%v", localHeader.Y)
	}

	localTitle, ok := h.tt.Find(filepath.Base(h.a.localDir))
	if !ok {
		t.Fatal("找不到本地目录名")
	}
	filesTitle, ok := h.tt.Find("many")
	if !ok {
		t.Fatal("找不到远端目录名")
	}
	if localTitle.Y != filesTitle.Y {
		t.Fatalf("左右目录标题未水平对齐: localTitle.Y=%v filesTitle.Y=%v", localTitle.Y, filesTitle.Y)
	}

	r00, ok := h.tt.Find("item-00.txt")
	if !ok {
		t.Fatal("找不到 item-00.txt")
	}
	onlyOne, ok := h.tt.Find("本地 only-one.txt")
	if !ok {
		t.Fatal("找不到 本地 only-one.txt")
	}
	if onlyOne.Y != r00.Y {
		t.Fatalf("首行文件未对齐: onlyOne.Y=%v r00.Y=%v", onlyOne.Y, r00.Y)
	}

	// 向下滚动远端列表，验证底部的 item-39.txt 可以滚入视口。
	cx, cy := r00.X+r00.W/2, r00.Y+100
	for range 10 {
		h.tt.Scroll(cx, cy, 0, 200)
		h.tt.Frame()
	}
	r39, ok := h.tt.Find("item-39.txt")
	if !ok {
		t.Fatalf("滚动后仍看不到底部文件 item-39.txt: %q", h.tt.Texts())
	}
	if r39.Y < filesTitle.Y+filesTitle.H || r39.Y+r39.H > 720 {
		t.Fatalf("底部文件不在远端栏可视范围内: r39=%+v", r39)
	}
}

// dragOnto 把一个文件行拖到另一个元素上松开。移动要超过拖动阈值，否则只算点击。
func dragOnto(t *testing.T, h *harness, from, to string) {
	t.Helper()
	src, ok := h.tt.Find(from)
	if !ok {
		t.Fatalf("找不到 %s: %q", from, h.tt.Texts())
	}
	dst, ok := h.tt.Find(to)
	if !ok {
		t.Fatalf("找不到 %s: %q", to, h.tt.Texts())
	}
	x, y := src.X+src.W/2, src.Y+min(src.H/2, 8)
	h.tt.Press(x, y)
	h.tt.Move(x+24, y)
	h.tt.Move(dst.X+dst.W/2, dst.Y+min(dst.H/2, 8))
	h.tt.Release(dst.X+dst.W/2, dst.Y+min(dst.H/2, 8))
	h.tt.Frame()
}
