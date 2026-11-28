package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"serval/core/ai"
	"serval/core/hosts"
	"serval/core/sshconn"
	"serval/locales"
)

// aiPermissionMode 控制 Agent 工具调用的执行权限。
type aiPermissionMode uint8

const (
	aiModeAuto     aiPermissionMode = iota // 自动执行：常规探测与命令自动执行，高危指令与生产变更仍拦截确认
	aiModeConfirm                          // 逐条确认：写/执行类工具需用户逐次点击确认
	aiModeObserver                         // 只读观察：仅允许读终端、读主机信息与 SFTP 只读探测
)

func (m aiPermissionMode) label() string {
	switch m {
	case aiModeObserver:
		return locales.AiModeObserver()
	case aiModeConfirm:
		return locales.AiModeConfirm()
	default:
		return locales.AiModeAuto()
	}
}

func (m aiPermissionMode) description() string {
	switch m {
	case aiModeObserver:
		return "只读终端/主机/SFTP，不执行命令"
	case aiModeConfirm:
		return "执行每条命令前弹窗/卡片确认"
	default:
		return "自动执行常规命令，高危/生产变更仍拦截"
	}
}

// aiSessionSnapshot 在主线程启动 Agent 前抓取当前目标终端/主机的只读快照，避免后台协程直接触碰 app 字段。
type aiSessionSnapshot struct {
	tabID          int
	hasTab         bool
	isLocal        bool
	protocol       string // "local", "ssh", "telnet", "serial"
	connected      bool
	termAlive      bool
	hostName       string
	hostAddr       string
	hostPort       int
	hostUser       string
	distro         string
	production     bool
	jumpCount      int
	jumpChain      []string
	activeForwards []string
	paneCount      int
	liveUser       string
	liveDir        string
	sftpDir        string
	client         *sshconn.Client
}

func (a *app) activeForwardsForTab(tabID int) []string {
	if len(a.tunnels) == 0 || tabID <= 0 {
		return nil
	}
	var out []string
	for id, lt := range a.tunnels {
		if lt == nil || lt.tabID != tabID || lt.tunnel == nil {
			continue
		}
		addr := lt.tunnel.Addr()
		if idx := slices.IndexFunc(a.forwards, func(f hosts.Forward) bool { return f.ID == id }); idx >= 0 {
			r := a.forwards[idx]
			desc := fmt.Sprintf("%s %s", r.Kind, addr)
			if r.TargetHost != "" && r.TargetPort > 0 {
				desc += fmt.Sprintf(" -> %s:%d", r.TargetHost, r.TargetPort)
			}
			out = append(out, desc)
		} else if addr != "" {
			out = append(out, addr)
		}
	}
	return out
}

func (a *app) captureAISessionSnapshot() aiSessionSnapshot {
	cur := a.current()
	snap := snapshotFromTab(cur)
	if cur != nil {
		snap.activeForwards = a.activeForwardsForTab(cur.id)
	}
	return snap
}

