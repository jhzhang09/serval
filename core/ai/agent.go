package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ToolFunction 描述暴露给模型的函数工具定义。
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolDefinition 是 OpenAI Chat Completions 工具声明。
type ToolDefinition struct {
	Type     string       `json:"type"` // 固定为 "function"
	Function ToolFunction `json:"function"`
}

// ToolCallFunction 描述模型发起的单次工具调用名称与 JSON 参数。
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall 表示模型返回的一次工具调用。
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // "function"
	Function ToolCallFunction `json:"function"`
}

// Usage 记录一次或累计 Token 用量。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
	CachedTokens     int `json:"cached_tokens,omitempty"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
}

// Add 将另一次调用的用量累加到当前统计。
func (u *Usage) Add(other Usage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.TotalTokens += other.TotalTokens
	u.CachedTokens += other.CachedTokens
	u.ReasoningTokens += other.ReasoningTokens
}

type streamChatRequest struct {
	Model         string           `json:"model"`
	Messages      []Message        `json:"messages"`
	Tools         []ToolDefinition `json:"tools,omitempty"`
	Temperature   float32          `json:"temperature,omitempty"`
	Stream        bool             `json:"stream"`
	StreamOptions *streamOptions   `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type usageWire struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptCacheHitToken int `json:"prompt_cache_hit_tokens"`
	PromptDetails       *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (w *usageWire) toUsage() Usage {
	if w == nil {
		return Usage{}
	}
	u := Usage{
		PromptTokens:     w.PromptTokens,
		CompletionTokens: w.CompletionTokens,
		TotalTokens:      w.TotalTokens,
		CachedTokens:     w.PromptCacheHitToken,
	}
	if w.PromptDetails != nil && w.PromptDetails.CachedTokens > u.CachedTokens {
		u.CachedTokens = w.PromptDetails.CachedTokens
	}
	if w.CompletionDetails != nil {
		u.ReasoningTokens = w.CompletionDetails.ReasoningTokens
	}
	if u.TotalTokens == 0 && (u.PromptTokens > 0 || u.CompletionTokens > 0) {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return u
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		Message *struct {
			Role             string     `json:"role"`
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content"`
			ToolCalls        []ToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usageWire `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// StreamResult 是单轮流式对话的完整结果。
type StreamResult struct {
	Thinking        string
	ThinkingSeconds int
	Content         string
	ToolCalls       []ToolCall
	Usage           Usage
}

// StreamProgress 用于流式增量推送模型当前步的推理与输出快照。
type StreamProgress struct {
	Thinking        string
	ThinkingSeconds int
	Content         string
	ToolCalls       []ToolCall
	Usage           Usage
}

func (c *Client) streamHTTPClient() *http.Client {
	// 复制传输配置，去掉固定总耗时限制以便支持长流式推理，由 ctx 控制超时与取消。
	var tr http.RoundTripper
	if c.httpClient != nil && c.httpClient.Transport != nil {
		tr = c.httpClient.Transport
	}
	return &http.Client{Transport: tr}
}

// StreamChat 发起支持流式 SSE、推理思考与工具调用的 Chat Completions 请求。
// 若兼容性端点返回非流式 JSON，也能平滑兼容解析。
func (c *Client) StreamChat(ctx context.Context, messages []Message, tools []ToolDefinition, onProgress func(StreamProgress)) (*StreamResult, error) {
	if c.apiKey == "" && !isLocalEndpoint(c.baseURL) {
		return nil, ErrNoAPIKey
	}
	u, err := url.Parse(c.baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, ErrInvalidEndpoint
	}

	res, status, err := c.doStreamChat(ctx, messages, tools, true, onProgress)
	if err != nil && status == http.StatusBadRequest {
		// 部分旧版 OpenAI 兼容网关不支持 stream_options，收到 400 时回退重试一次。
		res, _, err = c.doStreamChat(ctx, messages, tools, false, onProgress)
	}
	return res, err
}

func (c *Client) doStreamChat(ctx context.Context, messages []Message, tools []ToolDefinition, includeUsage bool, onProgress func(StreamProgress)) (*StreamResult, int, error) {
	reqPayload := streamChatRequest{
		Model:       c.model,
		Messages:    messages,
		Tools:       tools,
		Temperature: 0.2,
		Stream:      true,
	}
	if includeUsage {
		reqPayload.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	reqBody, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, 0, err
	}

	endpoint := c.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, 0, ErrInvalidEndpoint
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.streamHTTPClient().Do(req)
	if err != nil {
		return nil, 0, mapTransportError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, mapHTTPError(resp.StatusCode)
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "application/json") && !strings.Contains(ct, "text/event-stream") {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, resp.StatusCode, mapTransportError(err)
		}
		res, err := parseNonStreamChunk(body)
		if err != nil {
			return nil, resp.StatusCode, err
		}
		if onProgress != nil {
			onProgress(StreamProgress{
				Thinking:        res.Thinking,
				ThinkingSeconds: res.ThinkingSeconds,
				Content:         res.Content,
				ToolCalls:       res.ToolCalls,
				Usage:           res.Usage,
			})
		}
		return res, resp.StatusCode, nil
	}

	res, err := parseSSEStream(resp.Body, onProgress)
	if err != nil {
		return nil, resp.StatusCode, mapTransportError(err)
	}
	return res, resp.StatusCode, nil
}

func parseNonStreamChunk(body []byte) (*StreamResult, error) {
	var chunk streamChunk
	if err := json.Unmarshal(body, &chunk); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}
	if chunk.Error != nil && chunk.Error.Message != "" {
		return nil, fmt.Errorf("AI 服务返回错误: %s", chunk.Error.Message)
	}
	res := &StreamResult{Usage: chunk.Usage.toUsage()}
	if len(chunk.Choices) > 0 && chunk.Choices[0].Message != nil {
		m := chunk.Choices[0].Message
		res.Thinking = strings.TrimSpace(m.ReasoningContent)
		res.Content = m.Content
		res.ToolCalls = normalizeToolCalls(m.ToolCalls)
		if res.Thinking == "" {
			res.Thinking, res.Content = splitThinkTags(res.Content)
		}
	}
	if res.Content == "" && res.Thinking == "" && len(res.ToolCalls) == 0 {
		return nil, ErrEmptyResponse
	}
	return res, nil
}

func parseSSEStream(r io.Reader, onProgress func(StreamProgress)) (*StreamResult, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	var thinkingBuf strings.Builder
	var contentBuf strings.Builder
	var thinkStart time.Time
	var thinkEnd time.Time
	var toolOrder []int
	toolMap := map[int]*ToolCall{}
	var usage Usage
	var sawAny bool

	emit := func() {
		if onProgress == nil {
			return
		}
		sec := calcThinkingSeconds(thinkStart, thinkEnd, thinkingBuf.Len() > 0)
		th, ct := splitThinkTagsIfNeeded(thinkingBuf.String(), contentBuf.String())
		onProgress(StreamProgress{
			Thinking:        th,
			ThinkingSeconds: sec,
			Content:         ct,
			ToolCalls:       collectToolCalls(toolOrder, toolMap),
			Usage:           usage,
		})
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return nil, fmt.Errorf("AI 服务返回错误: %s", chunk.Error.Message)
		}
		sawAny = true
		if chunk.Usage != nil {
			usage = chunk.Usage.toUsage()
		}
		if len(chunk.Choices) == 0 {
			emit()
			continue
		}
		d := chunk.Choices[0].Delta
		reasonPiece := d.ReasoningContent
		if reasonPiece == "" {
			reasonPiece = d.Reasoning
		}
		if reasonPiece != "" {
			if thinkStart.IsZero() {
				thinkStart = time.Now()
			}
			thinkingBuf.WriteString(reasonPiece)
			thinkEnd = time.Now()
		}
		if d.Content != "" {
			if !thinkStart.IsZero() && thinkEnd.IsZero() {
				thinkEnd = time.Now()
			}
			contentBuf.WriteString(d.Content)
		}
		for _, tc := range d.ToolCalls {
			if !thinkStart.IsZero() && thinkEnd.IsZero() {
				thinkEnd = time.Now()
			}
			slot, ok := toolMap[tc.Index]
			if !ok {
				slot = &ToolCall{Type: "function"}
				toolMap[tc.Index] = slot
				toolOrder = append(toolOrder, tc.Index)
			}
			if tc.ID != "" {
				slot.ID = tc.ID
			}
			if tc.Type != "" {
				slot.Type = tc.Type
			}
			if tc.Function.Name != "" {
				slot.Function.Name += tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				slot.Function.Arguments += tc.Function.Arguments
			}
		}
		emit()
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !sawAny {
		return nil, ErrEmptyResponse
	}

	th, ct := splitThinkTagsIfNeeded(thinkingBuf.String(), contentBuf.String())
	tcs := collectToolCalls(toolOrder, toolMap)
	if th == "" && strings.TrimSpace(ct) == "" && len(tcs) == 0 {
		return nil, ErrEmptyResponse
	}
	return &StreamResult{
		Thinking:        th,
		ThinkingSeconds: calcThinkingSeconds(thinkStart, thinkEnd, th != ""),
		Content:         ct,
		ToolCalls:       tcs,
		Usage:           usage,
	}, nil
}

func calcThinkingSeconds(start, end time.Time, hasThinking bool) int {
	if !hasThinking || start.IsZero() {
		return 0
	}
	if end.IsZero() {
		end = time.Now()
	}
	sec := int(end.Sub(start).Round(time.Second).Seconds())
	if sec < 1 {
		sec = 1
	}
	return sec
}

func splitThinkTagsIfNeeded(existingThinking, content string) (string, string) {
	if existingThinking != "" {
		return existingThinking, content
	}
	return splitThinkTags(content)
}

func splitThinkTags(content string) (string, string) {
	const openTag = "<think>"
	const closeTag = "</think>"
	start := strings.Index(content, openTag)
	if start == -1 {
		return "", content
	}
	rest := content[start+len(openTag):]
	end := strings.Index(rest, closeTag)
	if end == -1 {
		// 仍在流式输出思考段
		return strings.TrimSpace(rest), strings.TrimSpace(content[:start])
	}
	thinking := strings.TrimSpace(rest[:end])
	after := strings.TrimLeft(rest[end+len(closeTag):], "\r\n")
	before := strings.TrimSpace(content[:start])
	if before != "" && after != "" {
		return thinking, before + "\n" + after
	}
	return thinking, before + after
}

func collectToolCalls(order []int, m map[int]*ToolCall) []ToolCall {
	if len(order) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(order))
	for _, idx := range order {
		if tc := m[idx]; tc != nil {
			copyTC := *tc
			if copyTC.Type == "" {
				copyTC.Type = "function"
			}
			if copyTC.ID == "" {
				copyTC.ID = fmt.Sprintf("call_%d", idx+1)
			}
			out = append(out, copyTC)
		}
	}
	return out
}

func normalizeToolCalls(in []ToolCall) []ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]ToolCall, len(in))
	for i, tc := range in {
		if tc.Type == "" {
			tc.Type = "function"
		}
		if tc.ID == "" {
			tc.ID = fmt.Sprintf("call_%d", i+1)
		}
		out[i] = tc
	}
	return out
}

// AgentEventKind 标识 Agent 多轮执行过程中的生命周期事件。
type AgentEventKind uint8

const (
	EventStepStart AgentEventKind = iota
	EventStepProgress
	EventToolStart
	EventToolDone
	EventStepDone
)

// AgentEvent 描述单次 Agent 循环回调给上层的状态变化。
type AgentEvent struct {
	Kind            AgentEventKind
	Step            int
	Thinking        string
	ThinkingSeconds int
	Content         string
	ToolCall        ToolCall
	ToolOutput      string
	ToolError       bool
	StepUsage       Usage
	TotalUsage      Usage
}

// AgentRunConfig 配置一次多轮工具调用 Agent 执行。
type AgentRunConfig struct {
	Client      *Client
	Messages    []Message
	Tools       []ToolDefinition
	MaxSteps    int
	ExecuteTool func(ctx context.Context, call ToolCall) (output string, isError bool)
	OnEvent     func(AgentEvent)
}

// AgentRunResult 返回完整的最终消息序列与累计 Token 用量。
type AgentRunResult struct {
	Messages   []Message
	Usage      Usage
	FinalText  string
	StepCount  int
	ReachedMax bool
}

// RunAgent 执行多轮模型推理与工具调用闭环，直到模型直接给出最终回答或达到最大步数。
func RunAgent(ctx context.Context, cfg AgentRunConfig) (*AgentRunResult, error) {
	if cfg.Client == nil {
		return nil, fmt.Errorf("ai client is nil")
	}
	maxSteps := cfg.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 12
	}
	msgs := append([]Message(nil), cfg.Messages...)
	var totalUsage Usage
	var finalText string

	emit := func(ev AgentEvent) {
		if cfg.OnEvent != nil {
			cfg.OnEvent(ev)
		}
	}

	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		emit(AgentEvent{Kind: EventStepStart, Step: step, TotalUsage: totalUsage})

		res, err := cfg.Client.StreamChat(ctx, msgs, cfg.Tools, func(p StreamProgress) {
			runningTotal := totalUsage
			runningTotal.Add(p.Usage)
			emit(AgentEvent{
				Kind:            EventStepProgress,
				Step:            step,
				Thinking:        p.Thinking,
				ThinkingSeconds: p.ThinkingSeconds,
				Content:         p.Content,
				TotalUsage:      runningTotal,
			})
		})
		if err != nil {
			return nil, err
		}

		totalUsage.Add(res.Usage)
		assistantMsg := Message{
			Role:             "assistant",
			Content:          res.Content,
			ReasoningContent: res.Thinking,
			ToolCalls:        res.ToolCalls,
		}
		msgs = append(msgs, assistantMsg)
		finalText = res.Content

		emit(AgentEvent{
			Kind:            EventStepDone,
			Step:            step,
			Thinking:        res.Thinking,
			ThinkingSeconds: res.ThinkingSeconds,
			Content:         res.Content,
			StepUsage:       res.Usage,
			TotalUsage:      totalUsage,
		})

		if len(res.ToolCalls) == 0 {
			return &AgentRunResult{
				Messages:  msgs,
				Usage:     totalUsage,
				FinalText: finalText,
				StepCount: step,
			}, nil
		}

		if cfg.ExecuteTool == nil {
			return nil, fmt.Errorf("收到模型工具调用但未配置 ExecuteTool")
		}

		for _, tc := range res.ToolCalls {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			emit(AgentEvent{
				Kind:       EventToolStart,
				Step:       step,
				ToolCall:   tc,
				TotalUsage: totalUsage,
			})
			out, isErr := cfg.ExecuteTool(ctx, tc)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			emit(AgentEvent{
				Kind:       EventToolDone,
				Step:       step,
				ToolCall:   tc,
				ToolOutput: out,
				ToolError:  isErr,
				TotalUsage: totalUsage,
			})
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    out,
			})
		}
	}

	return &AgentRunResult{
		Messages:   msgs,
		Usage:      totalUsage,
		FinalText:  finalText,
		StepCount:  maxSteps,
		ReachedMax: true,
	}, nil
}
