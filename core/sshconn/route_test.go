package sshconn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"serval/internal/devproxy"
	"serval/internal/devsshd"

	"golang.org/x/crypto/ssh"
)

// hopConfig 返回连接某台开发服务器的配置，每台服务器用自己的密码，便于验证按跳认证。
func hopConfig(server *devsshd.Server, password string, policy *HostKeyPolicy) Config {
	return Config{
		Host:     "127.0.0.1",
		Port:     server.Addr().Port,
		User:     "tester",
		Password: password,
		HostKeys: policy,
		Timeout:  5 * time.Second,
	}
}

func startWithPassword(t *testing.T, password string) *devsshd.Server {
	t.Helper()
	return startServer(t, devsshd.Options{Password: password})
}

func TestTwoHopJump(t *testing.T) {
	j1 := startWithPassword(t, "pass-j1")
	j2 := startWithPassword(t, "pass-j2")
	target := startWithPassword(t, "pass-target")
	policy := acceptAll(t)

	cfg := hopConfig(target, "pass-target", policy)
	cfg.Jumps = []Config{hopConfig(j1, "pass-j1", policy), hopConfig(j2, "pass-j2", policy)}
	client := mustDial(t, cfg)
	if len(client.chain) != 2 {
		t.Fatalf("应经过 2 台跳板机，实际 %d", len(client.chain))
	}

	if runtime.GOOS != "windows" {
		shell, err := client.OpenShell(80, 24)
		if err != nil {
			t.Fatal(err)
		}
		defer shell.Close()
		out := collect(shell)
		fmt.Fprintf(shell, "echo hop-$((1+1))\n")
		out.waitFor(t, "hop-2")
	}
	if _, err := client.Ping(context.Background()); err != nil {
		t.Fatalf("经过跳板机的连接应能保活: %v", err)
	}
}

// 每一跳都是独立的服务器，主机密钥要分别确认，顺序和连接顺序一致。
func TestJumpHostKeyConfirmedPerHop(t *testing.T) {
	j1 := startWithPassword(t, "a")
	target := startWithPassword(t, "b")
	var mu sync.Mutex
	var asked []string
	policy := &HostKeyPolicy{
		TrustStore: filepath.Join(t.TempDir(), "known_hosts"),
		Confirm: func(info HostKeyInfo) bool {
			mu.Lock()
			asked = append(asked, info.Host)
			mu.Unlock()
			return true
		},
	}
	cfg := hopConfig(target, "b", policy)
	cfg.Jumps = []Config{hopConfig(j1, "a", policy)}
	mustDial(t, cfg)

	want := []string{
		net.JoinHostPort("127.0.0.1", fmt.Sprint(j1.Addr().Port)),
		net.JoinHostPort("127.0.0.1", fmt.Sprint(target.Addr().Port)),
	}
	if strings.Join(asked, ",") != strings.Join(want, ",") {
		t.Fatalf("确认顺序 %v，期望 %v", asked, want)
	}
}

// 认证失败要能定位到具体哪一跳，界面才知道该向谁要密码。
func TestJumpAuthFailureIdentifiesHop(t *testing.T) {
	j1 := startWithPassword(t, "pass-j1")
	target := startWithPassword(t, "pass-target")
	policy := acceptAll(t)

	cfg := hopConfig(target, "pass-target", policy)
	cfg.Jumps = []Config{hopConfig(j1, "wrong", policy)}
	_, err := Dial(context.Background(), cfg)
	var hop *HopError
	if !errors.As(err, &hop) || hop.Hop != 0 || !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("跳板机认证失败应返回 HopError{Hop:0} 且是 ErrAuthFailed，得到 %v", err)
	}

	cfg = hopConfig(target, "wrong", policy)
	cfg.Jumps = []Config{hopConfig(j1, "pass-j1", policy)}
	_, err = Dial(context.Background(), cfg)
	if errors.As(err, &hop) || !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("目标主机认证失败不应标成跳板机错误，得到 %v", err)
	}
}

func TestTooManyJumps(t *testing.T) {
	cfg := Config{Host: "x", User: "u", HostKeys: acceptAll(t)}
	for range maxJumps + 1 {
		cfg.Jumps = append(cfg.Jumps, cfg)
	}
	if _, err := Dial(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "最多") {
		t.Fatalf("err = %v", err)
	}
}

func TestSOCKS5Proxy(t *testing.T) {
	target := startWithPassword(t, testPassword)
	for _, auth := range []bool{false, true} {
		user, pass := "", ""
		if auth {
			user, pass = "proxy-user", "proxy-pass"
		}
		proxy, err := devproxy.StartSOCKS5(user, pass)
		if err != nil {
			t.Fatal(err)
		}
		cfg := dialConfig(target, acceptAll(t))
		cfg.Proxy = &Proxy{Kind: "socks5", Addr: proxy.Addr(), User: user, Password: pass}
		client := mustDial(t, cfg)
		client.Close()
		if proxy.Tunnels.Load() != 1 {
			t.Errorf("auth=%v 连接应经过代理，隧道数 %d", auth, proxy.Tunnels.Load())
		}
		if auth {
			cfg.Proxy.Password = "wrong"
			if _, err := Dial(context.Background(), cfg); err == nil {
				t.Error("代理密码错误时不应连接成功")
			}
		}
		proxy.Close()
	}
}

