package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"serval/core/zmodem"
	"serval/design"
	"serval/locales"

	"github.com/egoist/mygo/ui"
)

// 终端里的 rz（上传）和 sz（下载）。
//
// zmodemConn 位于 SSH shell 和终端之间，作为终端的 Conn。一个 goroutine 统一读 shell：平时把数据
// 交给终端显示；发现 ZMODEM 起始信号后，之后的数据交给传输会话，会话结束时把多读到的数据还给终端。
// 只有这一个 goroutine 读 shell，所以传输结束后不会有卡在 Read 里的 goroutine 抢走终端输出。

// lrzsz 的等待时间（实测 lrzsz 0.13.1）：rz 每 10 秒重发一次 ZRINIT，约 50 秒后退出；sz 发出文件信息后
// 最多等 60 秒。超过这些时间对端多半已经退出，再写数据会被当成键盘输入交给 shell，所以留出余量后放弃。
const (
	zmodemUploadPatience   = 45 * time.Second
	zmodemDownloadPatience = 50 * time.Second
)

// maxZmodemQueue 是会话来不及处理时最多缓存的数据，超过后暂停读取 shell（SSH 的流控会让对端等）。
const maxZmodemQueue = 4 << 20

// shellConn 是 zmodemConn 需要的 shell 能力，*sshconn.Shell 满足它。
type shellConn interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

type zmodemConn struct {
	shell shellConn
	pr    *io.PipeReader
	pw    *io.PipeWriter
	// onRequest 在读取 goroutine 上调用，应该尽快返回（投递到主线程）。
	onRequest func(*zmodemSession)

	// wmu 保证写终端的顺序（读取 goroutine 平时写、会话结束时交还数据），持有期间可能阻塞在终端读取上。
	// mu 只保护会话状态，持有期间绝不做阻塞的写：终端在读输出时会把应答（比如光标位置）写回 Conn，
	// 如果写终端时持有 mu，终端的应答又要拿 mu，两边就会互相等待。
	wmu sync.Mutex

	mu      sync.Mutex
	space   *sync.Cond // 队列有空间了
	det     zmodem.Detector
	active  *zmodemSession
	readErr error
}

// zmodemSession 是一次 rz/sz 请求。
type zmodemSession struct {
	kind    zmodem.Kind
	prefix  []byte // 从起始信号开始的数据
	started time.Time

	// 以下字段由 zmodemConn.mu 保护。
	queue  [][]byte
	queued int
	notify chan struct{}
	cancel context.CancelFunc
}

func newZmodemConn(shell shellConn, onRequest func(*zmodemSession)) *zmodemConn {
	pr, pw := io.Pipe()
	z := &zmodemConn{shell: shell, pr: pr, pw: pw, onRequest: onRequest}
	z.space = sync.NewCond(&z.mu)
	go z.pump()
	return z
}

func (z *zmodemConn) Read(p []byte) (int, error) { return z.pr.Read(p) }

// Write 把键盘输入发给 shell。传输期间不发：输入会打乱协议数据；按 Ctrl+C 取消传输。
func (z *zmodemConn) Write(p []byte) (int, error) {
	z.mu.Lock()
	s := z.active
	var cancel context.CancelFunc
	if s != nil {
		cancel = s.cancel
	}
	z.mu.Unlock()
	if s == nil {
		return z.shell.Write(p)
	}
	if cancel != nil && bytes.IndexByte(p, 0x03) >= 0 {
		cancel()
	}
	return len(p), nil
}

func (z *zmodemConn) Resize(cols, rows int) error { return z.shell.Resize(cols, rows) }
func (z *zmodemConn) Close() error                { return z.shell.Close() }

func (z *zmodemConn) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := z.shell.Read(buf)
		if n > 0 {
			z.deliver(append([]byte(nil), buf[:n]...))
		}
		if err != nil {
			z.mu.Lock()
			z.readErr = err
			if z.active != nil {
				signal(z.active.notify)
			}
			z.mu.Unlock()
			z.pw.CloseWithError(err)
			return
		}
	}
}

