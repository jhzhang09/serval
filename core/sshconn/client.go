// Package sshconn 提供 Serval 的 SSH 连接、终端会话和 SFTP 能力。
//
// 这个包不依赖任何 UI 框架：界面层只通过 Config 的回调和 Shell 的
// io.ReadWriteCloser 接口与它交互，这样更换 UI 框架时无需改动连接逻辑。
package sshconn

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// defaultTimeout 限制每一跳建立连接和完成握手的时间，不包含用户确认主机密钥的时间。
const defaultTimeout = 15 * time.Second

// maxJumps 限制跳板机的级数，防止配置错误造成过长的链路。OpenSSH 没有硬性上限，
// 但实际环境很少超过三四级。
const maxJumps = 8

// 需要用户补充凭据的错误。界面据此询问密码或口令，而不是只显示一段报错。
var (
	// ErrNoAuthMethod 表示没有任何可尝试的认证方式。
	ErrNoAuthMethod = errors.New("没有可用的认证方式，请输入密码或配置私钥")
	// ErrAuthFailed 表示服务器拒绝了所有尝试过的认证方式。
	ErrAuthFailed = errors.New("认证失败，请检查用户名和密码")
	// ErrKeyEncrypted 表示私钥已加密但没有提供口令。
	ErrKeyEncrypted = errors.New("私钥已加密，请输入私钥口令")
	// ErrBadPassphrase 表示私钥口令不正确。
	ErrBadPassphrase = errors.New("私钥口令不正确")
	// ErrAgentUnavailable 表示主机要求转发 ssh-agent，但本机没有可用的 agent。
	ErrAgentUnavailable = errors.New("没有可用的 ssh-agent")
	// ErrAgentForwardDenied 表示服务器拒绝了转发 ssh-agent 的请求。
	ErrAgentForwardDenied = errors.New("服务器拒绝转发 ssh-agent")
)

// Question 描述多步验证（keyboard-interactive）里的一个问题。
type Question struct {
	Text string
	Echo bool
}

// ChallengeFunc 是多步验证的回调函数。
type ChallengeFunc func(name, instruction string, questions []Question) ([]string, error)

// Config 描述一次 SSH 连接。
type Config struct {
	Host string
	Port int
	User string

	Password string
	// KeyPEM 是私钥内容（来自保险库），优先于 KeyPath。
	KeyPEM        []byte
	KeyPath       string
	KeyPassphrase string
	// UseAgent 为 true 时尝试使用 SSH_AUTH_SOCK 指向的 ssh-agent。
	UseAgent bool
	// ForwardAgent 为 true 时，连接建立后把本机 ssh-agent 转发给这台机器。
	// 没有 SSH_AUTH_SOCK 或连不上 agent 时，整次连接失败，而不是悄悄不转发。
	ForwardAgent bool
	// Challenge 在服务器要求多步验证（OTP 等）时被调用。
	Challenge ChallengeFunc

	// HostKeys 决定如何校验服务器身份，必须提供。
	HostKeys *HostKeyPolicy
	// Timeout 为零时使用 defaultTimeout。
	Timeout time.Duration

	// Jumps 是依次经过的跳板机（OpenSSH 的 ProxyJump）：先连 Jumps[0]，再经由它连
	// Jumps[1]……最后经由最后一台连到目标。每一跳有自己的认证和主机密钥校验，
	// 跳板机自己的 Jumps 和 Proxy 会被忽略，链路只由最外层的配置决定。
	Jumps []Config
	// Proxy 是建立第一条 TCP 连接时使用的代理。有跳板机时只作用于第一台跳板机。
	Proxy *Proxy
}

func (c Config) addr() string {
	port := c.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(c.Host, strconv.Itoa(port))
}

// HopError 表示链路中某一台跳板机出错。目标主机本身的错误不会包成 HopError，
// 所以界面可以据此判断该向哪一台机器询问密码。
type HopError struct {
	// Hop 是出错的跳板机在 Config.Jumps 中的下标。
	Hop  int
	Host string
	Err  error
}

func (e *HopError) Error() string { return fmt.Sprintf("跳板机 %s：%v", e.Host, e.Err) }
func (e *HopError) Unwrap() error { return e.Err }

// Client 是一条已认证的 SSH 连接，可以在其上打开多个终端和 SFTP 通道。
type Client struct {
	conn *ssh.Client
	// chain 是经过的跳板机连接，按连接顺序排列；关闭时倒序关闭。
	chain  []*ssh.Client
	agents []net.Conn
	// forwardAgent 表示目标主机要求转发 ssh-agent，打开 shell 时再向服务器申请。
	forwardAgent bool
}

