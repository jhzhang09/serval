// Package devproxy 提供只用于测试的 SOCKS5 和 HTTP CONNECT 代理，只监听、只转发到回环地址。
package devproxy

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
)

// Server 是运行中的测试代理。
type Server struct {
	listener net.Listener
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	// Tunnels 统计成功建立的隧道数，测试用它确认流量确实经过了代理。
	Tunnels atomic.Int32
}

// Addr 返回监听地址。
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Close 停止代理，并断开所有正在转发的连接，不必等客户端先断开。
func (s *Server) Close() error {
	err := s.listener.Close()
	s.mu.Lock()
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return err
}

func (s *Server) track(c net.Conn) {
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func start(handle func(*Server, net.Conn)) (*Server, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{listener: l, conns: map[net.Conn]struct{}{}}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			s.track(conn)
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer s.untrack(conn)
				handle(s, conn)
			}()
		}
	}()
	return s, nil
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
	a.Close()
	b.Close()
	<-done
}

// StartSOCKS5 启动 SOCKS5 代理。user 非空时要求用户名密码认证（RFC 1929）。
func StartSOCKS5(user, password string) (*Server, error) {
	return start(func(s *Server, conn net.Conn) {
		defer conn.Close()
		target, err := socksHandshake(conn, user, password)
		if err != nil {
			return
		}
		upstream, err := net.Dial("tcp", target)
		if err == nil {
			s.track(upstream)
			defer s.untrack(upstream)
		}
		reply := []byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}
		if err != nil {
			reply[1] = 5 // 连接被拒绝
			conn.Write(reply)
			return
		}
		conn.Write(reply)
		s.Tunnels.Add(1)
		pipe(conn, upstream)
	})
}

func socksHandshake(conn net.Conn, user, password string) (string, error) {
	r := bufio.NewReader(conn)
	head := make([]byte, 2)
	if _, err := io.ReadFull(r, head); err != nil || head[0] != 5 {
		return "", errors.New("不是 SOCKS5")
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(r, methods); err != nil {
		return "", err
	}
	want := byte(0)
	if user != "" {
		want = 2
	}
	offered := false
	for _, m := range methods {
		offered = offered || m == want
	}
	if !offered {
		conn.Write([]byte{5, 0xff})
		return "", errors.New("客户端没有提供需要的认证方式")
	}
	conn.Write([]byte{5, want})
	if want == 2 {
		ver, _ := r.ReadByte()
		ulen, _ := r.ReadByte()
		u := make([]byte, ulen)
		io.ReadFull(r, u)
		plen, _ := r.ReadByte()
		p := make([]byte, plen)
		io.ReadFull(r, p)
		if ver != 1 || string(u) != user || string(p) != password {
			conn.Write([]byte{1, 1})
			return "", errors.New("认证失败")
		}
		conn.Write([]byte{1, 0})
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(r, req); err != nil || req[1] != 1 {
		return "", errors.New("只支持 CONNECT")
	}
	var host string
	switch req[3] {
	case 1:
		ip := make([]byte, 4)
		io.ReadFull(r, ip)
		host = net.IP(ip).String()
	case 3:
		n, _ := r.ReadByte()
		name := make([]byte, n)
		io.ReadFull(r, name)
		host = string(name)
	case 4:
		ip := make([]byte, 16)
		io.ReadFull(r, ip)
		host = net.IP(ip).String()
	default:
		return "", errors.New("未知地址类型")
	}
	portBytes := make([]byte, 2)
	io.ReadFull(r, portBytes)
	if !loopback(host) {
		conn.Write([]byte{5, 2, 0, 1, 0, 0, 0, 0, 0, 0})
		return "", errors.New("只允许转发到回环地址")
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes)))), nil
}

// StartHTTP 启动 HTTP CONNECT 代理。user 非空时要求 Basic 认证。
func StartHTTP(user, password string) (*Server, error) {
	return start(func(s *Server, conn net.Conn) {
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err != nil || req.Method != http.MethodConnect {
			fmt.Fprint(conn, "HTTP/1.1 405 Method Not Allowed\r\n\r\n")
			return
		}
		if user != "" {
			want := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
			if req.Header.Get("Proxy-Authorization") != want {
				fmt.Fprint(conn, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")
				return
			}
		}
		host, _, err := net.SplitHostPort(req.Host)
		if err != nil || !loopback(host) {
			fmt.Fprint(conn, "HTTP/1.1 403 Forbidden\r\n\r\n")
			return
		}
		upstream, err := net.Dial("tcp", req.Host)
		if err == nil {
			s.track(upstream)
			defer s.untrack(upstream)
		}
		if err != nil {
			fmt.Fprint(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
			return
		}
		fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		s.Tunnels.Add(1)
		pipe(conn, upstream)
	})
}
