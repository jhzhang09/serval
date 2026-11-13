package main

import (
	"context"
	"encoding/json"
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
		return !h.a.ai.loading && len(h.a.ai.turns) == 1
	})

	// 发起第三个请求后模拟 Esc/点外部关闭弹窗（open=false 后下一帧渲染）
	h.a.ai.query = "third"
	h.a.runAIGenerate()
	select {
	case <-req3Entered:
	case <-time.After(5 * time.Second):
		t.Fatal("第三个 AI 请求未发出")
	}
	h.a.ai.open = false
	h.tt.Frame()

	select {
	case <-req3Canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("关闭 AI 浮层后进行中的请求未被立即取消")
	}
	if h.a.ai.loading || h.a.ai.cancel != nil {
		t.Fatalf("关闭 AI 浮层后应停止加载状态: loading=%v cancelNil=%v", h.a.ai.loading, h.a.ai.cancel == nil)
	}
}
