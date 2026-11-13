package main

import (
	"net/url"
	"os"
	"os/user"
	"strings"
	"sync"
)

var (
	localUserOnce sync.Once
	localUserName string
)

func localUsername() string {
	localUserOnce.Do(func() {
		if u, err := user.Current(); err == nil && u.Username != "" {
			localUserName = u.Username
			return
		}
		localUserName = os.Getenv("USER")
	})
	return localUserName
}

// promptTracker 从终端输出流中被动提取当前登录用户与工作目录。
//
// 优先级为：OSC 7 路径 > OSC 0/2 标题 > 提示符行（如 [root@bogon ~]# 或 ubuntu@host:/app$）。
// 所有状态只在调用 feed 的读取 goroutine 内访问，仅在结果变化时返回 changed=true。
type promptTracker struct {
	mu   sync.Mutex
	user string
	dir  string

	state   byte // 0=正常, 1=ESC, 2=CSI, 3=OSC, 4=OSC_ESC
	oscBuf  []byte
	lineBuf []byte

	oscDirThisPrompt   bool
	oscTitleThisPrompt bool
}

func newPromptTracker() *promptTracker {
	return &promptTracker{}
}

func (pt *promptTracker) feed(data []byte) (user, dir string, changed bool) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	prevUser, prevDir := pt.user, pt.dir
	for _, b := range data {
		switch pt.state {
		case 0: // normal
			switch b {
			case 0x1b:
				pt.state = 1
			case '\n':
				pt.lineBuf = pt.lineBuf[:0]
				pt.oscDirThisPrompt = false
				pt.oscTitleThisPrompt = false
			case '\r':
				pt.lineBuf = pt.lineBuf[:0]
			case '\b', 0x7f:
				if len(pt.lineBuf) > 0 {
					pt.lineBuf = pt.lineBuf[:len(pt.lineBuf)-1]
				}
			default:
				if b >= 0x20 && len(pt.lineBuf) < 512 {
					pt.lineBuf = append(pt.lineBuf, b)
				}
			}
		case 1: // ESC
			switch b {
			case '[':
				pt.state = 2
			case ']':
				pt.state = 3
				pt.oscBuf = pt.oscBuf[:0]
			default:
				pt.state = 0
			}
		case 2: // CSI
			if b >= 0x40 && b <= 0x7e {
				pt.state = 0
			} else if b == 0x18 || b == 0x1a {
				pt.state = 0
			}
		case 3: // OSC
			switch b {
			case 0x07:
				pt.handleOSC(string(pt.oscBuf))
				pt.oscBuf = pt.oscBuf[:0]
				pt.state = 0
			case 0x1b:
				pt.state = 4
			case 0x18, 0x1a:
				pt.oscBuf = pt.oscBuf[:0]
				pt.state = 0
			default:
				if len(pt.oscBuf) < 2048 {
					pt.oscBuf = append(pt.oscBuf, b)
				}
			}
		case 4: // OSC + ESC
			if b == '\\' {
				pt.handleOSC(string(pt.oscBuf))
				pt.oscBuf = pt.oscBuf[:0]
				pt.state = 0
			} else if b == ']' {
				pt.oscBuf = pt.oscBuf[:0]
				pt.state = 3
			} else {
				pt.oscBuf = pt.oscBuf[:0]
				pt.state = 0
			}
		}
	}

	if len(pt.lineBuf) > 0 {
		if u, d, _, ok := parsePromptLine(string(pt.lineBuf)); ok {
			pt.user = u
			if d != "" && !pt.oscDirThisPrompt && (!pt.oscTitleThisPrompt || (strings.HasPrefix(d, "/") && !strings.HasPrefix(pt.dir, "/"))) {
				pt.dir = d
			}
		}
	}

	return pt.user, pt.dir, pt.user != prevUser || pt.dir != prevDir
}

