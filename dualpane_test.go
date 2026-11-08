package main

import (
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
