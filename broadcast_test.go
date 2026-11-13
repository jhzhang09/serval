package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestTerminalReplyIsNotUserInput(t *testing.T) {
	replies := []string{
		"\x1b[I",
		"\x1b[O",
		"\x1b[24;80R",
		"\x1b[0c",
		"\x1b[?1;2c",
		"\x1b[>0;1;0c",
		"\x1b[0n",
		"\x1b[?997;1n",
		"\x1b]11;rgb:0000/0000/0000\x07",
	}
	keys := []string{
		"\x1b",
		"\x1b[A",
		"\x1b[B",
		"\x1b[C",
		"\x1b[D",
		"\x1bOA",
		"\x1b[1;5A",
		"\x1b[200~",
		"\x1b[27u",
		"echo hi\r",
		"\x03",
	}
	for _, s := range replies {
		if !isTerminalReply([]byte(s)) {
			t.Errorf("应视为终端应答: %q", s)
		}
	}
	for _, s := range keys {
		if isTerminalReply([]byte(s)) {
			t.Errorf("应视为用户输入: %q", s)
		}
	}
}

func TestLocalBroadcastBytes(t *testing.T) {
	if got := string(localBroadcastBytes(0, ui.KeyA)); got != "a" {
		t.Fatalf("a = %q", got)
	}
	if got := string(localBroadcastBytes(ui.Shift, ui.KeyA)); got != "A" {
		t.Fatalf("A = %q", got)
	}
	if got := string(localBroadcastBytes(ui.Ctrl, ui.KeyC)); got != "\x03" {
		t.Fatalf("ctrl+c = %q", got)
	}
	if got := string(localBroadcastBytes(0, ui.KeyEnter)); got != "\r" {
		t.Fatalf("enter = %q", got)
	}
	if got := string(localBroadcastBytes(0, ui.KeyUp)); got != "\x1b[A" {
		t.Fatalf("up = %q", got)
	}
	if ui.Cmd != ui.Ctrl && localBroadcastBytes(ui.Cmd, ui.KeyK) != nil {
		t.Fatal("Command 组合应留给应用，不广播")
	}
	if localBroadcastBytes(ui.Alt, ui.KeyA) != nil {
		t.Fatal("Alt 组合不猜编码")
	}
}

