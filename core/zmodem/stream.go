package zmodem

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"time"
)

var (
	// ErrCancelled 表示对端取消了传输（收到连续的 CAN），或者本端被取消。
	ErrCancelled = errors.New("传输已取消")
	// ErrTimeout 表示对端长时间没有响应。
	ErrTimeout = errors.New("对端长时间没有响应")
	// ErrCommandRefused 表示对端要求执行命令（ZCOMMAND）。这是协议里的远程执行功能，一律拒绝。
	ErrCommandRefused = errors.New("对端要求在本机执行命令，已拒绝")

	// ErrAbandon 由调用方返回（例如 Receiver.Accept），表示对端多半已经不在等了：用户考虑得太久，
	// 超过了 lrzsz 的等待时间（sz 发出文件信息后等 60 秒，rz 等 50 秒左右就退出）。
	// 这时会话直接结束，不再向连接写任何东西：对端已退出时，写下去的字节会被当成键盘输入交给 shell。
	ErrAbandon = errors.New("对端已经不再等待")

	errBadCRC = errors.New("校验错误")
)

// Stream 是会话的两端。
//
// Recv 每次返回对端发来的下一段数据，ctx 结束时返回 ctx.Err()。用函数而不是 io.Reader，
// 是为了让调用方（终端）独占底层连接的读取：会话结束时多读到的数据通过 Leftover 交还，
// 不会有一个卡在 Read 里的 goroutine 抢走之后的终端输出。
type Stream struct {
	Recv func(ctx context.Context) ([]byte, error)
	Send io.Writer
}

