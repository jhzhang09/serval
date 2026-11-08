package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTree 按 files（相对路径 → 内容）建一个本地目录树。
func makeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<不存在>"
	}
	return string(b)
}

// folderHarness 连上开发服务器，并让终端上报一个临时的远程当前目录。
func folderHarness(t *testing.T) (*harness, *tab, string) {
	h := newHarness(t)
	tb := h.connectDev()
	remote, _ := filepath.EvalSymlinks(t.TempDir())
	setTerminalDir(t, h, tb, remote)
	return h, tb, remote
}

func TestDropNewFolderUploadsWholeTree(t *testing.T) {
	h, tb, remote := folderHarness(t)
	local := filepath.Join(t.TempDir(), "project")
	makeTree(t, local, map[string]string{"README.md": "说明", "src/main.go": "package main", "src/deep/x.bin": strings.Repeat("x", 70000)})
	os.MkdirAll(filepath.Join(local, "empty"), 0o755)
	os.Symlink(filepath.Join(local, "README.md"), filepath.Join(local, "link.md"))

	dropOnTerminal(t, h, local)
	h.pump("上传完成", func() bool { return len(tb.transfers) == 1 && tb.transfers[0].done.Load() })
	tr := tb.transfers[0]
	if tr.err != "" || !tr.folder || tr.filesDone.Load() != 3 {
		t.Fatalf("传输记录不对: err=%q folder=%v files=%d", tr.err, tr.folder, tr.filesDone.Load())
	}
	dst := filepath.Join(remote, "project")
	for rel, want := range map[string]string{"README.md": "说明", "src/main.go": "package main", "src/deep/x.bin": strings.Repeat("x", 70000)} {
		if got := readFile(t, filepath.Join(dst, rel)); got != want {
			t.Errorf("%s 内容不对（%d 字节）", rel, len(got))
		}
	}
	if info, err := os.Stat(filepath.Join(dst, "empty")); err != nil || !info.IsDir() {
		t.Error("空目录也应建好")
	}
	if _, err := os.Lstat(filepath.Join(dst, "link.md")); err == nil {
		t.Error("符号链接应跳过")
	}
	if !h.tt.HasText("跳过了 1 个符号链接或特殊文件") {
		t.Fatalf("应说明跳过了符号链接: %q", h.tt.Texts())
	}
}

// 远端已有同名文件夹：合并进去；有同名文件时只问一次。
func TestDropFolderMergesAndAsksOnce(t *testing.T) {
	// 子测试用英文名：t.TempDir 的路径带测试名，setTerminalDir 用 printf 发的 OSC 7 没做百分号编码，
	// 路径里有中文时终端不认（真实 shell 的集成脚本会编码）。
	for _, tc := range []struct {
		name, choice      string
		wantSame, wantNew string
	}{
		{"replace", "全部替换", "new", "new"},
		{"skip", "全部跳过", "old", "new"},
		{"cancel", "取消", "old", "<不存在>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, tb, remote := folderHarness(t)
			dst := filepath.Join(remote, "site")
			makeTree(t, dst, map[string]string{"a.txt": "old", "sub/b.txt": "old", "remote-only.txt": "keep"})
			local := filepath.Join(t.TempDir(), "site")
			makeTree(t, local, map[string]string{"a.txt": "new", "sub/b.txt": "new", "sub/c.txt": "new"})

			dropOnTerminal(t, h, local)
			h.pump("同名文件确认", func() bool { return len(h.a.pendingFolders) == 1 })
			if n := len(h.a.pendingFolders[0].conflicts); n != 2 {
				t.Fatalf("应找到 2 个同名文件，找到 %d 个", n)
			}
			h.tt.Frame()
			if !h.tt.HasText("文件夹 site 里有 2 个同名文件") {
				t.Fatalf("确认框标题不对: %q", h.tt.Texts())
			}
			h.click(tc.choice)
			h.tt.Frame()
			if tc.choice != "取消" {
				h.pump("上传完成", func() bool { return len(tb.transfers) == 1 && tb.transfers[0].done.Load() })
			}
			if len(h.a.pendingFolders) != 0 {
				t.Fatal("同一个文件夹只应问一次")
			}
			if got := readFile(t, filepath.Join(dst, "a.txt")); got != tc.wantSame {
				t.Errorf("a.txt = %q，期望 %q", got, tc.wantSame)
			}
			if got := readFile(t, filepath.Join(dst, "sub/b.txt")); got != tc.wantSame {
				t.Errorf("sub/b.txt = %q，期望 %q", got, tc.wantSame)
			}
			if got := readFile(t, filepath.Join(dst, "sub/c.txt")); got != tc.wantNew {
				t.Errorf("sub/c.txt = %q，期望 %q", got, tc.wantNew)
			}
			if got := readFile(t, filepath.Join(dst, "remote-only.txt")); got != "keep" {
				t.Errorf("合并时服务器上独有的文件应保留: %q", got)
			}
		})
	}
}

// 远端同名的是文件而不是文件夹：没法合并，说明原因，什么都不传。
func TestDropFolderOntoSameNamedFileRefuses(t *testing.T) {
	h, tb, remote := folderHarness(t)
	os.WriteFile(filepath.Join(remote, "data"), []byte("a file"), 0o644)
	local := filepath.Join(t.TempDir(), "data")
	makeTree(t, local, map[string]string{"x.txt": "x"})
	dropOnTerminal(t, h, local)
	h.pump("拒绝提示", func() bool { return h.tt.HasText("不是文件夹，没法合并") })
	if len(tb.transfers) != 0 || readFile(t, filepath.Join(remote, "data")) != "a file" {
		t.Fatal("类型冲突时不应上传，也不应改动远端")
	}
}

// 中途取消：已传完的文件保留，不留下半截的临时文件。
func TestCancelFolderUploadKeepsFinishedFiles(t *testing.T) {
	h, tb, remote := folderHarness(t)
	local := filepath.Join(t.TempDir(), "big")
	files := map[string]string{}
	for i := range 6 {
		files[filepath.ToSlash(filepath.Join("part", string(rune('a'+i))+".bin"))] = string(bytes.Repeat([]byte{byte(i)}, 8<<20))
	}
	makeTree(t, local, files)
	dropOnTerminal(t, h, local)
	h.pump("开始上传", func() bool { return len(tb.transfers) == 1 && tb.transfers[0].filesDone.Load() >= 1 })
	tr := tb.transfers[0]
	h.a.cancelTransfer(tr)
	h.pump("取消完成", func() bool { return tr.done.Load() })
	if tr.err != "已取消" {
		t.Fatalf("状态 = %q（全部传完了说明测试文件太小）", tr.err)
	}
	done := int(tr.filesDone.Load())
	var kept int
	filepath.WalkDir(filepath.Join(remote, "big"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(p, ".serval-part") {
			t.Errorf("不应留下半截文件: %s", p)
		}
		if d.Type().IsRegular() {
			kept++
		}
		return nil
	})
	if kept != done || done >= len(files) {
		t.Fatalf("应保留已传完的 %d 个文件，服务器上有 %d 个（共 %d 个）", done, kept, len(files))
	}
}
