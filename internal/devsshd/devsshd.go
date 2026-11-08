// Package devsshd 是仅用于测试和本机性能测量的 SSH 服务器，不会被打包进应用。
//
// 它能启动真实的 shell，因此 Start 拒绝监听回环地址以外的地址。
package devsshd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Options 配置开发服务器。
type Options struct {
	// Addr 必须是回环地址，默认 127.0.0.1:0。
	Addr string
	User string
	// Password 和 AuthorizedKey 至少提供一个。
	Password      string
	AuthorizedKey ssh.PublicKey
	// KeyboardInteractive 启用 keyboard-interactive 认证。
	KeyboardInteractive bool
	KeyboardQuestions   []string
	KeyboardAnswers     []string
	// HostKeys 为空时生成一把临时 ed25519 密钥。
	HostKeys []ssh.Signer
	// Shell 是启动的 shell 命令及参数，默认 /bin/sh。
	Shell []string
	Dir   string
}

// Server 是运行中的开发服务器。
type Server struct {
	listener net.Listener
	config   *ssh.ServerConfig
	opts     Options
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	wg       sync.WaitGroup
}

// Start 启动服务器并开始接受连接。
func Start(opts Options) (*Server, error) {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(opts.Addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("devsshd 只允许监听回环地址，收到 %q", opts.Addr)
	}
	if opts.Password == "" && opts.AuthorizedKey == nil && !opts.KeyboardInteractive {
		return nil, errors.New("devsshd 必须配置密码、公钥或多步验证")
	}
	if len(opts.Shell) == 0 {
		opts.Shell = []string{"/bin/sh"}
	}

	config := &ssh.ServerConfig{}
	if opts.Password != "" {
		config.PasswordCallback = func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) == opts.Password {
				return nil, nil
			}
			return nil, errors.New("密码错误")
		}
	}
	if opts.AuthorizedKey != nil {
		want := opts.AuthorizedKey.Marshal()
		config.PublicKeyCallback = func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(want) {
				return nil, nil
			}
			return nil, errors.New("公钥未授权")
		}
	}
	if opts.KeyboardInteractive {
		config.KeyboardInteractiveCallback = func(conn ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			questions := opts.KeyboardQuestions
			if len(questions) == 0 {
				questions = []string{"Verification code: "}
			}
			echos := make([]bool, len(questions))
			answers, err := client(conn.User(), "Two-factor authentication", questions, echos)
			if err != nil {
				return nil, err
			}
			expected := opts.KeyboardAnswers
			if len(expected) == 0 && opts.Password != "" {
				expected = []string{opts.Password}
			}
			if len(answers) != len(expected) {
				return nil, errors.New("答案数量不匹配")
			}
			for i := range answers {
				if answers[i] != expected[i] {
					return nil, errors.New("答案错误")
				}
			}
			return nil, nil
		}
	}
	if len(opts.HostKeys) == 0 {
		signer, err := NewSigner()
		if err != nil {
			return nil, err
		}
		opts.HostKeys = []ssh.Signer{signer}
	}
	for _, key := range opts.HostKeys {
		config.AddHostKey(key)
	}

	listener, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return nil, err
	}
	s := &Server{listener: listener, config: config, opts: opts, conns: map[net.Conn]struct{}{}}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

// NewSigner 生成一把临时 ed25519 密钥。
func NewSigner() (ssh.Signer, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(private)
}

// Addr 返回实际监听地址。
func (s *Server) Addr() *net.TCPAddr { return s.listener.Addr().(*net.TCPAddr) }

// HostKeys 返回服务器当前使用的主机密钥列表。
func (s *Server) HostKeys() []ssh.Signer {
	return slices.Clone(s.opts.HostKeys)
}

// Close 停止监听并断开所有连接。
func (s *Server) Close() error {
	err := s.listener.Close()
	s.mu.Lock()
	for conn := range s.conns {
		conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return err
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				conn.Close()
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
			}()
			s.serve(conn)
		}()
	}
}

func (s *Server) serve(raw net.Conn) {
	conn, chans, reqs, err := ssh.NewServerConn(raw, s.config)
	if err != nil {
		return
	}
	defer conn.Close()
	go s.serveGlobal(conn, reqs)
	for newChannel := range chans {
		if newChannel.ChannelType() == "direct-tcpip" {
			go s.forward(newChannel)
			continue
		}
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "只支持 session 和 direct-tcpip")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go s.session(channel, requests)
	}
}

