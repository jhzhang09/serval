package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"serval/core/zmodem"
)

// fakeShell 模拟远程 shell：output 是远程的输出，typed 记录发给远程的数据。
type fakeShell struct {
	out   chan []byte
	mu    sync.Mutex
	typed bytes.Buffer
}

func newFakeShell() *fakeShell { return &fakeShell{out: make(chan []byte, 64)} }

func (f *fakeShell) Read(p []byte) (int, error) {
	b, ok := <-f.out
	if !ok {
		return 0, io.EOF
	}
	return copy(p, b), nil
}
func (f *fakeShell) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.typed.Write(p)
}
func (f *fakeShell) Resize(int, int) error { return nil }
func (f *fakeShell) Close() error          { return nil }
func (f *fakeShell) sent() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.typed.String()
}

// readTerminal 读终端这一侧收到的数据，直到包含 want 或超时。
func readTerminal(t *testing.T, z *zmodemConn, want string) string {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		var all []byte
		buf := make([]byte, 4096)
		for !bytes.Contains(all, []byte(want)) {
			n, err := z.Read(buf)
			all = append(all, buf[:n]...)
			if err != nil {
				break
			}
		}
		got <- string(all)
	}()
	select {
	case s := <-got:
		return s
	case <-time.After(5 * time.Second):
		t.Fatalf("终端没有收到 %q", want)
		return ""
	}
}

func TestZmodemConnHandsStreamToSessionAndBack(t *testing.T) {
	shell := newFakeShell()
	requests := make(chan *zmodemSession, 1)
	z := newZmodemConn(shell, func(s *zmodemSession) { requests <- s })

	// 信号前的文字照常显示，信号之后的数据交给会话，不显示。
	shell.out <- []byte("rz waiting to receive.**\x18B0100000023be50\r\x8a\x11")
	if got := readTerminal(t, z, "receive."); strings.Contains(got, "\x18") {
		t.Fatalf("协议数据不应显示到终端: %q", got)
	}
	s := <-requests
	if s.kind != zmodem.Upload || !bytes.HasPrefix(s.prefix, []byte("**\x18B01")) {
		t.Fatalf("会话 = %+v", s)
	}

	// 传输期间：远程的输出进会话队列；键盘输入不发给 shell；Ctrl+C 取消传输。
	shell.out <- []byte("PROTOCOL-DATA")
	ctx, cancel := context.WithCancel(context.Background())
	z.setCancel(s, cancel)
	z.Write([]byte("ls\r"))
	if strings.Contains(shell.sent(), "ls") {
		t.Fatal("传输期间键盘输入不应发给 shell")
	}
	stream := z.stream(s)
	if chunk, err := stream.Recv(context.Background()); err != nil || string(chunk) != "PROTOCOL-DATA" {
		t.Fatalf("会话应收到远程数据: %q %v", chunk, err)
	}
	z.Write([]byte{0x03})
	if ctx.Err() == nil {
		t.Fatal("Ctrl+C 应取消传输")
	}

	// 结束：会话多读的数据和队列里剩下的数据按顺序还给终端，之后恢复正常。
	shell.out <- []byte("queued-after;")
	time.Sleep(50 * time.Millisecond) // 让读取 goroutine 把它放进队列
	// 和真实终端一样，终端一直在读；交还数据时终端还会写应答回来（拿 mu），不能卡住。
	got := make(chan string, 1)
	go func() { got <- readTerminal(t, z, "prompt$ ") }()
	z.finish(s, []byte("leftover;"))
	z.Write([]byte("[1;1R")) // 终端的应答
	shell.out <- []byte("prompt$ ")
	if out := <-got; !strings.Contains(out, "leftover;queued-after;prompt$ ") {
		t.Fatalf("交还终端的数据顺序不对: %q", out)
	}
	z.Write([]byte("ls\r"))
	if !strings.Contains(shell.sent(), "ls\r") {
		t.Fatal("传输结束后键盘输入应发给 shell")
	}
}

