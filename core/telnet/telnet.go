// Package telnet 实现标准 Telnet 协议客户端（RFC 854、RFC 857、RFC 858、RFC 1073、RFC 1091）。
// 纯 Go 标准库实现，不依赖任何第三方库或外部 UI。
package telnet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	// ErrRefused 表示目标端口拒绝连接。
	ErrRefused = errors.New("目标主机拒绝连接")
	// ErrTimeout 表示连接超时。
	ErrTimeout = errors.New("连接超时")
	// ErrUnreachable 表示网络或主机不可达。
	ErrUnreachable = errors.New("无法连接到目标主机")
)

func mapDialError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ErrTimeout
	}
	if errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "connection refused") {
		return ErrRefused
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return ErrUnreachable
	}
	return err
}

// Telnet 控制命令代码 (RFC 854)
const (
	cmdSE   = 240 // 子协商结束
	cmdNOP  = 241
	cmdDM   = 242
	cmdBRK  = 243
	cmdIP   = 244
	cmdAO   = 245
	cmdAYT  = 246
	cmdEC   = 247
	cmdEL   = 248
	cmdGA   = 249 // Go Ahead
	cmdSB   = 250 // 子协商开始
	cmdWILL = 251
	cmdWONT = 252
	cmdDO   = 253
	cmdDONT = 254
	cmdIAC  = 255 // Interpret As Command
)

// 选项代码
const (
	optEcho  = 1  // RFC 857
	optSGA   = 3  // Suppress Go Ahead, RFC 858
	optTTYPE = 24 // 终端类型协商, RFC 1091
	optNAWS  = 31 // 窗口大小协商, RFC 1073
)

// Conn 包装底层 TCP 连接并处理 Telnet 协议协商，对上层提供干净的标准 net.Conn。
type Conn struct {
	raw net.Conn

	pr *io.PipeReader
	pw *io.PipeWriter

	mu         sync.Mutex
	nawsAgreed bool
	cols, rows int

	closeOnce sync.Once
	closed    chan struct{}
}

// Dial 连接指定地址的 Telnet 服务器。
func Dial(addr string, timeout time.Duration) (*Conn, error) {
	d := net.Dialer{Timeout: timeout}
	raw, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, mapDialError(err)
	}
	return Wrap(raw), nil
}

// DialContext 使用 Context 连接 Telnet 服务器。
func DialContext(ctx context.Context, addr string) (*Conn, error) {
	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, mapDialError(err)
	}
	return Wrap(raw), nil
}

