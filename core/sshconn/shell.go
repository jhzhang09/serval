package sshconn

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Shell 是一个远程交互终端。它实现 io.ReadWriteCloser 和 Resize，
// 可以直接作为终端控件的数据连接使用。
type Shell struct {
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	once    sync.Once
	err     error
	// done 在会话结束（Wait 返回）后关闭，waitErr 是 Wait 的结果。
	done    chan struct{}
	waitErr error
}

// OpenShell 申请 PTY 并启动登录 shell。
func (c *Client) OpenShell(cols, rows int) (*Shell, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("打开会话失败: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, err
	}
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 38400,
		ssh.TTY_OP_OSPEED: 38400,
	}
	if err := session.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		session.Close()
		return nil, fmt.Errorf("申请 PTY 失败: %w", err)
	}
	// 在启动 shell 之前申请：服务器要靠这次请求给 shell 设置 SSH_AUTH_SOCK。
	if c.forwardAgent {
		if err := agent.RequestAgentForwarding(session); err != nil {
			session.Close()
			return nil, ErrAgentForwardDenied
		}
	}
	if err := session.Shell(); err != nil {
		session.Close()
		return nil, fmt.Errorf("启动 shell 失败: %w", err)
	}
	sh := &Shell{session: session, stdin: stdin, stdout: stdout, done: make(chan struct{})}
	go func() {
		sh.waitErr = session.Wait()
		close(sh.done)
	}()
	return sh, nil
}

// Exited 等待会话结束（最多 timeout），报告远端 shell 是不是自己退出的。
//
// 远端 shell 退出时服务器会发来退出状态（不管退出码是多少），用户输入 exit、logout 都属于这种；
// 连接中断时没有退出状态。界面据此区分“会话结束了”和“断线了，需要重连”，
// 不能靠 ping：高延迟链路上 ping 慢不代表断线。超时也按“没有收到退出状态”处理。
func (s *Shell) Exited(timeout time.Duration) bool {
	select {
	case <-s.done:
	case <-time.After(timeout):
		return false
	}
	var exitErr *ssh.ExitError
	return s.waitErr == nil || errors.As(s.waitErr, &exitErr)
}

func (s *Shell) Read(p []byte) (int, error)  { return s.stdout.Read(p) }
func (s *Shell) Write(p []byte) (int, error) { return s.stdin.Write(p) }

// Resize 通知远端终端尺寸变化。
func (s *Shell) Resize(cols, rows int) error {
	return s.session.WindowChange(rows, cols)
}

// Close 结束会话，可重复调用。远端已先行退出时返回 nil。
func (s *Shell) Close() error {
	s.once.Do(func() {
		if err := s.session.Close(); err != nil && !errors.Is(err, io.EOF) {
			s.err = err
		}
	})
	return s.err
}
