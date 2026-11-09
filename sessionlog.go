package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// 会话日志把远程终端收到的输出写成纯文本。
//
// 文件在用户选的本机文件夹里，不进保险库。默认关闭。
// 本地终端的输出由插件直接读伪终端，应用层拿不到那条字节流，所以不记录。
//
// 去掉颜色、标题这些控制序列，并把回车、退格作用在当前行上，普通命令的结果就和屏幕上一致。
// 光标定位和清屏不模拟，全屏程序的日志不是屏幕回放。

type sessionLog struct {
	mu     sync.Mutex
	f      *os.File
	dir    string
	strip  sessionStripper
	failed bool
}

// sessionStripper 跨多次写入保持当前行，因为回车可能落在两个数据包之间。
type sessionStripper struct {
	pending []byte
	line    []rune
	col     int
	done    strings.Builder
}

func (s *sessionLog) write(p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil || s.failed || len(p) == 0 {
		return nil
	}
	text := s.strip.apply(p)
	if text == "" {
		return nil
	}
	if _, err := s.f.Write([]byte(text)); err != nil {
		s.failed = true
		return err
	}
	if strings.Contains(text, "\n") {
		s.f.Sync()
	}
	return nil
}

func (s *sessionLog) open(dir, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 写入失败后文件句柄还在，但不能再写。换一个新文件继续，不把失败一直留着。
	if s.f != nil && s.dir == dir && !s.failed {
		return nil
	}
	s.closeLocked()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	s.f = f
	s.dir = dir
	s.failed = false
	return nil
}

func (s *sessionLog) activeDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil || s.failed {
		return ""
	}
	return s.dir
}

func (s *sessionLog) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

func (s *sessionLog) closeLocked() {
	if s.f == nil {
		return
	}
	if tail := s.strip.flush(); tail != "" {
		s.f.Write([]byte(tail))
	}
	s.f.Close()
	s.f = nil
	s.strip = sessionStripper{}
}

