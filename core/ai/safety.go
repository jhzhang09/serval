package ai

import (
	"slices"
	"strings"
)

// IsDangerousCommand 通过词法切分识别可能造成系统破坏、数据丢失、意外关机或含隐蔽换行的危险命令。
// 相比简单子串匹配，能区分 `grep reboot log` 等安全查询与真正的高危调用。
func IsDangerousCommand(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return false
	}

	// 含有换行符的多行指令，粘贴到终端存在首行自动触发执行的风险
	if strings.ContainsAny(trimmed, "\r\n") {
		return true
	}

	// Fork 炸弹
	if strings.Contains(trimmed, ":(){") && strings.Contains(trimmed, ":|:&") {
		return true
	}

	// 递归检查 $(...) 与反引号中的内嵌子命令，防止通过命令替换绕过检测
	for _, sub := range extractCommandSubstitutions(trimmed) {
		if IsDangerousCommand(sub) {
			return true
		}
	}

	// 按 shell 分隔符切分子命令（含单个后台 &）
	segments := splitShellSegments(trimmed)
	for i, seg := range segments {
		tokens := shellFields(seg)
		if len(tokens) == 0 {
			continue
		}

		// 检查管道到 shell（如 curl ... | sh, wget ... | bash）
		if i > 0 {
			idx := skipCommandWrappers(tokens)
			if idx < len(tokens) {
				first := normalizeProgName(tokens[idx])
				if first == "sh" || first == "bash" || first == "zsh" || first == "dash" {
					return true
				}
			}
		}

		// 检查重定向到设备节点或关键系统文件
		if hasDangerousRedirect(tokens) {
			return true
		}

		// 剥离前置 sudo / doas / env / timeout / nice 等包装命令（含带参数选项）
		idx := skipCommandWrappers(tokens)
		if idx >= len(tokens) {
			continue
		}
		prog := normalizeProgName(tokens[idx])
		args := tokens[idx+1:]

		// 递归检查 sh -c "..." / bash -c "..." / eval "..."
		if (prog == "sh" || prog == "bash" || prog == "zsh" || prog == "dash") && len(args) >= 2 {
			for ai := 0; ai < len(args)-1; ai++ {
				if args[ai] == "-c" || args[ai] == "-lc" {
					if IsDangerousCommand(args[ai+1]) {
						return true
					}
				}
			}
		}
		if prog == "eval" && len(args) > 0 {
			if IsDangerousCommand(strings.Join(args, " ")) {
				return true
			}
		}
		if prog == "xargs" && len(args) > 0 {
			xIdx := 0
			for xIdx < len(args) && strings.HasPrefix(args[xIdx], "-") {
				if (args[xIdx] == "-I" || args[xIdx] == "-n" || args[xIdx] == "-P" || args[xIdx] == "-d" || args[xIdx] == "-L") && xIdx+1 < len(args) {
					xIdx += 2
					continue
				}
				xIdx++
			}
			if xIdx < len(args) && isDangerousInvocation(normalizeProgName(args[xIdx]), args[xIdx+1:]) {
				return true
			}
		}

		if isDangerousInvocation(prog, args) {
			return true
		}
	}

	return false
}

// IsDangerousExecCommand 判断 ssh_exec 传入的命令是否高危。
// 与 IsDangerousCommand 不同，ssh_exec 是非交互执行，不会因粘贴换行自动回车，
// 因此折叠续行后逐行做高危词法检查，避免安全的多行只读脚本被误拦。
func IsDangerousExecCommand(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return false
	}
	if strings.Contains(trimmed, ":(){") && strings.Contains(trimmed, ":|:&") {
		return true
	}
	normalized := strings.ReplaceAll(trimmed, "\\\r\n", " ")
	normalized = strings.ReplaceAll(normalized, "\\\n", " ")
	for _, line := range strings.FieldsFunc(normalized, func(r rune) bool { return r == '\r' || r == '\n' }) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if IsDangerousCommand(line) {
			return true
		}
	}
	return false
}

