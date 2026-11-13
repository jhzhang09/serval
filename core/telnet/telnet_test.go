package telnet

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func setupTestServer(t *testing.T) (net.Listener, string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen error: %v", err)
	}
	return ln, ln.Addr().String()
}

func TestTelnetNegotiationAndData(t *testing.T) {
	ln, addr := setupTestServer(t)
	defer ln.Close()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		server, err := ln.Accept()
		if err != nil {
			return
		}
		defer server.Close()

		// 服务器发起 DO TTYPE 协商 (IAC DO 24)
		_, _ = server.Write([]byte{cmdIAC, cmdDO, optTTYPE})

		// 期待客户端回复 IAC WILL TTYPE (255 251 24)
		resp := make([]byte, 3)
		if _, err := io.ReadFull(server, resp); err != nil {
			t.Errorf("server read err: %v", err)
			return
		}
		if !bytes.Equal(resp, []byte{cmdIAC, cmdWILL, optTTYPE}) {
			t.Errorf("expected IAC WILL TTYPE, got %v", resp)
			return
		}

		// 服务器请求终端类型 (IAC SB TTYPE 1 IAC SE)
		_, _ = server.Write([]byte{cmdIAC, cmdSB, optTTYPE, 1, cmdIAC, cmdSE})

		// 期待客户端回复终端类型包含 "xterm-256color"
		ttypeResp := make([]byte, 20)
		if _, err := io.ReadFull(server, ttypeResp); err != nil {
			t.Errorf("server read ttype err: %v", err)
			return
		}
		if !bytes.Contains(ttypeResp, []byte("xterm-256color")) {
			t.Errorf("expected xterm-256color, got %v", ttypeResp)
			return
		}

		// 服务器发送普通文字与 IAC 转义
		_, _ = server.Write([]byte("Hello, Telnet!\n"))

		// 读客户端写入
		buf := make([]byte, 32)
		n, _ := server.Read(buf)
		if !bytes.Equal(buf[:n], []byte("cmd\xff\xfftest")) {
			t.Errorf("server received unexpected escaped data: %v", buf[:n])
		}
	}()

	telnetConn, err := Dial(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer telnetConn.Close()

	buf := make([]byte, 64)
	n, err := telnetConn.Read(buf)
	if err != nil {
		t.Fatalf("client read err: %v", err)
	}
	if got := string(buf[:n]); got != "Hello, Telnet!\n" {
		t.Fatalf("expected Hello, Telnet!\\n, got %q", got)
	}

	// 客户端写入普通数据与带 255 的数据
	nw, err := telnetConn.Write([]byte("cmd\xfftest"))
	if err != nil || nw != 8 {
		t.Fatalf("client write err: %v, nw=%d", err, nw)
	}

	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for server")
	}
}

func TestTelnetNAWS(t *testing.T) {
	ln, addr := setupTestServer(t)
	defer ln.Close()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		server, err := ln.Accept()
		if err != nil {
			return
		}
		defer server.Close()

		// 服务器支持 NAWS (IAC DO 31)
		_, _ = server.Write([]byte{cmdIAC, cmdDO, optNAWS})

		// 客户端收到后会回复 WILL NAWS 并上报窗口
		buf := make([]byte, 64)
		n, err := server.Read(buf)
		if err != nil {
			t.Errorf("server read NAWS err: %v", err)
			return
		}
		if !bytes.Contains(buf[:n], []byte{cmdIAC, cmdWILL, optNAWS}) {
			t.Errorf("expected WILL NAWS, got %v", buf[:n])
		}

		// 等待客户端调用 SetSize(120, 40) 上报新的窗口尺寸
		n2, err := server.Read(buf)
		if err != nil {
			t.Errorf("server read SetSize NAWS err: %v", err)
			return
		}
		if !bytes.Contains(buf[:n2], []byte{cmdIAC, cmdSB, optNAWS}) {
			t.Errorf("expected SB NAWS, got %v", buf[:n2])
		}
	}()

	telnetConn, err := Dial(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer telnetConn.Close()

	time.Sleep(50 * time.Millisecond)
	if err := telnetConn.SetSize(120, 40); err != nil {
		t.Fatalf("SetSize failed: %v", err)
	}

	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for NAWS server")
	}
}