func (z *zmodemConn) deliver(chunk []byte) {
	z.mu.Lock()
	for z.active != nil && z.active.queued > maxZmodemQueue {
		z.space.Wait()
	}
	if s := z.active; s != nil {
		s.queue = append(s.queue, chunk)
		s.queued += len(chunk)
		signal(s.notify)
		z.mu.Unlock()
		return
	}
	kind, show, prefix := z.det.Scan(chunk)
	if kind == 0 {
		z.mu.Unlock()
		z.writeTerminal(chunk)
		return
	}
	s := &zmodemSession{kind: kind, prefix: prefix, started: time.Now(), notify: make(chan struct{}, 1)}
	z.active = s
	z.mu.Unlock()
	if show > 0 {
		z.writeTerminal(chunk[:show])
	}
	z.onRequest(s)
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// stream 返回会话的两端：从队列里取对端数据，直接写 shell。
func (z *zmodemConn) stream(s *zmodemSession) zmodem.Stream {
	return zmodem.Stream{
		Send: z.shell,
		Recv: func(ctx context.Context) ([]byte, error) {
			for {
				if err := ctx.Err(); err != nil {
					return nil, err // 先看 ctx：已取消的会话不再拿走数据，留给终端
				}
				z.mu.Lock()
				if len(s.queue) > 0 {
					chunk := s.queue[0]
					s.queue = s.queue[1:]
					s.queued -= len(chunk)
					z.space.Broadcast()
					z.mu.Unlock()
					return chunk, nil
				}
				err := z.readErr
				z.mu.Unlock()
				if err != nil {
					return nil, err
				}
				select {
				case <-s.notify:
				case <-ctx.Done():
				}
			}
		},
	}
}

// transferring 报告是否有 rz/sz 会话在进行。
func (z *zmodemConn) transferring() bool {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.active != nil
}

// setCancel 记下取消函数，键盘上的 Ctrl+C 会调用它。
func (z *zmodemConn) setCancel(s *zmodemSession, cancel context.CancelFunc) {
	z.mu.Lock()
	s.cancel = cancel
	z.mu.Unlock()
}

func (z *zmodemConn) writeTerminal(b []byte) {
	z.wmu.Lock()
	defer z.wmu.Unlock()
	z.pw.Write(b)
}

// finish 结束会话，把会话多读到的数据和还没交给会话的数据按顺序还给终端。
func (z *zmodemConn) finish(s *zmodemSession, leftover []byte) {
	z.mu.Lock()
	if z.active != s {
		z.mu.Unlock()
		return
	}
	rest := s.queue
	s.queue = nil
	z.active = nil
	z.det.Reset()
	z.space.Broadcast()
	// 先拿 wmu 再放 mu：读取 goroutine 之后到的数据要等这些交还的数据写完才能写，顺序不会乱；
	// 而键盘输入（只拿 mu）不会被写终端卡住。
	z.wmu.Lock()
	z.mu.Unlock()
	defer z.wmu.Unlock()
	if len(leftover) > 0 {
		z.pw.Write(leftover)
	}
	for _, chunk := range rest {
		z.pw.Write(chunk)
	}
}

// zmodemTransfer 是窗格上正在进行的 rz/sz，供界面显示进度。进度字段在传输 goroutine 里写、
// 界面上读，用原子操作。
type zmodemTransfer struct {
	kind   zmodem.Kind
	name   atomic.Pointer[string]
	done   atomic.Int64
	total  atomic.Int64
	cancel context.CancelFunc
}

func (tr *zmodemTransfer) progress(info zmodem.FileInfo, done int64) {
	if cur := tr.name.Load(); cur == nil || *cur != info.Name {
		name := info.Name
		tr.name.Store(&name)
		tr.total.Store(info.Size)
	}
	tr.done.Store(done)
}

// errZmodemDeclined 表示用户在选择框里取消了。
var errZmodemDeclined = errors.New("没有选择")

// startZmodem 在主线程上处理一次 rz/sz 请求。
func (a *app) startZmodem(t *tab, p *pane, z *zmodemConn, s *zmodemSession) {
	if !slices.Contains(a.tabs, t) || p.zconn != z {
		z.finish(s, nil)
		return
	}
	if s.kind == zmodem.Upload {
		a.startZmodemUpload(t, p, z, s)
	} else {
		a.startZmodemDownload(t, p, z, s)
	}
}

func (a *app) startZmodemUpload(t *tab, p *pane, z *zmodemConn, s *zmodemSession) {
	paths, err := a.chooseUploadFiles()
	if err == nil && len(paths) == 0 {
		err = errZmodemDeclined
	}
	if time.Since(s.started) > zmodemUploadPatience {
		// rz 多半已经退出：不写任何东西，否则 ZMODEM 数据会被当成命令交给 shell。
		z.finish(s, nil)
		a.toast(ui.Toast{Title: locales.ZmRzNotWaiting(), Description: locales.ZmRzWaitHint(), Type: "error"})
		return
	}
	var files []zmodem.File
	var folders []string
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil {
			err = statErr
			break
		}
		if info.IsDir() {
			folders = append(folders, info.Name())
			continue
		}
		path := path
		files = append(files, zmodem.File{
			FileInfo: zmodem.FileInfo{Name: info.Name(), Size: info.Size(), ModTime: info.ModTime(), Mode: info.Mode()},
			Open:     func() (io.ReadSeekCloser, error) { return os.Open(path) },
		})
	}
	if len(folders) > 0 {
		a.toast(ui.Toast{Title: locales.ZmRzNoFolders(), Description: locales.ZmSkippedNames(strings.Join(folders, locales.FileListSeparator())), Type: "error"})
	}
	if err != nil || len(files) == 0 {
		// 告诉 rz 不传了，让它退出，回到命令提示符。
		zmodem.Cancel(z.shell)
		z.finish(s, nil)
		if err != nil && !errors.Is(err, errZmodemDeclined) {
			a.toast(ui.Toast{Title: locales.FileCannotUpload(), Description: err.Error(), Type: "error"})
		}
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	tr := &zmodemTransfer{kind: zmodem.Upload, cancel: cancel}
	p.zmodem = tr
	z.setCancel(s, cancel)
	var skipped []string
	var skippedMu sync.Mutex
	sender := &zmodem.Sender{
		Progress: tr.progress,
		Skipped: func(f zmodem.FileInfo) {
			skippedMu.Lock()
			skipped = append(skipped, f.Name)
			skippedMu.Unlock()
		},
	}
	go func() {
		leftover, err := sender.Run(ctx, z.stream(s), s.prefix, files)
		cancel()
		z.finish(s, leftover)
		a.post(func() {
			if p.zmodem == tr {
				p.zmodem = nil
			}
			if !slices.Contains(a.tabs, t) {
				return // 标签已经关了，连接随之断开，不用再提示结果
			}
			skippedMu.Lock()
			defer skippedMu.Unlock()
			a.zmodemFinished(zmodem.Upload, len(files)-len(skipped), skipped, "", err)
		})
	}()
}

func (a *app) startZmodemDownload(t *tab, p *pane, z *zmodemConn, s *zmodemSession) {
	ctx, cancel := context.WithCancel(context.Background())
	tr := &zmodemTransfer{kind: zmodem.Download, cancel: cancel}
	p.zmodem = tr
	z.setCancel(s, cancel)
	var (
		dir   string
		saved []string
	)
	receiver := &zmodem.Receiver{
		Progress: tr.progress,
		Accept: func(info zmodem.FileInfo) (zmodem.Sink, error) {
			if dir == "" {
				// 第一个文件到了才问保存位置：sz 在发出文件信息后会等我们，而开始前只等 30 秒。
				asked := time.Now()
				chosen, err := a.askOnMain(ctx, a.chooseFolder)
				if err != nil {
					return nil, err
				}
				if time.Since(asked) > zmodemDownloadPatience {
					return nil, zmodem.ErrAbandon
				}
				if chosen == "" {
					return nil, errZmodemDeclined
				}
				dir = chosen
			}
			tr.progress(info, 0)
			return newLocalSink(dir, info, func(path string) { saved = append(saved, path) })
		},
	}
	go func() {
		leftover, err := receiver.Run(ctx, z.stream(s), s.prefix)
		cancel()
		z.finish(s, leftover)
		a.post(func() {
			if p.zmodem == tr {
				p.zmodem = nil
			}
			if !slices.Contains(a.tabs, t) {
				return // 标签已经关了，连接随之断开，不用再提示结果
			}
			a.zmodemFinished(zmodem.Download, len(saved), nil, dir, err)
		})
	}()
}

// askOnMain 在主线程上调用 choose（系统对话框只能在主线程打开），等它返回。
func (a *app) askOnMain(ctx context.Context, choose func() (string, error)) (string, error) {
	type result struct {
		path string
		err  error
	}
	ch := make(chan result, 1)
	a.post(func() {
		path, err := choose()
		ch <- result{path, err}
	})
	select {
	case r := <-ch:
		return r.path, r.err
	case <-ctx.Done():
		return "", zmodem.ErrCancelled
	}
}

// zmodemFinished 告诉用户传输的结果。
func (a *app) zmodemFinished(kind zmodem.Kind, count int, skipped []string, dir string, err error) {
	switch {
	case err == nil:
		if count > 0 {
			desc := ""
			if dir != "" {
				desc = locales.ZmSavedIn(shortPath(dir))
			}
			title := locales.ZmUploadedCount(count)
			if kind == zmodem.Download {
				title = locales.ZmReceivedCount(count)
			}
			a.toast(ui.Toast{Title: title, Description: desc})
		}
		if len(skipped) > 0 {
			a.toast(ui.Toast{
				Title:       locales.ZmSkippedExistingTitle(),
				Description: locales.ZmSkippedExistingHint(strings.Join(skipped, locales.FileListSeparator())),
				Type:        "error",
			})
		}
	case errors.Is(err, errZmodemDeclined):
		// 用户在选择框里取消了，不用提示
	case errors.Is(err, zmodem.ErrAbandon):
		a.toast(ui.Toast{Title: locales.ZmSzNotWaiting(), Description: locales.ZmSzWaitHint(), Type: "error"})
	case errors.Is(err, zmodem.ErrCommandRefused):
		a.toast(ui.Toast{Title: locales.ZmCommandRefused(), Description: locales.ZmCommandRefusedHint(), Type: "error"})
	case errors.Is(err, zmodem.ErrCancelled):
		title := locales.ZmUploadCancelled()
		if kind == zmodem.Download {
			title = locales.ZmReceiveCancelled()
		}
		a.toast(ui.Toast{Title: title})
	case errors.Is(err, zmodem.ErrTimeout):
		title := locales.ZmUploadFailed()
		if kind == zmodem.Download {
			title = locales.ZmReceiveFailed()
		}
		a.toast(ui.Toast{Title: title, Description: locales.ZmTimeout(), Type: "error"})
	default:
		title := locales.ZmUploadFailed()
		if kind == zmodem.Download {
			title = locales.ZmReceiveFailed()
		}
		a.toast(ui.Toast{Title: title, Description: err.Error(), Type: "error"})
	}
}

// localSink 把收到的文件先写进同目录的临时文件，收完再改成正式名字；同名文件不覆盖，自动编号。
type localSink struct {
	f       *os.File
	dir     string
	name    string
	modTime time.Time
	onSaved func(path string)
}

func newLocalSink(dir string, info zmodem.FileInfo, onSaved func(string)) (zmodem.Sink, error) {
	f, err := os.CreateTemp(dir, "."+info.Name+".*.part")
	if err != nil {
		return nil, err
	}
	return &localSink{f: f, dir: dir, name: info.Name, modTime: info.ModTime, onSaved: onSaved}, nil
}

func (s *localSink) Write(p []byte) (int, error) { return s.f.Write(p) }

func (s *localSink) Commit() error {
	tmp := s.f.Name()
	if err := s.f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	defer os.Remove(tmp)
	if !s.modTime.IsZero() {
		os.Chtimes(tmp, s.modTime, s.modTime)
	}
	// 用硬链接放到正式名字：目标已存在时失败而不是覆盖，换下一个编号再试。
	ext := filepath.Ext(s.name)
	base := strings.TrimSuffix(s.name, ext)
	for i := 0; i < 1000; i++ {
		name := s.name
		if i > 0 {
			name = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		final := filepath.Join(s.dir, name)
		err := os.Link(tmp, final)
		if err == nil {
			s.onSaved(final)
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	return fmt.Errorf("%s", locales.ZmTooManyNames(s.dir))
}

func (s *localSink) Abort() {
	s.f.Close()
	os.Remove(s.f.Name())
}

// zmodemBar 是窗格里 rz/sz 传输的进度条：传的是哪个文件、传了多少，可以取消。
// 传输期间终端不接受键盘输入，这一行也说明了原因和 Ctrl+C 取消。
func (a *app) zmodemBar(c *ui.Context, colors design.Colors, p *pane) {
	tr := p.zmodem
	theme := c.Theme()
	verb, icon := locales.ZmUploading(), "arrow-up"
	if tr.kind == zmodem.Download {
		verb, icon = locales.ZmReceiving(), "arrow-down"
	}
	name := ""
	if n := tr.name.Load(); n != nil {
		name = *n
	}
	done, total := tr.done.Load(), tr.total.Load()
	frac := float32(0)
	if total > 0 {
		frac = min(float32(done)/float32(total), 1)
	}
	ui.Column(c).Key("zmodemBar").Shrink(0).BorderWidth(0, 0, 1, 0).BorderColor(colors.Hairline).Background(colors.Hover).Children(func() {
		ui.Row(c).Height(32).Padding(0, 12).Gap(8).AlignItems(ui.Center).FontSize(design.TextSmall).Children(func() {
			ui.Icon(c, design.Icon(icon)).FontSize(13).TextColor(theme.Accent)
			ui.Text(c, verb).NoWrap().Shrink(0)
			ui.Text(c, name).FontWeight(600).SingleLine().MinWidth(0).Shrink(1)
			ui.Box(c).Grow(1)
			if total > 0 {
				ui.Text(c, fmt.Sprintf("%s / %s", formatSize(done), formatSize(total))).
					TextColor(colors.TextFaint).FontFeatures("tnum").NoWrap().Shrink(0)
			}
			ui.ButtonBase(c).Key("cancelZmodem").Label(locales.ZmCancelTransfer()).Shrink(0).Tooltip(locales.ZmCancelHint()).Children(func() {
				ui.Text(c, locales.Cancel()).TextColor(theme.Accent).FontWeight(600).NoWrap()
			}).OnClick(tr.cancel)
		})
		ui.Box(c).Height(2).Draw(func(painter *ui.Painter, r ui.Rect) {
			painter.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W * frac, H: r.H}, theme.Accent, 0)
		})
	})
}
