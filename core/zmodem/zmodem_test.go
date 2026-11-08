package zmodem

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCRC16(t *testing.T) {
	// CRC-16/XMODEM 的标准校验值。
	if got := crc16(0, []byte("123456789")...); got != 0x31c3 {
		t.Fatalf("crc16 = %#x，期望 0x31c3", got)
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	for _, h := range []header{
		{kind: zrinit, p: [4]byte{0, 0, 0, canFDX | canOVIO | canFC32}},
		posHeader(zrpos, 0x12345678),
		posHeader(zdata, 0x18), // 含需要转义的字节
		{kind: zfile, p: [4]byte{0x11, 0x13, 0x18, zcbin}},
	} {
		for _, format := range []string{"hex", "bin16", "bin32"} {
			enc := &encoder{}
			switch format {
			case "hex":
				enc.hexHeader(h)
			case "bin16":
				enc.binHeader(h, false)
			case "bin32":
				enc.binHeader(h, true)
			}
			data := append([]byte("rz waiting to receive."), enc.take()...)
			r := newReader(context.Background(), Stream{Recv: eofRecv}, data)
			got, use32, err := r.readHeader()
			if err != nil || got != h || use32 != (format == "bin32") {
				t.Errorf("%s %+v: 解出 %+v use32=%v err=%v", format, h, got, use32, err)
			}
		}
	}
}

func eofRecv(context.Context) ([]byte, error) { return nil, io.EOF }

func TestDetectorFindsSignatureAcrossChunks(t *testing.T) {
	enc := &encoder{}
	enc.hexHeader(receiverInit)
	frame := enc.take()
	out := append([]byte("rz waiting to receive."), frame...)
	for split := 1; split < len(out); split++ {
		var d Detector
		var shown []byte
		var kind Kind
		var session []byte
		for _, chunk := range [][]byte{out[:split], out[split:]} {
			if kind != 0 {
				session = append(session, chunk...)
				continue
			}
			k, show, s := d.Scan(chunk)
			shown = append(shown, chunk[:show]...)
			if k != 0 {
				kind, session = k, s
			}
		}
		if kind != Upload || !bytes.HasPrefix(session, sigUpload) || !bytes.HasSuffix(out, session) {
			t.Fatalf("在第 %d 字节处拆开时没认出 rz: kind=%v session=%q", split, kind, session)
		}
		if !bytes.HasPrefix(shown, []byte("rz waiting")) && split > 10 {
			t.Fatalf("信号前的文字应显示: %q", shown)
		}
	}
	var d Detector
	if k, show, _ := d.Scan([]byte("普通输出 ** 带星号\x18")); k != 0 || show != len("普通输出 ** 带星号\x18") {
		t.Fatalf("普通输出不应识别成 ZMODEM: kind=%v show=%d", k, show)
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"report.txt":                 "report.txt",
		"../../.ssh/authorized_keys": "authorized_keys",
		`..\..\windows\evil.exe`:     "evil.exe",
		"/etc/passwd":                "passwd",
		"..":                         "未命名文件",
		"":                           "未命名文件",
		"a\x1b[31mb":                 "a[31mb",
		".bashrc":                    ".bashrc",
	} {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// memSink 把收到的文件放在内存里。
type memSink struct {
	mu        sync.Mutex
	files     map[string][]byte
	committed []string
	aborted   int
}

type memFile struct {
	s    *memSink
	name string
	buf  bytes.Buffer
}

func (f *memFile) Write(p []byte) (int, error) { return f.buf.Write(p) }
func (f *memFile) Commit() error {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	f.s.files[f.name] = f.buf.Bytes()
	f.s.committed = append(f.s.committed, f.name)
	return nil
}
func (f *memFile) Abort() { f.s.mu.Lock(); f.s.aborted++; f.s.mu.Unlock() }

func (s *memSink) receiver() *Receiver {
	return &Receiver{Accept: func(info FileInfo) (Sink, error) { return &memFile{s: s, name: info.Name}, nil }}
}

func randomBytes(t *testing.T, n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func memFiles(contents map[string][]byte, order []string) []File {
	var files []File
	for _, name := range order {
		data := contents[name]
		files = append(files, File{
			FileInfo: FileInfo{Name: name, Size: int64(len(data)), ModTime: time.Unix(1700000000, 0), Mode: 0o644},
			Open:     func() (io.ReadSeekCloser, error) { return nopCloser{bytes.NewReader(data)}, nil },
		})
	}
	return files
}

type nopCloser struct{ io.ReadSeeker }

func (nopCloser) Close() error { return nil }

// link 连接本包的发送端和接收端。tamper 可以在数据从发送端流向接收端时修改它，模拟线路错误。
func link(tamper func([]byte) []byte) (sender, receiver Stream) {
	toRecv := make(chan []byte, 1024)
	toSend := make(chan []byte, 1024)
	recvFrom := func(ch chan []byte) func(context.Context) ([]byte, error) {
		return func(ctx context.Context) ([]byte, error) {
			select {
			case b, ok := <-ch:
				if !ok {
					return nil, io.EOF
				}
				return b, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	sender = Stream{Recv: recvFrom(toSend), Send: chanWriter{toRecv, tamper}}
	receiver = Stream{Recv: recvFrom(toRecv), Send: chanWriter{toSend, nil}}
	return
}

type chanWriter struct {
	ch     chan []byte
	tamper func([]byte) []byte
}

func (w chanWriter) Write(p []byte) (int, error) {
	b := append([]byte(nil), p...)
	if w.tamper != nil {
		b = w.tamper(b)
	}
	w.ch <- b
	return len(p), nil
}

// runPair 让本包的发送端把 files 发给接收端，返回双方的错误。
func runPair(t *testing.T, files []File, sink *memSink, tamper func([]byte) []byte) (sendErr, recvErr error) {
	t.Helper()
	sStream, rStream := link(tamper)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, recvErr = sink.receiver().Run(ctx, rStream, nil)
	}()
	// 接收端先发 ZRINIT，发送端从它开始（和终端里检测到 rz 时一样）。
	_, sendErr = (&Sender{}).Run(ctx, sStream, nil, files)
	wg.Wait()
	return
}

func TestSendReceiveRoundTrip(t *testing.T) {
	all := make([]byte, 256*4)
	for i := range all {
		all[i] = byte(i) // 每种字节值都出现，包括需要转义的
	}
	contents := map[string][]byte{
		"empty":    nil,
		"one":      {0x18},
		"exact":    randomBytes(t, blockSize),
		"over":     randomBytes(t, blockSize+1),
		"allbytes": all,
		"big.bin":  randomBytes(t, 300*1024+7),
	}
	order := []string{"empty", "one", "exact", "over", "allbytes", "big.bin"}
	sink := &memSink{files: map[string][]byte{}}
	sendErr, recvErr := runPair(t, memFiles(contents, order), sink, nil)
	if sendErr != nil || recvErr != nil {
		t.Fatalf("send=%v recv=%v", sendErr, recvErr)
	}
	for _, name := range order {
		if !bytes.Equal(sink.files[name], contents[name]) {
			t.Errorf("%s 内容不一致: %d 字节，期望 %d", name, len(sink.files[name]), len(contents[name]))
		}
	}
}

// 线路上翻转一个字节：接收端发现校验错，要求重发，最终文件完全一致。
func TestCorruptedDataIsResent(t *testing.T) {
	data := randomBytes(t, 64*1024)
	var flipped atomic.Bool
	var seen atomic.Int64
	tamper := func(b []byte) []byte {
		start := seen.Add(int64(len(b))) - int64(len(b))
		if !flipped.Load() && start > 20*1024 && len(b) > 100 {
			flipped.Store(true)
			b[50] ^= 0x01
		}
		return b
	}
	sink := &memSink{files: map[string][]byte{}}
	sendErr, recvErr := runPair(t, memFiles(map[string][]byte{"data": data}, []string{"data"}), sink, tamper)
	if sendErr != nil || recvErr != nil {
		t.Fatalf("send=%v recv=%v", sendErr, recvErr)
	}
	if !flipped.Load() {
		t.Fatal("测试没有翻转任何字节")
	}
	if !bytes.Equal(sink.files["data"], data) {
		t.Fatalf("重发后内容仍不一致: %d 字节", len(sink.files["data"]))
	}
}

func TestReceiverSkipAndCancel(t *testing.T) {
	contents := map[string][]byte{"a": []byte("aaa"), "b": []byte("bbb")}
	sink := &memSink{files: map[string][]byte{}}
	sStream, rStream := link(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rc := &Receiver{Accept: func(info FileInfo) (Sink, error) {
		if info.Name == "a" {
			return nil, nil // 跳过
		}
		return &memFile{s: sink, name: info.Name}, nil
	}}
	var skipped []string
	done := make(chan error, 1)
	go func() { _, err := rc.Run(ctx, rStream, nil); done <- err }()
	_, err := (&Sender{Skipped: func(f FileInfo) { skipped = append(skipped, f.Name) }}).Run(ctx, sStream, nil, memFiles(contents, []string{"a", "b"}))
	if err != nil || <-done != nil {
		t.Fatalf("send=%v", err)
	}
	if len(skipped) != 1 || skipped[0] != "a" || string(sink.files["b"]) != "bbb" || sink.files["a"] != nil {
		t.Fatalf("skipped=%v files=%v", skipped, sink.files)
	}

	// 本端取消：接收端返回 ErrCancelled，并给对端发取消序列，对端随之停止。
	sStream, rStream = link(nil)
	ctx2, cancel2 := context.WithCancel(context.Background())
	rc = &Receiver{Accept: func(FileInfo) (Sink, error) {
		cancel2()
		return &memFile{s: sink, name: "x"}, nil
	}}
	go func() { _, err := rc.Run(ctx2, rStream, nil); done <- err }()
	_, sendErr := (&Sender{}).Run(ctx, sStream, nil, memFiles(map[string][]byte{"c": randomBytes(t, 50000)}, []string{"c"}))
	if err := <-done; !errors.Is(err, ErrCancelled) {
		t.Fatalf("接收端取消应返回 ErrCancelled，得到 %v", err)
	}
	if !errors.Is(sendErr, ErrCancelled) {
		t.Fatalf("发送端应看到对端取消，得到 %v", sendErr)
	}
}

// --- 与真实 lrzsz 互通。没有安装 lrzsz 时跳过（brew install lrzsz / apt install lrzsz）。

func lrzsz(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("没有安装 lrzsz（找不到 %s），跳过互通测试", name)
	}
	return p
}

// 远程 sz 发文件，本包接收。
func TestReceiveFromLrzszSz(t *testing.T) {
	sz := lrzsz(t, "sz")
	dir := t.TempDir()
	contents := map[string][]byte{"hello.txt": []byte("你好，Serval\n"), "blob.bin": randomBytes(t, 200*1024+3)}
	var args []string
	for name, data := range contents {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o640); err != nil {
			t.Fatal(err)
		}
		args = append(args, name)
	}
	cmd := exec.Command(sz, args...)
	cmd.Dir = dir
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	sink := &memSink{files: map[string][]byte{}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream := ReaderStream(stdout, stdin)
	// 和终端里一样：先在输出里找到起始信号，再把信号开始的数据交给会话。
	prefix := detect(t, ctx, stream, Download)
	if _, err := sink.receiver().Run(ctx, stream, prefix); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("sz 退出异常: %v", err)
	}
	for name, data := range contents {
		if !bytes.Equal(sink.files[name], data) {
			t.Errorf("%s 内容不一致", name)
		}
	}
}

// 远程 rz 等着收，本包发送。
func TestSendToLrzszRz(t *testing.T) {
	rz := lrzsz(t, "rz")
	dir := t.TempDir()
	contents := map[string][]byte{"up.txt": []byte("upload\n"), "big.bin": randomBytes(t, 300*1024+11)}
	cmd := exec.Command(rz)
	cmd.Dir = dir
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream := ReaderStream(stdout, stdin)
	prefix := detect(t, ctx, stream, Upload)
	var skipped []string
	sd := &Sender{Skipped: func(f FileInfo) { skipped = append(skipped, f.Name) }}
	if _, err := sd.Run(ctx, stream, prefix, memFiles(contents, []string{"up.txt", "big.bin"})); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("rz 退出异常: %v", err)
	}
	for name, data := range contents {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || sha256.Sum256(got) != sha256.Sum256(data) {
			t.Errorf("%s 内容不一致: err=%v", name, err)
		}
	}
	if len(skipped) != 0 {
		t.Errorf("不应跳过: %v", skipped)
	}

	// rz 默认不覆盖已有文件：再发一次同名文件会被跳过，并通知调用方。
	cmd = exec.Command(rz)
	cmd.Dir = dir
	stdin, _ = cmd.StdinPipe()
	stdout, _ = cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	stream = ReaderStream(stdout, stdin)
	prefix = detect(t, ctx, stream, Upload)
	if _, err := sd.Run(ctx, stream, prefix, memFiles(contents, []string{"up.txt"})); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	if len(skipped) != 1 || skipped[0] != "up.txt" {
		t.Fatalf("已有文件应被跳过并通知: %v", skipped)
	}
}

// detect 从输出里找起始信号，返回信号开始的数据。
func detect(t *testing.T, ctx context.Context, s Stream, want Kind) []byte {
	t.Helper()
	var d Detector
	for {
		chunk, err := s.Recv(ctx)
		if err != nil {
			t.Fatalf("没有等到起始信号: %v", err)
		}
		if k, _, session := d.Scan(chunk); k != 0 {
			if k != want {
				t.Fatalf("识别为 %v，期望 %v", k, want)
			}
			return session
		}
	}
}

// 终端里的真实情况：检测到 rz/sz 后要等用户选文件或文件夹。
// 上传：会话要等用户选好文件才开始，这期间 rz 每 10 秒重发一次 ZRINIT，堆积的帧头不能打乱传输。
// 下载：会话立即开始，在第一个文件到达时等用户选保存位置。
func TestSlowUserWithLrzsz(t *testing.T) {
	if testing.Short() {
		t.Skip("要等 26 秒")
	}
	rz, sz := lrzsz(t, "rz"), lrzsz(t, "sz")
	wait := func(ctx context.Context, s Stream, prefix []byte) []byte {
		// 像终端那样在用户选择期间继续收数据，全部交给会话。
		deadline := time.Now().Add(11 * time.Second)
		for time.Now().Before(deadline) {
			c, cancel := context.WithDeadline(ctx, deadline)
			chunk, err := s.Recv(c)
			cancel()
			prefix = append(prefix, chunk...)
			if err != nil && c.Err() == nil {
				t.Fatalf("等待期间连接出错: %v", err)
			}
		}
		// 确认真的堆积了重复的初始化帧头，否则这个测试没有测到要测的情况。
		if n := bytes.Count(prefix, sigUpload) + bytes.Count(prefix, sigDownload); n < 2 {
			t.Fatalf("等待期间只收到 %d 个初始化帧头，没有构造出堆积的情况", n)
		}
		t.Logf("等待期间堆积了 %d 个初始化帧头", bytes.Count(prefix, sigUpload)+bytes.Count(prefix, sigDownload))
		return prefix
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	t.Run("rz", func(t *testing.T) {
		dir := t.TempDir()
		cmd := exec.Command(rz)
		cmd.Dir = dir
		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer cmd.Process.Kill()
		stream := ReaderStream(stdout, stdin)
		prefix := wait(ctx, stream, detect(t, ctx, stream, Upload))
		data := randomBytes(t, 100*1024)
		if _, err := (&Sender{}).Run(ctx, stream, prefix, memFiles(map[string][]byte{"slow.bin": data}, []string{"slow.bin"})); err != nil {
			t.Fatal(err)
		}
		cmd.Wait()
		if got, _ := os.ReadFile(filepath.Join(dir, "slow.bin")); !bytes.Equal(got, data) {
			t.Fatalf("内容不一致: %d 字节", len(got))
		}
	})
	t.Run("sz", func(t *testing.T) {
		dir := t.TempDir()
		data := randomBytes(t, 100*1024)
		os.WriteFile(filepath.Join(dir, "slow.bin"), data, 0o644)
		cmd := exec.Command(sz, "slow.bin")
		cmd.Dir = dir
		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer cmd.Process.Kill()
		stream := ReaderStream(stdout, stdin)
		// 下载时会话立即开始（sz 30 秒收不到回应就会退出），等第一个文件到了再问保存位置。
		// sz 发出 ZFILE 后会等我们的 ZRPOS，实测 45 秒以上都没问题。
		prefix := detect(t, ctx, stream, Download)
		sink := &memSink{files: map[string][]byte{}}
		rc := sink.receiver()
		accept := rc.Accept
		rc.Accept = func(info FileInfo) (Sink, error) {
			time.Sleep(15 * time.Second) // 用户在选保存位置
			return accept(info)
		}
		if _, err := rc.Run(ctx, stream, prefix); err != nil {
			t.Fatal(err)
		}
		cmd.Wait()
		if !bytes.Equal(sink.files["slow.bin"], data) {
			t.Fatalf("内容不一致: %d 字节", len(sink.files["slow.bin"]))
		}
	})
}

// Accept 返回 ErrAbandon 时，会话结束且不向对端写任何东西（对端可能已经退出，写下去的会进 shell）。
func TestAbandonWritesNothing(t *testing.T) {
	sStream, rStream := link(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var written atomic.Int64
	counting := rStream
	counting.Send = writerFunc(func(p []byte) (int, error) {
		written.Add(int64(len(p)))
		return rStream.Send.Write(p)
	})
	rc := &Receiver{Accept: func(FileInfo) (Sink, error) {
		written.Store(0) // 只关心 Accept 之后写了什么
		return nil, ErrAbandon
	}}
	done := make(chan error, 1)
	go func() { _, err := rc.Run(ctx, counting, nil); done <- err }()
	sendCtx, stopSend := context.WithCancel(ctx)
	go (&Sender{}).Run(sendCtx, sStream, nil, memFiles(map[string][]byte{"a": []byte("x")}, []string{"a"}))
	if err := <-done; !errors.Is(err, ErrAbandon) {
		t.Fatalf("应返回 ErrAbandon，得到 %v", err)
	}
	stopSend()
	if n := written.Load(); n != 0 {
		t.Fatalf("放弃后不应再写任何东西，写了 %d 字节", n)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
