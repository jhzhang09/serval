package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// EstimateTokens 粗略估算文本对应的 token 数量（兼容中英文混合文本）。
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	runes := utf8.RuneCountInString(s)
	bytes := len(s)
	// 中文/多字节字符按 ~1.3 rune/token，英文按 ~3.5 字符/token 估算，取较大值偏保守
	if bytes > runes {
		return max(1, (runes*2+bytes)/4)
	}
	return max(1, (bytes+2)/3)
}

// EstimateMessagesTokens 估算消息序列的总 token 占用。
func EstimateMessagesTokens(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		total += 8 // role 与结构开销
		total += EstimateTokens(m.Content)
		for _, tc := range m.ToolCalls {
			total += 12 + EstimateTokens(tc.Function.Name) + EstimateTokens(tc.Function.Arguments)
		}
	}
	return total
}

// ModelContextWindow 返回常见模型的默认上下文窗口大小（token）。
func ModelContextWindow(model string) int {
	low := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(low, "claude"),
		strings.Contains(low, "gemini"),
		strings.Contains(low, "gpt-5"),
		strings.HasPrefix(low, "o3"),
		strings.HasPrefix(low, "o4"):
		return 200000
	case strings.Contains(low, "gpt-4o"),
		strings.Contains(low, "gpt-4.1"),
		strings.Contains(low, "deepseek"),
		strings.Contains(low, "qwen"),
		strings.Contains(low, "kimi"),
		strings.Contains(low, "glm"),
		strings.Contains(low, "minimax"):
		return 128000
	default:
		return 64000
	}
}

// IsContextOverflowError 判断服务端错误是否属于上下文窗口溢出（HTTP 413 或 token 超限提示）。
func IsContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusRequestEntityTooLarge {
		return true
	}
	low := strings.ToLower(err.Error())
	for _, kw := range []string{
		"context_length_exceeded",
		"maximum context length",
		"prompt is too long",
		"too many tokens",
		"input is too long",
		"exceeds the context window",
		"request entity too large",
	} {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}

// SanitizeMessageSequence 修复消息序列中的孤立 tool 消息或缺少对应 tool 结果的 assistant.tool_calls，
// 确保所有服务商协议校验（OpenAI / Anthropic / Google）均可通过。
func SanitizeMessageSequence(msgs []Message) []Message {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]Message, 0, len(msgs))
	pendingCalls := map[string]bool{}

	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			// 若前一条 assistant 还有未配对的 tool_calls，先剥离其未响应的 tool_calls
			if len(pendingCalls) > 0 && len(out) > 0 {
				stripUnmatchedToolCalls(out, pendingCalls)
				clear(pendingCalls)
			}
			out = append(out, m)
			for _, tc := range m.ToolCalls {
				if tc.ID != "" {
					pendingCalls[tc.ID] = true
				}
			}
		case "tool":
			if m.ToolCallID != "" && pendingCalls[m.ToolCallID] {
				delete(pendingCalls, m.ToolCallID)
				out = append(out, m)
			}
		default:
			if len(pendingCalls) > 0 && len(out) > 0 {
				stripUnmatchedToolCalls(out, pendingCalls)
				clear(pendingCalls)
			}
			out = append(out, m)
		}
	}
	if len(pendingCalls) > 0 && len(out) > 0 {
		stripUnmatchedToolCalls(out, pendingCalls)
	}
	return out
}

func stripUnmatchedToolCalls(msgs []Message, missing map[string]bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" || len(msgs[i].ToolCalls) == 0 {
			continue
		}
		var kept []ToolCall
		for _, tc := range msgs[i].ToolCalls {
			if !missing[tc.ID] {
				kept = append(kept, tc)
			}
		}
		msgs[i].ToolCalls = kept
		break
	}
}

