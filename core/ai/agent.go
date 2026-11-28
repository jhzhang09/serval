package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	ID           string           `json:"id"`
	Type         string           `json:"type"` // "function"
	Function     ToolCallFunction `json:"function"`
	ExtraContent json.RawMessage  `json:"extra_content,omitempty"`
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
			Role             string          `json:"role"`
			Content          string          `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			Reasoning        string          `json:"reasoning"`
			ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`
			ToolCalls        []struct {
				Index        *int            `json:"index"`
				ID           string          `json:"id"`
				Type         string          `json:"type"`
				ExtraContent json.RawMessage `json:"extra_content,omitempty"`
				Function     struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		Message *struct {
			Role             string          `json:"role"`
			Content          string          `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`
			ToolCalls        []ToolCall      `json:"tool_calls"`
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
	Thinking         string
	ThinkingSeconds  int
	ReasoningContent string
	ReasoningDetails json.RawMessage
	Content          string
	ToolCalls        []ToolCall
	Usage            Usage
}

// StreamProgress 用于流式增量推送模型当前步的推理与输出快照。
type StreamProgress struct {
	Thinking        string
	ThinkingSeconds int
	Content         string
	ToolCalls       []ToolCall
	Usage           Usage
}

const googleSkipThoughtSignature = "skip_thought_signature_validator"

func isGoogleEndpoint(baseURL, model string) bool {
	lowURL := strings.ToLower(strings.TrimSpace(baseURL))
	if strings.Contains(lowURL, "generativelanguage.googleapis.com") ||
		strings.Contains(lowURL, "aiplatform.googleapis.com") {
		return true
	}
	if isOpenRouterEndpoint(baseURL) {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(model)), "gemini")
}

func isOpenAIOfficialEndpoint(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "api.openai.com" || strings.HasSuffix(host, ".openai.azure.com")
}

func isOpenRouterEndpoint(baseURL string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(baseURL)), "openrouter.ai")
}

func isSafeToolCallID(id string) bool {
	if len(id) == 0 || len(id) > 40 {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

// prepareMessagesForProvider 按目标服务商协议清洗历史消息，避免跨模型切换（如 DeepSeek <-> Google / OpenRouter / OpenAI）触发 HTTP 400。
func prepareMessagesForProvider(baseURL, model string, msgs []Message) []Message {
	if len(msgs) == 0 {
		return nil
	}
	googleTarget := isGoogleEndpoint(baseURL, model)
	openAIOfficial := isOpenAIOfficialEndpoint(baseURL)
	openRouterTarget := isOpenRouterEndpoint(baseURL)
	lastUserIdx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastUserIdx = i
			break
		}
	}

	usedIDs := make(map[string]bool)
	for _, m := range msgs {
		if isSafeToolCallID(m.ToolCallID) {
			usedIDs[m.ToolCallID] = true
		}
		for _, tc := range m.ToolCalls {
			if isSafeToolCallID(tc.ID) {
				usedIDs[tc.ID] = true
			}
		}
	}
	idMap := make(map[string]string)
	normSeq := 0
	remapID := func(raw string) string {
		if isSafeToolCallID(raw) {
			return raw
		}
		if mapped, ok := idMap[raw]; ok {
			return mapped
		}
		for {
			normSeq++
			cand := fmt.Sprintf("call_norm_%d", normSeq)
			if !usedIDs[cand] {
				usedIDs[cand] = true
				idMap[raw] = cand
				return cand
			}
		}
	}

	out := make([]Message, len(msgs))
	for i, m := range msgs {
		if m.Role == "tool" {
			m.Name = ""
			if m.ToolCallID != "" {
				m.ToolCallID = remapID(m.ToolCallID)
			}
		}
		if m.Role == "assistant" {
			if i < lastUserIdx || googleTarget || openAIOfficial {
				m.ReasoningContent = ""
			}
			if googleTarget || openAIOfficial || (i < lastUserIdx && !openRouterTarget) {
				m.ReasoningDetails = nil
			} else if len(m.ReasoningDetails) > 0 {
				m.ReasoningDetails = slices.Clone(m.ReasoningDetails)
			}
		}
		if len(m.ToolCalls) > 0 {
			tcs := make([]ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				if tc.ID != "" {
					tc.ID = remapID(tc.ID)
				}
				if strings.TrimSpace(tc.Function.Arguments) == "" {
					tc.Function.Arguments = "{}"
				}
				if googleTarget {
					if trimmed := bytes.TrimSpace(tc.ExtraContent); len(trimmed) == 0 || string(trimmed) == "null" {
						tc.ExtraContent = json.RawMessage(`{"google":{"thought_signature":"` + googleSkipThoughtSignature + `"}}`)
					} else {
						tc.ExtraContent = slices.Clone(tc.ExtraContent)
					}
				} else {
					tc.ExtraContent = nil
				}
				tcs[j] = tc
			}
			m.ToolCalls = tcs
		}
		out[i] = m
	}
	return out
}

