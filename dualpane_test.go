package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
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

	localTitle, ok := h.tt.Find(drawerPath(filepath.ToSlash(h.a.localDir)))
	if !ok {
		t.Fatalf("找不到本地路径标题 %q: %q", drawerPath(filepath.ToSlash(h.a.localDir)), h.tt.Texts())
	}
	filesTitle, ok := h.tt.Find(drawerPath(tb.dir))
	if !ok {
		t.Fatalf("找不到远端路径标题 %q: %q", drawerPath(tb.dir), h.tt.Texts())
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

	// 拖动终端与文件抽屉之间的分隔条，验证抽屉总宽度可以左右调整。
	resizeGrip, ok := h.tt.Find("调整文件抽屉宽度")
	if !ok {
		t.Fatal("找不到 调整文件抽屉宽度")
	}
	gx, gy := resizeGrip.X+resizeGrip.W/2, resizeGrip.Y+resizeGrip.H/2
	h.tt.Press(gx, gy)
	h.tt.Move(gx-80, gy)
	h.tt.Release(gx-80, gy)
	h.tt.Frame()
	if h.a.filesWidth <= 560 {
		t.Fatalf("向左拖动后抽屉宽度应增大: filesWidth=%v", h.a.filesWidth)
	}

	// 拖动抽屉内部左右栏之间的分隔线，验证本地栏与远程栏比例可左右调整。
	remoteAfter, ok := h.tt.Find("远程")
	if !ok {
		t.Fatal("找不到远程标题")
	}
	midX, midY := remoteAfter.X-14, remoteAfter.Y+100
	h.tt.Press(midX, midY)
	h.tt.Move(midX-60, midY)
	h.tt.Release(midX-60, midY)
	h.tt.Frame()
	if h.a.filesSplit >= 50 {
		t.Fatalf("向左拖动内部拆分线后本地栏比例应减小: filesSplit=%v", h.a.filesSplit)
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

func TestDualPaneEditablePathsAndBookmarks(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	h.click("文件（" + shortcutText("J") + "）")
	h.pump("打开文件抽屉", func() bool { return tb.dir != "" })

	origRemote := tb.dir
	subRemote := filepath.Join(origRemote, "jump-sub")
	if err := os.MkdirAll(subRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subRemote, "marker.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	subLocal := filepath.Join(h.a.localDir, "local-sub")
	if err := os.MkdirAll(subLocal, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subLocal, "local-marker.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. 点击远端路径栏进入编辑，按 Esc 取消编辑
	h.click("编辑远程路径")
	h.tt.Frame()
	if !h.a.editingRemotePath {
		t.Fatal("点击编辑远程路径后应进入编辑状态")
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	if h.a.editingRemotePath || tb.dir != origRemote {
		t.Fatalf("按 Esc 后应取消编辑并保持原目录: editing=%v dir=%q", h.a.editingRemotePath, tb.dir)
	}

	// 2. 再次进入远端路径编辑，填入子目录并回车跳转
	h.click("编辑远程路径")
	h.tt.Frame()
	h.a.remotePathDraft = filepath.ToSlash(subRemote)
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.pump("远端路径跳转", func() bool {
		return tb.dir == filepath.ToSlash(subRemote) && h.tt.HasText("marker.txt")
	})

	// 3. 收藏当前远程目录，验证写进保险库且重编主机不丢失
	h.click("远程目录书签")
	if err := h.tt.ChooseMenuItem("收藏当前目录"); err != nil {
		t.Fatalf("收藏当前目录失败: %v，菜单=%q", err, h.tt.Menu())
	}
	h.tt.Frame()
	if !slices.Contains(h.host("dev").Bookmarks, filepath.ToSlash(subRemote)) {
		t.Fatalf("保险库主机记录未保存书签: %+v", h.host("dev").Bookmarks)
	}
	// 编辑主机信息后保存，已保存的书签必须保留
	h.a.openEdit(h.host("dev"))
	h.tt.Frame()
	h.click("保存")
	h.tt.Frame()
	if !slices.Contains(h.host("dev").Bookmarks, filepath.ToSlash(subRemote)) {
		t.Fatalf("编辑主机后书签丢失: %+v", h.host("dev").Bookmarks)
	}

	// 4. 返回上一级目录，然后通过书签菜单直达，再删除书签
	h.click("上一级")
	h.pump("返回上级目录", func() bool { return tb.dir == filepath.ToSlash(origRemote) })
	h.click("远程目录书签")
	if err := h.tt.ChooseMenuItem(filepath.ToSlash(subRemote)); err != nil {
		t.Fatalf("点击书签跳转失败: %v，菜单=%q", err, h.tt.Menu())
	}
	h.pump("书签跳转到子目录", func() bool {
		return tb.dir == filepath.ToSlash(subRemote) && h.tt.HasText("marker.txt")
	})
	h.click("远程目录书签")
	if err := h.tt.ChooseMenuItem("删除书签", filepath.ToSlash(subRemote)); err != nil {
		t.Fatalf("删除书签失败: %v，菜单=%q", err, h.tt.Menu())
	}
	h.tt.Frame()
	if len(h.host("dev").Bookmarks) != 0 {
		t.Fatalf("删除后书签应为空: %+v", h.host("dev").Bookmarks)
	}

	// 5. 点击本地路径栏输入本地子目录并回车跳转
	h.click("编辑本地路径")
	h.tt.Frame()
	h.a.localPathDraft = subLocal
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.pump("本地路径跳转", func() bool {
		return h.a.localDir == subLocal && h.tt.HasText("本地 local-marker.txt")
	})
}

func TestShortPathCrossPlatform(t *testing.T) {
	gotWin := shortPath(`C:\Users\alice\projects\serval`)
	if strings.Contains(gotWin, `\`) || gotWin != "…/alice/projects/serval" {
		t.Fatalf("Windows 路径缩写应使用正斜杠并保留末三级: got=%q", gotWin)
	}
	gotPOSIX := shortPath("/var/log/nginx/access")
	if gotPOSIX != "…/log/nginx/access" {
		t.Fatalf("POSIX 路径缩写不符合预期: got=%q", gotPOSIX)
	}
}

func TestLocalNewFolderRenameUploadAndDelete(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	sample := filepath.Join(h.a.localDir, "local-menu.txt")
	if err := os.WriteFile(sample, []byte("from-menu"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.click("文件（" + shortcutText("J") + "）")
	h.pump("两栏列出文件", func() bool {
		return tb.dir != "" && h.tt.HasText("local-menu.txt")
	})

	// 1. 新建本地文件夹
	h.click("新建本地文件夹")
	h.tt.Frame()
	h.a.fileOp.input = "local-created"
	h.tt.Frame()
	h.click("创建")
	h.pump("新建本地目录", func() bool {
		info, err := os.Stat(filepath.Join(h.a.localDir, "local-created"))
		return err == nil && info.IsDir() && h.tt.HasText("本地 local-created")
	})

	// 2. 右键本地文件夹重命名
	if err := h.tt.RightClick("本地 local-created"); err != nil {
		t.Fatalf("右键本地目录失败: %v", err)
	}
	if err := h.tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatalf("选择重命名失败: %v，菜单=%q", err, h.tt.Menu())
	}
	h.tt.Frame()
	h.a.fileOp.input = "local-renamed"
	h.tt.Frame()
	h.click("重命名")
	h.pump("重命名本地目录", func() bool {
		_, oldErr := os.Stat(filepath.Join(h.a.localDir, "local-created"))
		info, newErr := os.Stat(filepath.Join(h.a.localDir, "local-renamed"))
		return os.IsNotExist(oldErr) && newErr == nil && info.IsDir() && h.tt.HasText("本地 local-renamed")
	})

	// 3. 右键本地文件上传到远端
	if err := h.tt.RightClick("本地 local-menu.txt"); err != nil {
		t.Fatalf("右键本地文件失败: %v", err)
	}
	if err := h.tt.ChooseMenuItem("上传文件"); err != nil {
		t.Fatalf("右键上传文件失败: %v，菜单=%q", err, h.tt.Menu())
	}
	h.pump("右键上传完成", func() bool {
		got, err := os.ReadFile(filepath.Join(tb.dir, "local-menu.txt"))
		return err == nil && string(got) == "from-menu"
	})

	// 4. 右键删除本地空文件夹
	if err := h.tt.RightClick("本地 local-renamed"); err != nil {
		t.Fatalf("右键删除本地目录失败: %v", err)
	}
	if err := h.tt.ChooseMenuItem("删除"); err != nil {
		t.Fatalf("选择删除失败: %v，菜单=%q", err, h.tt.Menu())
	}
	h.tt.Frame()
	h.click("删除")
	h.pump("本地目录已删除", func() bool {
		_, err := os.Stat(filepath.Join(h.a.localDir, "local-renamed"))
		return os.IsNotExist(err)
	})
}