func (pt *promptTracker) handleOSC(payload string) {
	code, val, ok := strings.Cut(payload, ";")
	if !ok {
		return
	}
	switch code {
	case "7":
		if dir := parseOSC7Path(val); dir != "" {
			pt.dir = dir
			pt.oscDirThisPrompt = true
		}
	case "0", "2":
		if u, d, ok := parsePromptTitle(val); ok {
			pt.user = u
			pt.oscTitleThisPrompt = true
			if d != "" && !pt.oscDirThisPrompt {
				pt.dir = d
			}
		}
	}
}

func parseOSC7Path(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return ""
	}
	decoded, err := url.PathUnescape(u.Path)
	if err != nil {
		return u.Path
	}
	return decoded
}

// parsePromptTitle 解析 xterm 窗口标题（如 "root@bogon:~"、"ubuntu@host: /var/log"）。
func parsePromptTitle(title string) (user, dir string, ok bool) {
	s := strings.TrimSpace(title)
	if s == "" {
		return "", "", false
	}
	s = stripEnvPrefix(s)
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 {
		return "", "", false
	}
	u := s[:at]
	if !isValidPromptUser(u) {
		return "", "", false
	}
	rest := s[at+1:]
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 {
		return "", "", false
	}
	host := strings.TrimSpace(rest[:colon])
	if !isValidPromptHost(host) {
		return "", "", false
	}
	d := strings.TrimSpace(rest[colon+1:])
	if !isExplicitPath(d) {
		return "", "", false
	}
	return u, d, true
}

// parsePromptLine 解析常见 shell 提示符行（无 OSC 时兜底）。
// 支持 "[root@bogon ~]#"、"root@server:/var/log#"、"ubuntu@host:~/app$" 等格式。
func parsePromptLine(line string) (user, dir string, isAbsOrHome, ok bool) {
	s := strings.TrimRight(line, " \t")
	if s == "" {
		return "", "", false, false
	}
	last := s[len(s)-1]
	if last != '#' && last != '$' && last != '%' {
		return "", "", false, false
	}
	s = strings.TrimSpace(s[:len(s)-1])
	s = stripEnvPrefix(s)
	if s == "" {
		return "", "", false, false
	}

	// 格式 1：[user@host dir] 或 [user@host:dir]
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		inner := strings.TrimSpace(s[1 : len(s)-1])
		at := strings.IndexByte(inner, '@')
		if at <= 0 {
			return "", "", false, false
		}
		u := inner[:at]
		if !isValidPromptUser(u) {
			return "", "", false, false
		}
		rest := inner[at+1:]
		sep := strings.IndexAny(rest, " \t:")
		if sep <= 0 {
			return "", "", false, false
		}
		host := rest[:sep]
		if !isValidPromptHost(host) {
			return "", "", false, false
		}
		d := strings.TrimSpace(rest[sep+1:])
		if d == "" || strings.Contains(d, "://") {
			return "", "", false, false
		}
		return u, d, isExplicitPath(d), true
	}

	// 格式 2：user@host:dir
	at := strings.IndexByte(s, '@')
	if at <= 0 {
		return "", "", false, false
	}
	u := s[:at]
	if !isValidPromptUser(u) {
		return "", "", false, false
	}
	rest := s[at+1:]
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 {
		return "", "", false, false
	}
	host := rest[:colon]
	if !isValidPromptHost(host) {
		return "", "", false, false
	}
	d := strings.TrimSpace(rest[colon+1:])
	if d == "" || strings.Contains(d, "://") {
		return "", "", false, false
	}
	return u, d, isExplicitPath(d), true
}

func stripEnvPrefix(s string) string {
	for strings.HasPrefix(s, "(") {
		end := strings.IndexByte(s, ')')
		if end <= 0 || end+1 >= len(s) || s[end+1] != ' ' {
			break
		}
		s = strings.TrimSpace(s[end+1:])
	}
	return s
}

func isExplicitPath(p string) bool {
	return p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "/") ||
		strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../")
}

func isValidPromptUser(u string) bool {
	if len(u) == 0 || len(u) > 64 || u[0] == '-' {
		return false
	}
	for i := 0; i < len(u); i++ {
		c := u[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

func isValidPromptHost(h string) bool {
	if len(h) == 0 || len(h) > 128 {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}