func (c *Client) streamHTTPClient() *http.Client {
	if c.streamClient != nil {
		if c.httpClient != nil && c.httpClient.Transport != nil && c.httpClient.Transport != c.streamClient.Transport {
			// 若测试或外部替换了 c.httpClient.Transport，优先沿用该自定义 Transport。
			if _, isDefaultAI := c.httpClient.Transport.(*http.Transport); !isDefaultAI {
				return &http.Client{Transport: c.httpClient.Transport}
			}
		}
		return c.streamClient
	}
	var tr http.RoundTripper
	if c.httpClient != nil && c.httpClient.Transport != nil {
		tr = c.httpClient.Transport
	} else {
		tr = newAITransport(90 * time.Second)
	}
	return &http.Client{Transport: tr}
}

func (c *Client) closeIdleConnections() {
	if c.streamClient != nil {
		c.streamClient.CloseIdleConnections()
	}
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
}

type idleTimeoutReader struct {
	r        io.Reader
	timeout  time.Duration
	timer    *time.Timer
	timedOut *atomic.Bool
	cancel   context.CancelFunc
}

func newIdleTimeoutReader(r io.Reader, timeout time.Duration, cancel context.CancelFunc) (*idleTimeoutReader, *atomic.Bool) {
	timedOut := &atomic.Bool{}
	if timeout <= 0 {
		return &idleTimeoutReader{r: r, timedOut: timedOut, cancel: cancel}, timedOut
	}
	tr := &idleTimeoutReader{
		r:        r,
		timeout:  timeout,
		timedOut: timedOut,
		cancel:   cancel,
	}
	tr.timer = time.AfterFunc(timeout, func() {
		timedOut.Store(true)
		cancel()
	})
	return tr, timedOut
}

func (t *idleTimeoutReader) Read(p []byte) (int, error) {
	if t.timer != nil {
		t.timer.Reset(t.timeout)
	}
	n, err := t.r.Read(p)
	if t.timer != nil && n > 0 {
		t.timer.Reset(t.timeout)
	}
	return n, err
}

func (t *idleTimeoutReader) Stop() {
	if t.timer != nil {
		t.timer.Stop()
	}
}

func isRetryableStreamError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	return errors.Is(err, ErrStreamInterrupted)
}

// StreamChat 发起支持流式 SSE、推理思考与工具调用的 Chat Completions 请求。
// 若兼容性端点返回非流式 JSON 或仅支持 OpenAI Responses API，会自动平滑回退。
func (c *Client) StreamChat(ctx context.Context, messages []Message, tools []ToolDefinition, onProgress func(StreamProgress)) (*StreamResult, error) {
	if c.apiKey == "" && !isLocalEndpoint(c.baseURL) {
		return nil, ErrNoAPIKey
	}
	u, err := url.Parse(c.baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, ErrInvalidEndpoint
	}

	if c.useAnthropic() {
		var emittedAny atomic.Bool
		wrappedProgress := onProgress
		if onProgress != nil {
			wrappedProgress = func(p StreamProgress) {
				if p.Thinking != "" || p.Content != "" || len(p.ToolCalls) > 0 {
					emittedAny.Store(true)
				}
				onProgress(p)
			}
		}
		var aRes *StreamResult
		var aErr error
		backoffs := []time.Duration{300 * time.Millisecond, 900 * time.Millisecond}
		for attempt := 0; attempt <= len(backoffs); attempt++ {
			aRes, aErr = c.doAnthropicStream(ctx, messages, tools, wrappedProgress)
			if aErr == nil || emittedAny.Load() || ctx.Err() != nil || !isRetryableStreamError(aErr) || attempt == len(backoffs) {
				break
			}
			c.closeIdleConnections()
			timer := time.NewTimer(backoffs[attempt])
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, mapTransportError(ctx.Err())
			case <-timer.C:
			}
		}
		return aRes, aErr
	}

	prepared := prepareMessagesForProvider(c.baseURL, c.model, messages)

	var emittedAny atomic.Bool
	wrappedProgress := onProgress
	if onProgress != nil {
		wrappedProgress = func(p StreamProgress) {
			if p.Thinking != "" || p.Content != "" || len(p.ToolCalls) > 0 {
				emittedAny.Store(true)
			}
			onProgress(p)
		}
	}

	var res *StreamResult
	var status int
	backoffs := []time.Duration{300 * time.Millisecond, 900 * time.Millisecond}
	for attempt := 0; attempt <= len(backoffs); attempt++ {
		res, status, err = c.doStreamChat(ctx, prepared, tools, true, wrappedProgress)
		if err != nil && status == http.StatusBadRequest && !emittedAny.Load() {
			// 部分旧版 OpenAI 兼容网关不支持 stream_options，收到 400 时回退重试一次。
			res, status, err = c.doStreamChat(ctx, prepared, tools, false, wrappedProgress)
		}
		if err == nil || emittedAny.Load() || ctx.Err() != nil || !isRetryableStreamError(err) || attempt == len(backoffs) {
			break
		}
		c.closeIdleConnections()
		timer := time.NewTimer(backoffs[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, mapTransportError(ctx.Err())
		case <-timer.C:
		}
	}

	if err != nil && shouldFallbackToResponses(status, err) {
		var rRes *StreamResult
		var rStatus int
		var rErr error
		for attempt := 0; attempt <= len(backoffs); attempt++ {
			rRes, rStatus, rErr = c.doResponses(ctx, prepared, tools, true, wrappedProgress)
			if rErr == nil || emittedAny.Load() || ctx.Err() != nil || !isRetryableStreamError(rErr) || attempt == len(backoffs) {
				break
			}
			c.closeIdleConnections()
			timer := time.NewTimer(backoffs[attempt])
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, mapTransportError(ctx.Err())
			case <-timer.C:
			}
		}
		if rErr == nil {
			return rRes, nil
		} else if rStatus != 0 && rStatus != http.StatusNotFound && rStatus != http.StatusMethodNotAllowed {
			return nil, rErr
		}
	}
	return res, err
}

