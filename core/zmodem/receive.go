package zmodem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"
)

// errPeerCancelled 表示对端发来了取消序列。这时对端已经退出，不必再给它发取消。
var errPeerCancelled = fmt.Errorf("对端%w", ErrCancelled)

// FileInfo 是一个要传输的文件。
type FileInfo struct {
	// Name 是文件名。接收时已经处理过：只保留最后一段、去掉控制字符，不会包含路径。
	Name    string
	Size    int64 // 发送方声明的大小，-1 表示未知
	ModTime time.Time
	Mode    fs.FileMode
}

// Sink 接收一个文件的内容。Commit 在完整收到后调用；Abort 在中途失败或取消时调用，应删除写了一半的数据。
type Sink interface {
	io.Writer
	Commit() error
	Abort()
}

// Receiver 接收对端（远程 sz）发来的文件。
type Receiver struct {
	// Accept 在每个文件开始前调用，返回 nil 表示跳过这个文件，返回错误会中止整个传输。
	// 对端在等 Accept 的结果（lrzsz 的 sz 最多等 60 秒）：耗时太久时应返回 ErrAbandon。
	Accept func(FileInfo) (Sink, error)
	// Progress 报告当前文件已收到的字节数，可以为 nil。
	Progress func(FileInfo, int64)
}

// receiverInit 是接收方的 ZRINIT：全双工、可以边收边写、支持 CRC32，缓冲区大小 0 表示不限（流式）。
var receiverInit = header{kind: zrinit, p: [4]byte{0, 0, 0, canFDX | canOVIO | canFC32}}

// Run 执行一次接收。prefix 是已经从连接里读到的、从起始信号开始的数据。
// 返回会话结束后多读到的数据，调用方应把它交还终端显示。
func (rc *Receiver) Run(ctx context.Context, s Stream, prefix []byte) (leftover []byte, err error) {
	r := newReader(ctx, s, prefix)
	enc := &encoder{}
	var cur *incoming
	defer func() {
		if err != nil {
			if cur != nil {
				cur.sink.Abort()
			}
			if !errors.Is(err, errPeerCancelled) && !errors.Is(err, ErrAbandon) {
				s.Send.Write(cancelSequence)
			}
		}
		leftover = r.leftover()
	}()
	sendHex := func(h header) error {
		enc.hexHeader(h)
		_, err := s.Send.Write(enc.take())
		return err
	}
	if err := sendHex(receiverInit); err != nil {
		return nil, err
	}
	initAt := time.Now()
	for {
		h, use32, err := r.readHeader()
		if err != nil {
			return nil, err
		}
		switch h.kind {
		case zrqinit:
			// 对端在等我们回应时会重复发 ZRQINIT（例如用户还在选保存位置），堆积的只回一次。
			if cur == nil && time.Since(initAt) >= duplicateWindow {
				err = sendHex(receiverInit)
				initAt = time.Now()
			}
		case zsinit:
			// 发送方的初始化（带一个“注意字符串”），本实现不需要，读掉后确认。
			if _, _, perr := r.readSubpacket(use32); perr == errBadCRC {
				err = sendHex(header{kind: znak})
			} else if perr != nil {
				return nil, perr
			} else {
				err = sendHex(header{kind: zack})
			}
		case zfile:
			data, _, perr := r.readSubpacket(use32)
			if perr == errBadCRC {
				err = sendHex(header{kind: znak})
				break
			}
			if perr != nil {
				return nil, perr
			}
			info := parseFileInfo(data)
			if cur != nil && cur.info.Name == info.Name {
				// 同一个文件的 ZFILE 重复到达（对端没收到我们的 ZRPOS）：告诉它从哪里继续。
				err = sendHex(posHeader(zrpos, cur.offset))
				break
			}
			if cur != nil {
				cur.sink.Abort() // 上一个文件没收完就开始了新文件
				cur = nil
			}
			sink, aerr := rc.Accept(info)
			if aerr != nil {
				return nil, aerr
			}
			if sink == nil {
				err = sendHex(header{kind: zskip})
				break
			}
			cur = &incoming{info: info, sink: sink}
			err = sendHex(posHeader(zrpos, 0))
		case zdata:
			if cur == nil {
				continue // 没有文件的数据：忽略，对端超时后会重发 ZFILE
			}
			if h.pos() != cur.offset {
				err = sendHex(posHeader(zrpos, cur.offset))
				break
			}
			err = rc.receiveData(r, cur, use32, sendHex)
		case zeof:
			if cur == nil || h.pos() != cur.offset {
				continue // 协议规定：位置对不上的 ZEOF 忽略
			}
			if cerr := cur.sink.Commit(); cerr != nil {
				cur = nil // Commit 失败时由 Sink 自己清理
				return nil, cerr
			}
			cur = nil
			err = sendHex(receiverInit)
			initAt = time.Now()
		case zfin:
			enc.hexHeader(header{kind: zfin})
			if _, err := s.Send.Write(enc.take()); err != nil {
				return nil, err
			}
			r.consumeOverAndOut()
			return nil, nil
		case zcommand:
			return nil, ErrCommandRefused
		case zfreecnt:
			err = sendHex(posHeader(zack, 0)) // 0 表示不报告剩余空间
		case zabort, zferr:
			return nil, errPeerCancelled
		}
		if err != nil {
			return nil, err
		}
	}
}

