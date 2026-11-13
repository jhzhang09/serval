package main

import (
	"testing"
)

func TestPromptTrackerOSC0AndCentOSPrompt(t *testing.T) {
	pt := newPromptTracker()
	u, d, changed := pt.feed([]byte("\x1b]0;root@bogon:/etc/nginx\x07[root@bogon nginx]# "))
	if !changed || u != "root" || d != "/etc/nginx" {
		t.Fatalf("got (%q, %q, %v), want (root, /etc/nginx, true)", u, d, changed)
	}

	// 跨分片 OSC 0 更新目录
	_, _, changed = pt.feed([]byte("\r\n\x1b]0;root@bogon:"))
	if changed {
		t.Fatal("未完整接收 OSC 时不应提前变化")
	}
	u, d, changed = pt.feed([]byte("/var/log\x1b\\[root@bogon log]# "))
	if !changed || u != "root" || d != "/var/log" {
		t.Fatalf("got (%q, %q, %v), want (root, /var/log, true)", u, d, changed)
	}
}

func TestPromptTrackerOSC7OverOSC0AndUserSwitch(t *testing.T) {
	pt := newPromptTracker()
	u, d, changed := pt.feed([]byte("\x1b]7;file://vm1/home/alice/work\x07\x1b]0;alice@vm1: ~/work\x07alice@vm1:~/work$ "))
	if !changed || u != "alice" || d != "/home/alice/work" {
		t.Fatalf("OSC 7 绝对路径应优先于 OSC 0 缩写路径: got (%q, %q, %v)", u, d, changed)
	}

	// sudo -i 后只有 OSC 0，且用户切换为 root
	u, d, changed = pt.feed([]byte("\r\n\x1b]0;root@vm1: ~\x07[root@vm1 ~]# "))
	if !changed || u != "root" || d != "~" {
		t.Fatalf("切换用户后应更新为 root 与 ~: got (%q, %q, %v)", u, d, changed)
	}
}

func TestPromptTrackerPlainPromptFallback(t *testing.T) {
	pt := newPromptTracker()
	u, d, changed := pt.feed([]byte("Last login: Fri Nov 13\r\n[root@bogon ~]# "))
	if !changed || u != "root" || d != "~" {
		t.Fatalf("got (%q, %q, %v), want (root, ~, true)", u, d, changed)
	}

	u, d, changed = pt.feed([]byte("cd /opt/app\r\n(venv) ubuntu@ip-10-0-0-1:/opt/app$ "))
	if !changed || u != "ubuntu" || d != "/opt/app" {
		t.Fatalf("got (%q, %q, %v), want (ubuntu, /opt/app, true)", u, d, changed)
	}

	// 普通命令输出（如 git URL）不能误判成提示符
	_, _, changed = pt.feed([]byte("git clone git@github.com:org/repo\r\n"))
	if changed {
		t.Fatal("git URL 输出不应被误判为提示符")
	}
}

func TestPaneHeaderDisplaysUserAndDir(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	p := tb.focus

	// 刚连接时默认显示登录用户（devsshd 测试主机用户名是 tester）
	if !h.tt.HasText(tb.host.User) {
		t.Fatalf("顶栏应显示当前登录用户 %q: %q", tb.host.User, h.tt.Texts())
	}

	// 经真实 SSH 会话输出 OSC 0，验证顶栏实时更新为 root 与 /etc/nginx
	p.term.Send([]byte("printf '\\033]0;root@bogon:/etc/nginx\\007'\n"))
	h.pump("顶栏实时显示 root 与目录", func() bool {
		return h.tt.HasText("root") && h.tt.HasText("/etc/nginx")
	})
	if _, ok := h.tt.Find("root"); !ok {
		t.Fatal("顶栏应渲染 root 用户徽标")
	}
}