// IsReadOnlyProbeCommand 判断命令是否属于纯只读系统探测命令（无重定向、无命令替换且每段均为只读白名单）。
// 用于在生产环境主机的「自动执行」模式下放行常规只读诊断，同时对写操作/服务变更强制弹窗确认。
func IsReadOnlyProbeCommand(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" || strings.ContainsAny(trimmed, "\r\n") {
		return false
	}
	if len(extractCommandSubstitutions(trimmed)) > 0 {
		return false
	}
	segments := splitShellSegments(trimmed)
	if len(segments) == 0 {
		return false
	}
	for _, seg := range segments {
		tokens := shellFields(seg)
		if len(tokens) == 0 {
			return false
		}
		// 任何输出重定向（> / >>）均视为非只读
		for _, tok := range tokens {
			if strings.HasPrefix(tok, ">") || strings.Contains(tok, ">>") {
				return false
			}
		}
		idx := skipCommandWrappers(tokens)
		if idx >= len(tokens) {
			return false
		}
		prog := normalizeProgName(tokens[idx])
		args := tokens[idx+1:]
		if !isReadOnlyInvocation(prog, args) {
			return false
		}
	}
	return true
}

// BaseCommandProgram 当命令为单行、无命令替换、无重定向且仅包含单一简单命令时返回规范化程序名；
// 否则返回空字符串，用于会话级「允许同类命令」白名单判断，避免复合命令扩大授权范围。
func BaseCommandProgram(cmd string) string {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" || strings.ContainsAny(trimmed, "\r\n") {
		return ""
	}
	if len(extractCommandSubstitutions(trimmed)) > 0 {
		return ""
	}
	segments := splitShellSegments(trimmed)
	if len(segments) != 1 {
		return ""
	}
	tokens := shellFields(segments[0])
	if len(tokens) == 0 {
		return ""
	}
	for _, tok := range tokens {
		if strings.HasPrefix(tok, ">") || strings.Contains(tok, ">>") {
			return ""
		}
	}
	idx := skipCommandWrappers(tokens)
	if idx >= len(tokens) {
		return ""
	}
	return normalizeProgName(tokens[idx])
}

// IsSensitiveWritePath 判断写入目标路径是否属于高风险系统配置或认证文件。
func IsSensitiveWritePath(p string) bool {
	low := strings.ToLower(strings.TrimSpace(p))
	if low == "" {
		return false
	}
	prefixes := []string{
		"/etc/", "/boot/", "/usr/", "/bin/", "/sbin/", "/lib/", "/lib64/",
		"/var/spool/cron", "/etc/systemd/system/",
	}
	for _, pref := range prefixes {
		if strings.HasPrefix(low, pref) {
			return true
		}
	}
	suffixes := []string{
		"/.ssh/authorized_keys", "/.ssh/config", "/.bashrc", "/.zshrc", "/.profile", "/.bash_profile",
	}
	for _, suf := range suffixes {
		if strings.HasSuffix(low, suf) {
			return true
		}
	}
	return strings.Contains(low, "/systemd/system/")
}

