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

const anthropicAPIVersion = "2023-06-01"

// IsAnthropicEndpoint 判断目标配置是否使用原生 Anthropic Messages API。
func IsAnthropicEndpoint(baseURL string) bool {
	low := strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	return strings.Contains(low, "api.anthropic.com") || strings.HasSuffix(low, "/anthropic")
}

func (c *Client) useAnthropic() bool {
	if strings.EqualFold(strings.TrimSpace(c.protocol), "anthropic") {
		return true
	}
	return IsAnthropicEndpoint(c.baseURL)
}

func (c *Client) anthropicEndpoint(suffix string) string {
	base := strings.TrimRight(c.baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + suffix
	}
	return base + "/v1" + suffix
}

type anthropicCacheControl struct {
	Type string `json:"type"`
}

type anthropicContentBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text,omitempty"`
	Thinking     string                 `json:"thinking,omitempty"`
	Signature    string                 `json:"signature,omitempty"`
	ID           string                 `json:"id,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Input        any                    `json:"input,omitempty"`
	ToolUseID    string                 `json:"tool_use_id,omitempty"`
	Content      string                 `json:"content,omitempty"`
	IsError      bool                   `json:"is_error,omitempty"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicTool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  json.RawMessage        `json:"input_schema"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicMessagesRequest struct {
	Model     string                  `json:"model"`
	MaxTokens int                     `json:"max_tokens"`
	System    []anthropicContentBlock `json:"system,omitempty"`
	Messages  []anthropicMessage      `json:"messages"`
	Tools     []anthropicTool         `json:"tools,omitempty"`
	Stream    bool                    `json:"stream"`
}

type anthropicUsageWire struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func (w *anthropicUsageWire) toUsage() Usage {
	if w == nil {
		return Usage{}
	}
	totalInput := w.InputTokens + w.CacheCreationInputTokens + w.CacheReadInputTokens
	return Usage{
		PromptTokens:     totalInput,
		CompletionTokens: w.OutputTokens,
		TotalTokens:      totalInput + w.OutputTokens,
		CachedTokens:     w.CacheReadInputTokens,
	}
}

func buildAnthropicPayload(model string, messages []Message, tools []ToolDefinition, maxTokens int, stream bool) ([]byte, error) {
	if maxTokens <= 0 {
		maxTokens = 8192
	}
	var sysBlocks []anthropicContentBlock
	var outMsgs []anthropicMessage

	appendToRole := func(role string, blk anthropicContentBlock) {
		if len(outMsgs) > 0 && outMsgs[len(outMsgs)-1].Role == role {
			outMsgs[len(outMsgs)-1].Content = append(outMsgs[len(outMsgs)-1].Content, blk)
			return
		}
		outMsgs = append(outMsgs, anthropicMessage{
			Role:    role,
			Content: []anthropicContentBlock{blk},
		})
	}

	for _, m := range messages {
		switch m.Role {
		case "system":
			if txt := strings.TrimSpace(m.Content); txt != "" {
				sysBlocks = append(sysBlocks, anthropicContentBlock{
					Type: "text",
					Text: txt,
				})
			}
		case "user":
			txt := m.Content
			if txt == "" {
				txt = " "
			}
			appendToRole("user", anthropicContentBlock{
				Type: "text",
				Text: txt,
			})
		case "assistant":
			var blocks []anthropicContentBlock
			if len(m.ReasoningDetails) > 0 {
				var sigObj struct {
					AnthropicThinking  string `json:"anthropic_thinking"`
					AnthropicSignature string `json:"anthropic_signature"`
				}
				if json.Unmarshal(m.ReasoningDetails, &sigObj) == nil && sigObj.AnthropicThinking != "" && sigObj.AnthropicSignature != "" {
					blocks = append(blocks, anthropicContentBlock{
						Type:      "thinking",
						Thinking:  sigObj.AnthropicThinking,
						Signature: sigObj.AnthropicSignature,
					})
				}
			}
			if m.Content != "" {
				blocks = append(blocks, anthropicContentBlock{
					Type: "text",
					Text: m.Content,
				})
			}
			for _, tc := range m.ToolCalls {
				var parsedInput any = map[string]any{}
				if rawArgs := strings.TrimSpace(tc.Function.Arguments); rawArgs != "" {
					var obj any
					if json.Unmarshal([]byte(rawArgs), &obj) == nil && obj != nil {
						parsedInput = obj
					}
				}
				id := tc.ID
				if id == "" {
					id = nextSyntheticCallID()
				}
				blocks = append(blocks, anthropicContentBlock{
					Type:  "tool_use",
					ID:    id,
					Name:  tc.Function.Name,
					Input: parsedInput,
				})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: " "})
			}
			for _, blk := range blocks {
				appendToRole("assistant", blk)
			}
		case "tool":
			id := m.ToolCallID
			if id == "" {
				id = "tool_0"
			}
			appendToRole("user", anthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: id,
				Content:   m.Content,
			})
		}
	}

	// 为 Prompt Caching 标记系统提示词末尾、最后一个工具定义以及倒数第二条 user 消息
	if len(sysBlocks) > 0 {
		sysBlocks[len(sysBlocks)-1].CacheControl = &anthropicCacheControl{Type: "ephemeral"}
	}
	var aTools []anthropicTool
	for _, t := range tools {
		schema := t.Function.Parameters
		if len(bytes.TrimSpace(schema)) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		aTools = append(aTools, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: schema,
		})
	}
	if len(aTools) > 0 {
		aTools[len(aTools)-1].CacheControl = &anthropicCacheControl{Type: "ephemeral"}
	}
	if len(outMsgs) >= 4 {
		userSeen := 0
		for i := len(outMsgs) - 1; i >= 0; i-- {
			if outMsgs[i].Role == "user" && len(outMsgs[i].Content) > 0 {
				userSeen++
				if userSeen == 2 {
					outMsgs[i].Content[len(outMsgs[i].Content)-1].CacheControl = &anthropicCacheControl{Type: "ephemeral"}
					break
				}
			}
		}
	}

	return json.Marshal(anthropicMessagesRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    sysBlocks,
		Messages:  outMsgs,
		Tools:     aTools,
		Stream:    stream,
	})
}

func (c *Client) setAnthropicHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", anthropicAPIVersion)
	if c.apiKey != "" {
		req.Header.Set("x-api-key", c.apiKey)
	}
}

func (c *Client) doAnthropicStream(ctx context.Context, messages []Message, tools []ToolDefinition, onProgress func(StreamProgress)) (*StreamResult, error) {
	if c.apiKey == "" && !isLocalEndpoint(c.baseURL) {
		return nil, ErrNoAPIKey
	}
	u, err := url.Parse(c.baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, ErrInvalidEndpoint
	}

	reqBody, err := buildAnthropicPayload(c.model, messages, tools, 8192, true)
	if err != nil {
		return nil, err
	}

	stepCtx, stepCancel := context.WithCancel(ctx)
	defer stepCancel()

	req, err := http.NewRequestWithContext(stepCtx, "POST", c.anthropicEndpoint("/messages"), bytes.NewReader(reqBody))
	if err != nil {
		return nil, ErrInvalidEndpoint
	}
	c.setAnthropicHeaders(req)
	req.Header.Set("Accept", "text/event-stream, application/json")

	resp, err := c.streamHTTPClient().Do(req)
	if err != nil {
		return nil, mapTransportError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, parseAPIError(resp.StatusCode, body, c.apiKey)
	}

	idleReader, idleTimedOut := newIdleTimeoutReader(resp.Body, c.streamIdleTimeout, stepCancel)
	defer idleReader.Stop()

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "application/json") && !strings.Contains(ct, "text/event-stream") {
		body, readErr := io.ReadAll(io.LimitReader(idleReader, 4<<20))
		if readErr != nil {
			if idleTimedOut.Load() && ctx.Err() == nil {
				return nil, ErrTimeout
			}
			return nil, mapTransportError(readErr)
		}
		res, parseErr := parseAnthropicJSONResponse(body)
		if parseErr != nil {
			return nil, parseErr
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
		return res, nil
	}

	res, parseErr := parseAnthropicSSEStream(idleReader, onProgress)
	if parseErr != nil {
		if idleTimedOut.Load() && ctx.Err() == nil {
			return nil, ErrTimeout
		}
		return nil, mapTransportError(parseErr)
	}
	return res, nil
}

func (c *Client) doAnthropicChat(ctx context.Context, messages []Message, maxTokens int) (string, error) {
	if c.apiKey == "" && !isLocalEndpoint(c.baseURL) {
		return "", ErrNoAPIKey
	}
	reqBody, err := buildAnthropicPayload(c.model, messages, nil, maxTokens, false)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.anthropicEndpoint("/messages"), bytes.NewReader(reqBody))
	if err != nil {
		return "", ErrInvalidEndpoint
	}
	c.setAnthropicHeaders(req)

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
		return "", parseAPIError(resp.StatusCode, body, c.apiKey)
	}
	res, err := parseAnthropicJSONResponse(body)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(res.Content) == "" {
		return "", ErrEmptyResponse
	}
	return strings.TrimSpace(res.Content), nil
}

func (c *Client) listAnthropicModels(ctx context.Context) ([]string, error) {
	if c.apiKey == "" && !isLocalEndpoint(c.baseURL) {
		return nil, ErrNoAPIKey
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.anthropicEndpoint("/models"), nil)
	if err != nil {
		return nil, ErrInvalidEndpoint
	}
	req.Header.Set("anthropic-version", anthropicAPIVersion)
	if c.apiKey != "" {
		req.Header.Set("x-api-key", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, mapTransportError(err)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, parseAPIError(resp.StatusCode, body, c.apiKey)
	}
	if readErr != nil {
		return nil, mapTransportError(readErr)
	}
	models := parseModelsResponse(body)
	if len(models) == 0 {
		return nil, ErrEmptyResponse
	}
	return models, nil
}

func (c *Client) probeAnthropic(ctx context.Context) error {
	if _, err := c.listAnthropicModels(ctx); err == nil {
		return nil
	} else if errorsIsAuthOrRate(err) {
		return err
	}
	_, err := c.doAnthropicChat(ctx, []Message{{Role: "user", Content: "hi"}}, 16)
	return err
}

func errorsIsAuthOrRate(err error) bool {
	if err == nil {
		return false
	}
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.StatusCode == http.StatusUnauthorized ||
			apiErr.StatusCode == http.StatusForbidden ||
			apiErr.StatusCode == http.StatusTooManyRequests
	}
	return false
}

func encodeAnthropicThinkingDetails(thinking, signature string) json.RawMessage {
	if thinking == "" || signature == "" {
		return nil
	}
	b, err := json.Marshal(struct {
		AnthropicThinking  string `json:"anthropic_thinking"`
		AnthropicSignature string `json:"anthropic_signature"`
	}{
		AnthropicThinking:  thinking,
		AnthropicSignature: signature,
	})
	if err != nil {
		return nil
	}
	return b
}

func parseAnthropicJSONResponse(body []byte) (*StreamResult, error) {
	var payload struct {
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		} `json:"content"`
		Usage *anthropicUsageWire `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	if payload.Error != nil && payload.Error.Message != "" {
		return nil, fmt.Errorf("AI 服务返回错误: %s", payload.Error.Message)
	}
	var thinkBuf, textBuf strings.Builder
	var lastThinking, lastSig string
	var toolCalls []ToolCall
	for _, blk := range payload.Content {
		switch blk.Type {
		case "thinking":
			if blk.Thinking != "" {
				thinkBuf.WriteString(blk.Thinking)
				lastThinking = blk.Thinking
			}
			if blk.Signature != "" {
				lastSig = blk.Signature
			}
		case "text":
			textBuf.WriteString(blk.Text)
		case "tool_use":
			args := strings.TrimSpace(string(blk.Input))
			if args == "" || args == "null" {
				args = "{}"
			}
			id := blk.ID
			if id == "" {
				id = nextSyntheticCallID()
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:   id,
				Type: "function",
				Function: ToolCallFunction{
					Name:      blk.Name,
					Arguments: args,
				},
			})
		}
	}
	th := strings.TrimSpace(thinkBuf.String())
	ct := textBuf.String()
	if th == "" && strings.TrimSpace(ct) == "" && len(toolCalls) == 0 {
		return nil, ErrEmptyResponse
	}
	return &StreamResult{
		Thinking:         th,
		ReasoningDetails: encodeAnthropicThinkingDetails(lastThinking, lastSig),
		Content:          ct,
		ToolCalls:        toolCalls,
		Usage:            payload.Usage.toUsage(),
	}, nil
}