// --- 端到端：开发用 SSH 服务器上运行真实的 lrzsz，经过完整的界面和终端。

func lrzszPath(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("没有安装 lrzsz（找不到 %s），跳过", name)
	}
	return p
}

func randomData(t *testing.T, n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// shellQuote 给 POSIX shell 用的单引号引用。
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func TestSzDownloadsIntoChosenFolder(t *testing.T) {
	sz := lrzszPath(t, "sz")
	h := newHarness(t)
	tb := h.connectDev()
	remote := t.TempDir()
	data := randomData(t, 256*1024+5)
	if err := os.WriteFile(filepath.Join(remote, "report.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	// 已有同名文件：不覆盖，新文件自动编号。
	if err := os.WriteFile(filepath.Join(dest, "report.bin"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	asked := 0
	h.a.chooseFolder = func() (string, error) { asked++; return dest, nil }

	tb.focus.term.Send([]byte(fmt.Sprintf("%s %s\r", sz, shellQuote(filepath.Join(remote, "report.bin")))))
	saved := filepath.Join(dest, "report (1).bin")
	h.pump("文件存好", func() bool {
		got, err := os.ReadFile(saved)
		return err == nil && bytes.Equal(got, data) && tb.focus.zmodem == nil
	})
	if old, _ := os.ReadFile(filepath.Join(dest, "report.bin")); string(old) != "old" {
		t.Fatal("已有的同名文件不应被覆盖")
	}
	if asked != 1 {
		t.Fatalf("应只问一次保存位置，问了 %d 次", asked)
	}
	if !h.tt.HasText("已接收 1 个文件") {
		t.Fatalf("应提示接收结果: %q", h.tt.Texts())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dest, ".*.part")); len(leftovers) != 0 {
		t.Fatalf("不应留下临时文件: %v", leftovers)
	}
	assertShellUsable(t, h, tb, remote)
}

func TestRzUploadsChosenFiles(t *testing.T) {
	rz := lrzszPath(t, "rz")
	h := newHarness(t)
	tb := h.connectDev()
	local := t.TempDir()
	a, b := randomData(t, 300*1024+1), []byte("第二个文件\n")
	os.WriteFile(filepath.Join(local, "a.bin"), a, 0o644)
	os.WriteFile(filepath.Join(local, "b.txt"), b, 0o644)
	h.a.chooseUploadFiles = func() ([]string, error) {
		return []string{filepath.Join(local, "a.bin"), filepath.Join(local, "b.txt")}, nil
	}
	remote := t.TempDir()
	tb.focus.term.Send([]byte(fmt.Sprintf("cd %s && %s\r", shellQuote(remote), rz)))
	h.pump("上传完成", func() bool {
		ga, _ := os.ReadFile(filepath.Join(remote, "a.bin"))
		gb, _ := os.ReadFile(filepath.Join(remote, "b.txt"))
		return bytes.Equal(ga, a) && bytes.Equal(gb, b) && tb.focus.zmodem == nil
	})
	if !h.tt.HasText("已上传 2 个文件") {
		t.Fatalf("应提示上传结果: %q", h.tt.Texts())
	}
	assertShellUsable(t, h, tb, remote)
}

// 在文件选择框里取消：告诉 rz 不传了，rz 退出，shell 回到可用状态。
func TestRzCancelledInChooserReturnsToShell(t *testing.T) {
	rz := lrzszPath(t, "rz")
	h := newHarness(t)
	tb := h.connectDev()
	h.a.chooseUploadFiles = func() ([]string, error) { return nil, nil }
	remote := t.TempDir()
	tb.focus.term.Send([]byte(fmt.Sprintf("cd %s && %s\r", shellQuote(remote), rz)))
	assertShellUsable(t, h, tb, remote)
	if entries, _ := os.ReadDir(remote); len(entries) != 1 { // 只有 assertShellUsable 建的标记文件
		t.Fatalf("取消后不应上传任何文件: %v", entries)
	}
}

// assertShellUsable 确认传输结束后终端回到正常：键盘输入能发给 shell 并执行。
func assertShellUsable(t *testing.T, h *harness, tb *tab, dir string) {
	t.Helper()
	marker := filepath.Join(dir, "shell-ok")
	h.pump("传输结束", func() bool { return !tb.focus.zconn.transferring() })
	tb.focus.term.Send([]byte("touch " + shellQuote(marker) + "\r"))
	h.pump("shell 可以继续使用", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
}

// dropOnTerminal 把本地文件拖到当前窗格的终端中央，返回是否有元素接住。
func dropOnTerminal(t *testing.T, h *harness, paths ...string) bool {
	t.Helper()
	box, ok := h.tt.Find("Terminal")
	if !ok {
		t.Fatalf("找不到终端: %q", h.tt.Texts())
	}
	return h.tt.DropFiles(box.X+box.W/2, box.Y+box.H/2, paths...)
}

// setTerminalDir 让远程 shell 用 OSC 7 上报当前目录，和配置了 OSC 7 的 shell 一样。
func setTerminalDir(t *testing.T, h *harness, tb *tab, dir string) {
	t.Helper()
	tb.focus.term.Send([]byte(fmt.Sprintf("cd %s && printf '\\033]7;file://localhost%%s\\a' \"$PWD\"\r", shellQuote(dir))))
	h.pump("终端上报当前目录", func() bool { return tb.focus.term.Dir() != "" })
}

func TestDropFilesOnTerminalUploadsToCurrentDir(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	remote, err := filepath.EvalSymlinks(t.TempDir()) // macOS 的临时目录经过符号链接，shell 上报的是真实路径
	if err != nil {
		t.Fatal(err)
	}
	setTerminalDir(t, h, tb, remote)

	local := t.TempDir()
	data := randomData(t, 128*1024+3)
	file := filepath.Join(local, "dropped.bin")
	os.WriteFile(file, data, 0o644)
	if !dropOnTerminal(t, h, file) {
		t.Fatal("拖到远程终端上的文件应被接住")
	}
	h.pump("上传完成", func() bool {
		got, _ := os.ReadFile(filepath.Join(remote, "dropped.bin"))
		return bytes.Equal(got, data)
	})
	if !h.a.filesOpen {
		t.Fatal("拖放上传后应打开文件抽屉显示进度")
	}
	h.tt.Frame()
	if tb.focus.fileDrag {
		t.Fatal("松开后接收层应消失，不能挡住终端的鼠标操作")
	}
}

// 远端已有同名文件时逐个确认，不会互相覆盖掉确认框。
func TestDropAsksBeforeReplacingEachFile(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	remote, _ := filepath.EvalSymlinks(t.TempDir())
	setTerminalDir(t, h, tb, remote)
	local := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		os.WriteFile(filepath.Join(remote, name), []byte("old"), 0o644)
		os.WriteFile(filepath.Join(local, name), []byte("new "+name), 0o644)
	}
	dropOnTerminal(t, h, filepath.Join(local, "a.txt"), filepath.Join(local, "b.txt"))
	h.pump("两个替换确认", func() bool { return len(h.a.pendingUploads) == 2 })
	h.click("替换") // 第一个替换
	h.tt.Frame()
	h.click("取消") // 第二个不替换
	h.pump("传输结束", func() bool {
		got, _ := os.ReadFile(filepath.Join(remote, h.a.tabs[0].transfers[0].fileName))
		return len(h.a.pendingUploads) == 0 && strings.HasPrefix(string(got), "new")
	})
	replaced := h.a.tabs[0].transfers[0].fileName
	other := map[string]string{"a.txt": "b.txt", "b.txt": "a.txt"}[replaced]
	if got, _ := os.ReadFile(filepath.Join(remote, other)); string(got) != "old" {
		t.Fatalf("选择取消的 %s 不应被替换: %q", other, got)
	}
}
