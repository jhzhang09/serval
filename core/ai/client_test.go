package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChatSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, `{"error":{"message":"Invalid API key"}}`, http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}

		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		resp := chatResponse{
			Choices: []struct {
				Message struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			}{
				{
					Message: struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					}{
						Role:    "assistant",
						Content: "使用 du 命令查看最大的目录。\n```bash\ndu -ah / | sort -rh | head -n 10\n```",
					},
					FinishReason: "stop",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := NewClient(Config{
		BaseURL: ts.URL,
		APIKey:  "test-key",
		Model:   "gpt-4o-mini",
		Timeout: 2 * time.Second,
	})

	res, err := client.GenerateCommand(context.Background(), "Ubuntu 22.04", "查看占用空间最大的10个文件")
	if err != nil {
		t.Fatalf("GenerateCommand failed: %v", err)
	}

	if res.Command != "du -ah / | sort -rh | head -n 10" {
		t.Fatalf("unexpected command: %q", res.Command)
	}
	if res.Explanation != "使用 du 命令查看最大的目录。" {
		t.Fatalf("unexpected explanation: %q", res.Explanation)
	}
}

func TestExplainError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := chatResponse{
			Choices: []struct {
				Message struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			}{
				{
					Message: struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					}{
						Role:    "assistant",
						Content: "这是端口已被占用的错误。建议先查看占用进程。\n```bash\nlsof -i :8080\n```",
					},
					FinishReason: "stop",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := NewClient(Config{
		BaseURL: ts.URL,
		APIKey:  "test-key",
		Timeout: 2 * time.Second,
	})

	res, err := client.ExplainError(context.Background(), "Debian 12", "bind: address already in use")
	if err != nil {
		t.Fatalf("ExplainError failed: %v", err)
	}
	if res.Command != "lsof -i :8080" {
		t.Fatalf("unexpected fix command: %q", res.Command)
	}
}

func TestAuthError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided"}}`))
	}))
	defer ts.Close()

	client := NewClient(Config{
		BaseURL: ts.URL,
		APIKey:  "bad-key",
		Timeout: 2 * time.Second,
	})

	_, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); got != "AI 服务错误 (401): Incorrect API key provided" {
		t.Fatalf("unexpected error message: %s", got)
	}
}

func TestMissingAPIKey(t *testing.T) {
	client := NewClient(Config{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "",
	})

	_, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != ErrNoAPIKey {
		t.Fatalf("expected ErrNoAPIKey, got: %v", err)
	}
}