// PruneStaleToolOutputs 将较早轮次的大体积工具输出折叠为简短摘要，同时保留 handle_id 索引行以便模型按需重读。
func PruneStaleToolOutputs(msgs []Message, keepRecentTools int) []Message {
	if keepRecentTools <= 0 {
		keepRecentTools = 4
	}
	toolIndices := make([]int, 0, len(msgs))
	for i, m := range msgs {
		if m.Role == "tool" {
			toolIndices = append(toolIndices, i)
		}
	}
	if len(toolIndices) <= keepRecentTools {
		return msgs
	}
	cutoffIdx := toolIndices[len(toolIndices)-keepRecentTools]
	out := make([]Message, len(msgs))
	copy(out, msgs)
	for i := 0; i < cutoffIdx; i++ {
		if out[i].Role != "tool" || len(out[i].Content) <= 800 {
			continue
		}
		handleLine := ""
		for _, line := range strings.Split(out[i].Content, "\n") {
			if strings.Contains(line, "handle_id:") || strings.Contains(line, "handle_id=") {
				handleLine = "\n" + strings.TrimSpace(line)
				break
			}
		}
		runes := []rune(out[i].Content)
		if len(runes) > 320 {
			out[i].Content = string(runes[:320]) + "\n...[较早工具输出已折叠]" + handleLine
		}
	}
	return out
}

// CompactMessages 当上下文接近上限或发生 413 溢出时，保留系统提示与最近消息，并将早期对话压缩为结构化摘要。
// 若模型摘要失败或 client 为 nil，则自动回退到确定性滑动窗口裁剪，确保序列始终合法。
func CompactMessages(ctx context.Context, client *Client, msgs []Message, targetTokens int) ([]Message, bool) {
	cleaned := SanitizeMessageSequence(msgs)
	if len(cleaned) <= 4 {
		return cleaned, false
	}
	if targetTokens <= 0 {
		targetTokens = 32000
	}

	var sysMsgs []Message
	bodyStart := 0
	for bodyStart < len(cleaned) && cleaned[bodyStart].Role == "system" {
		sysMsgs = append(sysMsgs, cleaned[bodyStart])
		bodyStart++
	}
	body := cleaned[bodyStart:]
	if len(body) <= 3 {
		return cleaned, false
	}

	// 从尾部向前保留尽量多且在 ~55% targetTokens 预算内的完整对话片段，切分点必须落在 user 或普通 assistant 上
	keepBudget := max(targetTokens*55/100, 2000)
	splitIdx := len(body) - 1
	keptTokens := EstimateMessagesTokens(body[splitIdx:])
	for i := len(body) - 2; i >= 1; i-- {
		t := EstimateMessagesTokens(body[i : i+1])
		if keptTokens+t > keepBudget && len(body)-i >= 2 {
			break
		}
		keptTokens += t
		splitIdx = i
	}
	// 向前调整切分点，避免把 tool 消息与对应的 assistant(tool_calls) 拆开
	for splitIdx < len(body) && body[splitIdx].Role == "tool" {
		splitIdx++
	}
	if splitIdx <= 0 || splitIdx >= len(body) {
		return cleaned, false
	}

	older := body[:splitIdx]
	recent := body[splitIdx:]

	summaryText := ""
	if client != nil && len(older) > 0 {
		var transcript strings.Builder
		for _, m := range older {
			snippet := m.Content
			if runes := []rune(snippet); len(runes) > 600 {
				snippet = string(runes[:600]) + "…"
			}
			transcript.WriteString("[" + m.Role + "] " + snippet + "\n")
			for _, tc := range m.ToolCalls {
				transcript.WriteString(fmt.Sprintf("  -> tool:%s(%s)\n", tc.Function.Name, tc.Function.Arguments))
			}
		}
		sumCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		sumRes, err := client.Chat(sumCtx, []Message{
			{
				Role:    "system",
				Content: "请将以下历史运维排查对话浓缩为简明的中文摘要（保留已确认的主机状态、关键路径、已执行操作与未解决问题，不超过 400 字）。",
			},
			{
				Role:    "user",
				Content: transcript.String(),
			},
		})
		if err == nil && strings.TrimSpace(sumRes) != "" {
			summaryText = strings.TrimSpace(sumRes)
		}
	}

	if summaryText == "" {
		// 确定性回退摘要
		var b strings.Builder
		b.WriteString("早期对话摘要（已自动裁剪较早上下文）：\n")
		count := 0
		for _, m := range older {
			if m.Role == "user" || (m.Role == "assistant" && strings.TrimSpace(m.Content) != "") {
				line := strings.TrimSpace(strings.ReplaceAll(m.Content, "\n", " "))
				if r := []rune(line); len(r) > 120 {
					line = string(r[:120]) + "…"
				}
				b.WriteString("- " + m.Role + ": " + line + "\n")
				count++
				if count >= 6 {
					break
				}
			}
		}
		summaryText = strings.TrimSpace(b.String())
	}

	out := make([]Message, 0, len(sysMsgs)+1+len(recent))
	out = append(out, sysMsgs...)
	out = append(out, Message{
		Role:    "user",
		Content: "[历史会话摘要]\n" + summaryText,
	})
	out = append(out, recent...)
	return SanitizeMessageSequence(out), true
}

