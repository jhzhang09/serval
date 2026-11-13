package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"serval/core/hosts"
	"serval/core/keys"
	"serval/locales"

	"github.com/egoist/mygo/ui"
)

func TestSSHCommandForHost(t *testing.T) {
	all := []hosts.Host{
		{ID: "gw", Name: "gw", Address: "10.0.0.1", Port: 2222, User: "jump"},
		{ID: "app", Name: "app", Address: "10.0.1.5", Port: 2200, User: "deploy", ProxyJump: "gw"},
		{ID: "tl", Name: "tl", Address: "192.168.1.2", Port: 2323, Protocol: hosts.ProtocolTelnet},
		{ID: "ser", Name: "ser", Address: "/dev/ttyUSB0", BaudRate: 57600, Protocol: hosts.ProtocolSerial},
	}

	if got := sshCommandForHost(all[0], all, "local"); got != "ssh -p 2222 jump@10.0.0.1" {
		t.Fatalf("direct SSH cmd mismatch: %q", got)
	}
	if got := sshCommandForHost(all[1], all, "local"); got != "ssh -p 2200 -J jump@10.0.0.1:2222 deploy@10.0.1.5" {
		t.Fatalf("jump SSH cmd mismatch: %q", got)
	}
	if got := sshCommandForHost(all[2], all, "local"); got != "telnet 192.168.1.2 2323" {
		t.Fatalf("telnet cmd mismatch: %q", got)
	}
	if got := sshCommandForHost(all[3], all, "local"); got != "screen /dev/ttyUSB0 57600" {
		t.Fatalf("serial cmd mismatch: %q", got)
	}
}

func TestHostMenuActionsAndDeployKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	k1, err := keys.Generate(keys.Ed25519, "k1-deploy", "", "")
	if err != nil {
		t.Fatal(err)
	}
	k1.ID = "k1"
	k2, err := keys.Generate(keys.Ed25519, "k2-deploy", "", "")
	if err != nil {
		t.Fatal(err)
	}
	k2.ID = "k2"

	h := newHarness(t)
	if err := h.a.apply(keyChange(k1), keyChange(k2)); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()

	// 1. 右键复制 SSH 命令
	if err := h.tt.RightClick("dev"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("复制 SSH 命令"); err != nil {
		t.Fatalf("选择复制 SSH 命令失败: %v，菜单=%q", err, h.tt.Menu())
	}
	h.tt.Frame()
	wantCmd := fmt.Sprintf("ssh -p %d tester@127.0.0.1", h.devHost().Port)
	if h.tt.Clipboard() != wantCmd {
		t.Fatalf("剪贴板连接命令 = %q，期望 %q", h.tt.Clipboard(), wantCmd)
	}

	// 2. 右键“仅打开 SFTP”自动连接并打开文件抽屉
	if err := h.tt.RightClick("dev"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("仅打开 SFTP"); err != nil {
		t.Fatalf("选择仅打开 SFTP 失败: %v，菜单=%q", err, h.tt.Menu())
	}
	h.pump("输入密码弹窗", func() bool { return h.a.auth.open })
	h.a.auth.secret = testPassword
	h.tt.Frame()
	h.click("连接")
	h.trustAndWait(0)
	h.pump("自动展开 SFTP 文件抽屉", func() bool {
		return h.a.filesOpen && len(h.a.tabs) == 1 && h.a.tabs[0].dir != ""
	})

	// 3. 右键“向右分屏打开”在已连接会话上分屏
	if err := h.tt.RightClick("dev"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("向右分屏打开"); err != nil {
		t.Fatalf("选择向右分屏打开失败: %v，菜单=%q", err, h.tt.Menu())
	}
	h.pump("向右分屏打开新窗格", func() bool { return h.a.tabs[0].root.leaves() == 2 })

	// 4. 从主机右键菜单“部署公钥到服务器”部署第一把公钥
	if err := h.tt.RightClick("dev"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("部署公钥到服务器", "k1-deploy"); err != nil {
		t.Fatalf("从主机菜单部署公钥失败: %v，菜单=%q", err, h.tt.Menu())
	}
	authFile := filepath.Join(home, ".ssh", "authorized_keys")
	h.pump("写入 authorized_keys（k1）", func() bool {
		raw, err := os.ReadFile(authFile)
		return err == nil && strings.Contains(string(raw), k1.PublicKey)
	})

	// 5. 从密钥详情页“部署到主机”部署第二把公钥
	h.a.openKeys()
	h.a.showKey("k2")
	h.tt.Frame()
	h.click("部署到主机")
	if err := h.tt.ChooseMenuItem("dev"); err != nil {
		t.Fatalf("从密钥详情部署到主机失败: %v，菜单=%q", err, h.tt.Menu())
	}
	h.pump("写入 authorized_keys（k2）", func() bool {
		raw, err := os.ReadFile(authFile)
		return err == nil && strings.Contains(string(raw), k2.PublicKey)
	})
}

func TestHostHealthProbeAndCancel(t *testing.T) {
	reachLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer reachLn.Close()
	go func() {
		for {
			c, err := reachLn.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	reachPort := reachLn.Addr().(*net.TCPAddr).Port

	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := deadLn.Addr().(*net.TCPAddr).Port
	_ = deadLn.Close()

	h := newHarness(t,
		hosts.Host{ID: "ok-host", Name: "ok-host", Address: "127.0.0.1", Port: reachPort, User: "ops"},
		hosts.Host{ID: "dead-host", Name: "dead-host", Address: "127.0.0.1", Port: deadPort, User: "ops"},
	)
	h.tt.Frame()

	h.click("检测连通性")
	h.pump("完成连通性批量探测", func() bool {
		okSt := h.a.health["ok-host"].state
		deadSt := h.a.health["dead-host"].state
		return okSt == probeOK && deadSt == probeFail
	})

	if tip := hostTooltip(h.host("ok-host"), h.a.health["ok-host"]); !strings.Contains(tip, "可达") {
		t.Fatalf("可达主机提示信息异常: %q", tip)
	}
	if tip := hostTooltip(h.host("dead-host"), h.a.health["dead-host"]); !strings.Contains(tip, "不可达") {
		t.Fatalf("不可达主机提示信息异常: %q", tip)
	}

	// closeAll 应安全取消并清理探测状态
	h.a.closeAll()
	if h.a.probeCancel != nil {
		t.Fatal("closeAll 后 probeCancel 应置空")
	}
}

func TestPaneQuickAIButtonAndShortcut(t *testing.T) {
	ts := mockAIServer(t)
	defer ts.Close()

	h := newHarness(t)
	h.a.settings.AIBaseURL = ts.URL
	h.a.settings.AIModel = "gpt-4o"
	h.a.setAIKey("test-sk")

	tb := h.connectDev()
	tb.focus.term.Feed([]byte("fatal error: connection refused\r\n"))
	h.tt.Frame()

	// 点击窗格标题栏的快捷 AI 分析按钮
	h.click(locales.AiAnalyzeOutput())
	h.pump("窗格快捷 AI 按钮触发分析", func() bool {
		return h.a.ai.open && h.a.ai.command != ""
	})

	// 关闭 AI 抽屉后测试 Cmd+E / Ctrl+Shift+E 快捷键
	h.a.ai.open = false
	h.a.ai.command = ""
	h.tt.Frame()

	h.tt.Key(chromeShortcut(), ui.KeyE)
	h.pump("快捷键触发 AI 分析", func() bool {
		return h.a.ai.open && h.a.ai.command != ""
	})
}