func parseAnthropicSSEStream(r io.Reader, onProgress func(StreamProgress)) (*StreamResult, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	var thinkingBuf, contentBuf, sigBuf strings.Builder
	var thinkStart, thinkEnd time.Time
	var toolOrder []int
	toolMap := map[int]*ToolCall{}
	var usage Usage
	var sawAny bool
	var sawStop bool
	var currentEvent string

	emit := func() {
		if onProgress == nil {
			return
		}
		sec := calcThinkingSeconds(thinkStart, thinkEnd, thinkingBuf.Len() > 0)
		onProgress(StreamProgress{
			Thinking:        thinkingBuf.String(),
			ThinkingSeconds: sec,
			Content:         contentBuf.String(),
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
		var ev struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock *struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content_block"`
			Delta *struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			Message *struct {
				Usage *anthropicUsageWire `json:"usage"`
			} `json:"message"`
			Usage *anthropicUsageWire `json:"usage"`
			Error *struct {
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

		switch evType {
		case "message_start":
			if ev.Message != nil && ev.Message.Usage != nil {
				usage = ev.Message.Usage.toUsage()
				emit()
			}
		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				slot := &ToolCall{
					ID:   ev.ContentBlock.ID,
					Type: "function",
					Function: ToolCallFunction{
						Name: ev.ContentBlock.Name,
					},
				}
				if trimmedIn := bytes.TrimSpace(ev.ContentBlock.Input); len(trimmedIn) > 0 && string(trimmedIn) != "{}" && string(trimmedIn) != "null" {
					slot.Function.Arguments = string(trimmedIn)
				}
				toolMap[ev.Index] = slot
				toolOrder = append(toolOrder, ev.Index)
				emit()
			}
		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "thinking_delta":
				if ev.Delta.Thinking != "" {
					if thinkStart.IsZero() {
						thinkStart = time.Now()
					}
					thinkingBuf.WriteString(ev.Delta.Thinking)
					thinkEnd = time.Now()
					emit()
				}
			case "signature_delta":
				if ev.Delta.Signature != "" {
					sigBuf.WriteString(ev.Delta.Signature)
				}
			case "text_delta":
				if ev.Delta.Text != "" {
					if !thinkStart.IsZero() && thinkEnd.IsZero() {
						thinkEnd = time.Now()
					}
					contentBuf.WriteString(ev.Delta.Text)
					emit()
				}
			case "input_json_delta":
				if slot, ok := toolMap[ev.Index]; ok && ev.Delta.PartialJSON != "" {
					slot.Function.Arguments += ev.Delta.PartialJSON
					emit()
				}
			}
		case "message_delta":
			if ev.Usage != nil {
				deltaU := ev.Usage.toUsage()
				if deltaU.PromptTokens > usage.PromptTokens {
					usage.PromptTokens = deltaU.PromptTokens
				}
				if deltaU.CompletionTokens > usage.CompletionTokens {
					usage.CompletionTokens = deltaU.CompletionTokens
				}
				if deltaU.CachedTokens > usage.CachedTokens {
					usage.CachedTokens = deltaU.CachedTokens
				}
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
				emit()
			}
		case "message_stop":
			sawStop = true
		}
	}

	if err := scanner.Err(); err != nil {
		tcsPartial := collectToolCalls(toolOrder, toolMap)
		if !(sawStop && isStreamResetError(err) && (thinkingBuf.Len() > 0 || strings.TrimSpace(contentBuf.String()) != "" || len(tcsPartial) > 0)) {
			return nil, err
		}
	}
	if !sawAny {
		return nil, ErrEmptyResponse
	}
	th := strings.TrimSpace(thinkingBuf.String())
	ct := contentBuf.String()
	tcs := collectToolCalls(toolOrder, toolMap)
	for i := range tcs {
		if strings.TrimSpace(tcs[i].Function.Arguments) == "" {
			tcs[i].Function.Arguments = "{}"
		}
	}
	if th == "" && strings.TrimSpace(ct) == "" && len(tcs) == 0 {
		return nil, ErrEmptyResponse
	}
	return &StreamResult{
		Thinking:         th,
		ThinkingSeconds:  calcThinkingSeconds(thinkStart, thinkEnd, th != ""),
		ReasoningDetails: encodeAnthropicThinkingDetails(th, sigBuf.String()),
		Content:          ct,
		ToolCalls:        tcs,
		Usage:            usage,
	}, nil
}
