package sshconn

import (
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"serval/internal/devsshd"

	"golang.org/x/net/proxy"
)

func echoServer(t *testing.T) (host string, port int) {
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

func roundTrip(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("往返得到 %q", buf)
	}
}

func waitUnreachable(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err != nil {
			return
		}
		conn.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("关闭后 %s 仍能连接", addr)
}

func TestLocalForwardRoundTrip(t *testing.T) {
	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))
	host, port := echoServer(t)
	tun, err := client.ListenLocal("127.0.0.1", 0, host, port)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, tun.Addr())
	in, out := tun.Bytes()
	if in < 4 || out < 4 {
		t.Fatalf("流量统计 in=%d out=%d", in, out)
	}
	if err := tun.Close(); err != nil {
		t.Fatal(err)
	}
	waitUnreachable(t, tun.Addr())
}

func TestRemoteForwardRoundTrip(t *testing.T) {
	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))
	host, port := echoServer(t)
	tun, err := client.ListenRemote("127.0.0.1", 0, host, port)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, tun.Addr())
	if err := tun.Close(); err != nil {
		t.Fatal(err)
	}
	waitUnreachable(t, tun.Addr())
}

func TestDynamicForwardRoundTrip(t *testing.T) {
	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))
	host, port := echoServer(t)
	tun, err := client.ListenDynamic("", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	dialer, err := proxy.SOCKS5("tcp", tun.Addr(), nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("SOCKS 往返得到 %q", buf)
	}
	in, out := tun.Bytes()
	if in < 4 || out < 4 {
		t.Fatalf("流量统计 in=%d out=%d", in, out)
	}
}

func TestForwardRejectsBadInput(t *testing.T) {
	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, err := client.ListenLocal("127.0.0.1", ln.Addr().(*net.TCPAddr).Port, "127.0.0.1", 1); !errors.Is(err, ErrForwardInUse) {
		t.Fatalf("占用的端口应返回 ErrForwardInUse，得到 %v", err)
	}
	if _, err := client.ListenRemote("0.0.0.0", 9, "127.0.0.1", 1); !errors.Is(err, ErrForwardDenied) {
		t.Fatalf("非回环的远程监听应被拒绝，得到 %v", err)
	}
	if _, err := client.ListenLocal("127.0.0.1", 1, "", 1); err == nil {
		t.Fatal("空的目标地址应失败")
	}
}
