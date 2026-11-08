package zmodem

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"time"
)

// File 是一个要发送的文件。Open 在开始发送时调用，返回的文件在发送完后关闭。
type File struct {
	FileInfo
	Open func() (io.ReadSeekCloser, error)
}

// Sender 把文件发给对端（远程 rz）。
type Sender struct {
	// Progress 报告当前文件已发送的字节数，可以为 nil。
	Progress func(FileInfo, int64)
	// Skipped 在对端跳过某个文件时调用（例如 rz 默认不覆盖已有文件），可以为 nil。
	Skipped func(FileInfo)
}

// blockSize 是每个数据包的大小，协议的标准值。
const blockSize = 1024

// maxRetries 是同一个帧重发的上限。
const maxRetries = 10

// duplicateWindow 内重复到达的初始化帧头当作对端之前堆积的重发，不作回应。对端真的没收到时，
// 要等它自己的超时（lrzsz 是 10 秒左右）才会再发，远长于这个时间。
const duplicateWindow = 2 * time.Second

// ErrRejected 表示对端拒绝了文件（ZFERR、ZABORT 等）。
var ErrRejected = errors.New("对端拒绝接收")

type headerResult struct {
	h   header
	err error
}

// Run 执行一次发送。prefix 是已经从连接里读到的、从起始信号（对端的 ZRINIT）开始的数据。
// 返回会话结束后多读到的数据，调用方应把它交还终端显示。
func (sd *Sender) Run(ctx context.Context, s Stream, prefix []byte, files []File) (leftover []byte, err error) {
	r := newReader(ctx, s, prefix)
	enc := &encoder{}
	write := func() error {
		_, err := s.Send.Write(enc.take())
		return err
	}

	// 对端的 ZRINIT 决定用 CRC32 还是 CRC16、要不要转义控制字符、接收缓冲区多大。
	init, err := sd.awaitInit(r, enc, write)
	if err != nil {
		if !errors.Is(err, errPeerCancelled) {
			s.Send.Write(cancelSequence)
		}
		return r.leftover(), err
	}

	// 之后由一个 goroutine 专门读帧头：发数据时要随时能收到对端的 ZRPOS。
	// 这个 goroutine 不设超时：对端在流式接收时可以很久不发任何东西。等应答时由 next() 计时。
	readCtx, stopReading := context.WithCancel(ctx)
	r.ctx = readCtx
	r.timeout = 0
	headers := make(chan headerResult, 16)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			h, _, err := r.readHeader()
			select {
			case headers <- headerResult{h, err}:
			case <-readCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		stopReading()
		<-readerDone
		if err != nil && !errors.Is(err, errPeerCancelled) {
			s.Send.Write(cancelSequence)
		}
		leftover = r.leftover()
	}()

	session := &sendSession{
		Sender:  sd,
		ctx:     ctx,
		enc:     enc,
		write:   write,
		headers: headers,
		use32:   init.flags()&canFC32 != 0,
		bufSize: int64(init.p[0]) | int64(init.p[1])<<8,
	}
	enc.escapeCtl = init.flags()&escCtl != 0

	var bytesLeft int64
	for _, f := range files {
		bytesLeft += max(f.Size, 0)
	}
	for i, f := range files {
		if err := session.sendFile(f, len(files)-i, bytesLeft); err != nil {
			return nil, err
		}
		bytesLeft -= max(f.Size, 0)
	}
	return nil, session.finish()
}

// awaitInit 等对端的 ZRINIT。prefix 里通常已经有了；没有时先发 ZRQINIT 请对端发。
func (sd *Sender) awaitInit(r *reader, enc *encoder, write func() error) (header, error) {
	if len(r.buf) == 0 {
		enc.hexHeader(header{kind: zrqinit})
		if err := write(); err != nil {
			return header{}, err
		}
	}
	for {
		h, _, err := r.readHeader()
		if err != nil {
			return header{}, err
		}
		switch h.kind {
		case zrinit:
			return h, nil
		case zabort, zferr, zfin:
			return header{}, errPeerCancelled
		case zcommand:
			return header{}, ErrCommandRefused
		}
	}
}

type sendSession struct {
	*Sender
	ctx     context.Context
	enc     *encoder
	write   func() error
	headers <-chan headerResult
	use32   bool
	bufSize int64 // 对端的接收缓冲区，0 表示可以一直流式发送
}

// next 等对端的下一个帧头，超过 idleTimeout 没有应答算超时。
func (ss *sendSession) next() (header, error) {
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	select {
	case res := <-ss.headers:
		return res.h, res.err
	case <-timer.C:
		return header{}, ErrTimeout
	case <-ss.ctx.Done():
		return header{}, ErrCancelled
	}
}

// poll 不等待地看一眼对端有没有发来帧头。
func (ss *sendSession) poll() (header, bool, error) {
	select {
	case res := <-ss.headers:
		return res.h, true, res.err
	default:
		return header{}, false, nil
	}
}