func TestHTTPProxy(t *testing.T) {
	target := startWithPassword(t, testPassword)
	proxy, err := devproxy.StartHTTP("u", "p")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	cfg := dialConfig(target, acceptAll(t))
	cfg.Proxy = &Proxy{Kind: "http", Addr: proxy.Addr(), User: "u", Password: "p"}
	client := mustDial(t, cfg)
	// 能开 shell 或 Ping 说明 CONNECT 响应之后的数据（SSH 版本标识）没有被缓冲区吞掉。
	if runtime.GOOS != "windows" {
		shell, err := client.OpenShell(80, 24)
		if err != nil {
			t.Fatal(err)
		}
		shell.Close()
	} else if _, err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}

	cfg.Proxy.Password = "wrong"
	if _, err := Dial(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "认证") {
		t.Fatalf("代理认证失败应说明原因，得到 %v", err)
	}
}

// 代理只作用于第一跳，后面的跳板机经由 SSH 通道连接。
func TestProxyAppliesToFirstHopOnly(t *testing.T) {
	j1 := startWithPassword(t, "a")
	target := startWithPassword(t, "b")
	proxy, err := devproxy.StartSOCKS5("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	policy := acceptAll(t)
	cfg := hopConfig(target, "b", policy)
	cfg.Jumps = []Config{hopConfig(j1, "a", policy)}
	cfg.Proxy = &Proxy{Kind: "socks5", Addr: proxy.Addr()}
	mustDial(t, cfg)
	if n := proxy.Tunnels.Load(); n != 1 {
		t.Fatalf("代理只应承载第一跳，隧道数 %d", n)
	}
}

// silentListener 接受连接但从不说话，模拟卡住的服务器。
func silentListener(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { conn.Close() })
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func TestHandshakeTimeout(t *testing.T) {
	cfg := Config{Host: "127.0.0.1", Port: silentListener(t), User: "u", Password: "p",
		HostKeys: acceptAll(t), Timeout: 300 * time.Millisecond}
	start := time.Now()
	_, err := Dial(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("超时用了 %v", time.Since(start))
	}
}

// 回归：经由跳板机的连接是 SSH 通道，不支持 SetDeadline。超时必须照样生效。
func TestHandshakeTimeoutThroughJump(t *testing.T) {
	j1 := startWithPassword(t, "a")
	policy := acceptAll(t)
	cfg := Config{Host: "127.0.0.1", Port: silentListener(t), User: "u", Password: "p",
		HostKeys: policy, Timeout: 300 * time.Millisecond}
	cfg.Jumps = []Config{hopConfig(j1, "a", policy)}
	start := time.Now()
	_, err := Dial(context.Background(), cfg)
	var hop *HopError
	if err == nil || !strings.Contains(err.Error(), "超时") || errors.As(err, &hop) {
		t.Fatalf("目标主机超时应如实报告，且不标成跳板机错误，得到 %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("超时用了 %v", time.Since(start))
	}
}

func TestParseProxy(t *testing.T) {
	p, err := ParseProxy("socks5://alice:s3cret@127.0.0.1:1080")
	if err != nil || p.Kind != "socks5" || p.Addr != "127.0.0.1:1080" || p.User != "alice" || p.Password != "s3cret" {
		t.Fatalf("p=%+v err=%v", p, err)
	}
	if strings.Contains(p.String(), "s3cret") {
		t.Errorf("显示用的地址不应包含密码: %s", p)
	}
	if p, err := ParseProxy("http://proxy.local:3128"); err != nil || p.Kind != "http" {
		t.Errorf("p=%+v err=%v", p, err)
	}
	if p, err := ParseProxy("  "); err != nil || p != nil {
		t.Errorf("空字符串表示不用代理: p=%+v err=%v", p, err)
	}
	for _, bad := range []string{"ftp://x:1", "socks5://hostonly", "http://x:1/path", "::"} {
		if _, err := ParseProxy(bad); err == nil {
			t.Errorf("%q 应报错", bad)
		}
	}
}

// memTrust 是测试用的信任记录存储。
type memTrust struct {
	mu   sync.Mutex
	keys map[string][]ssh.PublicKey
}

func (m *memTrust) Lookup(host string) ([]ssh.PublicKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.keys[host], nil
}

func (m *memTrust) Trust(host string, key ssh.PublicKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[host] = append(m.keys[host], key)
	return nil
}

// 信任记录存在应用提供的存储里：第一次确认后保存，再连不问；服务器换了密钥则拒绝。
func TestTrustedKeysStore(t *testing.T) {
	server := startWithPassword(t, testPassword)
	trust := &memTrust{keys: map[string][]ssh.PublicKey{}}
	asked := 0
	policy := &HostKeyPolicy{Trusted: trust, Confirm: func(HostKeyInfo) bool { asked++; return true }}
	mustDial(t, dialConfig(server, policy)).Close()
	host := fmt.Sprintf("[127.0.0.1]:%d", server.Addr().Port)
	if asked != 1 || len(trust.keys[host]) != 1 {
		t.Fatalf("第一次连接应询问并保存到 %s: asked=%d keys=%v", host, asked, trust.keys)
	}
	mustDial(t, dialConfig(server, policy)).Close()
	if asked != 1 {
		t.Fatal("已信任的主机不应再询问")
	}

	// 换一台不同密钥、同一地址的服务器。
	port := server.Addr().Port
	server.Close()
	impostor, err := devsshd.Start(devsshd.Options{Addr: fmt.Sprintf("127.0.0.1:%d", port), Password: testPassword})
	if err != nil {
		t.Skipf("端口被占用: %v", err)
	}
	defer impostor.Close()
	var changed *HostKeyChangedError
	if _, err := Dial(context.Background(), dialConfig(impostor, policy)); !errors.As(err, &changed) || changed.Host != host {
		t.Fatalf("密钥变化应拒绝并指明主机，得到 %v", err)
	}
}
