// Package ai 提供面向终端的 AI 助手核心能力，基于 OpenAI 兼容的 Chat Completions API。
// 纯 Go 标准库实现，支持自定义 BaseURL、APIKey、Model。
// 不依赖 UI。
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	// ErrNoAPIKey 表示未配置 API Key。
	ErrNoAPIKey = errors.New("未配置 AI API Key")
	// ErrEmptyResponse 表示 AI 服务返回空内容。
	ErrEmptyResponse = errors.New("AI 服务返回内容为空")
	// ErrUnauthorized 表示 API Key 无效或无权访问。
	ErrUnauthorized = errors.New("API Key 无效或无访问权限")
	// ErrRateLimited 表示请求过于频繁或额度不足。
	ErrRateLimited = errors.New("AI 服务请求过于频繁或额度已用尽")
	// ErrBadGateway 表示服务端暂时不可用。
	ErrBadGateway = errors.New("AI 服务暂时不可用")
	// ErrTimeout 表示请求超时。
	ErrTimeout = errors.New("请求 AI 服务超时")
	// ErrInvalidEndpoint 表示 API 端点地址无效。
	ErrInvalidEndpoint = errors.New("无效的 API 端点地址")
)

// DefaultBaseURL 是默认的 OpenAI 兼容端点。
const DefaultBaseURL = "https://api.openai.com/v1"

// DefaultModel 是默认的快速低成本模型。
const DefaultModel = "gpt-4o-mini"

// Config 是 AI 客户端配置。
type Config struct {
	BaseURL string // 如 https://api.openai.com/v1 或本地 http://localhost:11434/v1
	APIKey  string // API 访问密钥
	Model   string // 模型名称，如 gpt-4o-mini, deepseek-chat, qwen-turbo
	Timeout time.Duration
}

// Client 是 OpenAI 兼容客户端。
type Client struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

// NewClient 创建 AI 客户端。
func NewClient(cfg Config) *Client {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultModel
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL: baseURL,
		apiKey:  strings.TrimSpace(cfg.APIKey),
		model:   model,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// Message 代表一条聊天消息。
type Message struct {
	Role             string     `json:"role"` // "system", "user", "assistant", "tool"
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float32   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func isLocalEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func mapHTTPError(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ErrUnauthorized
	case status == http.StatusTooManyRequests:
		return ErrRateLimited
	case status >= 500:
		return ErrBadGateway
	default:
		return fmt.Errorf("HTTP %d", status)
	}
}

func mapTransportError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ErrTimeout
	}
	return err
}

// Probe 检测 AI 服务连通性。优先请求 GET /models（不消耗 token），
// 若服务端不支持再回退到最短对话（max_tokens=1）。
func (c *Client) Probe(ctx context.Context) error {
	if c.apiKey == "" && !isLocalEndpoint(c.baseURL) {
		return ErrNoAPIKey
	}
	u, err := url.Parse(c.baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ErrInvalidEndpoint
	}

	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/models", nil)
	if err != nil {
		return ErrInvalidEndpoint
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return mapTransportError(err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden ||
		resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return mapHTTPError(resp.StatusCode)
	}

	// 部分兼容端点未实现 GET /models（返回 404/405），回退到最短对话。
	_, err = c.chatWithOptions(ctx, []Message{{Role: "user", Content: "hi"}}, 1)
	return err
}

// Chat 发送一条对话请求并返回响应文本。
func (c *Client) Chat(ctx context.Context, messages []Message) (string, error) {
	return c.chatWithOptions(ctx, messages, 0)
}

func (c *Client) chatWithOptions(ctx context.Context, messages []Message, maxTokens int) (string, error) {
	if c.apiKey == "" && !isLocalEndpoint(c.baseURL) {
		return "", ErrNoAPIKey
	}
	u, err := url.Parse(c.baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", ErrInvalidEndpoint
	}

	endpoint := c.baseURL + "/chat/completions"
	reqBody, err := json.Marshal(chatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0.2, // 保持命令生成和错误分析严谨稳定
		MaxTokens:   maxTokens,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", ErrInvalidEndpoint
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", mapTransportError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", mapTransportError(err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", mapHTTPError(resp.StatusCode)
	}

	var chatResp chatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return "", fmt.Errorf("解析响应失败: %w", err)
	}

	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return "", ErrEmptyResponse
	}

	return strings.TrimSpace(chatResp.Choices[0].Message.Content), nil
}