// Dial 建立连接并完成认证，有跳板机时逐跳建立。取消 ctx 会中断尚未完成的连接，
// 包括等待用户确认主机密钥的阶段。
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if len(cfg.Jumps) > maxJumps {
		return nil, fmt.Errorf("跳板机最多 %d 级，当前配置了 %d 级", maxJumps, len(cfg.Jumps))
	}
	route := append(append([]Config{}, cfg.Jumps...), cfg)
	for i, hop := range route {
		if hop.HostKeys == nil {
			return nil, hopErr(i, len(cfg.Jumps), hop, errors.New("sshconn: 缺少主机密钥校验策略"))
		}
		if hop.Host == "" || hop.User == "" {
			return nil, hopErr(i, len(cfg.Jumps), hop, errors.New("sshconn: 主机和用户名不能为空"))
		}
	}

	c := &Client{}
	for i, hop := range route {
		timeout := hop.Timeout
		if timeout == 0 {
			timeout = defaultTimeout
		}
		var raw net.Conn
		var err error
		if i == 0 {
			raw, err = dialFirst(ctx, hop.addr(), cfg.Proxy, timeout)
		} else {
			raw, err = dialThrough(ctx, c.chain[i-1], hop.addr(), timeout)
		}
		if err == nil {
			var client *ssh.Client
			var agentConn net.Conn
			client, agentConn, err = handshake(ctx, raw, hop, timeout)
			if err == nil && i == len(cfg.Jumps) && hop.ForwardAgent {
				// 只在最终目标主机上开启 agent 转发，跳板机只用来建隧道，不能给跳板机暴露本机钥匙。
				// 先登记通道处理，再打开 shell。服务器要到 shell 里的程序连接
				// SSH_AUTH_SOCK 时才会打开通道，那时处理函数必须已经在。
				if ferr := setupAgentForward(client); ferr != nil {
					client.Close()
					if agentConn != nil {
						agentConn.Close()
					}
					err = ferr
				}
			}
			if err == nil {
				if agentConn != nil {
					c.agents = append(c.agents, agentConn)
				}
				if i < len(cfg.Jumps) {
					c.chain = append(c.chain, client)
				} else {
					c.conn = client
					c.forwardAgent = hop.ForwardAgent
				}
				continue
			}
		}
		c.closeAll()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, hopErr(i, len(cfg.Jumps), hop, err)
	}
	return c, nil
}

// hopErr 只给跳板机的错误加上位置信息，目标主机的错误原样返回。
func hopErr(i, jumps int, hop Config, err error) error {
	if i >= jumps {
		return err
	}
	return &HopError{Hop: i, Host: hop.Host, Err: err}
}

// dialThrough 经由已连上的跳板机打开到下一跳的 TCP 通道（direct-tcpip）。
func dialThrough(ctx context.Context, via *ssh.Client, addr string, timeout time.Duration) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := via.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("经由跳板机连接 %s 失败: %w", addr, err)
	}
	return conn, nil
}

// handshake 在已建立的连接上完成 SSH 握手和认证。
//
// 超时用定时器实现而不是连接的 deadline：经由跳板机的连接是 SSH 通道，
// 不支持 SetDeadline。超时就关闭连接，等待用户确认主机密钥时暂停计时。
func handshake(ctx context.Context, raw net.Conn, cfg Config, timeout time.Duration) (*ssh.Client, net.Conn, error) {
	addr := cfg.addr()
	// 等用户回答多步验证（掏手机看验证码）期间暂停握手超时，和确认主机指纹时一样。
	// onWaiting 在计时器建好后赋值；认证回调要到握手开始后才会被调用。
	var onWaiting func(bool)
	if ask := cfg.Challenge; ask != nil {
		cfg.Challenge = func(name, instruction string, questions []Question) ([]string, error) {
			if onWaiting != nil {
				onWaiting(true)
				defer onWaiting(false)
			}
			return ask(name, instruction, questions)
		}
	}
	auth, agentConn, err := authMethods(cfg)
	if err != nil {
		raw.Close()
		return nil, nil, err
	}
	fail := func(err error) (*ssh.Client, net.Conn, error) {
		raw.Close()
		if agentConn != nil {
			agentConn.Close()
		}
		return nil, nil, err
	}

	var timedOut atomic.Bool
	timer := time.AfterFunc(timeout, func() {
		timedOut.Store(true)
		raw.Close()
	})
	defer timer.Stop()
	stopWatch := context.AfterFunc(ctx, func() { raw.Close() })
	defer stopWatch()

	onWaiting = func(waiting bool) {
		if waiting {
			timer.Stop()
		} else {
			timer.Reset(timeout)
		}
	}
	callback, algorithms, err := cfg.HostKeys.callback(addr, onWaiting)
	if err != nil {
		return fail(err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(raw, addr, &ssh.ClientConfig{
		User:              cfg.User,
		Auth:              auth,
		HostKeyCallback:   callback,
		HostKeyAlgorithms: algorithms,
	})
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return fail(ctx.Err())
		case timedOut.Load():
			return fail(fmt.Errorf("连接 %s 超时：服务器在 %s 内没有完成握手", addr, timeout))
		// x/crypto 没有为认证失败导出错误类型，只能匹配文字。匹配不到时
		// 仍然是一个如实的错误，只是界面不会弹出密码框。
		case strings.Contains(err.Error(), "unable to authenticate"):
			return fail(ErrAuthFailed)
		}
		return fail(fmt.Errorf("SSH 握手失败: %w", err))
	}
	return ssh.NewClient(sshConn, chans, reqs), agentConn, nil
}