func isReadOnlyInvocation(prog string, args []string) bool {
	switch prog {
	case "ls", "ll", "la", "dir", "pwd", "whoami", "id", "uname", "uptime", "hostname",
		"date", "df", "du", "free", "ps", "pgrep", "lscpu", "lsmem",
		"lsblk", "lsmod", "lspci", "lsusb", "lsof", "cat", "head", "tail",
		"grep", "egrep", "fgrep", "rg", "sort", "uniq", "wc", "cut", "tr", "diff",
		"stat", "file", "readlink", "realpath", "which", "whereis", "type", "ss", "netstat",
		"ifconfig", "route", "traceroute", "tracepath", "dig", "nslookup", "host",
		"env", "printenv", "echo", "printf", "journalctl", "dmesg", "last", "who", "w",
		"vmstat", "iostat", "mpstat", "sar", "sysctl":
		if prog == "sysctl" {
			for _, a := range args {
				if strings.Contains(a, "=") || a == "-w" {
					return false
				}
			}
		}
		return true
	case "top":
		// 仅允许非交互批处理模式 top -b -n <N>
		hasBatch := false
		hasCount := false
		for _, a := range args {
			if a == "-b" {
				hasBatch = true
			} else if a == "-n" || strings.HasPrefix(a, "-n") {
				hasCount = true
			}
		}
		return hasBatch && hasCount
	case "sed":
		for _, a := range args {
			low := strings.ToLower(a)
			if a == "-i" || strings.HasPrefix(a, "-i") || a == "--in-place" || strings.HasPrefix(a, "--in-place=") ||
				a == "-f" || strings.HasPrefix(a, "-f") || a == "--file" || strings.HasPrefix(a, "--file=") {
				return false
			}
			// 拒绝 GNU sed 的 e（执行命令）与 w（写文件）指令
			if strings.Contains(low, "/e") || strings.Contains(low, ";e") || low == "e" ||
				strings.Contains(low, "w ") || strings.Contains(low, "w/") {
				return false
			}
		}
		return true
	case "find":
		for _, a := range args {
			low := strings.ToLower(a)
			if low == "-delete" || low == "-exec" || low == "-execdir" || low == "-ok" {
				return false
			}
		}
		return true
	case "ip":
		if len(args) == 0 {
			return true
		}
		for _, a := range args {
			low := strings.ToLower(a)
			if low == "add" || low == "del" || low == "delete" || low == "set" || low == "change" || low == "replace" || low == "flush" {
				return false
			}
		}
		return true
	case "ping", "ping6":
		return slices.Contains(args, "-c")
	case "systemctl":
		if len(args) == 0 {
			return false
		}
		sub := strings.ToLower(args[0])
		return sub == "status" || sub == "list-units" || sub == "list-unit-files" ||
			sub == "is-active" || sub == "is-enabled" || sub == "show" || sub == "cat"
	case "service":
		return len(args) >= 2 && strings.ToLower(args[1]) == "status"
	case "docker", "podman":
		if len(args) == 0 {
			return false
		}
		sub := strings.ToLower(args[0])
		return sub == "ps" || sub == "images" || sub == "logs" || sub == "inspect" ||
			sub == "stats" || sub == "info" || sub == "version" || sub == "top"
	case "kubectl":
		if len(args) == 0 {
			return false
		}
		sub := strings.ToLower(args[0])
		return sub == "get" || sub == "describe" || sub == "logs" || sub == "top" || sub == "version"
	case "nginx", "apachectl", "httpd":
		return slices.Contains(args, "-t") || slices.Contains(args, "-T") || slices.Contains(args, "-v") || slices.Contains(args, "-V")
	case "crontab":
		return slices.Contains(args, "-l")
	}
	return false
}

func normalizeProgName(tok string) string {
	tok = strings.TrimLeft(tok, "({")
	tok = strings.TrimRight(tok, ")}")
	if idx := strings.LastIndexByte(tok, '/'); idx >= 0 {
		tok = tok[idx+1:]
	}
	return strings.ToLower(tok)
}

func skipCommandWrappers(tokens []string) int {
	idx := 0
	for idx < len(tokens) {
		low := normalizeProgName(tokens[idx])
		switch low {
		case "sudo":
			idx++
			for idx < len(tokens) {
				opt := tokens[idx]
				if opt == "--" {
					idx++
					break
				}
				if !strings.HasPrefix(opt, "-") {
					break
				}
				// 带值参数的 sudo 选项
				if opt == "-u" || opt == "-g" || opt == "-C" || opt == "-p" || opt == "-r" ||
					opt == "-t" || opt == "-h" || opt == "-T" || opt == "--user" || opt == "--group" {
					idx += 2
					continue
				}
				idx++
			}
			continue
		case "doas":
			idx++
			for idx < len(tokens) && strings.HasPrefix(tokens[idx], "-") {
				opt := tokens[idx]
				if opt == "-u" || opt == "-C" {
					idx += 2
					continue
				}
				idx++
			}
			continue
		case "env":
			idx++
			for idx < len(tokens) {
				opt := tokens[idx]
				if opt == "--" {
					idx++
					break
				}
				if opt == "-u" || opt == "-C" || opt == "--unset" {
					idx += 2
					continue
				}
				if strings.HasPrefix(opt, "-") || strings.Contains(opt, "=") {
					idx++
					continue
				}
				break
			}
			continue
		case "timeout":
			idx++
			for idx < len(tokens) && strings.HasPrefix(tokens[idx], "-") {
				opt := tokens[idx]
				if opt == "-k" || opt == "-s" || opt == "--kill-after" || opt == "--signal" {
					idx += 2
					continue
				}
				idx++
			}
			if idx < len(tokens) {
				idx++ // 跳过时长参数（如 10s）
			}
			continue
		case "nice":
			idx++
			for idx < len(tokens) && strings.HasPrefix(tokens[idx], "-") {
				opt := tokens[idx]
				if opt == "-n" || opt == "--adjustment" {
					idx += 2
					continue
				}
				idx++
			}
			continue
		case "nohup", "command", "builtin", "exec", "time":
			idx++
			continue
		}
		if strings.Contains(tokens[idx], "=") {
			idx++
			continue
		}
		break
	}
	return idx
}

