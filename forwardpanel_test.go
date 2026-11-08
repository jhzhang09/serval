package main

import (
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func localEcho(t *testing.T) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				io.Copy(conn, conn)
				conn.Close()
			}()
		}
	}()
	tcp := ln.Addr().(*net.TCPAddr)
	return tcp.IP.String(), tcp.Port
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestNormalizeForwardBind(t *testing.T) {
	if got, msg := normalizeForwardBind(" localhost "); got != "127.0.0.1" || msg != "" {
		t.Fatalf("localhost 应规范成回环，得到 %q %q", got, msg)
	}
	if got, msg := normalizeForwardBind(""); got != "127.0.0.1" || msg != "" {
		t.Fatalf("空地址应规范成回环，得到 %q %q", got, msg)
	}
	if _, msg := normalizeForwardBind("example.com"); msg == "" {
		t.Fatal("域名不能当监听地址")
	}
	if got, msg := normalizeForwardBind("0.0.0.0"); got != "0.0.0.0" || msg != "" {
		t.Fatalf("明确写出的地址应保留，得到 %q %q", got, msg)
	}
}

func dialFails(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
	if err != nil {
		return true
	}
	conn.Close()
	return false
}

// 规则进保险库、随主机连接自动开启，可以手动停止和再启动，删除前可以取消。
func TestForwardRuleAddStartStopDelete(t *testing.T) {
	h := newHarness(t)
	echoHost, echoPort := localEcho(t)
	listenPort := freeLoopbackPort(t)

	h.click("端口转发")
	h.tt.Frame()
	h.click("添加转发")
	h.tt.Frame()
	h.click("动态转发")
	h.tt.Frame()
	if h.tt.HasText("目标地址") {
		t.Fatal("动态转发不应要求目标地址")
	}
	h.click("本地转发")
	h.tt.Frame()

	p := &h.a.forwardPanel
	p.name = "fwd-db"
	p.bindHost = "127.0.0.1"
	p.bindPort = strconv.Itoa(listenPort)
	p.targetHost = echoHost
	p.targetPort = strconv.Itoa(echoPort)
	p.hostID = h.devHost().ID
	p.kind = 0
	h.tt.Frame()
	h.click("保存转发")
	h.tt.Frame()
	if h.a.forwardPanel.editing || h.a.forwardPanel.err != "" || len(h.a.forwards) != 1 {
		t.Fatalf("应保存一条规则: editing=%v err=%q forwards=%+v", h.a.forwardPanel.editing, h.a.forwardPanel.err, h.a.forwards)
	}
	if !h.tt.HasText("连接 dev 后自动开启") {
		t.Fatalf("未连接时应说明会自动开启: %q", h.tt.Texts())
	}
	if h.vaultFileContains("fwd-db") {
		t.Fatal("转发规则不应以明文出现在保险库文件里")
	}
	h.click("完成")
	h.tt.Frame()

	h.connectDev()
	var addr string
	h.pump("自动开启", func() bool {
		for _, live := range h.a.tunnels {
			if live != nil && live.tunnel != nil && strings.HasPrefix(live.tunnel.Addr(), "127.0.0.1:") {
				addr = live.tunnel.Addr()
				return true
			}
		}
		return false
	})
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if string(buf) != "ping" {
		t.Fatalf("经转发得到 %q", buf)
	}

	h.click("端口转发")
	h.tt.Frame()
	if !h.tt.HasText("监听 " + addr) {
		t.Fatalf("列表应显示正在监听: %q", h.tt.Texts())
	}
	h.click("停止 fwd-db")
	h.pump("停止转发", func() bool { return len(h.a.tunnels) == 0 && dialFails(addr) })

	h.click("启动 fwd-db")
	h.pump("再次启动", func() bool { return len(h.a.tunnels) == 1 })

	// 保存正在监听的规则要等旧端口放开再听，否则会报端口占用。
	h.click("fwd-db")
	h.tt.Frame()
	if !h.a.forwardPanel.editing {
		t.Fatal("点名称应进入编辑")
	}
	h.click("保存转发")
	var again string
	h.pump("保存后重新监听", func() bool {
		if h.a.forwardPanel.err != "" || len(h.a.tunnels) != 1 {
			return false
		}
		for _, live := range h.a.tunnels {
			if live != nil && live.tunnel != nil && strings.HasPrefix(live.tunnel.Addr(), "127.0.0.1:") {
				again = live.tunnel.Addr()
				return true
			}
		}
		return false
	})
	conn, err = net.DialTimeout("tcp", again, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err = conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf = make([]byte, 4)
	if _, err = io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if string(buf) != "ping" {
		t.Fatalf("重新监听后得到 %q", buf)
	}

	h.click("删除 fwd-db")
	h.tt.Frame()
	h.click("取消")
	h.tt.Frame()
	if len(h.a.forwards) != 1 {
		t.Fatal("取消删除后规则应还在")
	}
	h.click("删除 fwd-db")
	h.tt.Frame()
	h.click("删除")
	h.tt.Frame()
	if len(h.a.forwards) != 0 || len(h.a.tunnels) != 0 {
		t.Fatalf("删除后应没有规则和监听: forwards=%d tunnels=%d err=%q", len(h.a.forwards), len(h.a.tunnels), h.a.forwardPanel.err)
	}
}
