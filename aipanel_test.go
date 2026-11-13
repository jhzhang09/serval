package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"serval/core/ai"
)

// mockAIServer 创建模拟 OpenAI API 的 HTTP 服务
func mockAIServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-sk" {
			http.Error(w, `{"error":{"message":"Incorrect API key"}}`, http.StatusUnauthorized)
			return
		}

		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "gpt-4o"}},
			})
		case "/chat/completions":
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			res := map[string]any{
				"choices": []map[string]any{
					{
						"message": map[string]string{
							"content": `{"explanation":"使用 find 命令排查大文件","command":"find / -type f -size +100M"}`,
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(res)
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestAISettingsAndProbe 测试在设置中配置 AI 助手并测试连通性
func TestAISettingsAndProbe(t *testing.T) {
	ts := mockAIServer(t)
	defer ts.Close()

	h := newHarness(t)
	h.tt.Frame()

	// 打开设置
	h.click("设置")
	h.tt.Frame()
	if !h.a.settingsPanel.open {
		t.Fatal("设置面板未打开")
	}

	// 切换到 AI Tab
	h.click("AI")
	h.tt.Frame()

	// 填写配置
	h.a.settingsPanel.aiBaseURL = ts.URL
	h.a.settingsPanel.aiModel = "gpt-4o"
	h.a.settingsPanel.aiKey = "test-sk"
	h.tt.Frame()

	// 测试连接
	h.click("测试连接")
	h.pump("AI 测试连接", func() bool {
		return strings.Contains(h.a.settingsPanel.aiTestMsg, "测试成功")
	})

	// 关闭设置自动保存配置
	h.click("完成")
	h.tt.Frame()

	if h.a.settings.AIBaseURL != ts.URL || h.a.settings.AIModel != "gpt-4o" {
		t.Fatalf("设置未正确保存: baseURL=%s, model=%s", h.a.settings.AIBaseURL, h.a.settings.AIModel)
	}
	if h.a.getAIKey() != "test-sk" {
		t.Fatalf("API Key 未正确保存到加密保险库")
	}
}

// TestAIPanelGenerateAndInsertCommand 测试 AI 助手生成命令、复制及安全填入终端
func TestAIPanelGenerateAndInsertCommand(t *testing.T) {
	ts := mockAIServer(t)
	defer ts.Close()

	h := newHarness(t)
	h.tt.Frame()

	// 预先写入 AI 配置
	h.a.settings.AIBaseURL = ts.URL
	h.a.settings.AIModel = "gpt-4o"
	h.a.setAIKey("test-sk")

	// 开启一个本地终端作为活动标签
	h.click("新建")
	h.tt.Frame()
	h.click("本地终端")
	h.tt.Frame()
	if len(h.a.tabs) == 0 {
		t.Fatal("没有打开终端标签")
	}

	// 打开 AI 助手面板
	h.click("AI 助手")
	h.tt.Frame()
	if !h.a.ai.open {
		t.Fatal("AI 助手面板未打开")
	}

	// 输入查询并点击生成命令
	h.tt.Type("查找大于100M的文件")
	h.tt.Frame()
	if h.a.ai.query == "" {
		t.Fatalf("AutoFocus 未聚焦到 AI 输入框，query 为空")
	}
	h.click("生成命令")

	// 等待异步完成
	h.pump("AI 生成命令", func() bool {
		return h.a.ai.command != ""
	})

	if h.a.ai.command != "find / -type f -size +100M" {
		t.Fatalf("AI 返回命令不符合预期: %q, err=%s", h.a.ai.command, h.a.ai.err)
	}
	if !strings.Contains(h.a.ai.explanation, "find") {
		t.Fatalf("AI 返回解释不符合预期: %q", h.a.ai.explanation)
	}

	// 测试复制命令
	h.click("复制命令")
	h.tt.Frame()
	if h.tt.Clipboard() != "find / -type f -size +100M" {
		t.Fatalf("剪贴板未包含命令: got %q", h.tt.Clipboard())
	}

	// 测试安全填入终端（无回车符）
	h.click("填入终端（需确认）")
	h.tt.Frame()
	if h.a.ai.open {
		t.Fatal("填入终端后 AI 面板应当关闭")
	}

	// 验证终端存在
	active := h.a.current()
	if active == nil || active.focus == nil || active.focus.term == nil {
		t.Fatal("没有活动的终端")
	}

	// 验证高危命令拦截
	h.a.openAI()
	h.a.ai.turns = []aiTurn{{query: "danger", explanation: "危险测试", commands: []string{"rm -rf /var/log/*"}}}
	h.tt.Frame()
	h.click("填入终端（需确认）")
	h.tt.Frame()

	// 应该触发确认弹窗
	if !h.a.ai.confirmDanger {
		t.Fatal("高危命令未触发危险拦截确认弹窗")
	}
	if !h.tt.HasText("执行高危命令确认") {
		t.Fatal("界面未显示高危命令确认弹窗")
	}

	// 点击确认填入
	h.click("确认填入")
	h.tt.Frame()
	if h.a.ai.confirmDanger {
		t.Fatal("确认填入后 confirmDanger 状态应复位")
	}
}

// TestAIKeyEncryptedAndMigrated 验证 AI Key 加密写进保险库，并自动迁移旧明文配置。
func TestAIKeyEncryptedAndMigrated(t *testing.T) {
	h := newHarness(t)
	h.a.setAIKey("sk-super-secret-999")
	if h.a.getAIKey() != "sk-super-secret-999" {
		t.Fatalf("getAIKey mismatch: %q", h.a.getAIKey())
	}
	if h.vaultFileContains("sk-super-secret-999") {
		t.Fatal("AI Key 不应以明文形式出现在 vault.json 中")
	}
	if h.a.local.LegacyAIKey != "" {
		t.Fatal("deviceSettings 不应保留明文 AI Key")
	}

	// 模拟从旧版 device.json 迁移明文 Key
	h.a.setAIKey("")
	legacy := h.a.local
	legacy.LegacyAIKey = "sk-legacy-plain-123"
	if err := h.a.saveLocal(legacy); err != nil {
		t.Fatal(err)
	}
	h.a.loadLocal()
	if h.a.getAIKey() != "sk-legacy-plain-123" {
		t.Fatalf("旧版 AI Key 迁移失败: got %q", h.a.getAIKey())
	}
	if h.a.local.LegacyAIKey != "" {
		t.Fatal("迁移后旧明文字段应当被清空")
	}
}

// TestAIMultilineCommandCannotInsert 验证多行脚本不提供“填入终端”且底层拒绝填入。
func TestAIMultilineCommandCannotInsert(t *testing.T) {
	h := newHarness(t)
	h.click("新建")
	h.tt.Frame()
	h.click("本地终端")
	h.tt.Frame()

	h.a.openAI()
	h.a.ai.turns = []aiTurn{{
		query:       "multi",
		explanation: "多行脚本",
		commands:    []string{"echo one\necho two"},
	}}
	h.tt.Frame()
	if h.tt.HasText("填入终端（需确认）") {
		t.Fatal("多行命令不应显示“填入终端（需确认）”按钮")
	}
	if !h.tt.HasText("多行脚本请复制后核对执行") {
		t.Fatal("多行命令应提示用户复制后核对执行")
	}
	h.a.insertCommandToTerminal("echo one\necho two")
	if !h.a.ai.open {
		t.Fatal("多行命令应被 insertCommandToTerminal 拒绝，面板保持打开")
	}
}

// TestAIDangerousCommandPatterns 测试高危命令匹配逻辑
func TestAIDangerousCommandPatterns(t *testing.T) {
	dangers := []string{
		"rm -rf /",
		"rm -r /home",
		"dd if=/dev/zero of=/dev/sda",
		"mkfs.ext4 /dev/sda1",
		":(){ :|:& };:",
		"echo dangerous\nreboot",
		"reboot",
		"shutdown -h now",
		"chmod -R 777 /",
	}

	for _, cmd := range dangers {
		if !ai.IsDangerousCommand(cmd) {
			t.Errorf("应该识别为危险命令: %q", cmd)
		}
	}

	safe := []string{
		"ls -la",
		"cat /etc/os-release",
		"grep -r 'test' .",
		"docker ps",
		"git status",
	}

	for _, cmd := range safe {
		if ai.IsDangerousCommand(cmd) {
			t.Errorf("不应误判为危险命令: %q", cmd)
		}
	}
}
