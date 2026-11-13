package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"serval/core/ai"
	"serval/core/vault"
	"serval/locales"
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

	// 输入查询并点击发送
	h.tt.Type("查找大于100M的文件")
	h.tt.Frame()
	if h.a.ai.query == "" {
		t.Fatalf("AutoFocus 未聚焦到 AI 输入框，query 为空")
	}
	h.click("发送")

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

	// 测试安全填入终端（停靠式侧栏填入后保持打开，且不自动回车）
	h.click("填入终端（需确认）")
	h.tt.Frame()
	if !h.a.ai.open {
		t.Fatal("停靠式 AI 侧栏填入终端后应保持打开")
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

// TestAISettingsAutoSaveOnCloseAndTabSwitch 验证在设置里修改 AI 项后直接切换标签页或点“完成”可自动保存，且未修改默认项时不误写保险库。
func TestAISettingsAutoSaveOnCloseAndTabSwitch(t *testing.T) {
	ts := mockAIServer(t)
	defer ts.Close()

	h := newHarness(t)
	h.tt.Frame()

	// 打开设置并直接点“完成”：未修改默认值时不应写入保险库
	h.click("设置")
	h.tt.Frame()
	h.click(locales.Done())
	h.tt.Frame()
	if h.a.settings.AIBaseURL != "" || h.a.settings.AIModel != "" {
		t.Fatalf("未修改 AI 默认设置时不应写入保险库: baseURL=%q, model=%q", h.a.settings.AIBaseURL, h.a.settings.AIModel)
	}

	// 在 AI 页填写配置后直接切换到“通用”页，应自动提交保存
	h.click("设置")
	h.tt.Frame()
	h.click("AI")
	h.tt.Frame()
	h.a.settingsPanel.aiBaseURL = ts.URL
	h.a.settingsPanel.aiModel = "gpt-4o"
	h.a.settingsPanel.aiKey = "test-sk"
	h.tt.Frame()

	h.click(locales.SetGeneral())
	h.tt.Frame()
	if h.a.settings.AIBaseURL != ts.URL || h.a.settings.AIModel != "gpt-4o" {
		t.Fatalf("切换设置页签后 AI 配置未自动保存: baseURL=%q, model=%q", h.a.settings.AIBaseURL, h.a.settings.AIModel)
	}
	if h.a.getAIKey() != "test-sk" {
		t.Fatal("切换设置页签后 AI Key 未自动保存到保险库")
	}

	// 再次在 AI 页修改后直接点“完成”（不点测试连接、不按回车）也应自动保存并关闭设置
	h.click("AI")
	h.tt.Frame()
	h.a.settingsPanel.aiModel = "gpt-4.1-mini"
	h.a.settingsPanel.aiKey = "test-sk-2"
	h.tt.Frame()
	h.click(locales.Done())
	h.tt.Frame()

	if h.a.settingsPanel.open {
		t.Fatal("点击完成后设置面板应当关闭")
	}
	if h.a.settings.AIModel != "gpt-4.1-mini" {
		t.Fatalf("点击完成后 AI 模型未保存: %q", h.a.settings.AIModel)
	}
	if h.a.getAIKey() != "test-sk-2" {
		t.Fatal("点击完成后 AI Key 未保存到保险库")
	}
}

// TestAISettingsCloseRequireProtection 验证未设置主密码时填写 AI Key 点“完成”会先弹出设置主密码，取消保留输入，完成后加密保存并关闭设置。
func TestAISettingsCloseRequireProtection(t *testing.T) {
	h := newBareHarness(t)
	h.tt.Frame()

	h.click("设置")
	h.tt.Frame()
	h.click("AI")
	h.tt.Frame()
	h.a.settingsPanel.aiKey = "sk-needs-protection"
	h.tt.Frame()

	h.click(locales.Done())
	h.tt.Frame()
	if !h.a.setup.open {
		t.Fatal("未加密保险库保存 AI Key 时应当先弹出设置主密码")
	}
	if !h.a.settingsPanel.open {
		t.Fatal("设置主密码让位期间应当保留设置面板状态")
	}
	if h.a.vault.State() != vault.Unprotected {
		t.Fatalf("设置主密码前保险库不应已加密: state=%v", h.a.vault.State())
	}

	// 取消设置主密码：回到设置面板且输入的 Key 还在，保险库里无秘密
	h.click(locales.Cancel())
	h.tt.Frame()
	if h.a.setup.open || !h.a.settingsPanel.open {
		t.Fatalf("取消设置主密码后应恢复设置面板: setup=%v, settings=%v", h.a.setup.open, h.a.settingsPanel.open)
	}
	if h.a.settingsPanel.aiKey != "sk-needs-protection" {
		t.Fatal("取消设置主密码后用户输入不应丢失")
	}
	if h.a.getAIKey() != "" {
		t.Fatal("未设主密码前不应保存 AI Key")
	}

	// 再次点“完成”并完成主密码设置
	h.click(locales.Done())
	h.tt.Frame()
	h.a.setup.password = masterPassword
	h.a.setup.confirm = masterPassword
	h.tt.Frame()
	h.click(locales.SheetSetAndContinue())
	h.pump("完成主密码设置并保存 AI Key", func() bool {
		return !h.a.setup.open && h.a.vault.State() == vault.Unlocked && !h.a.settingsPanel.open
	})

	if h.a.getAIKey() != "sk-needs-protection" {
		t.Fatal("设置主密码完成后 AI Key 未保存到保险库")
	}
	if h.vaultFileContains("sk-needs-protection") {
		t.Fatal("vault.json 中不应出现明文 AI Key")
	}
}

// TestAIKeyMigratedAfterUnlock 验证锁定启动并在输入主密码解锁后，旧 device.json 里的明文 AI Key 会被自动迁移并清除。
func TestAIKeyMigratedAfterUnlock(t *testing.T) {
	h := newHarness(t)
	h.tt.Frame()

	// 写入旧版明文 AI Key，并删除记住的主密码，模拟锁定状态下冷启动
	legacy := h.a.local
	legacy.LegacyAIKey = "sk-locked-legacy-123"
	if err := h.a.saveLocal(legacy); err != nil {
		t.Fatal(err)
	}
	_ = h.device.Delete(rememberedKeyName(h.a.vault.ID()))
	h.reopen()

	if h.a.vault.State() != vault.Locked {
		t.Fatalf("reopen 后保险库应处于锁定状态: %v", h.a.vault.State())
	}
	if h.a.local.LegacyAIKey == "" {
		t.Fatal("锁定状态下不应提前清空待迁移的 LegacyAIKey")
	}

	h.a.unlock.password = masterPassword
	h.a.submitUnlock()
	h.pump("解锁保险库并迁移旧 AI Key", func() bool {
		return !h.a.unlock.busy && h.a.vault.State() == vault.Unlocked
	})

	if h.a.getAIKey() != "sk-locked-legacy-123" {
		t.Fatal("解锁后未自动迁移旧版明文 AI Key")
	}
	if h.a.local.LegacyAIKey != "" {
		t.Fatal("迁移后旧明文字段应当被清空")
	}
	if h.vaultFileContains("sk-locked-legacy-123") {
		t.Fatal("迁移进保险库的 AI Key 不应以明文存于 vault.json")
	}
}

// TestLockVaultClearsAIAndTransientState 验证锁定保险库会取消进行中的 AI 请求并清空敏感会话与临时界面状态。
func TestLockVaultClearsAIAndTransientState(t *testing.T) {
	h := newHarness(t)
	h.tt.Frame()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.a.ai = aiPanel{
		open:          true,
		loading:       true,
		cancel:        cancel,
		query:         "inspect secret",
		contextError:  "sensitive terminal stderr",
		explanation:   "some explanation",
		command:       "rm -rf /tmp/test",
		turns:         []aiTurn{{query: "q", explanation: "e", commands: []string{"ls"}}},
		chatHistory:   []ai.Message{{Role: "user", Content: "q"}},
		confirmDanger: true,
		pendingCmd:    "rm -rf /tmp/test",
	}
	h.a.lastCopiedText = "copied secret output"
	h.a.customGroups = []string{"TempGroup"}
	h.a.groupSheet = groupDialogState{open: true, name: "Draft"}
	h.a.connectingPhase = map[string]string{"h1": "正在连接…"}

	h.a.lockVault()

	if ctx.Err() != context.Canceled {
		t.Fatal("锁定保险库时应当取消正在进行的 AI 请求")
	}
	if h.a.ai.open || h.a.ai.loading || h.a.ai.contextError != "" || len(h.a.ai.turns) != 0 || len(h.a.ai.chatHistory) != 0 || h.a.ai.pendingCmd != "" {
		t.Fatalf("锁定保险库后 AI 面板状态未清空: %+v", h.a.ai)
	}
	if h.a.lastCopiedText != "" {
		t.Fatal("锁定保险库后 lastCopiedText 未清空")
	}
	if len(h.a.customGroups) != 0 || h.a.groupSheet.open || len(h.a.connectingPhase) != 0 {
		t.Fatalf("锁定保险库后临时界面状态未清空: customGroups=%+v groupSheet=%+v phase=%+v", h.a.customGroups, h.a.groupSheet, h.a.connectingPhase)
	}
}

// TestAIPanelEnterSubmitsExplainWhenContextErrorOnly 验证仅有报错上下文且输入为空时，回车与主按钮同样触发报错分析。
func TestAIPanelEnterSubmitsExplainWhenContextErrorOnly(t *testing.T) {
	ts := mockAIServer(t)
	defer ts.Close()

	h := newHarness(t)
	h.a.settings.AIBaseURL = ts.URL
	h.a.settings.AIModel = "gpt-4o"
	h.a.setAIKey("test-sk")
	h.tt.Frame()

	// 模拟打开 AI 报错面板（例如首次报错或手动填入 contextError 后尚未生成首轮结果）
	h.a.ai = aiPanel{
		open:         true,
		fresh:        true,
		contextError: "permission denied (publickey)",
	}
	h.tt.Frame()
	if !h.tt.HasText("分析报错") {
		t.Fatal("仅有报错上下文且无输入时主按钮应显示“分析报错”")
	}

	// 触发输入框 OnSubmit 同款路径
	h.a.submitAIInput()
	h.pump("AI 回车分析报错", func() bool {
		return len(h.a.ai.turns) == 1
	})
	if h.a.ai.turns[0].query != "分析终端报错" {
		t.Fatalf("首轮空输入回车应触发报错分析，实际 query=%q", h.a.ai.turns[0].query)
	}
}

// TestAIStaleCanceledRequestAndEscCloseCancels 验证：
// 1. 旧请求被新请求取消后，其延迟到达的回调不会清空新请求的 loading / cancel 状态；
// 2. 通过 Esc 或点击遮罩将 aiDialog 关闭（open=false）时，即使在设置让位判断之前也会取消进行中的请求。
func TestAIStaleCanceledRequestAndEscCloseCancels(t *testing.T) {
	var seq atomic.Int32
	req1Entered := make(chan struct{})
	req1Done := make(chan struct{})
	req2Entered := make(chan struct{})
	req2Release := make(chan struct{})
	req3Entered := make(chan struct{})
	req3Canceled := make(chan struct{})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := seq.Add(1)
		switch n {
		case 1:
			close(req1Entered)
			<-r.Context().Done()
			close(req1Done)
			http.Error(w, "canceled", http.StatusRequestTimeout)
		case 2:
			close(req2Entered)
			select {
			case <-req2Release:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]string{"content": `{"explanation":"ok","commands":["pwd"]}`}},
				},
			})
		case 3:
			close(req3Entered)
			<-r.Context().Done()
			close(req3Canceled)
			http.Error(w, "canceled", http.StatusRequestTimeout)
		}
	}))
	defer ts.Close()
	defer ts.CloseClientConnections()

	h := newHarness(t)
	h.a.settings.AIBaseURL = ts.URL
	h.a.settings.AIModel = "gpt-4o"
	h.a.setAIKey("test-sk")
	h.tt.Frame()

	h.a.ai = aiPanel{open: true, fresh: true, query: "first"}
	h.tt.Frame()
	h.a.runAIGenerate()
	select {
	case <-req1Entered:
	case <-time.After(5 * time.Second):
		t.Fatal("第一个 AI 请求未发出")
	}

	// 在第一个请求完成前直接发起第二个请求，取消第一个请求
	h.a.ai.query = "second"
	h.a.runAIGenerate()
	select {
	case <-req2Entered:
	case <-time.After(5 * time.Second):
		t.Fatal("第二个 AI 请求未发出")
	}
	select {
	case <-req1Done:
	case <-time.After(5 * time.Second):
		t.Fatal("第一个 AI 请求未被取消")
	}
	// 泵几帧让第一个请求延迟返回的 a.post 回调执行
	for range 5 {
		h.tt.Frame()
	}
	if !h.a.ai.loading || h.a.ai.cancel == nil {
		t.Fatalf("旧请求取消回调不应覆盖新请求的 loading/cancel 状态: loading=%v cancelNil=%v", h.a.ai.loading, h.a.ai.cancel == nil)
	}

	close(req2Release)
	h.pump("第二个 AI 请求完成", func() bool {
		return !h.a.ai.loading && len(h.a.ai.turns) == 2 && !h.a.ai.turns[1].running
	})

	// 发起第三个请求后点击“停止”按钮，应立即取消进行中的请求
	h.a.ai.query = "third"
	h.a.runAIGenerate()
	select {
	case <-req3Entered:
	case <-time.After(5 * time.Second):
		t.Fatal("第三个 AI 请求未发出")
	}
	h.tt.Frame()
	h.click(locales.AiStop())
	h.tt.Frame()

	select {
	case <-req3Canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("点击停止后进行中的 AI 请求未被立即取消")
	}
	if h.a.ai.loading || h.a.ai.cancel != nil {
		t.Fatalf("点击停止后应停止加载状态: loading=%v cancelNil=%v", h.a.ai.loading, h.a.ai.cancel == nil)
	}
}