// Result 是生成命令或错误分析的解析结果。
type Result struct {
	Explanation string   // 简明原理解释
	Command     string   // 主推荐命令（第一个代码块）
	Commands    []string // 提取出的全部代码块命令
	Raw         string   // 完整原始文本
}

// GenerateCommand 根据用户的自然语言需求，生成对应操作系统的 Shell 命令。
func (c *Client) GenerateCommand(ctx context.Context, osInfo, query string) (*Result, error) {
	systemPrompt := "你是一名资深 Linux/Unix 运维专家。根据用户的需求，生成安全、准确的 Shell 命令。\n" +
		"规则：\n" +
		"1. 输出第一部分简要解释原理（中文，1-2 句话）。\n" +
		"2. 输出第二部分将命令放在 ```bash 代码块中，仅包含可执行命令，不要添加多余文字。\n" +
		"3. 若涉及高危危险操作（如 rm -rf、格式化磁盘），必须在解释中做醒目警告。"

	var userPrompt string
	if osInfo != "" {
		userPrompt = fmt.Sprintf("目标环境：%s\n需求：%s", osInfo, query)
	} else {
		userPrompt = query
	}

	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	raw, err := c.Chat(ctx, messages)
	if err != nil {
		return nil, err
	}

	return parseMarkdownCommand(raw), nil
}

// ExplainError 分析终端报错并给出修复建议和命令。
func (c *Client) ExplainError(ctx context.Context, osInfo, errorText string) (*Result, error) {
	systemPrompt := "你是一名资深系统运维与故障排查专家。根据终端报错信息，分析可能的原因并提供修复方案。\n" +
		"规则：\n" +
		"1. 第一部分简要解释报错根因（中文，2-3 句话）。\n" +
		"2. 若有排查或修复命令，放在 ```bash 代码块中。\n" +
		"3. 不要包含虚构或不可靠的猜想。"

	var userPrompt string
	if osInfo != "" {
		userPrompt = fmt.Sprintf("运行环境：%s\n终端报错信息：\n%s", osInfo, errorText)
	} else {
		userPrompt = fmt.Sprintf("终端报错信息：\n%s", errorText)
	}

	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	raw, err := c.Chat(ctx, messages)
	if err != nil {
		return nil, err
	}

	return parseMarkdownCommand(raw), nil
}

// FollowUp 基于本次会话历史继续追问。
func (c *Client) FollowUp(ctx context.Context, osInfo string, history []Message, query string) (*Result, error) {
	systemPrompt := "你是一名资深 Linux/Unix 运维专家。结合前面的对话上下文回答用户的追问。\n" +
		"规则：\n" +
		"1. 简明解释（中文，1-3 句话）。\n" +
		"2. 若给出可执行命令，放在 ```bash 代码块中。"
	if osInfo != "" {
		systemPrompt += "\n目标环境：" + osInfo
	}

	messages := make([]Message, 0, len(history)+2)
	messages = append(messages, Message{Role: "system", Content: systemPrompt})
	messages = append(messages, history...)
	messages = append(messages, Message{Role: "user", Content: query})

	raw, err := c.Chat(ctx, messages)
	if err != nil {
		return nil, err
	}
	return parseMarkdownCommand(raw), nil
}

// ParseCommandText 从包含 Markdown 代码块或 JSON 的文本中提取命令与解释说明。
func ParseCommandText(text string) *Result {
	return parseMarkdownCommand(text)
}