// forward 处理 direct-tcpip 通道，让这台服务器能充当跳板机。
// 只允许转发到回环地址：它是测试工具，不能被当成通往其他网络的跳板。
func (s *Server) forward(newChannel ssh.NewChannel) {
	var target struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}
	if err := ssh.Unmarshal(newChannel.ExtraData(), &target); err != nil {
		newChannel.Reject(ssh.ConnectionFailed, "无法解析转发目标")
		return
	}
	if ip := net.ParseIP(target.Host); target.Host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		newChannel.Reject(ssh.Prohibited, "devsshd 只允许转发到回环地址")
		return
	}
	upstream, err := net.Dial("tcp", net.JoinHostPort(target.Host, fmt.Sprint(target.Port)))
	if err != nil {
		newChannel.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	channel, requests, err := newChannel.Accept()
	if err != nil {
		upstream.Close()
		return
	}
	go ssh.DiscardRequests(requests)
	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, channel); upstream.(*net.TCPConn).CloseWrite(); done <- struct{}{} }()
	go func() { io.Copy(channel, upstream); channel.CloseWrite(); done <- struct{}{} }()
	<-done
	<-done
	channel.Close()
	upstream.Close()
}

// serveGlobal 处理 tcpip-forward，让测试能覆盖远程端口转发。
// 只允许绑回环地址：这是测试工具，不能在机器的其他网卡上开端口。
func (s *Server) serveGlobal(conn *ssh.ServerConn, reqs <-chan *ssh.Request) {
	var mu sync.Mutex
	listeners := map[string]net.Listener{}
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, ln := range listeners {
			ln.Close()
		}
	}()
	for req := range reqs {
		switch req.Type {
		case "tcpip-forward":
			s.acceptForward(conn, req, &mu, listeners)
		case "cancel-tcpip-forward":
			s.cancelForward(req, &mu, listeners)
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func (s *Server) acceptForward(conn *ssh.ServerConn, req *ssh.Request, mu *sync.Mutex, listeners map[string]net.Listener) {
	var msg struct {
		Addr string
		Port uint32
	}
	if err := ssh.Unmarshal(req.Payload, &msg); err != nil || !loopbackBind(msg.Addr) {
		reply(req, false, nil)
		return
	}
	bind := msg.Addr
	if bind == "localhost" {
		bind = "127.0.0.1"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(int(msg.Port))))
	if err != nil {
		reply(req, false, nil)
		return
	}
	port := ln.Addr().(*net.TCPAddr).Port
	key := net.JoinHostPort(msg.Addr, strconv.Itoa(port))
	mu.Lock()
	if old := listeners[key]; old != nil {
		old.Close()
	}
	listeners[key] = ln
	mu.Unlock()
	var payload []byte
	if msg.Port == 0 {
		payload = ssh.Marshal(struct{ Port uint32 }{uint32(port)})
	}
	reply(req, true, payload)
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go openForwarded(conn, raw, msg.Addr, port)
		}
	}()
}

func (s *Server) cancelForward(req *ssh.Request, mu *sync.Mutex, listeners map[string]net.Listener) {
	var msg struct {
		Addr string
		Port uint32
	}
	if err := ssh.Unmarshal(req.Payload, &msg); err != nil {
		reply(req, false, nil)
		return
	}
	key := net.JoinHostPort(msg.Addr, strconv.Itoa(int(msg.Port)))
	mu.Lock()
	ln := listeners[key]
	delete(listeners, key)
	mu.Unlock()
	if ln == nil {
		reply(req, false, nil)
		return
	}
	ln.Close()
	reply(req, true, nil)
}

// loopbackBind 判断远程转发请求的监听地址是不是回环。空地址和 0.0.0.0 会听在所有网卡上，拒绝。
func loopbackBind(addr string) bool {
	if addr == "localhost" {
		return true
	}
	ip := net.ParseIP(addr)
	return ip != nil && ip.IsLoopback()
}

