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
)

func TestRunAgentStreamingToolLoop(t *testing.T) {
	var round atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		n := round.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		switch n {
		case 1:
			// 第一轮：流式推理 + 增量 tool_call 参数拼接 + usage
			chunks := []string{
				`{"choices":[{"delta":{"reasoning_content":"先查 CPU "}}]}`,
				`{"choices":[{"delta":{"reasoning_content":"和内存"}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"ssh_exec","arguments":"{\"command\":"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"lscpu && free -h\"}"}}]}}]}`,
				`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_cache_hit_tokens":40,"completion_tokens_details":{"reasoning_tokens":12}}}`,
			}
			for _, c := range chunks {
				fmt.Fprintf(w, "data: %s\n\n", c)
				if flusher != nil {
					flusher.Flush()
				}
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
		case 2:
			// 检查第一轮工具结果已回传
			msgs, _ := req["messages"].([]any)
			hasToolMsg := false
			for _, m := range msgs {
				mm, _ := m.(map[string]any)
				if mm["role"] == "tool" && mm["tool_call_id"] == "call_1" {
					hasToolMsg = true
				}
			}
			if !hasToolMsg {
				http.Error(w, "missing tool message", 400)
				return
			}
			chunks := []string{
				`{"choices":[{"delta":{"content":"### 配置概览\n\n| 项目 | 配置 |\n|---|---|\n| CPU | 4 vCPU |\n"}}]}`,
				`{"choices":[],"usage":{"prompt_tokens":220,"completion_tokens":35,"total_tokens":255,"prompt_tokens_details":{"cached_tokens":100}}}`,
			}
			for _, c := range chunks {
				fmt.Fprintf(w, "data: %s\n\n", c)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			http.Error(w, "unexpected round", 500)
		}
	}))
	defer ts.Close()

	client := NewClient(Config{BaseURL: ts.URL, APIKey: "sk-test", Model: "deepseek-chat"})
	var gotEvents []AgentEvent
	var executed []string

	res, err := RunAgent(context.Background(), AgentRunConfig{
		Client:   client,
		Messages: []Message{{Role: "user", Content: "这台服务配置如何"}},
		Tools: []ToolDefinition{{
			Type:     "function",
			Function: ToolFunction{Name: "ssh_exec", Parameters: json.RawMessage(`{"type":"object"}`)},
		}},
		ExecuteTool: func(_ context.Context, call ToolCall) (string, bool) {
			executed = append(executed, call.Function.Name+":"+call.Function.Arguments)
			return "CPU: 4\nMem: 16Gi", false
		},
		OnEvent: func(ev AgentEvent) {
			gotEvents = append(gotEvents, ev)
		},
	})
	if err != nil {
		t.Fatalf("RunAgent failed: %v", err)
	}
	if res.StepCount != 2 {
		t.Fatalf("expected 2 steps, got %d", res.StepCount)
	}
	if len(executed) != 1 || !strings.Contains(executed[0], "lscpu && free -h") {
		t.Fatalf("unexpected executed tools: %+v", executed)
	}
	if !strings.Contains(res.FinalText, "4 vCPU") {
		t.Fatalf("unexpected final text: %q", res.FinalText)
	}
	if res.Usage.PromptTokens != 320 || res.Usage.CompletionTokens != 55 || res.Usage.TotalTokens != 375 ||
		res.Usage.CachedTokens != 140 || res.Usage.ReasoningTokens != 12 {
		t.Fatalf("unexpected aggregated usage: %+v", res.Usage)
	}

	var sawThinking, sawToolStart, sawToolDone bool
	for _, ev := range gotEvents {
		if strings.Contains(ev.Thinking, "先查 CPU") {
			sawThinking = true
		}
		if ev.Kind == EventToolStart {
			sawToolStart = true
		}
		if ev.Kind == EventToolDone && strings.Contains(ev.ToolOutput, "Mem: 16Gi") {
			sawToolDone = true
		}
	}
	if !sawThinking || !sawToolStart || !sawToolDone {
		t.Fatalf("missing expected lifecycle events: think=%v start=%v done=%v", sawThinking, sawToolStart, sawToolDone)
	}
}

func TestStreamChatFallbackWithoutStreamOptionsAndJSONResponse(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if n == 1 {
			if _, ok := req["stream_options"]; !ok {
				t.Fatal("first attempt should include stream_options")
			}
			http.Error(w, `{"error":"unsupported stream_options"}`, http.StatusBadRequest)
			return
		}
		if _, ok := req["stream_options"]; ok {
			t.Fatal("fallback attempt must omit stream_options")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{
					"content": "<think>思考一下</think>\n已检查完成。",
				}},
			},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	defer ts.Close()

	client := NewClient(Config{BaseURL: ts.URL, APIKey: "sk-test", Model: "legacy-model"})
	res, err := client.StreamChat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("StreamChat fallback failed: %v", err)
	}
	if res.Thinking != "思考一下" || res.Content != "已检查完成。" || res.Usage.TotalTokens != 15 {
		t.Fatalf("unexpected fallback result: %+v", res)
	}
}