func (c *Client) doStreamChat(ctx context.Context, messages []Message, tools []ToolDefinition, includeUsage bool, onProgress func(StreamProgress)) (*StreamResult, int, error) {
	// 不强行传入 temperature，避免 gpt-5*/o3/o4 等仅接受默认采样参数的模型返回 HTTP 400。
	reqPayload := streamChatRequest{
		Model:    c.model,
		Messages: messages,
		Tools:    tools,
		Stream:   true,
	}
	if includeUsage {
		reqPayload.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	reqBody, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, 0, err
	}

	stepCtx, stepCancel := context.WithCancel(ctx)
	defer stepCancel()

	endpoint := c.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(stepCtx, "POST", endpoint, bytes.NewReader(reqBody))
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, resp.StatusCode, parseAPIError(resp.StatusCode, body, c.apiKey)
	}

	idleReader, idleTimedOut := newIdleTimeoutReader(resp.Body, c.streamIdleTimeout, stepCancel)
	defer idleReader.Stop()

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "application/json") && !strings.Contains(ct, "text/event-stream") {
		body, err := io.ReadAll(io.LimitReader(idleReader, 4<<20))
		if err != nil {
			if idleTimedOut.Load() && ctx.Err() == nil {
				return nil, resp.StatusCode, ErrTimeout
			}
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

	res, err := parseSSEStream(idleReader, onProgress)
	if err != nil {
		if idleTimedOut.Load() && ctx.Err() == nil {
			return nil, resp.StatusCode, ErrTimeout
		}
		return nil, resp.StatusCode, mapTransportError(err)
	}
	return res, resp.StatusCode, nil
}

func parseNonStreamChunk(body []byte) (*StreamResult, error) {
	var chunk streamChunk
	if err := json.Unmarshal(body, &chunk); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	if chunk.Error != nil && chunk.Error.Message != "" {
		return nil, fmt.Errorf("AI 服务返回错误: %s", chunk.Error.Message)
	}
	res := &StreamResult{Usage: chunk.Usage.toUsage()}
	if len(chunk.Choices) > 0 && chunk.Choices[0].Message != nil {
		m := chunk.Choices[0].Message
		res.ReasoningContent = strings.TrimSpace(m.ReasoningContent)
		res.Thinking = res.ReasoningContent
		if trimmedRD := bytes.TrimSpace(m.ReasoningDetails); len(trimmedRD) > 0 && string(trimmedRD) != "null" {
			res.ReasoningDetails = slices.Clone(m.ReasoningDetails)
		}
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

type reasoningDetailsAccumulator struct {
	items     []map[string]any
	rawSingle json.RawMessage
}

func (a *reasoningDetailsAccumulator) Add(raw json.RawMessage) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return
	}
	if trimmed[0] != '[' {
		a.rawSingle = slices.Clone(trimmed)
		return
	}
	var incoming []map[string]any
	if json.Unmarshal(trimmed, &incoming) != nil {
		return
	}
	for _, item := range incoming {
		if len(item) == 0 {
			continue
		}
		merged := false
		if len(a.items) > 0 {
			last := a.items[len(a.items)-1]
			if sameReasoningDetailSlot(last, item) {
				typ, _ := item["type"].(string)
				if typ == "reasoning.text" || typ == "reasoning.summary" {
					if txt, ok := item["text"].(string); ok && txt != "" {
						prev, _ := last["text"].(string)
						last["text"] = prev + txt
					}
					if sm, ok := item["summary"].(string); ok && sm != "" {
						prev, _ := last["summary"].(string)
						last["summary"] = prev + sm
					}
					for k, v := range item {
						if k != "text" && k != "summary" {
							last[k] = v
						}
					}
					merged = true
				}
			}
		}
		if !merged {
			a.items = append(a.items, item)
		}
	}
}

func sameReasoningDetailSlot(a, b map[string]any) bool {
	if a["type"] != b["type"] {
		return false
	}
	if a["id"] != b["id"] {
		return false
	}
	return a["index"] == b["index"]
}

func (a *reasoningDetailsAccumulator) Bytes() json.RawMessage {
	if len(a.items) > 0 {
		if b, err := json.Marshal(a.items); err == nil {
			return json.RawMessage(b)
		}
	}
	if len(a.rawSingle) > 0 {
		return slices.Clone(a.rawSingle)
	}
	return nil
}

func parseSSEStream(r io.Reader, onProgress func(StreamProgress)) (*StreamResult, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	var thinkingBuf strings.Builder
	var nativeReasoningBuf strings.Builder
	var reasoningDetailsAcc reasoningDetailsAccumulator
	var contentBuf strings.Builder
	var thinkStart time.Time
	var thinkEnd time.Time
	var toolOrder []int
	toolMap := map[int]*ToolCall{}
	idToSlot := map[string]int{}
	nextSyntheticIdx := 100000
	allocSlot := func() int {
		for {
			if _, exists := toolMap[nextSyntheticIdx]; !exists {
				idx := nextSyntheticIdx
				nextSyntheticIdx++
				return idx
			}
			nextSyntheticIdx++
		}
	}
	var usage Usage
	var sawAny bool
	var sawFinishReason bool

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
		if strings.TrimSpace(chunk.Choices[0].FinishReason) != "" {
			sawFinishReason = true
		}
		d := chunk.Choices[0].Delta
		if len(d.ReasoningDetails) > 0 {
			reasoningDetailsAcc.Add(d.ReasoningDetails)
		}
		if d.ReasoningContent != "" {
			nativeReasoningBuf.WriteString(d.ReasoningContent)
		}
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
			var idx int
			if tc.Index != nil {
				idx = *tc.Index
				if existing, ok := toolMap[idx]; ok && tc.ID != "" && existing.ID != "" && existing.ID != tc.ID {
					if mapped, seenID := idToSlot[tc.ID]; seenID {
						idx = mapped
					} else {
						idx = allocSlot()
					}
				}
			} else if tc.ID != "" {
				if mapped, seenID := idToSlot[tc.ID]; seenID {
					idx = mapped
				} else {
					idx = allocSlot()
				}
			} else if len(toolOrder) > 0 {
				idx = toolOrder[len(toolOrder)-1]
			} else {
				idx = 0
			}
			if tc.ID != "" {
				idToSlot[tc.ID] = idx
			}
			slot, ok := toolMap[idx]
			if !ok {
				slot = &ToolCall{Type: "function"}
				toolMap[idx] = slot
				toolOrder = append(toolOrder, idx)
			}
			if tc.ID != "" {
				slot.ID = tc.ID
			}
			if tc.Type != "" {
				slot.Type = tc.Type
			}
			if tc.Function.Name != "" {
				if slot.Function.Name == "" {
					slot.Function.Name = tc.Function.Name
				} else if slot.Function.Name != tc.Function.Name && !strings.HasSuffix(slot.Function.Name, tc.Function.Name) {
					slot.Function.Name += tc.Function.Name
				}
			}
			if tc.Function.Arguments != "" {
				slot.Function.Arguments += tc.Function.Arguments
			}
			if trimmedExtra := bytes.TrimSpace(tc.ExtraContent); len(trimmedExtra) > 0 && string(trimmedExtra) != "null" {
				slot.ExtraContent = slices.Clone(tc.ExtraContent)
			}
		}
		emit()
	}
	if err := scanner.Err(); err != nil {
		thPartial, ctPartial := splitThinkTagsIfNeeded(thinkingBuf.String(), contentBuf.String())
		tcsPartial := collectToolCalls(toolOrder, toolMap)
		if !(sawFinishReason && isStreamResetError(err) && (thPartial != "" || strings.TrimSpace(ctPartial) != "" || len(tcsPartial) > 0)) {
			return nil, err
		}
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
		Thinking:         th,
		ThinkingSeconds:  calcThinkingSeconds(thinkStart, thinkEnd, th != ""),
		ReasoningContent: nativeReasoningBuf.String(),
		ReasoningDetails: reasoningDetailsAcc.Bytes(),
		Content:          ct,
		ToolCalls:        tcs,
		Usage:            usage,
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

var syntheticCallIDSeq atomic.Uint64

func nextSyntheticCallID() string {
	return fmt.Sprintf("call_%d", syntheticCallIDSeq.Add(1))
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
				copyTC.ID = nextSyntheticCallID()
			}
			if len(copyTC.ExtraContent) > 0 {
				copyTC.ExtraContent = slices.Clone(copyTC.ExtraContent)
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
			tc.ID = nextSyntheticCallID()
		}
		if len(tc.ExtraContent) > 0 {
			tc.ExtraContent = slices.Clone(tc.ExtraContent)
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
	EventToolProgress
	EventToolDone
	EventStepDone
	EventCompacted
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
	CompactedNotice string
	StepUsage       Usage
	TotalUsage      Usage
}

// AgentRunConfig 配置一次多轮工具调用 Agent 执行。
type AgentRunConfig struct {
	Client            *Client
	Messages          []Message
	Tools             []ToolDefinition
	MaxSteps          int
	ContextWindow     int
	EnableCompaction  bool
	ExecuteTool       func(ctx context.Context, call ToolCall) (output string, isError bool)
	ExecuteToolStream func(ctx context.Context, call ToolCall, onProgress func(output string)) (output string, isError bool)
	IsReadOnlyTool    func(call ToolCall) bool
	OnEvent           func(AgentEvent)
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
		maxSteps = 20
	}
	ctxWindow := cfg.ContextWindow
	if ctxWindow <= 0 {
		ctxWindow = ModelContextWindow(cfg.Client.model)
	}
	msgs := SanitizeMessageSequence(append([]Message(nil), cfg.Messages...))
	var totalUsage Usage
	var finalText string
	lastSig := ""
	repeatCount := 0

	emit := func(ev AgentEvent) {
		if cfg.OnEvent != nil {
			cfg.OnEvent(ev)
		}
	}

	partialResult := func(step int, reachedMax bool) *AgentRunResult {
		return &AgentRunResult{
			Messages:   SanitizeMessageSequence(msgs),
			Usage:      totalUsage,
			FinalText:  finalText,
			StepCount:  step,
			ReachedMax: reachedMax,
		}
	}

	execOne := func(tctx context.Context, step int, tc ToolCall) (string, bool) {
		if cfg.ExecuteToolStream != nil {
			return cfg.ExecuteToolStream(tctx, tc, func(partial string) {
				emit(AgentEvent{
					Kind:       EventToolProgress,
					Step:       step,
					ToolCall:   tc,
					ToolOutput: partial,
					TotalUsage: totalUsage,
				})
			})
		}
		return cfg.ExecuteTool(tctx, tc)
	}

	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return partialResult(step-1, false), mapTransportError(err)
		}

		if cfg.EnableCompaction {
			msgs = PruneStaleToolOutputs(msgs, 4)
			if EstimateMessagesTokens(msgs) > ctxWindow*3/4 {
				if compacted, ok := CompactMessages(ctx, cfg.Client, msgs, ctxWindow/2); ok {
					msgs = compacted
					emit(AgentEvent{
						Kind:            EventCompacted,
						Step:            step,
						CompactedNotice: "上下文接近模型上限，已自动压缩较早历史",
						TotalUsage:      totalUsage,
					})
				}
			}
		}

		emit(AgentEvent{Kind: EventStepStart, Step: step, TotalUsage: totalUsage})

		var res *StreamResult
		var err error
		for attempt := 0; attempt < 2; attempt++ {
			res, err = cfg.Client.StreamChat(ctx, msgs, cfg.Tools, func(p StreamProgress) {
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
			if err != nil && attempt == 0 && cfg.EnableCompaction && IsContextOverflowError(err) && ctx.Err() == nil {
				if compacted, ok := CompactMessages(ctx, cfg.Client, msgs, ctxWindow/2); ok {
					msgs = compacted
					emit(AgentEvent{
						Kind:            EventCompacted,
						Step:            step,
						CompactedNotice: "请求超出上下文上限，已自动压缩历史并重试",
						TotalUsage:      totalUsage,
					})
					continue
				}
			}
			break
		}
		if err != nil {
			return partialResult(step-1, false), err
		}

		totalUsage.Add(res.Usage)
		assistantMsg := Message{
			Role:             "assistant",
			Content:          res.Content,
			ReasoningContent: res.ReasoningContent,
			ReasoningDetails: res.ReasoningDetails,
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
			return partialResult(step, false), nil
		}

		if cfg.ExecuteTool == nil && cfg.ExecuteToolStream == nil {
			return partialResult(step, false), fmt.Errorf("收到模型工具调用但未配置 ExecuteTool")
		}

		// 检查是否所有工具调用均为只读工具；若是且数量 > 1，则并发执行（上限 4 并发）
		canParallel := len(res.ToolCalls) > 1 && cfg.IsReadOnlyTool != nil
		if canParallel {
			for _, tc := range res.ToolCalls {
				if !cfg.IsReadOnlyTool(tc) {
					canParallel = false
					break
				}
			}
		}

		// 重复死循环检测（连续 3 次调用完全相同的工具与参数）
		allRepeat := true
		for _, tc := range res.ToolCalls {
			sig := tc.Function.Name + "\x00" + strings.TrimSpace(tc.Function.Arguments)
			if sig == lastSig {
				repeatCount++
			} else {
				lastSig = sig
				repeatCount = 1
			}
			if repeatCount < 3 {
				allRepeat = false
			}
		}
		if allRepeat && repeatCount >= 3 {
			for _, tc := range res.ToolCalls {
				emit(AgentEvent{Kind: EventToolStart, Step: step, ToolCall: tc, TotalUsage: totalUsage})
				guide := "检测到连续重复调用相同工具与参数，请更换排查方案或直接总结当前已知结果。"
				emit(AgentEvent{
					Kind:       EventToolDone,
					Step:       step,
					ToolCall:   tc,
					ToolOutput: guide,
					ToolError:  true,
					TotalUsage: totalUsage,
				})
				msgs = append(msgs, Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    guide,
				})
			}
			continue
		}

		if canParallel {
			type toolOut struct {
				out   string
				isErr bool
			}
			results := make([]toolOut, len(res.ToolCalls))
			for _, tc := range res.ToolCalls {
				emit(AgentEvent{
					Kind:       EventToolStart,
					Step:       step,
					ToolCall:   tc,
					TotalUsage: totalUsage,
				})
			}
			sem := make(chan struct{}, 4)
			var wg sync.WaitGroup
			for idx, tc := range res.ToolCalls {
				wg.Add(1)
				go func(i int, call ToolCall) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					if ctx.Err() != nil {
						return
					}
					out, isErr := execOne(ctx, step, call)
					results[i] = toolOut{out: out, isErr: isErr}
				}(idx, tc)
			}
			wg.Wait()
			if err := ctx.Err(); err != nil {
				return partialResult(step, false), mapTransportError(err)
			}
			for idx, tc := range res.ToolCalls {
				r := results[idx]
				emit(AgentEvent{
					Kind:       EventToolDone,
					Step:       step,
					ToolCall:   tc,
					ToolOutput: r.out,
					ToolError:  r.isErr,
					TotalUsage: totalUsage,
				})
				msgs = append(msgs, Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    r.out,
				})
			}
			continue
		}

		for _, tc := range res.ToolCalls {
			if err := ctx.Err(); err != nil {
				return partialResult(step, false), mapTransportError(err)
			}
			emit(AgentEvent{
				Kind:       EventToolStart,
				Step:       step,
				ToolCall:   tc,
				TotalUsage: totalUsage,
			})
			out, isErr := execOne(ctx, step, tc)
			if err := ctx.Err(); err != nil {
				return partialResult(step, false), mapTransportError(err)
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
				Content:    out,
			})
		}
	}

	return partialResult(maxSteps, true), nil
}