// incoming 是正在接收的文件。
type incoming struct {
	info   FileInfo
	sink   Sink
	offset int64
}

// receiveData 接收一个 ZDATA 帧里的数据包，直到帧结束或者出错。
func (rc *Receiver) receiveData(r *reader, cur *incoming, use32 bool, sendHex func(header) error) error {
	for {
		data, end, err := r.readSubpacket(use32)
		if err == errBadCRC {
			// 数据坏了：请发送方从最后一个正确的位置重发，之后的数据会被当作噪声跳过。
			return sendHex(posHeader(zrpos, cur.offset))
		}
		if err != nil {
			return err
		}
		if len(data) > 0 {
			if _, err := cur.sink.Write(data); err != nil {
				return err
			}
			cur.offset += int64(len(data))
			if rc.Progress != nil {
				rc.Progress(cur.info, cur.offset)
			}
		}
		switch end {
		case zcrcg:
		case zcrcq:
			if err := sendHex(posHeader(zack, cur.offset)); err != nil {
				return err
			}
		case zcrcw:
			return sendHex(posHeader(zack, cur.offset))
		case zcrce:
			return nil
		}
	}
}

// consumeOverAndOut 读掉发送方在 ZFIN 之后发的 "OO"。等的时间很短：读不到也不影响结果，
// 读到的其他字符留给终端显示。
func (r *reader) consumeOverAndOut() {
	r.timeout = 500 * time.Millisecond
	for range 2 {
		if len(r.buf) == 0 {
			ctx, cancel := context.WithTimeout(r.ctx, r.timeout)
			data, _ := r.stream.Recv(ctx)
			cancel()
			r.buf = append(r.buf, data...)
		}
		if len(r.buf) == 0 || r.buf[0] != 'O' {
			return
		}
		r.buf = r.buf[1:]
	}
}

// parseFileInfo 解析 ZFILE 的数据包："文件名\0大小 修改时间(八进制) 权限(八进制) ...\0"。
func parseFileInfo(data []byte) FileInfo {
	info := FileInfo{Size: -1}
	name, rest, _ := bytes.Cut(data, []byte{0})
	info.Name = SafeName(string(name))
	rest, _, _ = bytes.Cut(rest, []byte{0})
	fields := strings.Fields(string(rest))
	if len(fields) > 0 {
		if n, err := strconv.ParseInt(fields[0], 10, 64); err == nil && n >= 0 {
			info.Size = n
		}
	}
	if len(fields) > 1 {
		if t, err := strconv.ParseInt(fields[1], 8, 64); err == nil && t > 0 {
			info.ModTime = time.Unix(t, 0)
		}
	}
	if len(fields) > 2 {
		if m, err := strconv.ParseUint(fields[2], 8, 32); err == nil {
			info.Mode = fs.FileMode(m) & fs.ModePerm
		}
	}
	return info
}

// SafeName 把对端给的文件名变成可以安全放进所选文件夹的名字：只保留最后一段（防止
// "../../.ssh/authorized_keys" 这类名字写到别处），去掉控制字符。处理后为空的用“未命名文件”。
func SafeName(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return "未命名文件"
	}
	return name
}

// Cancel 让对端中止正在进行或等待中的传输（比如用户在选择文件时取消了）。
func Cancel(w io.Writer) error {
	_, err := w.Write(cancelSequence)
	return err
}