// TestAIAgentMultiStepSSHInspection 端到端测试：连接真实 devsshd，AI Agent 流式思考、多步调用 host_info 与 ssh_exec、渲染 Markdown 表格和 Token 统计。
func TestAIAgentMultiStepSSHInspection(t *testing.T) {
	var round atomic.Int32
	var sawSSHOutput atomic.Bool

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
		switch n {
		case 1:
			chunks := []string{
				`{"choices":[{"delta":{"reasoning_content":"先检查主机信息并执行远端硬件探测"}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tc_1","type":"function","function":{"name":"host_info","arguments":"{}"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"tc_2","type":"function","function":{"name":"ssh_exec","arguments":"{\"command\":\"printf SERVAL_AGENT_PROBE_OK\",\"reason\":\"探测远端连通与环境\"}"}}]}}]}`,
				`{"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150,"prompt_cache_hit_tokens":20,"completion_tokens_details":{"reasoning_tokens":15}}}`,
			}
			for _, c := range chunks {
				fmt.Fprintf(w, "data: %s\n\n", c)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
		case 2:
			msgs, _ := req["messages"].([]any)
			for _, m := range msgs {
				mm, _ := m.(map[string]any)
				if mm["role"] == "tool" {
					if content, _ := mm["content"].(string); strings.Contains(content, "SERVAL_AGENT_PROBE_OK") {
						sawSSHOutput.Store(true)
					}
				}
			}
			chunks := []string{
				`{"choices":[{"delta":{"content":"### 这台主机配置概览\n\n| 项目 | 配置 |\n| --- | --- |\n| 探针 | SERVAL_AGENT_PROBE_OK |\n\n` + "```bash\\nfree -h\\n```" + `"}}]}`,
				`{"choices":[],"usage":{"prompt_tokens":280,"completion_tokens":40,"total_tokens":320}}`,
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

	h := newHarness(t)
	h.a.settings.AIBaseURL = ts.URL
	h.a.settings.AIModel = "deepseek-chat"
	h.a.setAIKey("test-sk")

	// 连接测试 SSH 服务器
	h.connectDev()

	// 点击“AI 助手”打开停靠侧栏并点击“这台服务配置如何”快捷提问
	h.click("AI 助手")
	h.tt.Frame()
	h.click("这台服务配置如何")

	h.pump("Agent 多轮 SSH 探测完成", func() bool {
		return !h.a.ai.loading && len(h.a.ai.turns) == 1 && !h.a.ai.turns[0].running
	})

	if !sawSSHOutput.Load() {
		t.Fatal("第二轮模型请求未收到 ssh_exec 的真实远端输出结果")
	}
	if !h.tt.HasText("这台主机配置概览") || !h.tt.HasText("SERVAL_AGENT_PROBE_OK") {
		t.Fatalf("未渲染 Markdown 表格报告内容: %q", h.tt.Texts())
	}
	if !h.tt.HasText("Token 470") {
		t.Fatalf("未显示累计 Token 消耗统计: %q", h.tt.Texts())
	}

	// 展开工具卡片应能看到真实输出
	h.click("$ printf SERVAL_AGENT_PROBE_OK")
	h.tt.Frame()
	if !h.tt.HasText("exit_code: 0") {
		t.Fatalf("展开工具卡片未显示真实输出: %q", h.tt.Texts())
	}

	// 验证 toggleAI 收起再打开保留会话内容
	h.a.toggleAI()
	h.tt.Frame()
	if h.a.ai.open {
		t.Fatal("toggleAI 应关闭侧栏")
	}
	h.a.toggleAI()
	h.tt.Frame()
	if !h.a.ai.open || len(h.a.ai.turns) != 1 {
		t.Fatal("重新打开侧栏应保留会话记录")
	}
}

// TestAIAgentPermissionModes 测试三档权限模式：只读观察（允许 SFTP/终端读取但拦截 ssh_exec）、确认执行（弹窗卡片审批）、自动执行（普通放行、高危拦截）。
func TestAIAgentPermissionModes(t *testing.T) {
	var phase atomic.Int32
	var toolOutputs []string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)

		// 从末尾收集最新 tool 消息文本
		msgs, _ := req["messages"].([]any)
		var lastToolText string
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			if mm["role"] == "tool" {
				lastToolText, _ = mm["content"].(string)
			}
		}

		p := phase.Add(1)
		w.Header().Set("Content-Type", "application/json")
		var msg map[string]any
		switch p {
		case 1:
			// 只读观察模式下尝试执行 ssh_exec -> 应被阻止，且 sftp_read 应成功
			msg = map[string]any{
				"tool_calls": []map[string]any{
					{"id": "c_obs_read", "type": "function", "function": map[string]string{"name": "sftp_read", "arguments": `{"path":"go.mod"}`}},
					{"id": "c_obs_exec", "type": "function", "function": map[string]string{"name": "ssh_exec", "arguments": `{"command":"echo should_block"}`}},
				},
			}
		case 2:
			toolOutputs = append(toolOutputs, lastToolText)
			msg = map[string]any{"content": "只读探测结束"}
		case 3:
			// 确认执行模式：发起 ssh_exec，等待用户点击“允许执行”
			msg = map[string]any{
				"tool_calls": []map[string]any{
					{"id": "c_confirm_exec", "type": "function", "function": map[string]string{"name": "ssh_exec", "arguments": `{"command":"echo CONFIRM_PASS","reason":"验证确认模式"}`}},
				},
			}
		case 4:
			toolOutputs = append(toolOutputs, lastToolText)
			msg = map[string]any{"content": "确认模式执行结束"}
		case 5:
			// 自动执行模式：普通命令自动放行，高危 rm -rf 仍应拦截等待拒绝
			msg = map[string]any{
				"tool_calls": []map[string]any{
					{"id": "c_auto_danger", "type": "function", "function": map[string]string{"name": "ssh_exec", "arguments": `{"command":"rm -rf /tmp/serval-danger-test","reason":"高危拦截测试"}`}},
				},
			}
		case 6:
			toolOutputs = append(toolOutputs, lastToolText)
			msg = map[string]any{"content": "高危命令已拦截"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": msg}},
		})
	}))
	defer ts.Close()

	h := newHarness(t)
	h.a.settings.AIBaseURL = ts.URL
	h.a.settings.AIModel = "gpt-4o"
	h.a.setAIKey("test-sk")
	h.connectDev()

	// 1. 只读观察模式
	h.a.openAI()
	h.a.ai.mode = aiModeObserver
	h.a.ai.query = "observer check"
	h.a.runAIGenerate()
	h.pump("只读观察完成", func() bool {
		return !h.a.ai.loading && len(h.a.ai.turns) == 1 && !h.a.ai.turns[0].running
	})
	if len(toolOutputs) == 0 || !strings.Contains(toolOutputs[0], "只读观察") {
		t.Fatalf("只读观察模式应阻止 ssh_exec，实际=%v", toolOutputs)
	}
	if len(h.a.ai.turns[0].steps) == 0 || len(h.a.ai.turns[0].steps[0].Tools) != 2 || !strings.Contains(h.a.ai.turns[0].steps[0].Tools[0].Output, "module serval") {
		t.Fatalf("只读观察模式下 sftp_read 应成功读取文件内容")
	}

	// 2. 确认执行模式 -> 出现审批卡片 -> 点击允许执行
	h.a.ai.mode = aiModeConfirm
	h.a.ai.query = "confirm check"
	h.a.runAIGenerate()
	h.pump("出现命令执行确认卡片", func() bool {
		return h.a.ai.pendingApproval != nil && h.tt.HasText("允许执行")
	})
	h.click("允许执行")
	h.pump("确认执行完成", func() bool {
		return !h.a.ai.loading && len(h.a.ai.turns) == 2 && !h.a.ai.turns[1].running
	})
	if !strings.Contains(toolOutputs[len(toolOutputs)-1], "CONFIRM_PASS") {
		t.Fatalf("确认执行放行后应收到真实 stdout，实际=%q", toolOutputs[len(toolOutputs)-1])
	}

	// 3. 自动执行模式下的高危命令仍拦截 -> 点击拒绝
	h.a.ai.mode = aiModeAuto
	h.a.ai.query = "danger check"
	h.a.runAIGenerate()
	h.pump("自动模式下高危命令触发拦截卡片", func() bool {
		return h.a.ai.pendingApproval != nil && h.tt.HasText("高危命令拦截确认")
	})
	h.click("拒绝")
	h.pump("高危命令拒绝后完成", func() bool {
		return !h.a.ai.loading && len(h.a.ai.turns) == 3 && !h.a.ai.turns[2].running
	})
	if !strings.Contains(toolOutputs[len(toolOutputs)-1], "拒绝") {
		t.Fatalf("拒绝后应告知模型用户已拒绝，实际=%q", toolOutputs[len(toolOutputs)-1])
	}
}