func (s *sessionStripper) apply(p []byte) string {
	if len(s.pending) > 0 {
		p = append(append([]byte(nil), s.pending...), p...)
		s.pending = nil
	}
	s.done.Reset()
	for i := 0; i < len(p); {
		if p[i] == 0x1b {
			next, ok := skipEscape(p, i)
			if !ok {
				s.hold(p[i:])
				break
			}
			i = next
			continue
		}
		b := p[i]
		switch b {
		case '\r':
			s.col = 0
			i++
			continue
		case '\n':
			s.commit()
			i++
			continue
		case '\b':
			if s.col > 0 {
				s.col--
			}
			i++
			continue
		case '\t':
			n := 8 - (s.col % 8)
			for range n {
				s.put(' ')
			}
			i++
			continue
		}
		if b < 0x20 || b == 0x7f {
			i++
			continue
		}
		if !utf8.FullRune(p[i:]) {
			s.hold(p[i:])
			break
		}
		r, size := utf8.DecodeRune(p[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		s.put(r)
		i += size
	}
	return s.done.String()
}

func (s *sessionStripper) hold(rest []byte) {
	if len(rest) > 4096 {
		return
	}
	s.pending = append([]byte(nil), rest...)
}

func (s *sessionStripper) put(r rune) {
	if s.col < len(s.line) {
		s.line[s.col] = r
	} else {
		s.line = append(s.line, r)
	}
	s.col++
}

func (s *sessionStripper) commit() {
	s.done.WriteString(string(s.line))
	s.done.WriteByte('\n')
	s.line = s.line[:0]
	s.col = 0
}

func (s *sessionStripper) flush() string {
	if s.col == 0 && len(s.line) == 0 && len(s.pending) == 0 {
		return ""
	}
	text := string(s.line)
	s.line = s.line[:0]
	s.col = 0
	s.pending = nil
	return text
}

// skipEscape 跳过从 i 开始的一条控制序列。序列还没结束时 ok 为 false，调用方要把剩下的字节留到下一次。
func skipEscape(p []byte, i int) (next int, ok bool) {
	if i+1 >= len(p) {
		return i, false
	}
	switch p[i+1] {
	case '[':
		for j := i + 2; j < len(p); j++ {
			if p[j] >= 0x40 && p[j] <= 0x7e {
				return j + 1, true
			}
		}
		return i, false
	case ']', 'P', 'X', '^', '_':
		for j := i + 2; j < len(p); j++ {
			if p[j] == 0x07 {
				return j + 1, true
			}
			if p[j] == 0x1b {
				if j+1 >= len(p) {
					return i, false
				}
				if p[j+1] == '\\' {
					return j + 2, true
				}
			}
		}
		return i, false
	case '(', ')', '*', '+':
		if i+2 >= len(p) {
			return i, false
		}
		return i + 3, true
	default:
		return i + 2, true
	}
}

func sessionLogFileName(host string, paneID int, now time.Time) string {
	return fmt.Sprintf("%s_%s_p%d.log", sanitizeLogName(host), now.Format("20060102-150405"), paneID)
}

func sanitizeLogName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "session"
	}
	var b strings.Builder
	n := 0
	for _, r := range name {
		if n >= 40 {
			break
		}
		switch {
		case r < 0x20 || strings.ContainsRune(`\/:*?"<>|`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
		n++
	}
	if b.Len() == 0 {
		return "session"
	}
	return b.String()
}

func (a *app) ensureSessionLog(t *tab, p *pane) {
	if p == nil || p.log == nil || p.zconn == nil {
		return
	}
	if !a.local.SessionLog || strings.TrimSpace(a.local.SessionLogDir) == "" {
		p.log.close()
		return
	}
	dir := a.local.SessionLogDir
	if p.log.activeDir() == dir {
		return
	}
	name := sessionLogFileName(t.host.Name, p.id, time.Now())
	if err := p.log.open(dir, name); err != nil {
		a.noteSessionLogError()
	}
}

func (a *app) refreshSessionLogs() {
	for _, t := range a.tabs {
		if t.root == nil {
			continue
		}
		t.root.each(func(p *pane) { a.ensureSessionLog(t, p) })
	}
}

func (a *app) noteSessionLogError() {
	if a.sessionLogNoted {
		return
	}
	a.sessionLogNoted = true
	a.toast(ui.Toast{Title: "没法写入会话日志", Description: "请检查日志文件夹是否还在，然后在设置里重新选择。"})
}

func (a *app) setSessionLog(on bool) {
	if on && strings.TrimSpace(a.local.SessionLogDir) == "" {
		a.settingsPanel.sessionLog = false
		a.pickSessionLogDir(true)
		return
	}
	next := a.local
	next.SessionLog = on
	if err := a.saveLocal(next); err != nil {
		a.settingsPanel.err = "没能保存会话日志设置。"
		a.settingsPanel.sessionLog = a.local.SessionLog
		return
	}
	a.sessionLogNoted = false
	a.refreshSessionLogs()
}

// pickSessionLogDir 在后台打开文件夹选择框。enable 为真时，选中后顺便打开记录。
func (a *app) pickSessionLogDir(enable bool) {
	if a.chooseLogDir == nil || a.sessionLogPicking {
		return
	}
	a.sessionLogPicking = true
	go func() {
		dir, err := a.chooseLogDir()
		a.post(func() {
			a.sessionLogPicking = false
			if err != nil || strings.TrimSpace(dir) == "" {
				a.settingsPanel.sessionLog = a.local.SessionLog
				if err != nil {
					a.toast(ui.Toast{Title: "没能选择日志文件夹", Description: "可以稍后再试一次。"})
				}
				return
			}
			info, statErr := os.Stat(dir)
			if statErr != nil || !info.IsDir() {
				a.settingsPanel.sessionLog = a.local.SessionLog
				a.toast(ui.Toast{Title: "选中的不是文件夹", Description: "请重新选择一个用来存放日志的文件夹。"})
				return
			}
			next := a.local
			next.SessionLogDir = dir
			if enable {
				next.SessionLog = true
			}
			if err := a.saveLocal(next); err != nil {
				a.settingsPanel.err = "没能保存会话日志设置。"
				a.settingsPanel.sessionLog = a.local.SessionLog
				return
			}
			a.settingsPanel.sessionLog = next.SessionLog
			a.sessionLogNoted = false
			a.refreshSessionLogs()
			a.toast(ui.Toast{Title: "已选择日志文件夹"})
		})
	}()
}