// ReaderStream 把 r 和 w 包成 Stream：在后台 goroutine 里读 r。适合进程的标准输入输出这类
// 会话结束后不再使用的连接。
func ReaderStream(r io.Reader, w io.Writer) Stream {
	type chunk struct {
		data []byte
		err  error
	}
	ch := make(chan chunk, 16)
	go func() {
		for {
			buf := make([]byte, 32*1024)
			n, err := r.Read(buf)
			if n > 0 {
				ch <- chunk{data: buf[:n]}
			}
			if err != nil {
				ch <- chunk{err: err}
				close(ch)
				return
			}
		}
	}()
	return Stream{
		Send: w,
		Recv: func(ctx context.Context) ([]byte, error) {
			select {
			case c, ok := <-ch:
				if !ok {
					return nil, io.EOF
				}
				return c.data, c.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
}

// idleTimeout 是等对端数据的最长时间。lrzsz 的接收端每 10 秒左右重发一次 ZRINIT，
// 发送端在等应答时也会重试，30 秒没有任何数据说明对端已经退出或者连接断了。
const idleTimeout = 30 * time.Second

// reader 逐字节读取对端数据并解码帧头和数据包。
type reader struct {
	ctx     context.Context
	stream  Stream
	timeout time.Duration // 等一段数据的最长时间，0 表示不限
	buf     []byte
	cans    int // 连续收到的 CAN（ZDLE）个数
}

func newReader(ctx context.Context, s Stream, prefix []byte) *reader {
	return &reader{ctx: ctx, stream: s, timeout: idleTimeout, buf: append([]byte(nil), prefix...)}
}

// leftover 返回已经读到、还没解码的数据。
func (r *reader) leftover() []byte { return r.buf }

// rawByte 读下一个原始字节。
func (r *reader) rawByte() (byte, error) {
	for len(r.buf) == 0 {
		ctx, cancel := r.ctx, context.CancelFunc(func() {})
		if r.timeout > 0 { // 0 表示不限时，由调用方自己计时
			ctx, cancel = context.WithTimeout(r.ctx, r.timeout)
		}
		data, err := r.stream.Recv(ctx)
		timedOut := ctx.Err() == context.DeadlineExceeded && r.ctx.Err() == nil
		cancel()
		if len(data) > 0 {
			r.buf = append(r.buf, data...)
		}
		if err != nil && len(r.buf) == 0 {
			switch {
			case timedOut:
				return 0, ErrTimeout
			case r.ctx.Err() != nil:
				return 0, ErrCancelled
			}
			return 0, err
		}
	}
	c := r.buf[0]
	r.buf = r.buf[1:]
	return c, nil
}

// noteCAN 记录连续的 CAN：协议规定连续 5 个 CAN 表示取消。
func (r *reader) noteCAN(c byte) error {
	if c == zdle {
		r.cans++
		if r.cans >= 5 {
			return errPeerCancelled
		}
		return nil
	}
	r.cans = 0
	return nil
}

// errFrameEnd 是 zdleByte 遇到数据包结束标记时返回的伪错误，标记字符在 end 里。
type errFrameEnd struct{ end byte }

func (e errFrameEnd) Error() string { return fmt.Sprintf("数据包结束 %q", e.end) }

// zdleByte 读一个解码后的字节：处理 ZDLE 转义，跳过没有转义的 XON/XOFF（流控字符）。
func (r *reader) zdleByte() (byte, error) {
	for {
		c, err := r.rawByte()
		if err != nil {
			return 0, err
		}
		if err := r.noteCAN(c); err != nil {
			return 0, err
		}
		switch c {
		case xon, xon | 0x80, 0x13, 0x93:
			continue
		case zdle:
		default:
			return c, nil
		}
		// ZDLE 之后的字符决定含义。
		for {
			c, err = r.rawByte()
			if err != nil {
				return 0, err
			}
			if err := r.noteCAN(c); err != nil {
				return 0, err
			}
			switch c {
			case xon, xon | 0x80, 0x13, 0x93:
				continue
			case zdle:
				continue // 可能是取消序列，noteCAN 计数
			case zcrce, zcrcg, zcrcq, zcrcw:
				return 0, errFrameEnd{end: c}
			case zrub0:
				return 0x7f, nil
			case zrub1:
				return 0xff, nil
			}
			if c&0x60 == 0x40 {
				return c ^ 0x40, nil
			}
			return 0, fmt.Errorf("无效的转义字符 0x%02x", c)
		}
	}
}

// readHeader 读下一个帧头，跳过帧头之前的任何数据（对端打印的文字、损坏的数据）。
// 返回帧头和它是否用 CRC32（决定后面数据包的 CRC 长度）。
func (r *reader) readHeader() (header, bool, error) {
	for {
		c, err := r.rawByte()
		if err != nil {
			return header{}, false, err
		}
		if err := r.noteCAN(c); err != nil {
			return header{}, false, err
		}
		if c != zpad {
			continue
		}
		// 一个或多个 ZPAD 之后是 ZDLE 和格式字符。
		for c == zpad {
			if c, err = r.rawByte(); err != nil {
				return header{}, false, err
			}
		}
		if err := r.noteCAN(c); err != nil {
			return header{}, false, err
		}
		if c != zdle {
			continue
		}
		format, err := r.rawByte()
		if err != nil {
			return header{}, false, err
		}
		if err := r.noteCAN(format); err != nil {
			return header{}, false, err
		}
		var h header
		switch format {
		case zhex:
			h, err = r.hexHeaderBody()
		case zbin:
			h, err = r.binHeaderBody(false)
		case zbin32:
			h, err = r.binHeaderBody(true)
		default:
			continue // 不认识的格式（比如没有协商的 RLE），当作噪声跳过
		}
		if err == errBadCRC {
			continue // 损坏的帧头：继续找下一个，对端会重发
		}
		return h, format == zbin32, err
	}
}

func (r *reader) hexByte() (byte, error) {
	var v byte
	for range 2 {
		c, err := r.rawByte()
		if err != nil {
			return 0, err
		}
		c &= 0x7f
		switch {
		case c >= '0' && c <= '9':
			v = v<<4 | (c - '0')
		case c >= 'a' && c <= 'f':
			v = v<<4 | (c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			v = v<<4 | (c - 'A' + 10)
		default:
			return 0, errBadCRC
		}
	}
	return v, nil
}

func (r *reader) hexHeaderBody() (header, error) {
	var raw [7]byte
	for i := range raw {
		b, err := r.hexByte()
		if err != nil {
			return header{}, err
		}
		raw[i] = b
	}
	if crc16(0, raw[:5]...) != uint16(raw[5])<<8|uint16(raw[6]) {
		return header{}, errBadCRC
	}
	// 帧头后的回车换行和 XON 在找下一个帧头时会被当作噪声跳过，这里不必读掉。
	return header{kind: raw[0], p: [4]byte{raw[1], raw[2], raw[3], raw[4]}}, nil
}

func (r *reader) binHeaderBody(use32 bool) (header, error) {
	n := 7
	if use32 {
		n = 9
	}
	raw := make([]byte, n)
	for i := range raw {
		b, err := r.zdleByte()
		if err != nil {
			if _, ok := err.(errFrameEnd); ok {
				return header{}, errBadCRC
			}
			return header{}, err
		}
		raw[i] = b
	}
	if use32 {
		if crc32.ChecksumIEEE(raw[:5]) != binary.LittleEndian.Uint32(raw[5:]) {
			return header{}, errBadCRC
		}
	} else if crc16(0, raw[:5]...) != uint16(raw[5])<<8|uint16(raw[6]) {
		return header{}, errBadCRC
	}
	return header{kind: raw[0], p: [4]byte{raw[1], raw[2], raw[3], raw[4]}}, nil
}

// maxSubpacket 是接受的最大数据包。协议上限是 1024，有的实现用到 8192，再多一定是数据坏了。
const maxSubpacket = 8192

// readSubpacket 读一个数据包，返回数据和结束标记。CRC 不对时返回 errBadCRC。
func (r *reader) readSubpacket(use32 bool) ([]byte, byte, error) {
	var data []byte
	for {
		b, err := r.zdleByte()
		if err != nil {
			fe, ok := err.(errFrameEnd)
			if !ok {
				return nil, 0, err
			}
			if err := r.checkCRC(data, fe.end, use32); err != nil {
				return nil, 0, err
			}
			return data, fe.end, nil
		}
		if len(data) >= maxSubpacket {
			return nil, 0, errBadCRC
		}
		data = append(data, b)
	}
}

func (r *reader) checkCRC(data []byte, end byte, use32 bool) error {
	n := 2
	if use32 {
		n = 4
	}
	got := make([]byte, n)
	for i := range got {
		b, err := r.zdleByte()
		if err != nil {
			if _, ok := err.(errFrameEnd); ok {
				return errBadCRC
			}
			return err
		}
		got[i] = b
	}
	if use32 {
		c := crc32.Update(0, crc32.IEEETable, data)
		c = crc32.Update(c, crc32.IEEETable, []byte{end})
		if c != binary.LittleEndian.Uint32(got) {
			return errBadCRC
		}
		return nil
	}
	if crc16(crc16(0, data...), end) != uint16(got[0])<<8|uint16(got[1]) {
		return errBadCRC
	}
	return nil
}