func snapshotFromTab(t *tab) aiSessionSnapshot {
	if t == nil {
		return aiSessionSnapshot{}
	}
	user, dir := paneUserAndDir(t, t.focus)
	proto := strings.ToLower(strings.TrimSpace(t.host.Protocol))
	if t.local {
		proto = "local"
	} else if proto == "" {
		proto = hosts.ProtocolSSH
	}
	termAlive := t.focus != nil && t.focus.term != nil && !t.focus.ended
	connected := t.local || t.client != nil || ((proto == hosts.ProtocolTelnet || proto == hosts.ProtocolSerial) && termAlive)
	jumps := hosts.SplitJumps(t.host.ProxyJump)
	panes := 1
	if t.root != nil {
		if n := t.root.leaves(); n > 0 {
			panes = n
		}
	}
	snap := aiSessionSnapshot{
		tabID:      t.id,
		hasTab:     true,
		isLocal:    t.local,
		protocol:   proto,
		connected:  connected,
		termAlive:  termAlive,
		hostName:   tabLabel(t),
		hostAddr:   t.host.Address,
		hostPort:   t.host.Port,
		hostUser:   t.host.User,
		distro:     t.host.Distro,
		production: t.host.Production,
		jumpCount:  len(jumps),
		jumpChain:  jumps,
		paneCount:  panes,
		liveUser:   user,
		liveDir:    dir,
		sftpDir:    t.dir,
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
	// 静态指令前缀保持稳定，利于 Anthropic / OpenAI Prompt Caching 命中。
	b.WriteString("你是 Serval 内置的高级 Linux/Unix 系统运维与故障排查 Agent。\n")
	b.WriteString("你可以调用提供的工具主动检查当前终端与服务器真实状态，绝不能凭空编造未实际查到的硬件规格、进程、端口或文件内容。\n\n")
	b.WriteString("工作原则：\n")
	b.WriteString("1. 当用户询问服务器配置、资源负载、端口进程、服务故障或日志问题时，优先调用工具获取真实输出，再结合多步结果给出结论。\n")
	b.WriteString("2. 尽量合并轻量级只读探测命令（如一次执行 `uname -a; uptime; lscpu | head -n 20; free -h; df -h`）；需要修改或新建配置文件时，优先使用 `sftp_edit` 或 `sftp_write`（带 Diff 审批），不要用 `cat > ... <<'EOF'` 拼写大脚本。\n")
	b.WriteString("3. 当 `ssh_exec` 或 `sftp_read` 返回结果被截断并附带 `handle_id` 时，可使用 `tool_output_read` 按行号分页或关键词检索完整输出，不要盲目重跑耗时命令。\n")
	b.WriteString("4. 如果某次工具调用被权限模式拒绝或用户拒绝，尊重该结果并改用只读方法或给出建议命令，不要反复重试被拒命令。\n")
	b.WriteString("5. 对于需要 3 步及以上的复杂排查、部署或修复任务，先调用 `update_plan` 建立结构化执行计划并在每一步完成后更新状态。\n")
	b.WriteString("6. 使用清晰的中文 Markdown 总结结果：对比/概览类信息优先使用 Markdown 表格（如 `| 项目 | 配置 |`），关键命令放在 ```bash 代码块中，异常项给出明确建议。\n\n")

	b.WriteString("当前环境状态：\n")
	b.WriteString(fmt.Sprintf("- 权限模式：%s\n", mode.label()))
	if !snap.hasTab {
		b.WriteString("- 当前没有打开的终端标签页。\n")
		return b.String()
	}
	switch snap.protocol {
	case "local":
		b.WriteString(fmt.Sprintf("- 目标类型：本机终端 (%s)\n", snap.distro))
	case hosts.ProtocolTelnet, hosts.ProtocolSerial:
		protoLabel := "Telnet"
		if snap.protocol == hosts.ProtocolSerial {
			protoLabel = "串口 Serial"
		}
		state := "已连接"
		if !snap.connected {
			state = "已断开"
		}
		b.WriteString(fmt.Sprintf("- 目标类型：%s 交互终端（%s），状态：%s\n", protoLabel, snap.hostName, state))
		b.WriteString("- 协议限制：Telnet 与串口终端不支持后台 `ssh_exec` 与 `sftp_*`；请使用 `terminal_read` 读取当前屏幕输出，并用 `terminal_insert` 将安全命令填入终端供用户确认执行。\n")
	default:
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
		if len(snap.jumpChain) > 0 {
			b.WriteString("- 跳板机链路：" + strings.Join(snap.jumpChain, " -> ") + "\n")
		}
		if len(snap.activeForwards) > 0 {
			b.WriteString("- 活动端口转发：" + strings.Join(snap.activeForwards, "; ") + "\n")
		}
	}
	if snap.paneCount > 1 {
		b.WriteString(fmt.Sprintf("- 当前标签分屏窗格数：%d\n", snap.paneCount))
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
				Description: "获取当前活动标签页的主机连接状态、协议类型、系统发行版、登录用户、工作目录、跳板机链路、活动端口转发及是否生产环境（不含密码与私钥等机密）。",
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
				Description: "在当前连接的 SSH 主机（或本机终端）的当前工作目录下执行非交互式 Shell 命令并返回 stdout、stderr 与 exit_code。",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"command":{"type":"string","description":"要执行的单次 Shell 命令"},
						"reason":{"type":"string","description":"简短说明执行该命令的目的（中文）"},
						"timeout_sec":{"type":"integer","description":"超时秒数，默认20，最大300"}
					},
					"required":["command"]
				}`),
			},
		},
		{
			Type: "function",
			Function: ai.ToolFunction{
				Name:        "sftp_list",
				Description: "通过 SFTP（或本地文件）只读列出远端目录内容，支持相对路径（基于当前工作目录）。",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"目标目录路径，留空则使用当前工作目录或主目录"}}}`),
			},
		},
		{
			Type: "function",
			Function: ai.ToolFunction{
				Name:        "sftp_read",
				Description: "通过 SFTP（或本地文件）只读读取文本文件或日志内容（自动拒绝二进制文件）；排查大日志时可设 tail=true 读取文件末尾。",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"要读取的文件路径，支持相对当前工作目录的路径"},
						"max_bytes":{"type":"integer","description":"读取字节上限，默认16384，最大32768"},
						"tail":{"type":"boolean","description":"为 true 时读取文件末尾最新内容，适合排查大日志"}
					},
					"required":["path"]
				}`),
			},
		},
		{
			Type: "function",
			Function: ai.ToolFunction{
				Name:        "sftp_write",
				Description: "通过 SFTP（或本地文件）新建或覆盖写入文本文件，并在执行前展示 Unified Diff 供人工审批。",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"目标文件路径"},
						"content":{"type":"string","description":"完整文件内容（UTF-8 文本）"},
						"reason":{"type":"string","description":"说明写入或修改目的（中文）"},
						"mode":{"type":"string","description":"可选八进制文件权限（如 0755、0644）"}
					},
					"required":["path","content"]
				}`),
			},
		},
		{
			Type: "function",
			Function: ai.ToolFunction{
				Name:        "sftp_edit",
				Description: "通过 SFTP（或本地文件）对已有文本文件做精确字符串替换（支持 Unified Diff 审批与并发冲突检测），适合修改配置文件或脚本。",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"目标文件路径"},
						"old_text":{"type":"string","description":"需要被替换的原文本片段（需精确匹配）"},
						"new_text":{"type":"string","description":"替换后的新文本片段"},
						"replace_all":{"type":"boolean","description":"是否替换全部匹配项，默认 false（要求唯一匹配）"},
						"reason":{"type":"string","description":"说明修改目的（中文）"}
					},
					"required":["path","old_text","new_text"]
				}`),
			},
		},
		{
			Type: "function",
			Function: ai.ToolFunction{
				Name:        "tool_output_read",
				Description: "分页或按关键词检索之前被截断的工具大输出（需传入返回消息中的 handle_id）。",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"handle_id":{"type":"string","description":"被截断输出附带的句柄 ID（例如 out_1）"},
						"offset_line":{"type":"integer","description":"起始行号（1-based），默认 1"},
						"limit_lines":{"type":"integer","description":"读取行数，默认 80，最大 200"},
						"grep":{"type":"string","description":"可选关键词过滤（不区分大小写）"}
					},
					"required":["handle_id"]
				}`),
			},
		},
		{
			Type: "function",
			Function: ai.ToolFunction{
				Name:        "update_plan",
				Description: "创建或更新当前复杂任务的结构化执行计划（Plan / Todo 进度条），向用户实时展示每一步状态。",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"steps":{
							"type":"array",
							"description":"有序执行步骤列表（最多 12 步）",
							"items":{
								"type":"object",
								"properties":{
									"title":{"type":"string","description":"步骤标题（简短明确）"},
									"status":{"type":"string","enum":["pending","in_progress","completed","failed"]},
									"note":{"type":"string","description":"可选的简短状态补充"}
								},
								"required":["title","status"]
							}
						}
					},
					"required":["steps"]
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

func isReadOnlyAgentTool(tc ai.ToolCall) bool {
	switch tc.Function.Name {
	case "host_info", "terminal_read", "sftp_list", "sftp_read", "tool_output_read":
		return true
	default:
		return false
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
	case "sftp_write":
		var p struct {
			Path   string `json:"path"`
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &p)
		if p.Path != "" {
			if p.Reason != "" {
				argsPretty = p.Reason + "\n写入 " + p.Path
			}
			return "写入文件 " + p.Path, argsPretty
		}
		return "写入文件", argsPretty
	case "sftp_edit":
		var p struct {
			Path   string `json:"path"`
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &p)
		if p.Path != "" {
			if p.Reason != "" {
				argsPretty = p.Reason + "\n修改 " + p.Path
			}
			return "编辑文件 " + p.Path, argsPretty
		}
		return "编辑文件", argsPretty
	case "tool_output_read":
		var p struct {
			HandleID string `json:"handle_id"`
			Grep     string `json:"grep"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &p)
		if p.HandleID != "" {
			if p.Grep != "" {
				return fmt.Sprintf("检索输出 %s (%s)", p.HandleID, p.Grep), argsPretty
			}
			return "读取完整输出 " + p.HandleID, argsPretty
		}
		return "读取完整输出", argsPretty
	case "update_plan":
		var p struct {
			Steps []aiPlanStep `json:"steps"`
		}
		if json.Unmarshal([]byte(rawArgs), &p) == nil && len(p.Steps) > 0 {
			done := 0
			for _, st := range p.Steps {
				if st.Status == planCompleted {
					done++
				}
			}
			return fmt.Sprintf("更新执行计划（已完成 %d/%d）", done, len(p.Steps)), argsPretty
		}
		return "更新执行计划", argsPretty
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

type aiTurnExecState struct {
	mu             sync.Mutex
	sftp           *sshconn.Files
	sftpClient     *sshconn.Client
	outputStore    *ai.ToolOutputStore
	sessionAllowed map[string]bool
	redact         bool
}

func (st *aiTurnExecState) filesFor(client *sshconn.Client) (*sshconn.Files, error) {
	if client == nil {
		return nil, errors.New("当前 SSH 客户端尚未就绪或已断开")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.sftp != nil && st.sftpClient == client {
		return st.sftp, nil
	}
	if st.sftp != nil {
		_ = st.sftp.Close()
		st.sftp = nil
		st.sftpClient = nil
	}
	f, err := client.OpenFiles()
	if err != nil {
		return nil, err
	}
	st.sftp = f
	st.sftpClient = client
	return f, nil
}

func (st *aiTurnExecState) close() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.sftp != nil {
		_ = st.sftp.Close()
		st.sftp = nil
		st.sftpClient = nil
	}
}

func (st *aiTurnExecState) isAllowedProg(prog string) bool {
	if prog == "" {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.sessionAllowed[prog]
}

func (st *aiTurnExecState) allowProg(prog string) {
	if prog == "" {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.sessionAllowed == nil {
		st.sessionAllowed = map[string]bool{}
	}
	st.sessionAllowed[prog] = true
}

func (st *aiTurnExecState) maybeRedact(s string) string {
	if !st.redact || s == "" {
		return s
	}
	out, _ := ai.RedactSecrets(s)
	return out
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

func (a *app) targetTabForSnapshot(snap aiSessionSnapshot) *tab {
	if snap.hasTab {
		return a.tabByID(snap.tabID)
	}
	return a.current()
}

func (a *app) requestAgentApproval(ctx context.Context, tabID int, convID string, seq uint64, tool *aiToolRecord, kind aiApprovalKind, cmd, reason, target, diff, filePath, baseProg string, dangerous, production bool) (aiApprovalDecision, error) {
	ch := make(chan aiApprovalDecision, 1)
	if err := a.runMainSync(ctx, func() {
		sess := a.aiSessionForTab(tabID)
		ap := &aiApproval{
			seq:        seq,
			tool:       tool,
			kind:       kind,
			command:    cmd,
			reason:     reason,
			target:     target,
			diff:       diff,
			filePath:   filePath,
			baseProg:   baseProg,
			dangerous:  dangerous,
			production: production,
			answer:     ch,
		}
		if conv := sess.convByID(convID); conv != nil {
			if seq != conv.ReqSeq {
				ch <- aiApprovalDecision{}
				return
			}
			if tool != nil {
				tool.Status = toolWaitingApproval
			}
			conv.PendingApproval = ap
			if sess.convID == convID {
				sess.pendingApproval = ap
			}
			return
		}
		if seq != sess.reqSeq {
			ch <- aiApprovalDecision{}
			return
		}
		if tool != nil {
			tool.Status = toolWaitingApproval
		}
		sess.pendingApproval = ap
	}); err != nil {
		return aiApprovalDecision{}, err
	}

	select {
	case dec := <-ch:
		return dec, nil
	case <-ctx.Done():
		a.post(func() {
			if tool != nil && tool.Status == toolWaitingApproval {
				tool.Status = toolDenied
			}
			sess := a.aiSessionForTab(tabID)
			if conv := sess.convByID(convID); conv != nil && conv.PendingApproval != nil && conv.PendingApproval.answer == ch {
				conv.PendingApproval = nil
			}
			if sess.pendingApproval != nil && sess.pendingApproval.answer == ch {
				sess.pendingApproval = nil
			}
		})
		return aiApprovalDecision{}, ctx.Err()
	}
}

func (a *app) refreshAISnapshot(ctx context.Context, snap aiSessionSnapshot) aiSessionSnapshot {
	var updated aiSessionSnapshot
	var ok bool
	if err := a.runMainSync(ctx, func() {
		t := a.targetTabForSnapshot(snap)
		if t == nil {
			return
		}
		updated = snapshotFromTab(t)
		updated.activeForwards = a.activeForwardsForTab(t.id)
		ok = true
	}); err != nil || !ok {
		return snap
	}
	return updated
}

func resolveRemoteAgentPath(p, liveDir string, files *sshconn.Files) string {
	p = strings.TrimSpace(p)
	if p == "" {
		if strings.HasPrefix(liveDir, "/") {
			return liveDir
		}
		if files != nil {
			if home, err := files.Home(); err == nil && home != "" {
				return home
			}
		}
		return "."
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if files != nil {
			if home, err := files.Home(); err == nil && home != "" {
				if p == "~" {
					return home
				}
				return path.Join(home, strings.TrimPrefix(p, "~/"))
			}
		}
		return p
	}
	if !strings.HasPrefix(p, "/") {
		if strings.HasPrefix(liveDir, "/") {
			return path.Join(liveDir, p)
		}
		if files != nil {
			if home, err := files.Home(); err == nil && home != "" {
				return path.Join(home, p)
			}
		}
	}
	return p
}

func resolveLocalAgentPath(p, liveDir string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		if liveDir != "" {
			return liveDir
		}
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return "."
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(p, "~/"))
		}
	}
	if !filepath.IsAbs(p) && liveDir != "" {
		return filepath.Join(liveDir, p)
	}
	return p
}

func wrapRemoteCommandWithDir(cmd, liveDir string) string {
	dir := strings.TrimSpace(liveDir)
	if !strings.HasPrefix(dir, "/") || strings.ContainsRune(dir, '\x00') {
		return cmd
	}
	quoted := "'" + strings.ReplaceAll(dir, "'", `'\''`) + "'"
	return "cd " + quoted + " 2>/dev/null; " + cmd
}

func readLocalFileHead(f *os.File, limit int64) (data []byte, totalSize int64, truncated bool, err error) {
	info, err := f.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	totalSize = info.Size()
	if totalSize == 0 {
		return nil, 0, false, nil
	}
	toRead := totalSize
	if toRead > limit {
		toRead = limit
		truncated = true
	}
	buf := make([]byte, toRead)
	n, rErr := io.ReadFull(f, buf)
	if rErr != nil && rErr != io.EOF && rErr != io.ErrUnexpectedEOF {
		return nil, totalSize, false, rErr
	}
	return buf[:n], totalSize, truncated, nil
}

func readLocalFileTail(f *os.File, limit int64) (data []byte, totalSize int64, truncated bool, err error) {
	info, err := f.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	totalSize = info.Size()
	if totalSize <= 0 {
		return nil, 0, false, nil
	}
	if totalSize > limit {
		truncated = true
		if _, err := f.Seek(-limit, io.SeekEnd); err != nil {
			return nil, totalSize, false, err
		}
	}
	buf := make([]byte, min(totalSize, limit))
	n, rErr := io.ReadFull(f, buf)
	if rErr != nil && rErr != io.EOF && rErr != io.ErrUnexpectedEOF {
		return nil, totalSize, false, rErr
	}
	data = buf[:n]
	if truncated {
		if idx := bytes.IndexByte(data, '\n'); idx >= 0 && idx+1 < len(data) {
			data = data[idx+1:]
		}
	}
	return data, totalSize, truncated, nil
}

func writeLocalFileAtomic(targetPath string, data []byte, expectExists bool, expectSize, expectUnix int64, mode fs.FileMode) error {
	info, err := os.Lstat(targetPath)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return errors.New("目标路径不是普通文件")
		}
		if !expectExists || info.Size() != expectSize || info.ModTime().Unix() != expectUnix {
			return sshconn.ErrRemoteChanged
		}
		if mode == 0 {
			mode = info.Mode().Perm()
		}
	case errors.Is(err, os.ErrNotExist):
		if expectExists {
			return sshconn.ErrRemoteChanged
		}
		if mode == 0 {
			mode = 0644
		}
	default:
		return err
	}
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, ".serval-aiwrite-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if mode != 0 {
		_ = os.Chmod(tmpName, mode.Perm())
	}
	if err = os.Rename(tmpName, targetPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (a *app) executeAgentTool(ctx context.Context, convID string, seq uint64, snap aiSessionSnapshot, mode aiPermissionMode, toolRec *aiToolRecord, tc ai.ToolCall) (string, bool) {
	return a.executeAgentToolStream(ctx, convID, seq, snap, mode, nil, toolRec, tc, nil)
}

func (a *app) executeAgentToolStream(ctx context.Context, convID string, seq uint64, snap aiSessionSnapshot, mode aiPermissionMode, turnState *aiTurnExecState, toolRec *aiToolRecord, tc ai.ToolCall, onProgress func(string)) (string, bool) {
	if turnState == nil {
		turnState = &aiTurnExecState{outputStore: ai.NewToolOutputStore(), redact: !a.local.DisableAIRedact}
		defer turnState.close()
	}
	snap = a.refreshAISnapshot(ctx, snap)

	switch tc.Function.Name {
	case "host_info":
		if !snap.hasTab {
			return "当前没有打开的终端标签页。", true
		}
		info := map[string]any{
			"is_local":        snap.isLocal,
			"protocol":        snap.protocol,
			"connected":       snap.connected,
			"term_alive":      snap.termAlive,
			"host_name":       snap.hostName,
			"host_address":    snap.hostAddr,
			"port":            snap.hostPort,
			"user":            snap.liveUser,
			"cwd":             snap.liveDir,
			"sftp_dir":        snap.sftpDir,
			"distro":          snap.distro,
			"production":      snap.production,
			"jump_count":      snap.jumpCount,
			"jump_chain":      snap.jumpChain,
			"active_forwards": snap.activeForwards,
			"pane_count":      snap.paneCount,
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
			t := a.targetTabForSnapshot(snap)
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
		return fmt.Sprintf("user=%s cwd=%s\n--- 终端最近输出 ---\n%s", user, dir, turnState.maybeRedact(text)), false

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
		if snap.protocol == hosts.ProtocolTelnet || snap.protocol == hosts.ProtocolSerial {
			return fmt.Sprintf("当前会话协议为 %s，不支持后台 ssh_exec。请使用 terminal_read 读取屏幕输出，或用 terminal_insert 将单行命令填入终端供用户确认后执行。", snap.protocol), true
		}
		if !snap.isLocal && snap.client == nil {
			return "当前 SSH 客户端尚未就绪或已断开，无法执行命令。", true
		}
		if mode == aiModeObserver {
			return "当前权限模式为「只读观察」，已阻止执行命令。如需执行请提示用户切换到「确认执行」或「自动执行」。", true
		}

		dangerous := ai.IsDangerousExecCommand(cmd)
		baseProg := ""
		if !dangerous {
			baseProg = ai.BaseCommandProgram(cmd)
		}
		sessionGranted := baseProg != "" && turnState.isAllowedProg(baseProg)
		needConfirm := dangerous || (!sessionGranted && (mode == aiModeConfirm || (snap.production && !ai.IsReadOnlyProbeCommand(cmd))))
		if needConfirm {
			target := snap.hostName
			if snap.liveUser != "" && snap.hostAddr != "" {
				target = snap.liveUser + "@" + snap.hostAddr
			}
			dec, err := a.requestAgentApproval(ctx, snap.tabID, convID, seq, toolRec, approvalExec, cmd, params.Reason, target, "", "", baseProg, dangerous, snap.production)
			if err != nil {
				return "等待确认已取消", true
			}
			if !dec.approved {
				if toolRec != nil {
					_ = a.runMainSync(ctx, func() { toolRec.Status = toolDenied })
				}
				return "用户拒绝执行该命令。", true
			}
			if edited := strings.TrimSpace(dec.editedCmd); edited != "" && edited != cmd {
				cmd = edited
				if toolRec != nil {
					_ = a.runMainSync(ctx, func() {
						toolRec.Summary = "$ " + cmd
						if params.Reason != "" {
							toolRec.Args = params.Reason + "\n$ " + cmd
						} else {
							toolRec.Args = "$ " + cmd
						}
					})
				}
			}
			if dec.allowSession && !dangerous && baseProg != "" {
				turnState.allowProg(baseProg)
				_ = a.runMainSync(ctx, func() {
					sess := a.aiSessionForTab(snap.tabID)
					if conv := sess.convByID(convID); conv != nil {
						if conv.SessionAllowedProgs == nil {
							conv.SessionAllowedProgs = map[string]bool{}
						}
						conv.SessionAllowedProgs[baseProg] = true
					}
				})
			}
			if toolRec != nil {
				_ = a.runMainSync(ctx, func() { toolRec.Status = toolRunning })
			}
		}

		timeout := time.Duration(params.TimeoutSec) * time.Second
		if timeout <= 0 {
			timeout = 20 * time.Second
		}
		if timeout > 300*time.Second {
			timeout = 300 * time.Second
		}
		execCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		var liveMu sync.Mutex
		var liveBuf strings.Builder
		var lastEmit time.Time
		emitChunk := func(stream, chunk string) {
			if onProgress == nil || chunk == "" {
				return
			}
			liveMu.Lock()
			if liveBuf.Len() < 8192 {
				if stream == "stderr" && liveBuf.Len() == 0 {
					liveBuf.WriteString("[stderr] ")
				}
				liveBuf.WriteString(chunk)
			}
			now := time.Now()
			shouldEmit := now.Sub(lastEmit) >= 120*time.Millisecond
			var snapshot string
			if shouldEmit {
				lastEmit = now
				snapshot = liveBuf.String()
			}
			liveMu.Unlock()
			if shouldEmit {
				onProgress(turnState.maybeRedact(snapshot))
			}
		}

		var res sshconn.ExecResult
		var err error
		if snap.isLocal {
			res, err = execLocalCommandStream(execCtx, cmd, snap.liveDir, 256<<10, emitChunk)
		} else {
			remoteCmd := wrapRemoteCommandWithDir(cmd, snap.liveDir)
			res, err = snap.client.ExecContextStream(execCtx, remoteCmd, 256<<10, emitChunk)
		}
		if err != nil {
			return "命令执行失败：" + err.Error(), true
		}
		return formatExecResultWithStore(res, turnState), res.ExitCode != 0

	case "sftp_list":
		if !snap.hasTab || !snap.connected {
			return "当前没有已连接的终端会话。", true
		}
		if snap.protocol == hosts.ProtocolTelnet || snap.protocol == hosts.ProtocolSerial {
			return fmt.Sprintf("当前会话协议为 %s，不支持 SFTP 文件浏览。", snap.protocol), true
		}
		var params struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &params)
		targetPath := strings.TrimSpace(params.Path)
		if snap.isLocal {
			targetPath = resolveLocalAgentPath(targetPath, snap.liveDir)
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
				var size int64
				if info, iErr := e.Info(); iErr == nil {
					size = info.Size()
				}
				lines = append(lines, fmt.Sprintf("%s  %-10d  %s", kind, size, e.Name()))
			}
			return fmt.Sprintf("目录 %s：\n%s", targetPath, strings.Join(lines, "\n")), false
		}

		files, err := turnState.filesFor(snap.client)
		if err != nil {
			return "打开 SFTP 通道失败：" + err.Error(), true
		}
		targetPath = resolveRemoteAgentPath(targetPath, snap.liveDir, files)
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
		if snap.protocol == hosts.ProtocolTelnet || snap.protocol == hosts.ProtocolSerial {
			return fmt.Sprintf("当前会话协议为 %s，不支持 SFTP 文件读取。", snap.protocol), true
		}
		var params struct {
			Path     string `json:"path"`
			MaxBytes int    `json:"max_bytes"`
			Tail     bool   `json:"tail"`
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
		var totalSize int64
		var truncated bool
		var err error
		if snap.isLocal {
			p = resolveLocalAgentPath(p, snap.liveDir)
			var f *os.File
			f, err = os.Open(p)
			if err != nil {
				return "读取文件失败：" + err.Error(), true
			}
			defer f.Close()
			if params.Tail {
				data, totalSize, truncated, err = readLocalFileTail(f, limit)
			} else {
				data, totalSize, truncated, err = readLocalFileHead(f, limit)
			}
			if err != nil {
				return "读取文件失败：" + err.Error(), true
			}
		} else {
			files, fErr := turnState.filesFor(snap.client)
			if fErr != nil {
				return "打开 SFTP 通道失败：" + fErr.Error(), true
			}
			p = resolveRemoteAgentPath(p, snap.liveDir, files)
			size, _, _, statErr := files.Stat(p)
			if statErr == nil {
				totalSize = size
			}
			if params.Tail {
				data, _, truncated, err = files.ReadTailLimited(ctx, p, limit)
			} else {
				data, _, err = files.ReadLimited(ctx, p, limit)
				if errors.Is(err, sshconn.ErrFileTooLarge) {
					// 超出 limit 时回退读末尾或前段提示，并尽量把前 256KB 存入 ToolOutputStore 供检索
					if fullData, _, fullErr := files.ReadLimited(ctx, p, 256<<10); fullErr == nil {
						truncated = true
						redactedFull := turnState.maybeRedact(string(fullData))
						hid := turnState.outputStore.Put("sftp_read:"+p, redactedFull)
						preview, _ := sanitizeToolText(redactedFull, int(limit))
						return fmt.Sprintf("[文件 %s 共 %d 字节，已截取前 %d 字节；handle_id: %s，可用 tool_output_read 分页/检索]\n%s",
							p, totalSize, len(preview), hid, preview), false
					}
					return fmt.Sprintf("文件 %s 体积较大（%d 字节，超过 %d 字节上限），请设置 tail=true 读取末尾日志，或使用 ssh_exec 配合 head/grep 查看。", p, totalSize, limit), true
				}
			}
			if err != nil {
				return "读取远端文件失败：" + err.Error(), true
			}
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return fmt.Sprintf("文件 %s 包含二进制内容，已拒绝读取。", p), true
		}
		text := turnState.maybeRedact(string(data))
		if truncated {
			hid := ""
			if turnState.outputStore != nil {
				hid = turnState.outputStore.Put("sftp_read:"+p, text)
			}
			if hid != "" {
				return fmt.Sprintf("[文件 %s 共 %d 字节，已按 tail=%v 截取（%d 字节）；handle_id: %s]\n%s",
					p, totalSize, params.Tail, len(data), hid, text), false
			}
			return fmt.Sprintf("[文件 %s 共 %d 字节，已截取 %d 字节]\n%s", p, totalSize, len(data), text), false
		}
		return text, false

	case "sftp_write":
		if !snap.hasTab || !snap.connected {
			return "当前没有已连接的终端会话。", true
		}
		if snap.protocol == hosts.ProtocolTelnet || snap.protocol == hosts.ProtocolSerial {
			return fmt.Sprintf("当前会话协议为 %s，不支持 SFTP 文件写入。", snap.protocol), true
		}
		if mode == aiModeObserver {
			return "当前权限模式为「只读观察」，已阻止写入文件。", true
		}
		var params struct {
			Path    string `json:"path"`
			Content string `json:"content"`
			Reason  string `json:"reason"`
			Mode    string `json:"mode"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
			return "参数解析失败：" + err.Error(), true
		}
		p := strings.TrimSpace(params.Path)
		if p == "" {
			return "path 不能为空", true
		}
		if strings.ContainsRune(params.Content, '\x00') {
			return "拒绝写入包含二进制 NUL 字节的内容", true
		}
		var fileMode fs.FileMode
		if mStr := strings.TrimSpace(params.Mode); mStr != "" {
			parsed, mErr := strconv.ParseUint(mStr, 8, 32)
			if mErr != nil {
				return "无效的文件权限 mode：" + mStr, true
			}
			fileMode = fs.FileMode(parsed)
		}

		var before string
		var expectExists bool
		var expectSize, expectUnix int64
		var files *sshconn.Files

		if snap.isLocal {
			p = resolveLocalAgentPath(p, snap.liveDir)
			if info, err := os.Lstat(p); err == nil {
				if !info.Mode().IsRegular() {
					return "目标路径不是普通文件，无法写入", true
				}
				expectExists = true
				expectSize = info.Size()
				expectUnix = info.ModTime().Unix()
				if expectSize <= 256<<10 {
					if raw, rErr := os.ReadFile(p); rErr == nil && bytes.IndexByte(raw, 0) < 0 {
						before = string(raw)
					}
				}
			}
		} else {
			var fErr error
			files, fErr = turnState.filesFor(snap.client)
			if fErr != nil {
				return "打开 SFTP 通道失败：" + fErr.Error(), true
			}
			p = resolveRemoteAgentPath(p, snap.liveDir, files)
			if size, mod, fMode, sErr := files.Stat(p); sErr == nil {
				if !fMode.IsRegular() {
					return "远端路径不是普通文件，无法写入", true
				}
				expectExists = true
				expectSize = size
				expectUnix = mod.Unix()
				if size <= 256<<10 {
					if raw, _, rErr := files.ReadLimited(ctx, p, 256<<10); rErr == nil {
						before = string(raw)
					}
				}
			}
		}

		diffText := ai.UnifiedDiff(p, before, params.Content, 3)
		if diffText == "" {
			return "文件内容未发生变化，无需写入。", false
		}
		sensitive := ai.IsSensitiveWritePath(p)
		needConfirm := sensitive || mode == aiModeConfirm || snap.production || expectExists
		if needConfirm {
			target := snap.hostName
			if snap.liveUser != "" && snap.hostAddr != "" {
				target = snap.liveUser + "@" + snap.hostAddr
			}
			dec, err := a.requestAgentApproval(ctx, snap.tabID, convID, seq, toolRec, approvalFileWrite, params.Content, params.Reason, target, diffText, p, "", sensitive, snap.production)
			if err != nil {
				return "等待确认已取消", true
			}
			if !dec.approved {
				if toolRec != nil {
					_ = a.runMainSync(ctx, func() { toolRec.Status = toolDenied })
				}
				return "用户拒绝写入该文件。", true
			}
			if toolRec != nil {
				_ = a.runMainSync(ctx, func() { toolRec.Status = toolRunning })
			}
		}

		if snap.isLocal {
			if err := writeLocalFileAtomic(p, []byte(params.Content), expectExists, expectSize, expectUnix, fileMode); err != nil {
				return "写入本地文件失败：" + err.Error(), true
			}
		} else {
			if err := files.WriteOrCreateFile(ctx, p, []byte(params.Content), expectExists, expectSize, expectUnix, fileMode, false); err != nil {
				return "写入远端文件失败：" + err.Error(), true
			}
		}
		return fmt.Sprintf("已写入文件 %s（%d 字节）", p, len(params.Content)), false

	case "sftp_edit":
		if !snap.hasTab || !snap.connected {
			return "当前没有已连接的终端会话。", true
		}
		if snap.protocol == hosts.ProtocolTelnet || snap.protocol == hosts.ProtocolSerial {
			return fmt.Sprintf("当前会话协议为 %s，不支持 SFTP 文件编辑。", snap.protocol), true
		}
		if mode == aiModeObserver {
			return "当前权限模式为「只读观察」，已阻止编辑文件。", true
		}
		var params struct {
			Path       string `json:"path"`
			OldText    string `json:"old_text"`
			NewText    string `json:"new_text"`
			ReplaceAll bool   `json:"replace_all"`
			Reason     string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
			return "参数解析失败：" + err.Error(), true
		}
		p := strings.TrimSpace(params.Path)
		if p == "" || params.OldText == "" {
			return "path 与 old_text 不能为空", true
		}
		if strings.ContainsRune(params.NewText, '\x00') {
			return "拒绝写入包含二进制 NUL 字节的内容", true
		}

		var before string
		var expectSize, expectUnix int64
		var files *sshconn.Files

		if snap.isLocal {
			p = resolveLocalAgentPath(p, snap.liveDir)
			info, err := os.Lstat(p)
			if err != nil {
				return "读取文件失败：" + err.Error(), true
			}
			if !info.Mode().IsRegular() {
				return "目标路径不是普通文件", true
			}
			if info.Size() > 256<<10 {
				return "文件超过 256KB，请改用分段脚本处理", true
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				return "读取文件失败：" + err.Error(), true
			}
			if bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) {
				return "目标文件不是 UTF-8 文本文件，拒绝编辑", true
			}
			before = string(raw)
			expectSize = info.Size()
			expectUnix = info.ModTime().Unix()
		} else {
			var fErr error
			files, fErr = turnState.filesFor(snap.client)
			if fErr != nil {
				return "打开 SFTP 通道失败：" + fErr.Error(), true
			}
			p = resolveRemoteAgentPath(p, snap.liveDir, files)
			size, mod, fMode, sErr := files.Stat(p)
			if sErr != nil {
				return "读取远端文件失败：" + sErr.Error(), true
			}
			if !fMode.IsRegular() {
				return "远端路径不是普通文件", true
			}
			if size > 256<<10 {
				return "文件超过 256KB，请改用分段脚本处理", true
			}
			raw, mod2, rErr := files.ReadLimited(ctx, p, 256<<10)
			if rErr != nil {
				return "读取远端文件失败：" + rErr.Error(), true
			}
			before = string(raw)
			expectSize = size
			if !mod2.IsZero() {
				expectUnix = mod2.Unix()
			} else {
				expectUnix = mod.Unix()
			}
		}

		matches := strings.Count(before, params.OldText)
		if matches == 0 {
			return "未在文件中找到完全匹配的 old_text，请先用 sftp_read 核对上下文。", true
		}
		if matches > 1 && !params.ReplaceAll {
			return fmt.Sprintf("old_text 在文件中出现 %d 次，请提供更具体的上下文或设置 replace_all=true。", matches), true
		}
		var after string
		if params.ReplaceAll {
			after = strings.ReplaceAll(before, params.OldText, params.NewText)
		} else {
			after = strings.Replace(before, params.OldText, params.NewText, 1)
		}
		if after == before {
			return "替换后文件内容无变化。", false
		}

		diffText := ai.UnifiedDiff(p, before, after, 3)
		sensitive := ai.IsSensitiveWritePath(p)
		// 修改已有文件始终展示 Unified Diff 供人工确认，防止静默篡改配置
		target := snap.hostName
		if snap.liveUser != "" && snap.hostAddr != "" {
			target = snap.liveUser + "@" + snap.hostAddr
		}
		dec, err := a.requestAgentApproval(ctx, snap.tabID, convID, seq, toolRec, approvalFileWrite, after, params.Reason, target, diffText, p, "", sensitive, snap.production)
		if err != nil {
			return "等待确认已取消", true
		}
		if !dec.approved {
			if toolRec != nil {
				_ = a.runMainSync(ctx, func() { toolRec.Status = toolDenied })
			}
			return "用户拒绝编辑该文件。", true
		}
		if toolRec != nil {
			_ = a.runMainSync(ctx, func() { toolRec.Status = toolRunning })
		}

		if snap.isLocal {
			if err := writeLocalFileAtomic(p, []byte(after), true, expectSize, expectUnix, 0); err != nil {
				return "保存本地文件失败：" + err.Error(), true
			}
		} else {
			if err := files.WriteFile(ctx, p, []byte(after), expectSize, expectUnix, false); err != nil {
				return "保存远端文件失败：" + err.Error(), true
			}
		}
		return fmt.Sprintf("已更新文件 %s（替换 %d 处）", p, func() int {
			if params.ReplaceAll {
				return matches
			}
			return 1
		}()), false

	case "tool_output_read":
		var params struct {
			HandleID   string `json:"handle_id"`
			OffsetLine int    `json:"offset_line"`
			LimitLines int    `json:"limit_lines"`
			Grep       string `json:"grep"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
			return "参数解析失败：" + err.Error(), true
		}
		if turnState.outputStore == nil {
			return "当前会话暂无缓存的完整输出。", true
		}
		out, err := turnState.outputStore.Read(params.HandleID, params.OffsetLine, params.LimitLines, params.Grep)
		if err != nil {
			return err.Error(), true
		}
		return turnState.maybeRedact(out), false

	case "update_plan":
		var params struct {
			Steps []aiPlanStep `json:"steps"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
			return "参数解析失败：" + err.Error(), true
		}
		clean := make([]aiPlanStep, 0, min(len(params.Steps), 12))
		done := 0
		for _, st := range params.Steps {
			title := strings.TrimSpace(st.Title)
			if title == "" {
				continue
			}
			status := st.Status
			switch status {
			case planPending, planInProgress, planCompleted, planFailed:
			default:
				status = planPending
			}
			if status == planCompleted {
				done++
			}
			clean = append(clean, aiPlanStep{
				Title:  title,
				Status: status,
				Note:   strings.TrimSpace(st.Note),
			})
			if len(clean) >= 12 {
				break
			}
		}
		if err := a.runMainSync(ctx, func() {
			sess := a.aiSessionForTab(snap.tabID)
			if conv := sess.convByID(convID); conv != nil {
				wasEmpty := len(conv.Plan) == 0
				conv.Plan = clean
				if len(clean) == 0 {
					conv.PlanExpanded = false
				} else if wasEmpty {
					conv.PlanExpanded = true
				}
				if sess.convID == convID {
					sess.plan = conv.Plan
					sess.planExpanded = conv.PlanExpanded
				}
			} else if sess.convID == "" || sess.convID == convID {
				wasEmpty := len(sess.plan) == 0
				sess.plan = clean
				if len(clean) == 0 {
					sess.planExpanded = false
				} else if wasEmpty {
					sess.planExpanded = true
				}
			}
		}); err != nil {
			return "更新执行计划失败：" + err.Error(), true
		}
		return fmt.Sprintf("已更新执行计划（共 %d 步，已完成 %d 步）。", len(clean), done), false

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
			dec, err := a.requestAgentApproval(ctx, snap.tabID, convID, seq, toolRec, approvalInsert, cmd, "填入当前终端输入框（不回车）", snap.hostName, "", "", "", dangerous, snap.production)
			if err != nil || !dec.approved {
				return "已取消填入终端。", true
			}
			if edited := strings.TrimSpace(dec.editedCmd); edited != "" {
				cmd = edited
			}
		}
		var pasted bool
		if err := a.runMainSync(ctx, func() {
			t := a.targetTabForSnapshot(snap)
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

func sanitizeToolText(s string, maxBytes int) (string, bool) {
	if s == "" {
		return "", false
	}
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ToValidUTF8(s, "\uFFFD")
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

func formatExecResult(res sshconn.ExecResult) string {
	return formatExecResultWithStore(res, nil)
}

func formatExecResultWithStore(res sshconn.ExecResult, turnState *aiTurnExecState) string {
	const maxForLLM = 12000
	var b strings.Builder
	b.WriteString(fmt.Sprintf("exit_code: %d\n", res.ExitCode))

	rawOut := strings.TrimRight(res.Stdout, "\r\n")
	rawErr := strings.TrimRight(res.Stderr, "\r\n")
	if turnState != nil {
		rawOut = turnState.maybeRedact(rawOut)
		rawErr = turnState.maybeRedact(rawErr)
	}

	out, outTrunc := sanitizeToolText(rawOut, maxForLLM)
	errOut, errTrunc := sanitizeToolText(rawErr, 4000)

	handleNotice := ""
	if (outTrunc || errTrunc) && turnState != nil && turnState.outputStore != nil {
		var full strings.Builder
		if rawOut != "" {
			full.WriteString("--- stdout ---\n" + rawOut + "\n")
		}
		if rawErr != "" {
			full.WriteString("--- stderr ---\n" + rawErr + "\n")
		}
		if hid := turnState.outputStore.Put("ssh_exec", full.String()); hid != "" {
			handleNotice = fmt.Sprintf("（完整输出已缓存，handle_id: %s，可用 tool_output_read 分页/检索）", hid)
		}
	}

	if out != "" {
		if outTrunc {
			if handleNotice != "" {
				out += "\n...[stdout 已截断 " + handleNotice + "]"
			} else {
				out += "\n...[stdout 输出过长已截断，建议使用 head/grep 精简查询]"
			}
		}
		b.WriteString("--- stdout ---\n")
		b.WriteString(out)
		b.WriteByte('\n')
	}
	if errOut != "" {
		if errTrunc {
			errOut += "\n...[stderr 已截断]"
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
