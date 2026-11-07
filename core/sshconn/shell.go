package sshconn

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Shell 是一个远程交互终端。它实现 io.ReadWriteCloser 和 Resize，
// 可以直接作为终端控件的数据连接使用。
type Shell struct {
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	once    sync.Once
	err     error
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
	if err := session.Shell(); err != nil {
		session.Close()
		return nil, fmt.Errorf("启动 shell 失败: %w", err)
	}
	return &Shell{session: session, stdin: stdin, stdout: stdout}, nil
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
