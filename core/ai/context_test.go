package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestToolOutputStoreAndSanitize(t *testing.T) {
	store := NewToolOutputStore()
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("line %02d: status=%s", i, func() string {
			if i%5 == 0 {
				return "ERROR"
			}
			return "OK"
		}()))
	}
	id := store.Put("ssh_exec", strings.Join(lines, "\n"))
	if id == "" {
		t.Fatal("expected non-empty handle_id")
	}
	page, err := store.Read(id, 1, 5, "")
	if err != nil || !strings.Contains(page, "1: line 01") || !strings.Contains(page, "可传 offset_line=6") {
		t.Fatalf("unexpected paged output: err=%v\n%s", err, page)
	}
	grepped, err := store.Read(id, 1, 10, "error")
	if err != nil || !strings.Contains(grepped, "5: line 05") || strings.Contains(grepped, "line 01") {
		t.Fatalf("unexpected grep output: err=%v\n%s", err, grepped)
	}

	// SanitizeMessageSequence 应移除孤立 tool 与未闭合的 tool_calls
	dirty := []Message{
		{Role: "system", Content: "sys"},
		{Role: "tool", ToolCallID: "orphan", Content: "bad"},
		{Role: "assistant", Content: "calling", ToolCalls: []ToolCall{{ID: "c1"}, {ID: "c2"}}},
		{Role: "tool", ToolCallID: "c1", Content: "ok"},
		{Role: "user", Content: "next"},
	}
	cleaned := SanitizeMessageSequence(dirty)
	if len(cleaned) != 4 || len(cleaned[1].ToolCalls) != 1 || cleaned[1].ToolCalls[0].ID != "c1" {
		t.Fatalf("unexpected sanitized sequence: %+v", cleaned)
	}
}

func TestRunAgentParallelReadOnlyAnd413Retry(t *testing.T) {
	var round atomic.Int32
	var activeTools atomic.Int32
	var maxConcurrent atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req streamChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		cur := round.Add(1)
		if cur == 1 {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role": "assistant",
						"tool_calls": []map[string]any{
							{"id": "t1", "type": "function", "function": map[string]string{"name": "sftp_read", "arguments": `{"path":"a"}`}},
							{"id": "t2", "type": "function", "function": map[string]string{"name": "sftp_read", "arguments": `{"path":"b"}`}},
						},
					},
				}},
			})
			return
		}
		if cur == 2 {
			// 模拟上下文溢出（413）触发自动压缩重试
			http.Error(w, `{"error":{"message":"context_length_exceeded"}}`, http.StatusRequestEntityTooLarge)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "完成并行探测并在压缩后恢复。",
				},
			}},
		})
	}))
	defer ts.Close()

	client := NewClient(Config{BaseURL: ts.URL, APIKey: "sk-test", Model: "gpt-4o"})
	var compacted bool
	res, err := RunAgent(context.Background(), AgentRunConfig{
		Client:           client,
		EnableCompaction: true,
		Messages: []Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "q1"},
			{Role: "assistant", Content: "a1"},
			{Role: "user", Content: "q2"},
			{Role: "assistant", Content: "a2"},
			{Role: "user", Content: "check a and b"},
		},
		IsReadOnlyTool: func(call ToolCall) bool {
			return call.Function.Name == "sftp_read"
		},
		ExecuteTool: func(_ context.Context, call ToolCall) (string, bool) {
			n := activeTools.Add(1)
			for {
				prev := maxConcurrent.Load()
				if n <= prev || maxConcurrent.CompareAndSwap(prev, n) {
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
			activeTools.Add(-1)
			return "content:" + call.Function.Arguments, false
		},
		OnEvent: func(ev AgentEvent) {
			if ev.Kind == EventCompacted {
				compacted = true
			}
		},
	})
	if err != nil {
		t.Fatalf("RunAgent failed: %v", err)
	}
	if maxConcurrent.Load() < 2 {
		t.Fatalf("expected parallel execution for read-only tools, maxConcurrent=%d", maxConcurrent.Load())
	}
	if !compacted || !strings.Contains(res.FinalText, "完成并行探测") {
		t.Fatalf("expected compaction + retry success, compacted=%v final=%q", compacted, res.FinalText)
	}
}
