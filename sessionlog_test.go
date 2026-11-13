package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/terminal"
)

// holdConn 给不启动本机 shell 的终端当连接：读一直等到关闭。
type holdConn struct {
	done chan struct{}
	once sync.Once
}

func newHoldConn() *holdConn { return &holdConn{done: make(chan struct{})} }

func (h *holdConn) Read([]byte) (int, error) {
	<-h.done
	return 0, io.EOF
}

func (h *holdConn) Write(p []byte) (int, error) { return len(p), nil }

func (h *holdConn) Close() error {
	h.once.Do(func() { close(h.done) })
	return nil
}

func stripText(chunks ...string) string {
	var s sessionStripper
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(s.apply([]byte(c)))
	}
	b.WriteString(s.flush())
	return b.String()
}

func terminalText(t *testing.T, chunks ...string) string {
	t.Helper()
	term, err := terminal.New(terminal.Options{Conn: newHoldConn()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { term.Close() })
	term.Resize(80, 24)
	for _, c := range chunks {
		term.Feed([]byte(c))
	}
	return term.Text()
}

// normalizeScreen 去掉终端 Text() 会裁掉的行尾空格和文末空行，用来和日志对照。
func normalizeScreen(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// 普通输出去掉颜色、标题和未写完的转义序列后，应和终端屏幕上的文字一致。
func TestSessionLogStripMatchesTerminal(t *testing.T) {
	chunks := []string{
		"\x1b[31mhello\x1b[0m\r\n",
		"hello\rbye\r\n",
		"ab\bc\r\n",
		"a\tb\r\n",
		"\x1b]0;title\x07visible\r\n",
		"\x1b[3",
		"1mX\x1b[0m\r\n",
		"中文\r\n",
	}
	got := normalizeScreen(stripText(chunks...))
	want := normalizeScreen(terminalText(t, chunks...))
	if got != want {
		t.Fatalf("日志与终端不一致\n日志: %q\n终端: %q", got, want)
	}
	for _, s := range []string{"hello", "byelo", "ac", "a       b", "visible", "X", "中文"} {
		if !strings.Contains(got, s) {
			t.Errorf("结果里没有 %q: %q", s, got)
		}
	}
	if strings.Contains(got, "\x1b") || strings.Contains(got, "title") {
		t.Fatalf("控制序列或标题残留: %q", got)
	}
}

// 清屏和光标定位不模拟：日志保留已经出现过的行，不声称和全屏程序的屏幕一致。
func TestSessionLogKeepsLinesFullScreenClears(t *testing.T) {
	got := stripText("before\r\n\x1b[2J\x1b[Hafter\r\n")
	if !strings.Contains(got, "before\n") || !strings.Contains(got, "after\n") {
		t.Fatalf("应保留清屏前后的文字: %q", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("清屏序列不应写进日志: %q", got)
	}
}

func TestSessionLogFile(t *testing.T) {
	now := time.Date(2026, 11, 9, 8, 7, 6, 0, time.Local)
	if got, want := sessionLogFileName(`bad/name:?.txt`, 3, now), "bad_name__.txt_20261109-080706_p3.log"; got != want {
		t.Fatalf("文件名 = %q，期望 %q", got, want)
	}
	if got, want := sessionLogFileName("   ", 1, now), "session_20261109-080706_p1.log"; got != want {
		t.Fatalf("空主机名 = %q，期望 %q", got, want)
	}
	if n := utf8.RuneCountInString(sanitizeLogName(strings.Repeat("名", 50))); n != 40 {
		t.Fatalf("主机名应截到 40 个字符，得到 %d", n)
	}

	dir := filepath.Join(t.TempDir(), "nested", "logs")
	var s sessionLog
	if err := s.open(dir, "one.log"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "one.log"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("文件权限 = %o，期望 600", info.Mode().Perm())
	}
	dinfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dinfo.Mode().Perm() != 0o700 {
		t.Fatalf("目录权限 = %o，期望 700", dinfo.Mode().Perm())
	}
	if err := s.write([]byte("\x1b[31mkeep\x1b[0m\r\n")); err != nil {
		t.Fatal(err)
	}
	// 关掉底层句柄，下一次写入就会失败，用来确认失败后能换新文件继续。
	if err := s.f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.write([]byte("lost\n")); err == nil {
		t.Fatal("句柄关掉后写入应失败")
	}
	if s.activeDir() != "" {
		t.Fatal("写入失败后应视为没有打开，方便重新选择目录")
	}
	if err := s.open(dir, "two.log"); err != nil {
		t.Fatal(err)
	}
	if err := s.write([]byte("next\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.write([]byte("tail")); err != nil {
		t.Fatal(err)
	}
	s.close()
	one, err := os.ReadFile(filepath.Join(dir, "one.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(one) != "keep\n" {
		t.Fatalf("第一份日志 = %q", one)
	}
	two, err := os.ReadFile(filepath.Join(dir, "two.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(two) != "next\ntail" {
		t.Fatalf("第二份日志 = %q", two)
	}
}

// 默认关闭。打开后，远程窗格的输出写入所选文件夹，内容和终端一致，不进保险库。
// 取消选择、选中的不是文件夹时保持关闭。本地终端不记录。关掉开关后不再追加。
func TestSessionLogSettingAndRemoteOutput(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	tb.focus.term.Send([]byte("echo session-log-off-$((1+2))\n"))
	h.pump("关闭时的输出", func() bool { return strings.Contains(tb.focus.term.Text(), "session-log-off-3") })
	if h.a.local.SessionLog || tb.focus.log.activeDir() != "" {
		t.Fatal("会话日志默认应关闭，且不应创建文件")
	}

	var picks int
	h.a.chooseLogDir = func() (string, error) {
		picks++
		return "", nil
	}
	h.click("设置")
	h.tt.Frame()
	seen := picks
	h.click("记录终端输出")
	h.pump("取消选择", func() bool { return picks > seen && !h.a.sessionLogPicking })
	h.tt.Frame()
	if h.a.sessionLogPicking || h.a.local.SessionLog || h.a.settingsPanel.sessionLog {
		t.Fatal("取消选择文件夹后不应打开记录")
	}

	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.a.chooseLogDir = func() (string, error) {
		picks++
		return file, nil
	}
	seen = picks
	h.click("记录终端输出")
	h.pump("不是文件夹", func() bool { return picks > seen && !h.a.sessionLogPicking && h.tt.HasText("选中的不是文件夹") })
	h.tt.Frame()
	if h.a.sessionLogPicking || h.a.local.SessionLog {
		t.Fatal("选中文件时不应打开记录")
	}

	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	h.a.chooseLogDir = func() (string, error) {
		picks++
		return dir, nil
	}
	seen = picks
	h.click("记录终端输出")
	h.pump("打开记录", func() bool {
		return picks > seen && h.a.local.SessionLog && h.a.local.SessionLogDir == dir && !h.a.sessionLogPicking
	})
	raw, err := os.ReadFile(h.a.devicePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"sessionLog": true`) || !strings.Contains(string(raw), dir) {
		t.Fatalf("本机设置里没有会话日志: %s", raw)
	}
	if h.vaultFileContains("sessionLog") || h.vaultFileContains(dir) {
		t.Fatal("会话日志设置不应写进保险库")
	}
	h.click("完成")
	h.tt.Frame()

	tb.focus.term.Send([]byte("echo session-log-$((5+4))\n"))
	var logPath string
	h.pump("日志文件", func() bool {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.log"))
		if len(matches) != 1 {
			return false
		}
		logPath = matches[0]
		b, err := os.ReadFile(logPath)
		return err == nil && strings.Contains(string(b), "session-log-9") && strings.Contains(tb.focus.term.Text(), "session-log-9")
	})
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logged), "session-log-off-3") {
		t.Fatal("打开记录之前的输出不应补记")
	}
	if strings.Contains(string(logged), "\x1b") {
		t.Fatalf("日志里还有控制序列: %q", logged)
	}
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("日志权限 = %o，期望 600", info.Mode().Perm())
	}
	if !strings.HasPrefix(filepath.Base(logPath), "dev_") {
		t.Fatalf("日志文件名应带主机名: %s", logPath)
	}
	if h.vaultFileContains("session-log-9") {
		t.Fatal("终端输出不应写进保险库")
	}

	h.click("新建")
	h.tt.Frame()
	h.click("本地终端")
	h.pump("本地终端", func() bool { return len(h.a.tabs) == 2 && h.a.tabs[1].local })
	local := h.a.tabs[1].focus
	if local.log == nil || local.log.activeDir() != "" {
		t.Fatal("本地终端不应写会话日志")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.log")); len(matches) != 1 {
		t.Fatalf("本地终端多写出了日志: %v", matches)
	}

	h.click("设置")
	h.tt.Frame()
	h.click("记录终端输出")
	h.tt.Frame()
	if h.a.local.SessionLog || tb.focus.log.activeDir() != "" {
		t.Fatal("关掉记录后不应再写文件")
	}
	tb.focus.term.Send([]byte("echo session-log-off-$((3+4))\n"))
	h.pump("关闭后的输出", func() bool { return strings.Contains(tb.focus.term.Text(), "session-log-off-7") })
	logged, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logged), "session-log-off-7") {
		t.Fatal("关掉记录后不应再追加")
	}
}
