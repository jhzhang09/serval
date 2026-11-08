package sshconn

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	// ErrForwardDenied 表示服务器拒绝了 tcpip-forward。
	ErrForwardDenied = errors.New("sshconn: 服务器拒绝了端口转发")
	// ErrForwardInUse 表示本机端口已经被占用。
	ErrForwardInUse = errors.New("sshconn: 端口已被占用")
)

// Tunnel 是一条正在监听的端口转发。关闭后本机端口立即释放；远程转发还会通知服务器取消监听。
// Close 可能要等网络往返，不要在界面主线程上直接调用。
type Tunnel struct {
	addr string

	ln     net.Listener
	mu     sync.Mutex
	closed bool
	once   sync.Once

	in, out atomic.Int64
	errText atomic.Pointer[string]
}

// Addr 返回正在监听的 host:port。端口填 0 时，这里是系统分配后的地址。
func (t *Tunnel) Addr() string {
	if t == nil {
		return ""
	}
	return t.addr
}

// Bytes 返回从目标收到的字节数和发往目标的字节数。
func (t *Tunnel) Bytes() (in, out int64) {
	if t == nil {
		return 0, 0
	}
	return t.in.Load(), t.out.Load()
}

// Err 返回最近一次单条连接失败的说明。监听本身仍在。
func (t *Tunnel) Err() string {
	if t == nil {
		return ""
	}
	p := t.errText.Load()
	if p == nil {
		return ""
	}
	return *p
}

// Close 停止监听。可以重复调用。
func (t *Tunnel) Close() error {
	if t == nil {
		return nil
	}
	var err error
	t.once.Do(func() {
		t.mu.Lock()
		t.closed = true
		ln := t.ln
		t.mu.Unlock()
		if ln != nil {
			err = ln.Close()
		}
	})
	return err
}

func (t *Tunnel) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

func (t *Tunnel) note(msg string) { t.errText.Store(&msg) }

// ListenLocal 在本机监听，把连进来的连接经 SSH 接到目标（OpenSSH -L）。
// bind 为空时监听 127.0.0.1。port 为 0 时由系统分配端口。
func (c *Client) ListenLocal(bind string, port int, targetHost string, targetPort int) (*Tunnel, error) {
	if err := c.usable(); err != nil {
		return nil, err
	}
	addr, err := listenAddr(bind, port)
	if err != nil {
		return nil, err
	}
	target, err := targetAddr(targetHost, targetPort)
	if err != nil {
		return nil, err
	}
	ln, err := listenTCP(addr)
	if err != nil {
		return nil, err
	}
	t := &Tunnel{addr: ln.Addr().String(), ln: ln}
	go t.accept(ln, func(conn net.Conn) {
		remote, err := c.conn.Dial("tcp", target)
		if err != nil {
			conn.Close()
			t.note("有一次转发没能连上目标")
			return
		}
		relay(t, conn, remote)
	})
	return t, nil
}

// ListenRemote 请服务器在 bind:port 监听，把连进来的连接接到本机目标（OpenSSH -R）。
// bind 为空时请服务器只监听它自己的 127.0.0.1。
func (c *Client) ListenRemote(bind string, port int, targetHost string, targetPort int) (*Tunnel, error) {
	if err := c.usable(); err != nil {
		return nil, err
	}
	addr, err := listenAddr(bind, port)
	if err != nil {
		return nil, err
	}
	target, err := targetAddr(targetHost, targetPort)
	if err != nil {
		return nil, err
	}
	ln, err := c.conn.Listen("tcp", addr)
	if err != nil {
		if strings.Contains(err.Error(), "denied") {
			return nil, ErrForwardDenied
		}
		return nil, fmt.Errorf("sshconn: 远程监听失败: %w", err)
	}
	t := &Tunnel{addr: ln.Addr().String(), ln: ln}
	go t.accept(ln, func(conn net.Conn) {
		remote, err := net.Dial("tcp", target)
		if err != nil {
			conn.Close()
			t.note("有一次转发没能连上本机目标")
			return
		}
		relay(t, conn, remote)
	})
	return t, nil
}

// ListenDynamic 在本机监听一个只支持 CONNECT 的 SOCKS5 代理，目标地址由客户端给出（OpenSSH -D）。
func (c *Client) ListenDynamic(bind string, port int) (*Tunnel, error) {
	if err := c.usable(); err != nil {
		return nil, err
	}
	addr, err := listenAddr(bind, port)
	if err != nil {
		return nil, err
	}
	ln, err := listenTCP(addr)
	if err != nil {
		return nil, err
	}
	t := &Tunnel{addr: ln.Addr().String(), ln: ln}
	go t.accept(ln, func(conn net.Conn) { t.serveSOCKS(conn, c.conn) })
	return t, nil
}

func (c *Client) usable() error {
	if c == nil || c.conn == nil {
		return errors.New("sshconn: 连接已关闭")
	}
	return nil
}