func shouldFallbackToResponses(status int, err error) bool {
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
		return true
	}
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "responses") || strings.Contains(low, "not a chat model")
}

type responsesRequest struct {
	Model        string           `json:"model"`
	Instructions string           `json:"instructions,omitempty"`
	Input        []any            `json:"input"`
	Tools        []map[string]any `json:"tools,omitempty"`
	Stream       bool             `json:"stream,omitempty"`
}

type responsesUsageWire struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (w *responsesUsageWire) toUsage() Usage {
	if w == nil {
		return Usage{}
	}
	u := Usage{
		PromptTokens:     w.InputTokens,
		CompletionTokens: w.OutputTokens,
		TotalTokens:      w.TotalTokens,
	}
	if w.InputTokensDetails != nil {
		u.CachedTokens = w.InputTokensDetails.CachedTokens
	}
	if w.OutputTokensDetails != nil {
		u.ReasoningTokens = w.OutputTokensDetails.ReasoningTokens
	}
	if u.TotalTokens == 0 && (u.PromptTokens > 0 || u.CompletionTokens > 0) {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return u
}

type responsesOutputItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Role      string `json:"role"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
}

type responsesObject struct {
	OutputText string                `json:"output_text"`
	Output     []responsesOutputItem `json:"output"`
	Usage      *responsesUsageWire   `json:"usage"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) doResponses(ctx context.Context, messages []Message, tools []ToolDefinition, stream bool, onProgress func(StreamProgress)) (*StreamResult, int, error) {
	var instParts []string
	var inputItems []any
	for _, m := range messages {
		switch m.Role {
		case "system":
			if s := strings.TrimSpace(m.Content); s != "" {
				instParts = append(instParts, s)
			}
		case "user":
			inputItems = append(inputItems, map[string]any{
				"type":    "message",
				"role":    "user",
				"content": m.Content,
			})
		case "assistant":
			if strings.TrimSpace(m.Content) != "" {
				inputItems = append(inputItems, map[string]any{
					"type":    "message",
					"role":    "assistant",
					"content": m.Content,
				})
			}
			for _, tc := range m.ToolCalls {
				inputItems = append(inputItems, map[string]any{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				})
			}
		case "tool":
			inputItems = append(inputItems, map[string]any{
				"type":    "function_call_output",
				"call_id": m.ToolCallID,
				"output":  m.Content,
			})
		}
	}

	var respTools []map[string]any
	for _, t := range tools {
		respTools = append(respTools, map[string]any{
			"type":        "function",
			"name":        t.Function.Name,
			"description": t.Function.Description,
			"parameters":  t.Function.Parameters,
		})
	}

	reqBody, err := json.Marshal(responsesRequest{
		Model:        c.model,
		Instructions: strings.Join(instParts, "\n\n"),
		Input:        inputItems,
		Tools:        respTools,
		Stream:       stream,
	})
	if err != nil {
		return nil, 0, err
	}

	stepCtx, stepCancel := context.WithCancel(ctx)
	defer stepCancel()

	endpoint := c.baseURL + "/responses"
	req, err := http.NewRequestWithContext(stepCtx, "POST", endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, 0, ErrInvalidEndpoint
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream, application/json")
	} else {
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	hc := c.httpClient
	if stream {
		hc = c.streamHTTPClient()
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, mapTransportError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, resp.StatusCode, parseAPIError(resp.StatusCode, body, c.apiKey)
	}

	var bodyReader io.Reader = resp.Body
	var idleTimedOut *atomic.Bool
	if stream {
		var idleReader *idleTimeoutReader
		idleReader, idleTimedOut = newIdleTimeoutReader(resp.Body, c.streamIdleTimeout, stepCancel)
		defer idleReader.Stop()
		bodyReader = idleReader
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "application/json") && !strings.Contains(ct, "text/event-stream") {
		body, readErr := io.ReadAll(io.LimitReader(bodyReader, 4<<20))
		if readErr != nil {
			if idleTimedOut != nil && idleTimedOut.Load() && ctx.Err() == nil {
				return nil, resp.StatusCode, ErrTimeout
			}
			return nil, resp.StatusCode, mapTransportError(readErr)
		}
		res, parseErr := parseNonStreamResponses(body)
		if parseErr != nil {
			return nil, resp.StatusCode, parseErr
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

	res, parseErr := parseResponsesSSEStream(bodyReader, onProgress)
	if parseErr != nil {
		if idleTimedOut != nil && idleTimedOut.Load() && ctx.Err() == nil {
			return nil, resp.StatusCode, ErrTimeout
		}
		return nil, resp.StatusCode, mapTransportError(parseErr)
	}
	return res, resp.StatusCode, nil
}

func extractResponsesResult(obj *responsesObject) (thinking string, content string, toolCalls []ToolCall) {
	if obj == nil {
		return "", "", nil
	}
	var thinkBuf, textBuf strings.Builder
	for i, item := range obj.Output {
		switch item.Type {
		case "reasoning":
			for _, sm := range item.Summary {
				if sm.Text != "" {
					if thinkBuf.Len() > 0 {
						thinkBuf.WriteByte('\n')
					}
					thinkBuf.WriteString(sm.Text)
				}
			}
		case "message":
			for _, part := range item.Content {
				if part.Text != "" {
					textBuf.WriteString(part.Text)
				}
			}
		case "function_call":
			callID := item.CallID
			if callID == "" {
				callID = item.ID
			}
			if callID == "" {
				callID = fmt.Sprintf("call_%d", i+1)
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:   callID,
				Type: "function",
				Function: ToolCallFunction{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			})
		}
	}
	content = textBuf.String()
	if content == "" && obj.OutputText != "" {
		content = obj.OutputText
	}
	return strings.TrimSpace(thinkBuf.String()), content, toolCalls
}

func parseNonStreamResponses(body []byte) (*StreamResult, error) {
	var obj responsesObject
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	if obj.Error != nil && obj.Error.Message != "" {
		return nil, fmt.Errorf("AI 服务返回错误: %s", obj.Error.Message)
	}
	thinking, content, toolCalls := extractResponsesResult(&obj)
	if thinking == "" {
		thinking, content = splitThinkTags(content)
	}
	if thinking == "" && strings.TrimSpace(content) == "" && len(toolCalls) == 0 {
		return nil, ErrEmptyResponse
	}
	return &StreamResult{
		Thinking:  thinking,
		Content:   content,
		ToolCalls: toolCalls,
		Usage:     obj.Usage.toUsage(),
	}, nil
}

func parseResponsesSSEStream(r io.Reader, onProgress func(StreamProgress)) (*StreamResult, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	var thinkingBuf, contentBuf strings.Builder
	var thinkStart, thinkEnd time.Time
	var toolOrder []int
	toolMap := map[int]*ToolCall{}
	itemToIndex := map[string]int{}
	var usage Usage
	var sawAny bool
	var sawCompleted bool
	var currentEvent string

	ensureSlot := func(idx int, itemID string) *ToolCall {
		if slot, ok := toolMap[idx]; ok {
			if itemID != "" {
				itemToIndex[itemID] = idx
			}
			return slot
		}
		if itemID != "" {
			if existingIdx, ok := itemToIndex[itemID]; ok {
				return toolMap[existingIdx]
			}
			itemToIndex[itemID] = idx
		}
		slot := &ToolCall{Type: "function"}
		toolMap[idx] = slot
		toolOrder = append(toolOrder, idx)
		return slot
	}

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
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var ev struct {
			Type        string               `json:"type"`
			Delta       string               `json:"delta"`
			Text        string               `json:"text"`
			Arguments   string               `json:"arguments"`
			OutputIndex int                  `json:"output_index"`
			ItemID      string               `json:"item_id"`
			Item        *responsesOutputItem `json:"item"`
			Response    *responsesObject     `json:"response"`
			Error       *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		evType := ev.Type
		if evType == "" {
			evType = currentEvent
		}
		currentEvent = ""
		sawAny = true

		if ev.Error != nil && ev.Error.Message != "" {
			return nil, fmt.Errorf("AI 服务返回错误: %s", ev.Error.Message)
		}
		if ev.Response != nil && ev.Response.Error != nil && ev.Response.Error.Message != "" {
			return nil, fmt.Errorf("AI 服务返回错误: %s", ev.Response.Error.Message)
		}

		switch evType {
		case "response.output_text.delta":
			if ev.Delta != "" {
				if !thinkStart.IsZero() && thinkEnd.IsZero() {
					thinkEnd = time.Now()
				}
				contentBuf.WriteString(ev.Delta)
				emit()
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.reasoning.delta":
			if ev.Delta != "" {
				if thinkStart.IsZero() {
					thinkStart = time.Now()
				}
				thinkingBuf.WriteString(ev.Delta)
				thinkEnd = time.Now()
				emit()
			}
		case "response.output_item.added":
			if ev.Item != nil && ev.Item.Type == "function_call" {
				slot := ensureSlot(ev.OutputIndex, ev.Item.ID)
				if ev.Item.CallID != "" {
					slot.ID = ev.Item.CallID
				} else if ev.Item.ID != "" && slot.ID == "" {
					slot.ID = ev.Item.ID
				}
				if ev.Item.Name != "" {
					slot.Function.Name = ev.Item.Name
				}
				if ev.Item.Arguments != "" && slot.Function.Arguments == "" {
					slot.Function.Arguments = ev.Item.Arguments
				}
				emit()
			}
		case "response.function_call_arguments.delta":
			idx := ev.OutputIndex
			if ev.ItemID != "" {
				if mapped, ok := itemToIndex[ev.ItemID]; ok {
					idx = mapped
				}
			}
			slot := ensureSlot(idx, ev.ItemID)
			slot.Function.Arguments += ev.Delta
			emit()
		case "response.function_call_arguments.done":
			idx := ev.OutputIndex
			if ev.ItemID != "" {
				if mapped, ok := itemToIndex[ev.ItemID]; ok {
					idx = mapped
				}
			}
			slot := ensureSlot(idx, ev.ItemID)
			if ev.Arguments != "" {
				slot.Function.Arguments = ev.Arguments
			}
			emit()
		case "response.output_item.done":
			if ev.Item != nil && ev.Item.Type == "function_call" {
				slot := ensureSlot(ev.OutputIndex, ev.Item.ID)
				if ev.Item.CallID != "" {
					slot.ID = ev.Item.CallID
				}
				if ev.Item.Name != "" {
					slot.Function.Name = ev.Item.Name
				}
				if ev.Item.Arguments != "" {
					slot.Function.Arguments = ev.Item.Arguments
				}
				emit()
			} else if ev.Item != nil && ev.Item.Type == "message" && contentBuf.Len() == 0 {
				for _, part := range ev.Item.Content {
					if part.Text != "" {
						contentBuf.WriteString(part.Text)
					}
				}
				if contentBuf.Len() > 0 {
					emit()
				}
			}
		case "response.completed", "response.done":
			sawCompleted = true
			if ev.Response != nil {
				if ev.Response.Usage != nil {
					usage = ev.Response.Usage.toUsage()
				}
				fThink, fContent, fTools := extractResponsesResult(ev.Response)
				if thinkingBuf.Len() == 0 && fThink != "" {
					thinkingBuf.WriteString(fThink)
				}
				if contentBuf.Len() == 0 && fContent != "" {
					contentBuf.WriteString(fContent)
				}
				if len(toolOrder) == 0 && len(fTools) > 0 {
					for i, tc := range fTools {
						cp := tc
						toolMap[i] = &cp
						toolOrder = append(toolOrder, i)
					}
				}
				emit()
			}
		}
	}

	if err := scanner.Err(); err != nil {
		thPartial, ctPartial := splitThinkTagsIfNeeded(thinkingBuf.String(), contentBuf.String())
		tcsPartial := collectToolCalls(toolOrder, toolMap)
		if !(sawCompleted && isStreamResetError(err) && (thPartial != "" || strings.TrimSpace(ctPartial) != "" || len(tcsPartial) > 0)) {
			return nil, err
		}
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