// Ping 发送一次保活请求并返回往返时间，同时起到防止 NAT 断开空闲连接的作用。
func (c *Client) Ping(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		// 服务器不认识这个请求时也会回复（拒绝），同样算一次有效往返。
		_, _, err := c.conn.SendRequest("keepalive@openssh.com", true, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return 0, err
		}
		// Windows 时钟粒度较粗，本机回环往返可能落在同一个时钟滴答内（0s），
		// 至少返回 1ns 以区分“尚未测过（0）”与“已完成测速（<1 ms）”。
		return max(time.Nanosecond, time.Since(start)), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Close 关闭连接及其上的所有通道，再倒序关闭经过的跳板机。可以重复调用；
// 关闭后再 Ping 会返回错误而不是崩溃。
func (c *Client) Close() error {
	err := c.conn.Close()
	c.closeChain()
	return err
}

// closeAll 用于连接建立到一半失败时，关掉已经建立的部分。
func (c *Client) closeAll() {
	if c.conn != nil {
		c.conn.Close()
	}
	c.closeChain()
}

func (c *Client) closeChain() {
	for i := len(c.chain) - 1; i >= 0; i-- {
		c.chain[i].Close()
	}
	for _, a := range c.agents {
		a.Close()
	}
}

// setupAgentForward 让这台连接上的 auth-agent 通道转到本机 ssh-agent。
// 套接字不存在或连不上时返回 ErrAgentUnavailable，不把套接字路径写进错误。
func setupAgentForward(client *ssh.Client) error {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return ErrAgentUnavailable
	}
	if err := agent.ForwardToRemote(client, sock); err != nil {
		if strings.Contains(err.Error(), "already have handler") {
			return fmt.Errorf("转发 ssh-agent 失败: %w", err)
		}
		return ErrAgentUnavailable
	}
	return nil
}

// authMethods 按私钥、agent、密码的顺序组装认证方式。
func authMethods(cfg Config) ([]ssh.AuthMethod, net.Conn, error) {
	var methods []ssh.AuthMethod

	if len(cfg.KeyPEM) > 0 || cfg.KeyPath != "" {
		signer, err := loadKey(cfg.KeyPEM, cfg.KeyPath, cfg.KeyPassphrase)
		if err != nil {
			return nil, nil, err
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	var agentConn net.Conn
	if sock := os.Getenv("SSH_AUTH_SOCK"); cfg.UseAgent && sock != "" {
		// agent 不可用时不算致命错误，用户可能还配置了其他认证方式。
		if conn, err := net.Dial("unix", sock); err == nil {
			agentConn = conn
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password))
	}

	// 没有私钥、agent、密码时先问密码再握手：单有多步验证回调不算一种认证方式，
	// 否则会白白握手一次（还要先确认指纹）才发现需要密码。
	if len(methods) == 0 {
		if agentConn != nil {
			agentConn.Close()
		}
		return nil, nil, ErrNoAuthMethod
	}

	if cfg.Challenge != nil || cfg.Password != "" {
		passwordUsed := false
		methods = append(methods, ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			if len(questions) == 0 {
				return nil, nil
			}
			single := len(questions) == 1 && !echos[0]
			// 基于 PAM 的服务器常用 keyboard-interactive 要密码：只有一个隐藏输入、而且看起来是在要密码的问题，
			// 用保存的密码自动回答，只答一次。验证码之类的问题交给用户，不能拿密码去填。
			if cfg.Password != "" && !passwordUsed && single && looksLikePasswordPrompt(questions[0]) {
				passwordUsed = true
				return []string{cfg.Password}, nil
			}
			// 答过密码后服务器又问密码，说明密码不对：直接失败，走“密码不正确”的流程，
			// 而不是把密码问题当成验证码弹给用户。
			if passwordUsed && single && looksLikePasswordPrompt(questions[0]) {
				return nil, ErrAuthFailed
			}
			if cfg.Challenge == nil {
				return nil, errors.New("服务器要求多步验证（例如验证码）")
			}
			qs := make([]Question, len(questions))
			for i := range questions {
				qs[i] = Question{Text: questions[i], Echo: echos[i]}
			}
			return cfg.Challenge(name, instruction, qs)
		}))
	}
	return methods, agentConn, nil
}