func listenAddr(bind string, port int) (string, error) {
	bind = strings.TrimSpace(bind)
	if bind == "" {
		bind = "127.0.0.1"
	}
	if port < 0 || port > 65535 {
		return "", errors.New("sshconn: 端口无效")
	}
	return net.JoinHostPort(bind, strconv.Itoa(port)), nil
}

func targetAddr(host string, port int) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" || port < 1 || port > 65535 {
		return "", errors.New("sshconn: 目标地址无效")
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func listenTCP(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return nil, ErrForwardInUse
		}
		return nil, fmt.Errorf("sshconn: 监听失败: %w", err)
	}
	return ln, nil
}

func (t *Tunnel) accept(ln net.Listener, handle func(net.Conn)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if !t.isClosed() {
				t.note("监听已停止")
			}
			return
		}
		go handle(conn)
	}
}

// relay 把两端拼起来。in 是从目标读到的字节，out 是写到目标的字节。
// 计数发生在每次 Write 返回时，连接还没结束也能看到流量。
func relay(t *Tunnel, user, far net.Conn) {
	defer user.Close()
	defer far.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(countWriter{far, &t.out}, user)
		closeWrite(far)
	}()
	go func() {
		defer wg.Done()
		io.Copy(countWriter{user, &t.in}, far)
		closeWrite(user)
	}()
	wg.Wait()
}

// countWriter 把成功写出的字节累加到 n。
type countWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.n.Add(int64(n))
	}
	return n, err
}

func closeWrite(c net.Conn) {
	type halfCloser interface{ CloseWrite() error }
	if h, ok := c.(halfCloser); ok {
		h.CloseWrite()
	}
}

// serveSOCKS 只实现 SOCKS5 的无认证 CONNECT（RFC 1928）。BIND 和 UDP 直接拒绝。
func (t *Tunnel) serveSOCKS(conn net.Conn, client *ssh.Client) {
	defer conn.Close()
	host, port, err := socks5Handshake(conn)
	if err != nil {
		return
	}
	remote, err := client.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		socks5Reply(conn, 5)
		t.note("有一次转发没能连上目标")
		return
	}
	if err := socks5Reply(conn, 0); err != nil {
		remote.Close()
		return
	}
	relay(t, conn, remote)
}

func socks5Handshake(conn net.Conn) (host string, port int, err error) {
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetDeadline(time.Time{})
	head := make([]byte, 2)
	if _, err = io.ReadFull(conn, head); err != nil {
		return "", 0, err
	}
	if head[0] != 5 || head[1] == 0 {
		return "", 0, errors.New("sshconn: 不是 SOCKS5")
	}
	methods := make([]byte, int(head[1]))
	if _, err = io.ReadFull(conn, methods); err != nil {
		return "", 0, err
	}
	noAuth := false
	for _, m := range methods {
		if m == 0 {
			noAuth = true
			break
		}
	}
	if !noAuth {
		conn.Write([]byte{5, 0xff})
		return "", 0, errors.New("sshconn: SOCKS5 只支持无认证")
	}
	if _, err = conn.Write([]byte{5, 0}); err != nil {
		return "", 0, err
	}
	req := make([]byte, 4)
	if _, err = io.ReadFull(conn, req); err != nil {
		return "", 0, err
	}
	if req[0] != 5 || req[1] != 1 {
		socks5Reply(conn, 7)
		return "", 0, errors.New("sshconn: 只支持 SOCKS5 CONNECT")
	}
	switch req[3] {
	case 1:
		buf := make([]byte, 4)
		if _, err = io.ReadFull(conn, buf); err != nil {
			return "", 0, err
		}
		host = net.IP(buf).String()
	case 4:
		buf := make([]byte, 16)
		if _, err = io.ReadFull(conn, buf); err != nil {
			return "", 0, err
		}
		host = net.IP(buf).String()
	case 3:
		nbuf := make([]byte, 1)
		if _, err = io.ReadFull(conn, nbuf); err != nil {
			return "", 0, err
		}
		if nbuf[0] == 0 {
			return "", 0, errors.New("sshconn: SOCKS5 域名为空")
		}
		buf := make([]byte, int(nbuf[0]))
		if _, err = io.ReadFull(conn, buf); err != nil {
			return "", 0, err
		}
		host = string(buf)
	default:
		socks5Reply(conn, 8)
		return "", 0, errors.New("sshconn: 不认识的 SOCKS5 地址类型")
	}
	pbuf := make([]byte, 2)
	if _, err = io.ReadFull(conn, pbuf); err != nil {
		return "", 0, err
	}
	port = int(pbuf[0])<<8 | int(pbuf[1])
	if port == 0 {
		return "", 0, errors.New("sshconn: SOCKS5 端口无效")
	}
	return host, port, nil
}

func socks5Reply(conn net.Conn, rep byte) error {
	_, err := conn.Write([]byte{5, rep, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}