func extractCommandSubstitutions(s string) []string {
	var out []string
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote == '\'' {
			if ch == '\'' {
				quote = 0
			}
			continue
		}
		if ch == '\\' && i+1 < len(s) {
			i++
			continue
		}
		if ch == '"' {
			if quote == '"' {
				quote = 0
			} else {
				quote = '"'
			}
			continue
		}
		if ch == '\'' && quote == 0 {
			quote = '\''
			continue
		}
		if ch == '$' && i+1 < len(s) && s[i+1] == '(' {
			depth := 1
			j := i + 2
			for j < len(s) && depth > 0 {
				if s[j] == '(' {
					depth++
				} else if s[j] == ')' {
					depth--
				}
				j++
			}
			if depth == 0 {
				out = append(out, s[i+2:j-1])
				i = j - 1
			}
			continue
		}
		if ch == '`' {
			j := i + 1
			for j < len(s) && s[j] != '`' {
				if s[j] == '\\' && j+1 < len(s) {
					j += 2
					continue
				}
				j++
			}
			if j < len(s) {
				out = append(out, s[i+1:j])
				i = j
			}
		}
	}
	return out
}

func splitShellSegments(s string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			cur.WriteByte(ch)
			if ch == '\\' && quote == '"' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\\' && i+1 < len(s) {
			cur.WriteByte(ch)
			i++
			cur.WriteByte(s[i])
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			cur.WriteByte(ch)
			continue
		}
		if ch == ';' || ch == '|' {
			out = append(out, cur.String())
			cur.Reset()
			if ch == '|' && i+1 < len(s) && s[i+1] == '|' {
				i++
			}
			continue
		}
		if ch == '&' {
			// 保留 >&2 / <&0 / &>file 等文件描述符重定向，仅在独立 & 或 && 处切分命令
			prev := byte(0)
			if i > 0 {
				prev = s[i-1]
			}
			next := byte(0)
			if i+1 < len(s) {
				next = s[i+1]
			}
			if prev == '>' || prev == '<' || (next == '>' && prev != '&') {
				cur.WriteByte(ch)
				continue
			}
			out = append(out, cur.String())
			cur.Reset()
			if next == '&' {
				i++
			}
			continue
		}
		cur.WriteByte(ch)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func shellFields(s string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote == '\'' {
			if ch == '\'' {
				quote = 0
				continue
			}
			cur.WriteByte(ch)
			continue
		}
		if quote == '"' {
			if ch == '\\' && i+1 < len(s) {
				next := s[i+1]
				if next == '"' || next == '\\' || next == '$' || next == '`' {
					i++
					cur.WriteByte(next)
					continue
				}
			}
			if ch == '"' {
				quote = 0
				continue
			}
			cur.WriteByte(ch)
			continue
		}
		if ch == '\\' && i+1 < len(s) {
			i++
			cur.WriteByte(s[i])
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == ' ' || ch == '\t' {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(ch)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func isDangerousTargetPath(target string) bool {
	dangerousTargets := []string{
		"/dev/sd", "/dev/nvme", "/dev/vd", "/dev/hd", "/dev/disk",
		"/etc/passwd", "/etc/shadow", "/etc/sudoers",
	}
	low := strings.ToLower(strings.TrimSpace(target))
	for _, dt := range dangerousTargets {
		if strings.HasPrefix(low, dt) {
			return true
		}
	}
	return false
}

func hasDangerousRedirect(tokens []string) bool {
	for i, tok := range tokens {
		target := ""
		if strings.HasPrefix(tok, ">>") {
			target = strings.TrimPrefix(tok, ">>")
			if target == "" && i+1 < len(tokens) {
				target = tokens[i+1]
			}
		} else if strings.HasPrefix(tok, ">") {
			target = strings.TrimPrefix(tok, ">")
			if target == "" && i+1 < len(tokens) {
				target = tokens[i+1]
			}
		}
		if target != "" && isDangerousTargetPath(target) {
			return true
		}
	}
	return false
}

func isDangerousInvocation(prog string, args []string) bool {
	switch {
	case prog == "rm":
		criticalPrefixes := []string{
			"/etc/", "/boot/", "/usr/", "/bin/", "/sbin/", "/lib/", "/lib64/", "/var/lib/", "/root/.ssh/",
		}
		for _, a := range args {
			low := strings.ToLower(a)
			if low == "--recursive" || (strings.HasPrefix(low, "-") && !strings.HasPrefix(low, "--") && strings.ContainsRune(low, 'r')) {
				return true
			}
			if !strings.HasPrefix(low, "-") {
				for _, pref := range criticalPrefixes {
					if strings.HasPrefix(low, pref) {
						return true
					}
				}
			}
		}
	case prog == "tee":
		for _, a := range args {
			if !strings.HasPrefix(a, "-") && isDangerousTargetPath(a) {
				return true
			}
		}
	case strings.HasPrefix(prog, "mkfs"):
		return true
	case prog == "dd":
		for _, a := range args {
			if strings.HasPrefix(strings.ToLower(a), "of=/dev/") {
				return true
			}
		}
	case prog == "shutdown" || prog == "reboot" || prog == "poweroff" || prog == "halt":
		return true
	case prog == "init" && len(args) > 0 && (args[0] == "0" || args[0] == "6"):
		return true
	case prog == "systemctl" && len(args) > 0:
		sub := strings.ToLower(args[0])
		if sub == "poweroff" || sub == "reboot" || sub == "halt" {
			return true
		}
		if (sub == "stop" || sub == "disable") && len(args) > 1 {
			svc := strings.ToLower(args[1])
			if svc == "ssh" || svc == "sshd" || svc == "ssh.service" || svc == "sshd.service" ||
				svc == "networking" || svc == "networkmanager" || svc == "systemd-networkd" {
				return true
			}
		}
	case prog == "kill":
		for _, a := range args {
			if a == "-1" {
				return true
			}
		}
	case prog == "killall" || prog == "pkill":
		for _, a := range args {
			if strings.Contains(strings.ToLower(a), "sshd") {
				return true
			}
		}
	case prog == "iptables" || prog == "ip6tables":
		for _, a := range args {
			if a == "-F" || a == "--flush" || a == "-X" {
				return true
			}
		}
	case prog == "ufw":
		return len(args) > 0 && strings.ToLower(args[0]) == "disable"
	case prog == "nft":
		return len(args) >= 2 && strings.ToLower(args[0]) == "flush"
	case prog == "find":
		for i, a := range args {
			low := strings.ToLower(a)
			if low == "-delete" {
				return true
			}
			if (low == "-exec" || low == "-execdir") && i+1 < len(args) {
				if isDangerousInvocation(normalizeProgName(args[i+1]), args[i+2:]) {
					return true
				}
			}
		}
	case prog == "chmod" || prog == "chown":
		hasRec := false
		hasWide := false
		hasRoot := false
		for _, a := range args {
			low := strings.ToLower(a)
			if low == "-r" || low == "--recursive" {
				hasRec = true
			}
			if prog == "chmod" && (low == "777" || low == "000") {
				hasWide = true
			}
			if low == "/" {
				hasRoot = true
			}
		}
		if hasRec && (hasWide || hasRoot) {
			return true
		}
	}
	return false
}