// looksLikePasswordPrompt 判断 keyboard-interactive 的问题是不是在要密码（而不是验证码）。
func looksLikePasswordPrompt(q string) bool {
	q = strings.ToLower(q)
	return strings.Contains(q, "password") || strings.Contains(q, "密码")
}

func loadKey(data []byte, path, passphrase string) (ssh.Signer, error) {
	var err error
	if len(data) == 0 {
		if data, err = os.ReadFile(path); err != nil {
			return nil, fmt.Errorf("读取私钥失败: %w", err)
		}
	}
	var signer ssh.Signer
	if passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(data)
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		return nil, ErrKeyEncrypted
	}
	if errors.Is(err, x509.IncorrectPasswordError) {
		return nil, ErrBadPassphrase
	}
	if err != nil {
		return nil, fmt.Errorf("解析私钥失败: %w", err)
	}
	return signer, nil
}

// Output 在远端执行一条单次命令并返回其标准输出（适用于探针等非交互请求）。
func (c *Client) Output(cmd string) ([]byte, error) {
	if c == nil || c.conn == nil {
		return nil, errors.New("sshconn: 客户端未连接")
	}
	sess, err := c.conn.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	return sess.Output(cmd)
}

// ExecResult 是远端非交互命令的执行结果。
type ExecResult struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool
}

type limitedWriter struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.limit <= 0 {
		w.buf.Write(p)
		return len(p), nil
	}
	rem := w.limit - w.buf.Len()
	if rem > 0 {
		if len(p) > rem {
			w.buf.Write(p[:rem])
			w.truncated = true
		} else {
			w.buf.Write(p)
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	// 消费全部字节，避免远端因管道阻塞而无法退出。
	return len(p), nil
}

// ExecContext 在远端执行一条单次命令，分别收集标准输出与标准错误，并支持超时或取消。
// 命令成功送达远端并返回非零退出码时，err 为 nil，通过 ExitCode 体现结果。
func (c *Client) ExecContext(ctx context.Context, cmd string, maxBytes int) (ExecResult, error) {
	if c == nil || c.conn == nil {
		return ExecResult{}, errors.New("sshconn: 客户端未连接")
	}
	if err := ctx.Err(); err != nil {
		return ExecResult{}, err
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 10
	}
	sess, err := c.conn.NewSession()
	if err != nil {
		return ExecResult{}, err
	}
	defer sess.Close()

	stdout := &limitedWriter{limit: maxBytes}
	stderr := &limitedWriter{limit: maxBytes}
	sess.Stdout = stdout
	sess.Stderr = stderr

	if err := sess.Start(cmd); err != nil {
		return ExecResult{}, err
	}

	done := make(chan error, 1)
	go func() {
		done <- sess.Wait()
	}()

	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGTERM)
		_ = sess.Close()
		select {
		case <-done:
		case <-time.After(300 * time.Millisecond):
		}
		return ExecResult{
			Stdout:    stdout.buf.String(),
			Stderr:    stderr.buf.String(),
			ExitCode:  -1,
			Truncated: stdout.truncated || stderr.truncated,
		}, ctx.Err()
	}

	res := ExecResult{
		Stdout:    stdout.buf.String(),
		Stderr:    stderr.buf.String(),
		Truncated: stdout.truncated || stderr.truncated,
	}
	if waitErr != nil {
		var exitErr *ssh.ExitError
		if errors.As(waitErr, &exitErr) {
			res.ExitCode = exitErr.ExitStatus()
			return res, nil
		}
		return res, waitErr
	}
	return res, nil
}
