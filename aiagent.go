package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"serval/core/ai"
	"serval/core/hosts"
	"serval/core/sshconn"
)

// aiPermissionMode 控制 Agent 工具调用的执行权限。
type aiPermissionMode uint8

const (
	aiModeAuto     aiPermissionMode = iota // 自动执行：常规探测与命令自动执行，高危指令仍拦截确认
	aiModeConfirm                          // 逐条确认：写/执行类工具需用户逐次点击确认
	aiModeObserver                         // 只读观察：仅允许读终端、读主机信息与 SFTP 只读探测
)

func (m aiPermissionMode) label() string {
	switch m {
	case aiModeObserver:
		return "只读观察"
	case aiModeConfirm:
		return "确认执行"
	default:
		return "自动执行"
	}
}

func (m aiPermissionMode) description() string {
	switch m {
	case aiModeObserver:
		return "只读终端/主机/SFTP，不执行命令"
	case aiModeConfirm:
		return "执行每条命令前弹窗/卡片确认"
	default:
		return "自动执行常规命令，高危指令仍拦截"
	}
}

// aiSessionSnapshot 在主线程启动 Agent 前抓取当前目标终端/主机的只读快照，避免后台协程直接触碰 app 字段。
type aiSessionSnapshot struct {
	tabID      int
	hasTab     bool
	isLocal    bool
	connected  bool
	hostName   string
	hostAddr   string
	hostPort   int
	hostUser   string
	distro     string
	production bool
	jumpCount  int
	liveUser   string
	liveDir    string
	client     *sshconn.Client
}

func (a *app) captureAISessionSnapshot() aiSessionSnapshot {
	t := a.current()
	if t == nil {
		return aiSessionSnapshot{}
	}
	user, dir := paneUserAndDir(t, t.focus)
	snap := aiSessionSnapshot{
		tabID:      t.id,
		hasTab:     true,
		isLocal:    t.local,
		connected:  t.local || t.client != nil,
		hostName:   tabLabel(t),
		hostAddr:   t.host.Address,
		hostPort:   t.host.Port,
		hostUser:   t.host.User,
		distro:     t.host.Distro,
		production: t.host.Production,
		jumpCount:  len(hosts.SplitJumps(t.host.ProxyJump)),
		liveUser:   user,
		liveDir:    dir,
		client:     t.client,
	}
	if snap.isLocal {
		snap.distro = localOSName()
		if snap.liveDir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				snap.liveDir = home
			}
		}
	}
	return snap
}

func localOSName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	default:
		return "Linux"
	}
}

func agentSystemPrompt(snap aiSessionSnapshot, mode aiPermissionMode) string {
	var b strings.Builder
	b.WriteString("你是 Serval 内置的高级 Linux/Unix 系统运维与故障排查 Agent。\n")
	b.WriteString("你可以调用提供的工具主动检查当前终端与服务器真实状态，绝不能凭空编造未实际查到的硬件规格、进程、端口或文件内容。\n\n")
	b.WriteString("工作原则：\n")
	b.WriteString("1. 当用户询问服务器配置、资源负载、端口进程、服务故障或日志问题时，优先调用工具获取真实输出，再结合多步结果给出结论。\n")
	b.WriteString("2. 尽量合并轻量级只读探测命令（如一次执行 `uname -a; uptime; lscpu | head -n 20; free -h; df -h`），若输出被截断或需要深入排查再继续补充工具调用。\n")
	b.WriteString("3. 如果某次工具调用被权限模式拒绝或用户拒绝，尊重该结果并改用只读方法或给出建议命令，不要反复重试被拒命令。\n")
	b.WriteString("4. 使用清晰的中文 Markdown 总结结果：对比/概览类信息优先使用 Markdown 表格（如 `| 项目 | 配置 |`），关键命令放在 ```bash 代码块中，异常项给出明确建议。\n\n")

	b.WriteString("当前环境状态：\n")
	b.WriteString(fmt.Sprintf("- 权限模式：%s\n", mode.label()))
	if !snap.hasTab {
		b.WriteString("- 当前没有打开的终端标签页。\n")
		return b.String()
	}
	if snap.isLocal {
		b.WriteString(fmt.Sprintf("- 目标类型：本机终端 (%s)\n", snap.distro))
	} else {
		port := snap.hostPort
		if port == 0 {
			port = 22
		}
		state := "已连接"
		if !snap.connected {
			state = "未连接"
		}
		b.WriteString(fmt.Sprintf("- 目标主机：%s (%s@%s:%d)，状态：%s", snap.hostName, snap.liveUser, snap.hostAddr, port, state))
		if snap.distro != "" {
			b.WriteString("，系统：" + snap.distro)
		}
		if snap.production {
			b.WriteString("，【生产环境】")
		}
		b.WriteString("\n")
	}
	if snap.liveDir != "" {
		b.WriteString(fmt.Sprintf("- 当前工作目录：%s\n", snap.liveDir))
	}
	return b.String()
}