// ToolOutputStore 保存单次会话中被截断的完整工具输出（环形缓冲，最多 32 个句柄、单条上限 256KB），
// 供模型通过 tool_output_read 分页或按关键词检索。
type ToolOutputStore struct {
	mu      sync.Mutex
	seq     int
	order   []string
	entries map[string]toolOutputEntry
}

type toolOutputEntry struct {
	source string
	lines  []string
}

// NewToolOutputStore 创建一个线程安全的工具大输出缓存。
func NewToolOutputStore() *ToolOutputStore {
	return &ToolOutputStore{
		entries: make(map[string]toolOutputEntry),
	}
}

// Put 存入一段输出并返回供模型查询的 handle_id（例如 out_1）。
func (s *ToolOutputStore) Put(source, content string) string {
	if s == nil {
		return ""
	}
	const maxStoredBytes = 256 << 10
	if len(content) > maxStoredBytes {
		cut := maxStoredBytes
		for cut > 0 && !utf8.RuneStart(content[cut]) {
			cut--
		}
		content = content[:cut] + "\n...[存储上限已达 256KB]"
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")

	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := fmt.Sprintf("out_%d", s.seq)
	s.entries[id] = toolOutputEntry{
		source: source,
		lines:  lines,
	}
	s.order = append(s.order, id)
	const maxHandles = 32
	for len(s.order) > maxHandles {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.entries, oldest)
	}
	return id
}

// Read 按 1-based 行号分页或按关键词过滤读取已缓存的工具输出。
func (s *ToolOutputStore) Read(handleID string, offsetLine, limitLines int, grep string) (string, error) {
	if s == nil {
		return "", errors.New("输出缓存未初始化")
	}
	s.mu.Lock()
	entry, ok := s.entries[strings.TrimSpace(handleID)]
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("未找到 handle_id=%q（可能已过期或超出缓存数量）", handleID)
	}

	if offsetLine <= 0 {
		offsetLine = 1
	}
	if limitLines <= 0 {
		limitLines = 80
	}
	if limitLines > 200 {
		limitLines = 200
	}

	needle := strings.ToLower(strings.TrimSpace(grep))
	var matched []string
	if needle != "" {
		for idx, line := range entry.lines {
			if strings.Contains(strings.ToLower(line), needle) {
				matched = append(matched, fmt.Sprintf("%d: %s", idx+1, line))
			}
		}
	} else {
		for idx, line := range entry.lines {
			matched = append(matched, fmt.Sprintf("%d: %s", idx+1, line))
		}
	}

	total := len(matched)
	if total == 0 {
		return fmt.Sprintf("handle_id=%s source=%s total_lines=%d（未匹配到内容）", handleID, entry.source, len(entry.lines)), nil
	}
	start := offsetLine - 1
	if start >= total {
		return fmt.Sprintf("handle_id=%s source=%s total_matched=%d offset=%d 超出范围", handleID, entry.source, total, offsetLine), nil
	}
	end := min(start+limitLines, total)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("handle_id=%s source=%s lines=%d-%d/%d\n", handleID, entry.source, start+1, end, total))
	b.WriteString(strings.Join(matched[start:end], "\n"))
	if end < total {
		b.WriteString(fmt.Sprintf("\n...[还有 %d 行，可传 offset_line=%d 继续读取]", total-end, end+1))
	}
	return b.String(), nil
}