func openForwarded(conn *ssh.ServerConn, raw net.Conn, host string, port int) {
	originIP, originPort := "127.0.0.1", 1
	if tcp, ok := raw.RemoteAddr().(*net.TCPAddr); ok && tcp.IP != nil {
		originIP = tcp.IP.String()
		if tcp.Port > 0 {
			originPort = tcp.Port
		}
	}
	ch, reqs, err := conn.OpenChannel("forwarded-tcpip", ssh.Marshal(struct {
		Addr       string
		Port       uint32
		OriginAddr string
		OriginPort uint32
	}{host, uint32(port), originIP, uint32(originPort)}))
	if err != nil {
		raw.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	bridge(raw, ch)
}

func bridge(a, b io.ReadWriteCloser) {
	type halfCloser interface{ CloseWrite() error }
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(a, b)
		if h, ok := a.(halfCloser); ok {
			h.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		io.Copy(b, a)
		if h, ok := b.(halfCloser); ok {
			h.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
	a.Close()
	b.Close()
}

func reply(req *ssh.Request, ok bool, payload []byte) {
	if req.WantReply {
		req.Reply(ok, payload)
	}
}

// session 处理单个通道上的 pty-req、shell、window-change 和 sftp 请求。
func (s *Server) session(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	var ptmx *os.File
	var shellCmd *exec.Cmd
	var size *pty.Winsize
	term := "xterm-256color"
	done := make(chan struct{})

	for {
		select {
		case <-done:
			// pty 只在这个协程里关闭：window-change 的 Setsize 也在这里执行，
			// 放到 shell 协程里关会和 Setsize 读文件描述符发生数据竞争。
			ptmx.Close()
			return
		case req, ok := <-requests:
			if !ok {
				// 客户端断开时必须主动结束 shell：另一个 goroutine 还阻塞在 PTY 读取上，
				// 只关闭 PTY 不一定能让 shell 收到 SIGHUP，会留下孤儿进程。
				if shellCmd != nil {
					shellCmd.Process.Signal(syscall.SIGHUP)
				}
				if ptmx != nil {
					ptmx.Close()
				}
				return
			}
			switch req.Type {
			case "pty-req":
				var payload struct {
					Term       string
					Cols, Rows uint32
					W, H       uint32
					Modes      string
				}
				if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
					req.Reply(false, nil)
					continue
				}
				term = payload.Term
				size = &pty.Winsize{Cols: uint16(payload.Cols), Rows: uint16(payload.Rows)}
				req.Reply(true, nil)
			case "window-change":
				if ptmx != nil && len(req.Payload) >= 8 {
					pty.Setsize(ptmx, &pty.Winsize{
						Cols: uint16(binary.BigEndian.Uint32(req.Payload)),
						Rows: uint16(binary.BigEndian.Uint32(req.Payload[4:])),
					})
				}
				if req.WantReply {
					req.Reply(true, nil)
				}
			case "shell":
				if size == nil {
					size = &pty.Winsize{Cols: 80, Rows: 24}
				}
				f, cmd, err := s.startShell(channel, term, size, done)
				if err != nil {
					req.Reply(false, nil)
					return
				}
				ptmx, shellCmd = f, cmd
				req.Reply(true, nil)
			case "subsystem":
				var payload struct{ Name string }
				if err := ssh.Unmarshal(req.Payload, &payload); err != nil || payload.Name != "sftp" {
					req.Reply(false, nil)
					continue
				}
				req.Reply(true, nil)
				var sftpOpts []sftp.ServerOption
				if s.opts.Dir != "" {
					sftpOpts = append(sftpOpts, sftp.WithServerWorkingDirectory(s.opts.Dir))
				}
				server, err := sftp.NewServer(channel, sftpOpts...)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(requests)
				server.Serve()
				server.Close()
				return
			default:
				if req.WantReply {
					req.Reply(false, nil)
				}
			}
		}
	}
}

func (s *Server) startShell(channel ssh.Channel, term string, size *pty.Winsize, done chan struct{}) (*os.File, *exec.Cmd, error) {
	cmd := exec.Command(s.opts.Shell[0], s.opts.Shell[1:]...)
	cmd.Dir = s.opts.Dir
	cmd.Env = append(os.Environ(), "TERM="+term)
	ptmx, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return nil, nil, err
	}
	go io.Copy(ptmx, channel)
	go func() {
		defer close(done)
		io.Copy(channel, ptmx) // shell 退出后 PTY 读取返回 EIO
		code := 0
		if err := cmd.Wait(); err != nil {
			var exit *exec.ExitError
			code = 1
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			}
		}
		channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{uint32(code)}))
	}()
	return ptmx, cmd, nil
}