// sendFile 发送一个文件：ZFILE 协商，然后按对端要求的位置发数据，直到对端用 ZRINIT 确认收完。
func (ss *sendSession) sendFile(f File, filesLeft int, bytesLeft int64) error {
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	info := fmt.Sprintf("%s\x00%d %o %o 0 %d %d\x00", f.Name, max(f.Size, 0), f.ModTime.Unix(),
		0o100000|uint32(f.Mode.Perm()), filesLeft, bytesLeft)
	var sentAt time.Time
	sendZFILE := func() error {
		sentAt = time.Now()
		h := header{kind: zfile}
		h.p[3] = zcbin
		ss.enc.binHeader(h, ss.use32)
		ss.enc.subpacket([]byte(info), zcrcw, ss.use32)
		return ss.write()
	}
	if err := sendZFILE(); err != nil {
		return err
	}
	retries := 0
	for {
		h, err := ss.next()
		if err != nil {
			return err
		}
		switch h.kind {
		case zrinit, znak:
			// 对端没收到 ZFILE（或者校验错），重发。刚发出 ZFILE 就收到的 ZRINIT 是之前堆积的：
			// 对端在等我们开始时会每隔几秒重发 ZRINIT（例如用户还在选文件），这些不是对 ZFILE 的回答。
			if h.kind == zrinit && time.Since(sentAt) < duplicateWindow {
				continue
			}
			if retries++; retries > maxRetries {
				return ErrTimeout
			}
			if err := sendZFILE(); err != nil {
				return err
			}
		case zskip:
			if ss.Skipped != nil {
				ss.Skipped(f.FileInfo)
			}
			return nil
		case zcrc:
			// 对端想比较已有文件的 CRC（断点续传用），回复整个文件的 CRC32。
			crc, err := fileCRC(src)
			if err != nil {
				return err
			}
			h := posHeader(zcrc, int64(crc))
			ss.enc.binHeader(h, ss.use32)
			if err := ss.write(); err != nil {
				return err
			}
		case zrpos:
			// sendData 自己处理之后的重发请求，只在对端确认收完、跳过或者出错时返回。
			_, err := ss.sendData(f, src, h.pos())
			return err
		case zabort, zferr:
			return ErrRejected
		case zfin:
			return errPeerCancelled
		}
	}
}

// sendData 从 pos 开始发送数据，处理对端中途的 ZRPOS，最后发 ZEOF 并等确认。
// 返回 done 表示对端确认收完了这个文件。
func (ss *sendSession) sendData(f File, src io.ReadSeeker, pos int64) (done bool, err error) {
	buf := make([]byte, blockSize)
	retries := 0
restart:
	if _, err := src.Seek(pos, io.SeekStart); err != nil {
		return false, err
	}
	ss.enc.binHeader(posHeader(zdata, pos), ss.use32)
	if err := ss.write(); err != nil {
		return false, err
	}
	sinceAck := int64(0)
	for {
		// 发每个包之前看一眼对端有没有要求从别处重发。
		if h, ok, err := ss.poll(); err != nil {
			return false, err
		} else if ok {
			switch h.kind {
			case zrpos:
				if retries++; retries > maxRetries {
					return false, ErrTimeout
				}
				pos = h.pos()
				goto restart
			case zskip:
				if ss.Skipped != nil {
					ss.Skipped(f.FileInfo)
				}
				return true, nil
			case zabort, zferr:
				return false, ErrRejected
			case zfin:
				return false, errPeerCancelled
			}
		}
		n, rerr := io.ReadFull(src, buf)
		if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
			return false, rerr
		}
		atEnd := rerr != nil
		end := byte(zcrcg)
		switch {
		case atEnd:
			end = zcrce
		case ss.bufSize > 0 && sinceAck+int64(n) >= ss.bufSize:
			end = zcrcw // 对端缓冲区满了，等它确认
		}
		ss.enc.subpacket(buf[:n], end, ss.use32)
		if err := ss.write(); err != nil {
			return false, err
		}
		pos += int64(n)
		sinceAck += int64(n)
		if ss.Progress != nil {
			ss.Progress(f.FileInfo, pos)
		}
		if atEnd {
			break
		}
		if end == zcrcw {
			if err := ss.awaitAck(); err != nil {
				return false, err
			}
			sinceAck = 0
		}
	}

	ss.enc.binHeader(posHeader(zeof, pos), ss.use32)
	if err := ss.write(); err != nil {
		return false, err
	}
	for {
		h, err := ss.next()
		if err != nil {
			return false, err
		}
		switch h.kind {
		case zrinit:
			return true, nil
		case zrpos:
			if retries++; retries > maxRetries {
				return false, ErrTimeout
			}
			pos = h.pos()
			goto restart
		case zack:
			// 迟到的确认，继续等
		case zskip:
			if ss.Skipped != nil {
				ss.Skipped(f.FileInfo)
			}
			return true, nil
		case zabort, zferr:
			return false, ErrRejected
		case zfin:
			return false, errPeerCancelled
		}
	}
}

// awaitAck 等对端对 ZCRCW 数据包的 ZACK。
func (ss *sendSession) awaitAck() error {
	for {
		h, err := ss.next()
		if err != nil {
			return err
		}
		switch h.kind {
		case zack:
			return nil
		case zabort, zferr:
			return ErrRejected
		case zfin:
			return errPeerCancelled
		}
	}
}

// finish 结束会话：发 ZFIN，等对端的 ZFIN，再发 "OO"。
func (ss *sendSession) finish() error {
	for range 3 {
		ss.enc.hexHeader(header{kind: zfin})
		if err := ss.write(); err != nil {
			return err
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case res := <-ss.headers:
			timer.Stop()
			if res.err != nil {
				return res.err
			}
			if res.h.kind == zfin {
				ss.enc.buf = append(ss.enc.buf, 'O', 'O')
				return ss.write()
			}
		case <-timer.C:
		case <-ss.ctx.Done():
			timer.Stop()
			return ErrCancelled
		}
	}
	// 文件都已经确认收到了，对端只是没回 ZFIN：不算失败。
	return nil
}

func fileCRC(src io.ReadSeeker) (uint32, error) {
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	h := crc32.NewIEEE()
	if _, err := io.Copy(h, src); err != nil {
		return 0, err
	}
	return h.Sum32(), nil
}
