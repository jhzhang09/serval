package sshconn

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// Proxy 是建立第一条连接时使用的代理。
type Proxy struct {
	// Kind 是 "socks5" 或 "http"（HTTP CONNECT 隧道）。
	Kind     string
	Addr     string
	User     string
	Password string
}

// ParseProxy 解析 socks5://[用户:密码@]主机:端口 或 http://[用户:密码@]主机:端口。
// 空字符串表示不用代理，返回 nil。
func ParseProxy(s string) (*Proxy, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("代理地址格式不对: %w", err)
	}
	p := &Proxy{Addr: u.Host}
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h":
		p.Kind = "socks5"
	case "http":
		p.Kind = "http"
	default:
		return nil, fmt.Errorf("不支持的代理类型 %q，可以用 socks5:// 或 http://", u.Scheme)
	}
	if u.Port() == "" || u.Hostname() == "" {
		return nil, errors.New("代理地址需要写明主机和端口，例如 socks5://127.0.0.1:1080")
	}
	if u.Path != "" && u.Path != "/" {
		return nil, errors.New("代理地址不能带路径")
	}
	if u.User != nil {
		p.User = u.User.Username()
		p.Password, _ = u.User.Password()
	}
	return p, nil
}

// String 返回不含密码的地址，用于界面显示和日志，避免把代理密码露出来。
func (p *Proxy) String() string {
	if p == nil {
		return ""
	}
	if p.User != "" {
		return fmt.Sprintf("%s://%s@%s", p.Kind, p.User, p.Addr)
	}
	return fmt.Sprintf("%s://%s", p.Kind, p.Addr)
}

// dialFirst 建立链路上的第一条 TCP 连接，按需经过代理。
func dialFirst(ctx context.Context, addr string, p *Proxy, timeout time.Duration) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	direct := &net.Dialer{}
	if p == nil {
		conn, err := direct.DialContext(dialCtx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("连接 %s 失败: %w", addr, err)
		}
		return conn, nil
	}

	var conn net.Conn
	var err error
	switch p.Kind {
	case "socks5":
		var auth *proxy.Auth
		if p.User != "" {
			auth = &proxy.Auth{User: p.User, Password: p.Password}
		}
		var d proxy.Dialer
		d, err = proxy.SOCKS5("tcp", p.Addr, auth, direct)
		if err == nil {
			conn, err = d.(proxy.ContextDialer).DialContext(dialCtx, "tcp", addr)
		}
	case "http":
		conn, err = dialHTTPConnect(dialCtx, direct, p, addr)
	default:
		err = fmt.Errorf("不支持的代理类型 %q", p.Kind)
	}
	if err != nil {
		return nil, fmt.Errorf("经由代理 %s 连接 %s 失败: %w", p, addr, err)
	}
	return conn, nil
}

// dialHTTPConnect 通过 HTTP CONNECT 建立隧道。
func dialHTTPConnect(ctx context.Context, d *net.Dialer, p *Proxy, addr string) (net.Conn, error) {
	conn, err := d.DialContext(ctx, "tcp", p.Addr)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", addr, addr)
	if p.User != "" {
		token := base64.StdEncoding.EncodeToString([]byte(p.User + ":" + p.Password))
		req += "Proxy-Authorization: Basic " + token + "\r\n"
	}
	req += "\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		if resp.StatusCode == http.StatusProxyAuthRequired {
			return nil, errors.New("代理要求认证，或者用户名密码不对")
		}
		return nil, fmt.Errorf("代理拒绝了连接：%s", resp.Status)
	}
	// SSH 服务器一连上就会发送版本标识，它可能已经和响应头一起被读进了缓冲区，
	// 不能丢掉，所以后续读取先从缓冲区取。
	return &bufferedConn{Conn: conn, r: br}, nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