// Wrap 包装一个已经建立的底层流为 Telnet 协议连接。
func Wrap(raw net.Conn) *Conn {
	pr, pw := io.Pipe()
	c := &Conn{
		raw:    raw,
		pr:     pr,
		pw:     pw,
		cols:   80,
		rows:   24,
		closed: make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Resize 是 SetSize 的别名，满足终端与 shell 接口。
func (c *Conn) Resize(cols, rows int) error {
	return c.SetSize(cols, rows)
}

// SetSize 向服务器上报终端尺寸变更 (RFC 1073 NAWS)。
func (c *Conn) SetSize(cols, rows int) error {
	c.mu.Lock()
	c.cols, c.rows = cols, rows
	agreed := c.nawsAgreed
	c.mu.Unlock()

	if !agreed {
		return nil
	}
	return c.sendNAWS(cols, rows)
}

func (c *Conn) sendNAWS(cols, rows int) error {
	buf := []byte{
		cmdIAC, cmdSB, optNAWS,
		byte(cols >> 8), byte(cols & 0xff),
		byte(rows >> 8), byte(rows & 0xff),
		cmdIAC, cmdSE,
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.raw.Write(buf)
	return err
}

func (c *Conn) send(bytes []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.raw.Write(bytes)
	return err
}

// readLoop 在后台解包 Telnet 协议控制指令与数据。
func (c *Conn) readLoop() {
	buf := make([]byte, 4096)
	var (
		inIAC   bool
		inSB    bool
		sbOpt   byte
		sbBuf   bytes.Buffer
		cmdType byte
	)

	cleanData := make([]byte, 0, 4096)

	for {
		n, err := c.raw.Read(buf)
		if n > 0 {
			cleanData = cleanData[:0]
			for i := 0; i < n; i++ {
				b := buf[i]

				if inSB {
					if inIAC {
						if b == cmdSE {
							// 子协商结束，处理子协商请求
							c.handleSB(sbOpt, sbBuf.Bytes())
							inSB = false
							inIAC = false
							sbOpt = 0
							sbBuf.Reset()
						} else if b == cmdIAC {
							// 真正的 0xFF
							sbBuf.WriteByte(cmdIAC)
							inIAC = false
						} else {
							inIAC = false
						}
					} else if b == cmdIAC {
						inIAC = true
					} else if sbOpt == 0 {
						sbOpt = b
					} else {
						sbBuf.WriteByte(b)
					}
					continue
				}

				if inIAC {
					if cmdType != 0 {
						// 已收到 WILL/WONT/DO/DONT，当前字节是选项参数
						c.handleOption(cmdType, b)
						cmdType = 0
						inIAC = false
					} else {
						switch b {
						case cmdIAC:
							// IAC IAC 转义为一个 0xFF 数据字节
							cleanData = append(cleanData, cmdIAC)
							inIAC = false
						case cmdWILL, cmdWONT, cmdDO, cmdDONT:
							cmdType = b
						case cmdSB:
							inSB = true
							inIAC = false
							sbOpt = 0
							sbBuf.Reset()
						default:
							// 其余无参命令（NOP、GA 等），忽略
							inIAC = false
						}
					}
					continue
				}

				if b == cmdIAC {
					inIAC = true
					continue
				}

				cleanData = append(cleanData, b)
			}

			if len(cleanData) > 0 {
				if _, werr := c.pw.Write(cleanData); werr != nil {
					break
				}
			}
		}

		if err != nil {
			c.pw.CloseWithError(err)
			break
		}
	}
}

func (c *Conn) handleOption(cmd, opt byte) {
	switch cmd {
	case cmdDO:
		switch opt {
		case optTTYPE:
			_ = c.send([]byte{cmdIAC, cmdWILL, optTTYPE})
		case optNAWS:
			c.mu.Lock()
			c.nawsAgreed = true
			cols, rows := c.cols, c.rows
			c.mu.Unlock()
			_ = c.send([]byte{cmdIAC, cmdWILL, optNAWS})
			_ = c.sendNAWS(cols, rows)
		case optSGA:
			_ = c.send([]byte{cmdIAC, cmdWILL, optSGA})
		default:
			_ = c.send([]byte{cmdIAC, cmdWONT, opt})
		}
	case cmdDONT:
		_ = c.send([]byte{cmdIAC, cmdWONT, opt})
	case cmdWILL:
		switch opt {
		case optEcho, optSGA:
			_ = c.send([]byte{cmdIAC, cmdDO, opt})
		default:
			_ = c.send([]byte{cmdIAC, cmdDONT, opt})
		}
	case cmdWONT:
		_ = c.send([]byte{cmdIAC, cmdDONT, opt})
	}
}

func (c *Conn) handleSB(opt byte, data []byte) {
	if opt == optTTYPE && len(data) >= 1 && data[0] == 1 { // SEND
		// 回复 "xterm-256color"
		termType := "xterm-256color"
		resp := []byte{cmdIAC, cmdSB, optTTYPE, 0} // IS
		resp = append(resp, []byte(termType)...)
		resp = append(resp, cmdIAC, cmdSE)
		_ = c.send(resp)
	}
}

// Read 读取处理过的远端文本数据。
func (c *Conn) Read(b []byte) (n int, err error) {
	return c.pr.Read(b)
}

// Write 写入客户端输入到远端，自动对 0xFF 进行 IAC 转义。
func (c *Conn) Write(b []byte) (n int, err error) {
	var escaped bytes.Buffer
	for _, ch := range b {
		if ch == cmdIAC {
			escaped.WriteByte(cmdIAC)
			escaped.WriteByte(cmdIAC)
		} else {
			escaped.WriteByte(ch)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	nw, err := c.raw.Write(escaped.Bytes())
	if err != nil {
		return 0, err
	}
	_ = nw
	return len(b), nil
}

// Close 关闭连接。
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.pr.Close()
		_ = c.pw.Close()
		_ = c.raw.Close()
	})
	return nil
}

// LocalAddr 返回本地地址。
func (c *Conn) LocalAddr() net.Addr { return c.raw.LocalAddr() }

// RemoteAddr 返回远端服务器地址。
func (c *Conn) RemoteAddr() net.Addr { return c.raw.RemoteAddr() }

// SetDeadline 设置读写超时。
func (c *Conn) SetDeadline(t time.Time) error { return c.raw.SetDeadline(t) }

// SetReadDeadline 设置读超时。
func (c *Conn) SetReadDeadline(t time.Time) error { return c.raw.SetReadDeadline(t) }

// SetWriteDeadline 设置写超时。
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.raw.SetWriteDeadline(t) }