func agentToolDefinitions() []ai.ToolDefinition {
	return []ai.ToolDefinition{
		{
			Type: "function",
			Function: ai.ToolFunction{
				Name:        "host_info",
				Description: "获取当前活动标签页的主机连接状态、系统发行版、登录用户、工作目录、是否生产环境（不含密码与私钥等机密）。",
				Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			},
		},
		{
			Type: "function",
			Function: ai.ToolFunction{
				Name:        "terminal_read",
				Description: "读取当前活动终端窗格最近的输出文本、当前用户和工作目录，用于分析报错或上下文。",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"lines":{"type":"integer","description":"读取末尾非空行数，默认60，最大200"}}}`),
			},
		},
		{
			Type: "function",
			Function: ai.ToolFunction{
				Name:        "ssh_exec",
				Description: "在当前连接的 SSH 主机（或本机终端）上执行非交互式 Shell 命令并返回 stdout、stderr 与 exit_code。",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"command":{"type":"string","description":"要执行的单次 Shell 命令"},
						"reason":{"type":"string","description":"简短说明执行该命令的目的（中文）"},
						"timeout_sec":{"type":"integer","description":"超时秒数，默认15，最大60"}
					},
					"required":["command"]
				}`),
			},
		},
		{
			Type: "function",
			Function: ai.ToolFunction{
				Name:        "sftp_list",
				Description: "通过 SFTP（或本地文件）只读列出远端目录内容。",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"目标目录路径，留空则使用当前工作目录或主目录"}}}`),
			},
		},
		{
			Type: "function",
			Function: ai.ToolFunction{
				Name:        "sftp_read",
				Description: "通过 SFTP（或本地文件）只读读取文本文件或日志内容（最大 32KB，自动拒绝二进制文件）。",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"要读取的文件路径"},
						"max_bytes":{"type":"integer","description":"读取字节上限，默认16384，最大32768"}
					},
					"required":["path"]
				}`),
			},
		},
		{
			Type: "function",
			Function: ai.ToolFunction{
				Name:        "terminal_insert",
				Description: "将单行安全命令填入当前终端输入缓冲区供用户核对（绝不自动回车执行）。",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{"command":{"type":"string","description":"要填入终端的单行命令"}},
					"required":["command"]
				}`),
			},
		},
	}
}

func summarizeToolCall(tc ai.ToolCall) (summary, argsPretty string) {
	rawArgs := strings.TrimSpace(tc.Function.Arguments)
	argsPretty = rawArgs
	switch tc.Function.Name {
	case "ssh_exec":
		var p struct {
			Command string `json:"command"`
			Reason  string `json:"reason"`
		}
		if json.Unmarshal([]byte(rawArgs), &p) == nil && strings.TrimSpace(p.Command) != "" {
			cmd := strings.TrimSpace(p.Command)
			if p.Reason != "" {
				argsPretty = p.Reason + "\n$ " + cmd
			} else {
				argsPretty = "$ " + cmd
			}
			return "$ " + cmd, argsPretty
		}
		return "执行远端命令", argsPretty
	case "terminal_read":
		return "读取当前终端输出", argsPretty
	case "host_info":
		return "读取当前主机信息", argsPretty
	case "sftp_list":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &p)
		if p.Path != "" {
			return "列出目录 " + p.Path, argsPretty
		}
		return "列出当前目录", argsPretty
	case "sftp_read":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &p)
		if p.Path != "" {
			return "读取文件 " + p.Path, argsPretty
		}
		return "读取文件", argsPretty
	case "terminal_insert":
		var p struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &p)
		if p.Command != "" {
			return "填入终端：" + strings.TrimSpace(p.Command), "$ " + strings.TrimSpace(p.Command)
		}
		return "填入命令到终端", argsPretty
	default:
		return tc.Function.Name, argsPretty
	}
}

func (a *app) runMainSync(ctx context.Context, fn func()) error {
	done := make(chan struct{})
	a.post(func() {
		fn()
		close(done)
	})
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *app) requestAgentApproval(ctx context.Context, seq uint64, tool *aiToolRecord, cmd, reason, target string, dangerous, production bool) (bool, error) {
	ch := make(chan bool, 1)
	if err := a.runMainSync(ctx, func() {
		if seq != a.ai.reqSeq {
			ch <- false
			return
		}
		if tool != nil {
			tool.Status = toolWaitingApproval
		}
		a.ai.pendingApproval = &aiApproval{
			seq:        seq,
			tool:       tool,
			command:    cmd,
			reason:     reason,
			target:     target,
			dangerous:  dangerous,
			production: production,
			answer:     ch,
		}
	}); err != nil {
		return false, err
	}

	select {
	case ok := <-ch:
		return ok, nil
	case <-ctx.Done():
		a.post(func() {
			if a.ai.pendingApproval != nil && a.ai.pendingApproval.answer == ch {
				a.ai.pendingApproval = nil
			}
		})
		return false, ctx.Err()
	}
}

func (a *app) executeAgentTool(ctx context.Context, seq uint64, snap aiSessionSnapshot, mode aiPermissionMode, toolRec *aiToolRecord, tc ai.ToolCall) (string, bool) {
	switch tc.Function.Name {
	case "host_info":
		if !snap.hasTab {
			return "当前没有打开的终端标签页。", true
		}
		info := map[string]any{
			"is_local":     snap.isLocal,
			"connected":    snap.connected,
			"host_name":    snap.hostName,
			"host_address": snap.hostAddr,
			"port":         snap.hostPort,
			"user":         snap.liveUser,
			"cwd":          snap.liveDir,
			"distro":       snap.distro,
			"production":   snap.production,
			"jump_count":   snap.jumpCount,
		}
		b, _ := json.MarshalIndent(info, "", "  ")
		return string(b), false

	case "terminal_read":
		var params struct {
			Lines int `json:"lines"`
		}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &params)
		lines := params.Lines
		if lines <= 0 {
			lines = 60
		}
		if lines > 200 {
			lines = 200
		}
		var text, user, dir string
		var found bool
		if err := a.runMainSync(ctx, func() {
			t := a.tabByID(snap.tabID)
			if t == nil {
				t = a.current()
			}
			if t == nil || t.focus == nil || t.focus.term == nil {
				return
			}
			found = true
			user, dir = paneUserAndDir(t, t.focus)
			text = captureTermTail(t.focus.term.Text(), lines)
		}); err != nil {
			return "读取终端超时或已取消", true
		}
		if !found {
			return "当前没有可用的活动终端窗格。", true
		}
		if strings.TrimSpace(text) == "" {
			return fmt.Sprintf("user=%s cwd=%s\n（当前终端屏幕暂无输出）", user, dir), false
		}
		return fmt.Sprintf("user=%s cwd=%s\n--- 终端最近输出 ---\n%s", user, dir, text), false

	case "ssh_exec":
		var params struct {
			Command    string `json:"command"`
			Reason     string `json:"reason"`
			TimeoutSec int    `json:"timeout_sec"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
			return "参数解析失败：" + err.Error(), true
		}
		cmd := strings.TrimSpace(params.Command)
		if cmd == "" {
			return "command 不能为空", true
		}
		if !snap.hasTab || !snap.connected {
			return "当前没有已连接的终端会话，无法执行命令。", true
		}
		if mode == aiModeObserver {
			return "当前权限模式为「只读观察」，已阻止执行命令。如需执行请提示用户切换到「确认执行」或「自动执行」。", true
		}
		dangerous := ai.IsDangerousCommand(cmd)
		if mode == aiModeConfirm || dangerous {
			target := snap.hostName
			if snap.liveUser != "" && snap.hostAddr != "" {
				target = snap.liveUser + "@" + snap.hostAddr
			}
			approved, err := a.requestAgentApproval(ctx, seq, toolRec, cmd, params.Reason, target, dangerous, snap.production)
			if err != nil {
				return "等待确认已取消", true
			}
			if !approved {
				if toolRec != nil {
					_ = a.runMainSync(ctx, func() { toolRec.Status = toolDenied })
				}
				return "用户拒绝执行该命令。", true
			}
			if toolRec != nil {
				_ = a.runMainSync(ctx, func() { toolRec.Status = toolRunning })
			}
		}

		timeout := time.Duration(params.TimeoutSec) * time.Second
		if timeout <= 0 {
			timeout = 15 * time.Second
		}
		if timeout > 60*time.Second {
			timeout = 60 * time.Second
		}
		execCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		var res sshconn.ExecResult
		var err error
		if snap.isLocal {
			res, err = execLocalCommand(execCtx, cmd, snap.liveDir, 64<<10)
		} else {
			res, err = snap.client.ExecContext(execCtx, cmd, 64<<10)
		}
		if err != nil {
			return "命令执行失败：" + err.Error(), true
		}
		return formatExecResult(res), res.ExitCode != 0

	case "sftp_list":
		if !snap.hasTab || !snap.connected {
			return "当前没有已连接的终端会话。", true
		}
		var params struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &params)
		targetPath := strings.TrimSpace(params.Path)
		if targetPath == "" {
			targetPath = snap.liveDir
		}
		if snap.isLocal {
			if targetPath == "" {
				targetPath, _ = os.UserHomeDir()
			}
			entries, err := os.ReadDir(targetPath)
			if err != nil {
				return "读取本地目录失败：" + err.Error(), true
			}
			var lines []string
			for i, e := range entries {
				if i >= 120 {
					lines = append(lines, fmt.Sprintf("... 仅展示前 120 项（共 %d 项）", len(entries)))
					break
				}
				kind := "file"
				if e.IsDir() {
					kind = "dir "
				}
				lines = append(lines, fmt.Sprintf("%s  %s", kind, e.Name()))
			}
			return fmt.Sprintf("目录 %s：\n%s", targetPath, strings.Join(lines, "\n")), false
		}

		files, err := snap.client.OpenFiles()
		if err != nil {
			return "打开 SFTP 通道失败：" + err.Error(), true
		}
		defer files.Close()
		if targetPath == "" {
			targetPath, _ = files.Home()
		}
		entries, err := files.List(targetPath)
		if err != nil {
			return "列出远端目录失败：" + err.Error(), true
		}
		var lines []string
		for i, e := range entries {
			if i >= 120 {
				lines = append(lines, fmt.Sprintf("... 仅展示前 120 项（共 %d 项）", len(entries)))
				break
			}
			kind := "file"
			if e.IsDir {
				kind = "dir "
			}
			lines = append(lines, fmt.Sprintf("%s  %-10d  %s", kind, e.Size, e.Name))
		}
		return fmt.Sprintf("目录 %s：\n%s", targetPath, strings.Join(lines, "\n")), false

	case "sftp_read":
		if !snap.hasTab || !snap.connected {
			return "当前没有已连接的终端会话。", true
		}
		var params struct {
			Path     string `json:"path"`
			MaxBytes int    `json:"max_bytes"`
		}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &params)
		p := strings.TrimSpace(params.Path)
		if p == "" {
			return "path 不能为空", true
		}
		limit := int64(params.MaxBytes)
		if limit <= 0 {
			limit = 16 << 10
		}
		if limit > 32<<10 {
			limit = 32 << 10
		}

		var data []byte
		var err error
		if snap.isLocal {
			if !filepath.IsAbs(p) && snap.liveDir != "" {
				p = filepath.Join(snap.liveDir, p)
			}
			var f *os.File
			f, err = os.Open(p)
			if err != nil {
				return "读取文件失败：" + err.Error(), true
			}
			defer f.Close()
			buf := make([]byte, limit+1)
			n, rErr := f.Read(buf)
			if rErr != nil && n == 0 {
				return "读取文件失败：" + rErr.Error(), true
			}
			data = buf[:n]
			if int64(len(data)) > limit {
				data = data[:limit]
			}
		} else {
			files, fErr := snap.client.OpenFiles()
			if fErr != nil {
				return "打开 SFTP 通道失败：" + fErr.Error(), true
			}
			defer files.Close()
			data, _, err = files.ReadLimited(ctx, p, limit)
			if err != nil {
				return "读取远端文件失败：" + err.Error(), true
			}
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return fmt.Sprintf("文件 %s 包含二进制内容，已拒绝读取。", p), true
		}
		return string(data), false

	case "terminal_insert":
		var params struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &params)
		cmd := strings.TrimSpace(strings.TrimRight(params.Command, "\r\n"))
		if cmd == "" {
			return "command 不能为空", true
		}
		if strings.ContainsAny(cmd, "\r\n") {
			return "多行脚本不支持直接填入终端缓冲区。", true
		}
		if mode == aiModeObserver {
			return "当前为「只读观察」模式，未修改终端输入区。", true
		}
		dangerous := ai.IsDangerousCommand(cmd)
		if mode == aiModeConfirm || dangerous {
			approved, err := a.requestAgentApproval(ctx, seq, toolRec, cmd, "填入当前终端输入框（不回车）", snap.hostName, dangerous, snap.production)
			if err != nil || !approved {
				return "已取消填入终端。", true
			}
		}
		var pasted bool
		if err := a.runMainSync(ctx, func() {
			t := a.tabByID(snap.tabID)
			if t == nil {
				t = a.current()
			}
			if t == nil || t.focus == nil || t.focus.term == nil {
				return
			}
			safe := strings.NewReplacer("\r", "", "\n", "").Replace(cmd)
			t.focus.term.Paste(safe)
			pasted = true
		}); err != nil || !pasted {
			return "当前没有活动的终端窗格。", true
		}
		return "已将命令安全填入终端输入区（未按回车执行）：" + cmd, false

	default:
		return "未知工具：" + tc.Function.Name, true
	}
}

