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
	"net/http"
	"strings"
	"time"
)

var (
	// ErrNoAPIKey 表示未配置 API Key。
	ErrNoAPIKey = errors.New("未配置 AI API Key，请在设置中填写")
	// ErrEmptyResponse 表示 AI 服务返回空内容。
	ErrEmptyResponse = errors.New("AI 服务返回内容为空")
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
	Role    string `json:"role"` // "system", "user", "assistant"
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float32   `json:"temperature,omitempty"`
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

// Chat 发送一条对话请求并返回响应文本。
func (c *Client) Chat(ctx context.Context, messages []Message) (string, error) {
	if c.apiKey == "" && !strings.Contains(c.baseURL, "localhost") && !strings.Contains(c.baseURL, "127.0.0.1") {
		return "", ErrNoAPIKey
	}

	url := c.baseURL + "/chat/completions"
	reqBody, err := json.Marshal(chatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0.2, // 保持命令生成和错误分析严谨稳定
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("AI 请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp chatResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != nil && errResp.Error.Message != "" {
			return "", fmt.Errorf("AI 服务错误 (%d): %s", resp.StatusCode, errResp.Error.Message)
		}
		return "", fmt.Errorf("AI 服务错误响应码: %d (%s)", resp.StatusCode, string(body))
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
	Explanation string // 简明原理解释
	Command     string // 提取出的单行或多行 Shell 命令
	Raw         string // 完整原始文本
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
			res.Command = jsonObj.Command
			return res
		}
	}

	// 2. Markdown 代码块解析
	idx := strings.Index(text, "```")
	if idx == -1 {
		res.Explanation = text
		return res
	}

	res.Explanation = strings.TrimSpace(text[:idx])
	rest := text[idx+3:]
	// 去掉 bash/sh/shell 标记
	if nl := strings.Index(rest, "\n"); nl != -1 {
		rest = rest[nl+1:]
	}
	endIdx := strings.Index(rest, "```")
	if endIdx != -1 {
		res.Command = strings.TrimSpace(rest[:endIdx])
	} else {
		res.Command = strings.TrimSpace(rest)
	}

	return res
}

// IsDangerousCommand 识别可能造成系统破坏、数据丢失、意外关机或含隐蔽换行的危险命令。
func IsDangerousCommand(cmd string) bool {
	s := strings.ToLower(strings.TrimSpace(cmd))
	if s == "" {
		return false
	}

	// 含有换行符的多行指令，粘贴到终端存在首行自动触发执行的风险
	if strings.Contains(s, "\n") || strings.Contains(s, "\r") {
		return true
	}

	dangerousSubstrings := []string{
		"rm -rf", "rm -r ", "rm -fr",
		"mkfs", "dd if=", "> /dev/sd", "> /dev/nvme", "> /dev/vd",
		"shutdown", "reboot", "poweroff", "init 0", "init 6",
		":(){ :|:& };:",
		"chmod -r 777", "chmod 777 -r",
		"chmod -r 000",
	}

	for _, sub := range dangerousSubstrings {
		if strings.Contains(s, sub) {
			return true
		}
	}

	// 匹配整词命令
	fields := strings.Fields(s)
	if len(fields) > 0 {
		switch fields[0] {
		case "reboot", "poweroff", "halt":
			return true
		}
	}

	return false
}