// parseMarkdownCommand 从包含 Markdown 代码块或 JSON 的文本中提取命令与解释说明。
func parseMarkdownCommand(text string) *Result {
	res := &Result{Raw: text}
	trimmed := strings.TrimSpace(text)

	// 1. 优先尝试解析 JSON 格式
	var jsonObj struct {
		Explanation string `json:"explanation"`
		Command     string `json:"command"`
	}
	if strings.HasPrefix(trimmed, "{") && json.Unmarshal([]byte(trimmed), &jsonObj) == nil {
		if jsonObj.Command != "" || jsonObj.Explanation != "" {
			res.Explanation = jsonObj.Explanation
			res.Command = strings.TrimSpace(jsonObj.Command)
			if res.Command != "" {
				res.Commands = []string{res.Command}
			}
			return res
		}
	}

	// 2. Markdown 多代码块解析
	rest := text
	var explainParts []string
	for {
		idx := strings.Index(rest, "```")
		if idx == -1 {
			if tail := strings.TrimSpace(rest); tail != "" {
				explainParts = append(explainParts, tail)
			}
			break
		}
		if head := strings.TrimSpace(rest[:idx]); head != "" {
			explainParts = append(explainParts, head)
		}
		after := rest[idx+3:]
		if nl := strings.Index(after, "\n"); nl != -1 {
			after = after[nl+1:]
		}
		endIdx := strings.Index(after, "```")
		var block string
		if endIdx != -1 {
			block = strings.TrimSpace(after[:endIdx])
			rest = after[endIdx+3:]
		} else {
			block = strings.TrimSpace(after)
			rest = ""
		}
		if block != "" {
			res.Commands = append(res.Commands, block)
		}
		if endIdx == -1 {
			break
		}
	}

	res.Explanation = strings.Join(explainParts, "\n")
	if len(res.Commands) > 0 {
		res.Command = res.Commands[0]
	}
	return res
}

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

	// 按 shell 分隔符切分子命令
	segments := splitShellSegments(trimmed)
	for i, seg := range segments {
		tokens := shellFields(seg)
		if len(tokens) == 0 {
			continue
		}

		// 检查管道到 shell（如 curl ... | sh, wget ... | bash）
		if i > 0 {
			first := strings.ToLower(tokens[0])
			if first == "sudo" && len(tokens) > 1 {
				first = strings.ToLower(tokens[1])
			}
			if first == "sh" || first == "bash" || first == "zsh" || first == "dash" {
				return true
			}
		}

		// 检查重定向到设备节点或关键系统文件
		if hasDangerousRedirect(tokens) {
			return true
		}

		// 剥离前置 sudo / env 等包装命令
		idx := 0
		for idx < len(tokens) {
			low := strings.ToLower(tokens[idx])
			if low == "sudo" || low == "doas" || low == "env" || strings.Contains(tokens[idx], "=") {
				idx++
				continue
			}
			break
		}
		if idx >= len(tokens) {
			continue
		}
		prog := strings.ToLower(tokens[idx])
		args := tokens[idx+1:]

		if isDangerousInvocation(prog, args) {
			return true
		}
	}

	return false
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
		if ch == '&' && i+1 < len(s) && s[i+1] == '&' {
			out = append(out, cur.String())
			cur.Reset()
			i++
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
		if quote != 0 {
			if ch == quote {
				quote = 0
				continue
			}
			cur.WriteByte(ch)
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

func hasDangerousRedirect(tokens []string) bool {
	dangerousTargets := []string{
		"/dev/sd", "/dev/nvme", "/dev/vd", "/dev/hd", "/dev/disk",
		"/etc/passwd", "/etc/shadow", "/etc/sudoers",
	}
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
		if target == "" {
			continue
		}
		low := strings.ToLower(target)
		for _, dt := range dangerousTargets {
			if strings.HasPrefix(low, dt) {
				return true
			}
		}
	}
	return false
}

func isDangerousInvocation(prog string, args []string) bool {
	switch {
	case prog == "rm":
		for _, a := range args {
			low := strings.ToLower(a)
			if low == "--recursive" || (strings.HasPrefix(low, "-") && !strings.HasPrefix(low, "--") && strings.ContainsRune(low, 'r')) {
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
	case prog == "find":
		for _, a := range args {
			low := strings.ToLower(a)
			if low == "-delete" {
				return true
			}
		}
	case prog == "chmod":
		hasRec := false
		hasWide := false
		hasRoot := false
		for _, a := range args {
			low := strings.ToLower(a)
			if low == "-r" || low == "--recursive" {
				hasRec = true
			}
			if low == "777" || low == "000" {
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