func captureTermTail(raw string, maxLines int) string {
	lines := strings.Split(raw, "\n")
	var nonEmpty []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty = append(nonEmpty, strings.TrimRight(line, "\r"))
		}
	}
	if len(nonEmpty) > maxLines {
		nonEmpty = nonEmpty[len(nonEmpty)-maxLines:]
	}
	return strings.Join(nonEmpty, "\n")
}

func execLocalCommand(ctx context.Context, command, dir string, maxBytes int) (sshconn.ExecResult, error) {
	shell := "/bin/sh"
	if runtime.GOOS == "windows" {
		shell = "cmd.exe"
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, shell, "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, shell, "-c", command)
	}
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	outStr := stdout.String()
	errStr := stderr.String()
	truncated := false
	if maxBytes > 0 {
		if len(outStr) > maxBytes {
			outStr = outStr[:maxBytes]
			truncated = true
		}
		if len(errStr) > maxBytes {
			errStr = errStr[:maxBytes]
			truncated = true
		}
	}
	res := sshconn.ExecResult{
		Stdout:    outStr,
		Stderr:    errStr,
		Truncated: truncated,
	}
	if err != nil {
		if ctx.Err() != nil {
			res.ExitCode = -1
			return res, ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return res, err
	}
	return res, nil
}

func formatExecResult(res sshconn.ExecResult) string {
	const maxForLLM = 12000
	var b strings.Builder
	b.WriteString(fmt.Sprintf("exit_code: %d\n", res.ExitCode))
	out := strings.TrimRight(res.Stdout, "\r\n")
	errOut := strings.TrimRight(res.Stderr, "\r\n")
	if out != "" {
		if len(out) > maxForLLM {
			out = out[:maxForLLM] + "\n...[stdout 输出过长已截断，建议使用 head/grep 精简查询]"
		}
		b.WriteString("--- stdout ---\n")
		b.WriteString(out)
		b.WriteByte('\n')
	}
	if errOut != "" {
		if len(errOut) > 4000 {
			errOut = errOut[:4000] + "\n...[stderr 已截断]"
		}
		b.WriteString("--- stderr ---\n")
		b.WriteString(errOut)
		b.WriteByte('\n')
	}
	if out == "" && errOut == "" {
		b.WriteString("(无输出)")
	}
	return strings.TrimSpace(b.String())
}