// TestAIAgentComposerSlashMentionsAndProfiles 测试设置页多模型配置保存、底部模型切换、@ 引用上下文与 / 快捷命令。
func TestAIAgentComposerSlashMentionsAndProfiles(t *testing.T) {
	var seenModel string
	var seenUserPrompt string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer sk-ds-1" && auth != "Bearer test-sk" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		seenModel, _ = req["model"].(string)
		msgs, _ := req["messages"].([]any)
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			if mm["role"] == "user" {
				seenUserPrompt, _ = mm["content"].(string)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	}))
	defer ts.Close()

	h := newHarness(t)
	h.click("设置")
	h.tt.Frame()
	h.click("AI")
	h.tt.Frame()

	// 点击 DeepSeek 预设后填入本地 mock 地址并保存为多模型配置
	h.click("DeepSeek")
	h.tt.Frame()
	h.a.settingsPanel.aiBaseURL = ts.URL
	h.a.settingsPanel.aiKey = "sk-ds-1"
	h.tt.Frame()
	h.click("存为新配置")
	h.tt.Frame()
	if len(h.a.settings.AIProfiles) != 1 || h.a.settings.AIProfiles[0].Name != "DeepSeek" {
		t.Fatalf("未能保存多模型配置: %+v", h.a.settings.AIProfiles)
	}

	h.click(locales.Done())
	h.tt.Frame()

	// 打开本地终端与 AI 侧栏
	h.click("新建")
	h.tt.Frame()
	h.click("本地终端")
	h.tt.Frame()
	h.a.openAI()
	h.tt.Frame()

	if !h.tt.HasText("DeepSeek · deepseek-chat") {
		t.Fatalf("底部模型切换栏应显示当前激活配置，界面=%q", h.tt.Texts())
	}

	// 测试 @ 引用上下文
	h.click("@")
	h.tt.Frame()
	h.click("@主机信息")
	h.tt.Frame()
	if len(h.a.ai.attachedContexts) != 1 || h.a.ai.attachedContexts[0] != "host" {
		t.Fatalf("点击 @主机信息 后应附加 host 上下文: %+v", h.a.ai.attachedContexts)
	}

	// 测试 / 快捷命令执行
	h.click("/")
	h.tt.Frame()
	h.click("/inspect")
	h.pump("斜杠命令执行完成", func() bool {
		return !h.a.ai.loading && len(h.a.ai.turns) == 1 && !h.a.ai.turns[0].running
	})
	if seenModel != "deepseek-chat" || !strings.Contains(seenUserPrompt, "[附加主机信息]") {
		t.Fatalf("期望使用 deepseek-chat 且附带主机信息，实际 model=%q prompt=%q", seenModel, seenUserPrompt)
	}

	// /clear 清空对话
	h.a.ai.query = "/clear"
	h.a.submitAIInput()
	h.tt.Frame()
	if len(h.a.ai.turns) != 0 {
		t.Fatalf("/clear 后应清空对话轮次: %d", len(h.a.ai.turns))
	}
}
