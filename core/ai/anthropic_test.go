package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicStreamAndToolLoop(t *testing.T) {
	step := 0
	var sawAPIKey, sawVersion, sawCacheControl bool
	var sawThinkingSigInSecondTurn bool

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.Error(w, "not found: "+r.URL.Path, http.StatusNotFound)
			return
		}
		sawAPIKey = r.Header.Get("x-api-key") == "sk-ant-test"
		sawVersion = r.Header.Get("anthropic-version") == anthropicAPIVersion

		var req anthropicMessagesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.System) > 0 && req.System[len(req.System)-1].CacheControl != nil {
			sawCacheControl = true
		}

		step++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		writeEv := func(ev string, payload string) {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev, payload)
			if flusher != nil {
				flusher.Flush()
			}
		}

		if step == 1 {
			writeEv("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":40}}}`)
			writeEv("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`)
			writeEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"先检查磁盘"}}`)
			writeEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-xyz"}}`)
			writeEv("content_block_stop", `{"type":"content_block_stop","index":0}`)
			writeEv("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"ssh_exec","input":{}}}`)
			writeEv("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"df -h\"}"}}`)
			writeEv("content_block_stop", `{"type":"content_block_stop","index":1}`)
			writeEv("message_delta", `{"type":"message_delta","usage":{"output_tokens":25}}`)
			writeEv("message_stop", `{"type":"message_stop"}`)
			return
		}

		for _, m := range req.Messages {
			if m.Role == "assistant" {
				for _, b := range m.Content {
					if b.Type == "thinking" && b.Signature == "sig-xyz" {
						sawThinkingSigInSecondTurn = true
					}
				}
			}
		}
		writeEv("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":150,"cache_read_input_tokens":100}}}`)
		writeEv("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`)
		writeEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"磁盘空间充足。"}}`)
		writeEv("content_block_stop", `{"type":"content_block_stop","index":0}`)
		writeEv("message_delta", `{"type":"message_delta","usage":{"output_tokens":12}}`)
		writeEv("message_stop", `{"type":"message_stop"}`)
	}))
	defer ts.Close()

	client := NewClient(Config{
		BaseURL:  ts.URL + "/v1",
		APIKey:   "sk-ant-test",
		Model:    "claude-sonnet-4-5",
		Protocol: "anthropic",
	})
	res, err := RunAgent(context.Background(), AgentRunConfig{
		Client: client,
		Messages: []Message{
			{Role: "system", Content: "ops agent"},
			{Role: "user", Content: "check disk"},
		},
		Tools: []ToolDefinition{{
			Type:     "function",
			Function: ToolFunction{Name: "ssh_exec", Parameters: json.RawMessage(`{"type":"object"}`)},
		}},
		ExecuteTool: func(_ context.Context, call ToolCall) (string, bool) {
			if call.Function.Name != "ssh_exec" || !strings.Contains(call.Function.Arguments, "df -h") {
				return "bad call", true
			}
			return "Filesystem /dev/sda1 50%", false
		},
	})
	if err != nil {
		t.Fatalf("RunAgent failed: %v", err)
	}
	if !sawAPIKey || !sawVersion || !sawCacheControl || !sawThinkingSigInSecondTurn {
		t.Fatalf("missing anthropic headers/caching/signature: key=%v ver=%v cache=%v sig=%v",
			sawAPIKey, sawVersion, sawCacheControl, sawThinkingSigInSecondTurn)
	}
	if res.FinalText != "磁盘空间充足。" || res.Usage.CachedTokens != 140 {
		t.Fatalf("unexpected result: %+v", res)
	}
}