func TestZmodemInputHookSkipsReplies(t *testing.T) {
	shell := newFakeShell()
	var got []string
	z := newZmodemConn(shell, func(*zmodemSession) {})
	z.onInput = func(b []byte) { got = append(got, string(append([]byte(nil), b...))) }

	if _, err := z.Write([]byte("echo hi\r")); err != nil {
		t.Fatal(err)
	}
	if _, err := z.Write([]byte("\x1b[I")); err != nil {
		t.Fatal(err)
	}
	if _, err := z.Write([]byte("\x1b[12;40R")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "echo hi\r" {
		t.Fatalf("只应镜像用户输入: %q", got)
	}
	if shell.sent() != "echo hi\r\x1b[I\x1b[12;40R" {
		t.Fatalf("写进 shell 的内容变了: %q", shell.sent())
	}

	z.mu.Lock()
	z.active = &zmodemSession{}
	z.mu.Unlock()
	if _, err := z.Write([]byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("传输期间不应镜像: %q", got)
	}
}

func TestBroadcastInputReachesMembers(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	first := tb.focus

	h.click("向右拆分（" + shortcutText("D") + "）")
	h.pump("拆分", func() bool { return tb.root.leaves() == 2 })
	second := tb.focus
	if second == first {
		t.Fatal("新窗格应获得焦点")
	}

	h.click("搜索主机和操作")
	h.a.pal.query = "广播输入"
	h.tt.Frame()
	h.click("广播输入")
	h.tt.Frame()
	if !h.a.broadcast[second.id] || h.a.broadcast[first.id] {
		t.Fatalf("开启广播应只加入当前窗格: %v", h.a.broadcast)
	}
	if !h.tt.HasText("广播") {
		t.Fatal("已加入的窗格应标出“广播”")
	}

	// Send 和真实按键一样写进 Conn，广播钩子就在这条路上。
	second.term.Send([]byte("echo only-source-$((20+3))\n"))
	h.pump("源窗格输出", func() bool { return strings.Contains(second.term.Text(), "only-source-23") })
	settle := time.Now().Add(500 * time.Millisecond)
	h.pump("另一窗格不应收到", func() bool {
		return time.Now().After(settle) || strings.Contains(first.term.Text(), "only-source-23")
	})
	if strings.Contains(first.term.Text(), "only-source-23") {
		t.Fatal("未加入的窗格收到了输入")
	}

	h.click(fmt.Sprintf("窗格操作 %d", paneOrdinal(tb.root, first)))
	h.tt.Frame()
	h.click(broadcastButtonLabel(false, tb.host.Name, paneOrdinal(tb.root, first)))
	h.tt.Frame()
	if !h.a.broadcast[first.id] {
		t.Fatal("点加入后应成为广播成员")
	}

	second.term.Send([]byte("echo bcast-both-$((30+4))\n"))
	h.pump("两个窗格都收到", func() bool {
		return strings.Contains(second.term.Text(), "bcast-both-34") && strings.Contains(first.term.Text(), "bcast-both-34")
	})
	for _, p := range []*pane{first, second} {
		if n := strings.Count(p.term.Text(), "bcast-both-34"); n < 1 || n > 4 {
			t.Fatalf("窗格 %d 里标记出现 %d 次", p.id, n)
		}
	}

	h.a.closeFocused(tb)
	h.tt.Frame()
	if h.a.broadcast[second.id] {
		t.Fatal("关闭的窗格应离开广播")
	}
	if !h.a.broadcast[first.id] || !h.a.broadcastOn {
		t.Fatal("另一个窗格应继续留在广播里")
	}
	h.a.closeFocused(tb)
	h.tt.Frame()
	if h.a.broadcastOn || len(h.a.tabs) != 0 {
		t.Fatalf("最后一个窗格关闭后广播应结束: on=%v tabs=%d", h.a.broadcastOn, len(h.a.tabs))
	}
}

func TestBroadcastProductionConfirm(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	tb.host.Production = true
	h.tt.Frame()

	h.click("搜索主机和操作")
	h.a.pal.query = "广播输入"
	h.tt.Frame()
	h.click("广播输入")
	h.tt.Frame()
	if h.a.broadcastAsk == nil {
		t.Fatal("生产环境加入广播前应先确认")
	}
	if h.a.broadcast[tb.focus.id] {
		t.Fatal("确认前不应加入")
	}
	h.click("取消")
	h.tt.Frame()
	if h.a.broadcastOn || h.a.broadcastAsk != nil || len(h.a.broadcast) != 0 {
		t.Fatalf("取消后广播应保持关闭: on=%v ask=%v members=%v", h.a.broadcastOn, h.a.broadcastAsk, h.a.broadcast)
	}

	h.click("搜索主机和操作")
	h.a.pal.query = "广播输入"
	h.tt.Frame()
	h.click("广播输入")
	h.tt.Frame()
	h.click("加入广播")
	h.tt.Frame()
	if !h.a.broadcast[tb.focus.id] || h.a.broadcastAsk != nil {
		t.Fatalf("确认后应加入: members=%v ask=%v", h.a.broadcast, h.a.broadcastAsk)
	}

	first := tb.focus
	h.click("向右拆分（" + shortcutText("D") + "）")
	h.pump("拆分", func() bool { return tb.root.leaves() == 2 })
	second := tb.focus
	if h.a.broadcast[second.id] {
		t.Fatal("新窗格不应自动加入广播")
	}
	h.click(fmt.Sprintf("窗格操作 %d", paneOrdinal(tb.root, second)))
	h.tt.Frame()
	h.click(broadcastButtonLabel(false, tb.host.Name, paneOrdinal(tb.root, second)))
	h.tt.Frame()
	if h.a.broadcastAsk == nil || h.a.broadcast[second.id] {
		t.Fatal("第二个生产窗格也应先确认")
	}
	h.click("取消")
	h.tt.Frame()
	if h.a.broadcast[second.id] || !h.a.broadcast[first.id] || !h.a.broadcastOn {
		t.Fatal("取消第二个窗格时，已经加入的窗格和广播本身应保留")
	}

	h.click(fmt.Sprintf("窗格操作 %d", paneOrdinal(tb.root, second)))
	h.tt.Frame()
	h.click(broadcastButtonLabel(false, tb.host.Name, paneOrdinal(tb.root, second)))
	h.tt.Frame()
	h.click("加入广播")
	h.tt.Frame()
	if !h.a.broadcast[second.id] {
		t.Fatal("确认后第二个窗格应加入")
	}
	// 拆分把焦点给了新窗格。加入之后它才是镜像源。
	h.tt.Frame()
	second.term.Send([]byte("echo prod-both-$((7+1))\n"))
	h.pump("生产环境的两个窗格", func() bool {
		return strings.Contains(first.term.Text(), "prod-both-8") && strings.Contains(second.term.Text(), "prod-both-8")
	})
}
