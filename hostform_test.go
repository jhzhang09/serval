package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"serval/core/hosts"
	"serval/core/vault"
)

// devHost 返回保险库里的 dev 主机。
func (h *harness) devHost() hosts.Host { return h.host("dev") }

// trustAndWait 信任指纹（如果问了），等待会话建立，期间不应再询问密码。
func (h *harness) trustAndWait(before int) {
	h.t.Helper()
	h.pump("指纹或会话", func() bool { return len(h.a.prompts) > 0 || len(h.a.tabs) == before+1 || h.a.auth.open })
	if len(h.a.prompts) > 0 {
		h.click("信任并连接")
	}
	h.pump("会话建立", func() bool { return len(h.a.tabs) == before+1 || h.a.auth.open })
	if h.a.auth.open {
		h.t.Fatalf("不应再询问密码或口令: %q", h.a.auth.message)
	}
}

// vaultFileContains 报告保险库文件里是否出现了 s 的明文。
func (h *harness) vaultFileContains(s string) bool {
	raw, err := os.ReadFile(filepath.Join(h.dir, "vault.json"))
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.Contains(string(raw), s)
}

// 添加主机时选择密码登录并填写密码：密码加密存进保险库，连接时不再询问。
func TestPasswordHostUsesSavedPassword(t *testing.T) {
	h := newHarness(t)
	h.click("新建")
	h.tt.Frame()
	h.click("添加主机")
	f := &h.a.form
	f.address, f.user, f.name = "127.0.0.1", "tester", "pw-host"
	f.port = strings.TrimPrefix(h.devHost().Target(), "tester@127.0.0.1:")
	f.auth, f.password = 1, testPassword
	h.tt.Frame()
	h.click("添加")
	if h.a.form.open {
		t.Fatalf("保存失败: %q", h.a.form.err)
	}
	if saved := h.host("pw-host"); saved.Auth != hosts.AuthPassword || saved.Password != testPassword {
		t.Fatalf("saved=%+v", saved)
	}
	if h.vaultFileContains(testPassword) || h.vaultFileContains("pw-host") {
		t.Fatal("保险库文件里不应出现密码或主机名的明文")
	}

	h.click("pw-host")
	h.trustAndWait(0)

	// 编辑时提示已保存；清除后保险库里就没有了。
	h.a.openEdit(h.host("pw-host"))
	h.tt.Frame()
	if !h.a.form.passwordSaved || !h.tt.HasText("清除") {
		t.Fatalf("编辑表单应提示密码已保存: %q", h.tt.Texts())
	}
	h.click("清除")
	h.click("保存")
	if h.host("pw-host").Password != "" {
		t.Fatal("清除后保险库里仍有密码")
	}
}

// 第一次保存秘密时设置主密码：取消则表单还在；设置后数据加密，重启后本机记住的密钥自动解锁。
func TestFirstSecretAsksForMasterPassword(t *testing.T) {
	h := newBareHarness(t)
	if !h.vaultFileContains("tester") {
		t.Fatal("还没设置主密码时，主机信息以明文保存（不含任何秘密）")
	}
	h.click("新建")
	h.tt.Frame()
	h.click("添加主机")
	f := &h.a.form
	f.address, f.user, f.name, f.auth, f.password = "10.0.0.5", "ops", "db", 1, "db-secret"
	h.tt.Frame()
	h.click("添加")
	h.tt.Frame()
	if !h.a.setup.open || !h.tt.HasText("设置主密码") {
		t.Fatalf("保存密码前应先设置主密码: %q", h.tt.Texts())
	}
	if h.host("db").ID != "" {
		t.Fatal("设置主密码之前不应保存带密码的主机")
	}
	h.click("取消")
	h.tt.Frame()
	if !h.a.form.open || h.a.form.password != "db-secret" {
		t.Fatal("取消设置主密码后应回到表单，内容保留")
	}

	h.click("添加")
	h.tt.Frame()
	s := &h.a.setup
	s.password, s.confirm = "first master pw", "different"
	h.tt.Frame()
	h.click("设置并继续")
	if !strings.Contains(h.a.setup.err, "不一致") {
		t.Fatalf("err=%q", h.a.setup.err)
	}
	s.confirm = "first master pw"
	h.tt.Frame()
	h.click("设置并继续")
	h.tt.Frame()
	if !h.a.setup.busy || !h.tt.HasText("正在设置…") {
		t.Fatalf("派生期间按钮应显示正在设置并处于处理中: busy=%v texts=%q", h.a.setup.busy, h.tt.Texts())
	}
	// 重复调用不会触发第二次。
	h.a.submitSetup()
	h.pump("设置主密码完成", func() bool { return !h.a.setup.open })
	if h.a.setup.open || h.a.form.open || h.host("db").Password != "db-secret" {
		t.Fatalf("设置主密码后应继续保存主机: setup=%v form=%v", h.a.setup.open, h.a.form.open)
	}
	if h.a.vault.State() != vault.Unlocked || h.vaultFileContains("db-secret") || h.vaultFileContains("tester") {
		t.Fatal("设置主密码后全部记录都应加密")
	}

	// 默认勾选了“在这台设备上记住”：重启后自动解锁。
	h.reopen()
	if h.a.vault.State() != vault.Unlocked || h.host("db").Password != "db-secret" {
		t.Fatalf("记住后重启应自动解锁: state=%v", h.a.vault.State())
	}

	// 不记住：重启后是锁定页面，主机列表不可见。
	h.device.Delete(rememberedKeyName(h.a.vault.ID()))
	h.reopen()
	if h.a.vault.State() != vault.Locked || !h.tt.HasText("Serval 已锁定") || h.tt.HasText("ops@10.0.0.5") {
		t.Fatalf("没有记住时应显示锁定页面，且看不到主机: %q", h.tt.Texts())
	}
	h.a.unlock.password, h.a.unlock.remember = "wrong master", false
	h.tt.Frame()
	h.click("解锁")
	h.tt.Frame()
	if !h.a.unlock.busy || !h.tt.HasText("正在解锁…") || h.a.unlock.password != "" {
		t.Fatalf("解锁派生期间应显示正在解锁并清空密码输入: busy=%v texts=%q", h.a.unlock.busy, h.tt.Texts())
	}
	// 重复点击或调用不触发第二次。
	h.a.submitUnlock()
	h.pump("解锁错误结果", func() bool { return !h.a.unlock.busy })
	if !strings.Contains(h.a.unlock.err, "不正确") || h.a.unlock.password != "" {
		t.Fatalf("err=%q", h.a.unlock.err)
	}
	h.a.unlock.password = "first master pw"
	h.tt.Frame()
	h.click("解锁")
	h.pump("解锁成功", func() bool { return h.a.vault.State() == vault.Unlocked })
	if h.a.vault.State() != vault.Unlocked || !h.tt.HasText("ops@10.0.0.5") {
		t.Fatalf("解锁后应显示主机: %q", h.tt.Texts())
	}
	if _, err := h.device.Get(rememberedKeyName(h.a.vault.ID())); err == nil {
		t.Error("解锁时没勾选记住，不应保存密钥")
	}
}

// 连接时勾选“记住密码”而保险库还没有主密码：先设置主密码，再继续连接并保存。
func TestRememberFromPromptSetsUpMasterPassword(t *testing.T) {
	h := newBareHarness(t)
	h.enterPassword(testPassword)
	h.tt.Frame()
	if !h.a.setup.open {
		t.Fatal("勾选记住密码时应先设置主密码")
	}
	h.a.setup.password, h.a.setup.confirm = "a master pw", "a master pw"
	h.tt.Frame()
	h.click("设置并继续")
	h.pump("主机密钥询问", func() bool { return len(h.a.prompts) > 0 })
	h.click("信任并连接")
	h.pump("会话建立", func() bool { return len(h.a.tabs) == 1 })
	if h.devHost().Password != testPassword {
		t.Fatal("连接成功后应保存密码")
	}
}

// 连接时输入密码并勾选记住：连接成功后才保存，下次直接连。
func TestRememberPasswordFromPrompt(t *testing.T) {
	h := newHarness(t)
	h.enterPassword("wrong")
	h.pump("主机密钥询问", func() bool { return len(h.a.prompts) > 0 })
	h.click("信任并连接")
	h.pump("再次询问", func() bool { return h.a.auth.open && h.a.auth.message != "" })
	if h.devHost().Password != "" {
		t.Fatal("输错的密码不应保存")
	}
	if !h.a.auth.remember {
		t.Error("记住密码应默认勾选")
	}
	h.a.auth.secret = testPassword
	h.tt.Frame()
	h.click("连接")
	h.pump("会话建立", func() bool { return len(h.a.tabs) == 1 })
	if h.devHost().Password != testPassword {
		t.Fatal("连接成功后应保存密码")
	}

	h.a.closeAll()
	h.tt.Frame()
	h.click("dev")
	h.trustAndWait(0)
}

func TestUncheckedRememberDoesNotSave(t *testing.T) {
	h := newHarness(t)
	h.click("dev")
	h.pump("密码询问", func() bool { return h.a.auth.open })
	h.a.auth.secret, h.a.auth.remember = testPassword, false
	h.tt.Frame()
	h.click("连接")
	h.pump("主机密钥询问", func() bool { return len(h.a.prompts) > 0 })
	h.click("信任并连接")
	h.pump("会话建立", func() bool { return len(h.a.tabs) == 1 })
	if h.devHost().Password != "" {
		t.Fatal("没勾选记住却保存了密码")
	}
}

// 保存的密码失效（服务器改了密码）时说清楚，重新输入后覆盖旧的。
func TestWrongSavedPasswordAsksAgain(t *testing.T) {
	h := newHarness(t)
	dev := h.devHost()
	dev.Password = "old"
	h.a.apply(hostChange(dev))
	h.click("dev")
	h.pump("主机密钥询问", func() bool { return len(h.a.prompts) > 0 })
	h.click("信任并连接")
	h.pump("再次询问", func() bool { return h.a.auth.open })
	if !strings.Contains(h.a.auth.message, "保存的密码不正确") {
		t.Fatalf("提示 = %q", h.a.auth.message)
	}
	h.a.auth.secret = testPassword
	h.tt.Frame()
	h.click("连接")
	h.pump("会话建立", func() bool { return len(h.a.tabs) == 1 })
	if h.devHost().Password != testPassword {
		t.Fatal("应覆盖旧密码")
	}
}

// 右键菜单：编辑、复制（连同密码）、删除（先确认，连同密码）。
func TestHostMenuEditDuplicateDelete(t *testing.T) {
	h := newHarness(t)
	h.a.openAdd()
	f := &h.a.form
	f.address, f.user, f.name, f.group, f.auth, f.password = "10.9.9.9", "ops", "billing", "支付", 1, "s3cret"
	h.tt.Frame()
	h.click("添加")
	h.tt.Frame()
	if !h.tt.HasText("支付") {
		t.Errorf("侧栏应出现新分组: %q", h.tt.Texts())
	}
	original := h.host("billing")

	if err := h.tt.RightClick("billing"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("编辑"); err != nil {
		t.Fatalf("%v，菜单: %q", err, h.tt.Menu())
	}
	h.tt.Frame()
	if h.a.form.editing != original.ID || h.a.form.address != "10.9.9.9" || h.a.form.auth != 1 {
		t.Fatalf("编辑表单没有带出原值: %+v", h.a.form)
	}
	h.a.form.name = "billing-api"
	h.tt.Frame()
	h.click("保存")
	if got := h.host("billing-api"); got.ID != original.ID || got.Password != "s3cret" {
		t.Fatalf("编辑应保留 ID 和密码: %+v", got)
	}

	h.tt.Frame()
	h.tt.RightClick("billing-api")
	if err := h.tt.ChooseMenuItem("复制"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	if h.a.form.name != "billing-api 副本" || h.a.form.editing != "" {
		t.Fatalf("复制表单 = %+v", h.a.form)
	}
	h.click("添加")
	duplicate := h.host("billing-api 副本")
	if duplicate.ID == "" || duplicate.ID == original.ID || duplicate.Password != "s3cret" {
		t.Fatalf("复制应新增主机并沿用密码: %+v", duplicate)
	}

	h.tt.Frame()
	h.tt.RightClick("billing-api")
	if err := h.tt.ChooseMenuItem("删除"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	if !h.tt.HasText("删除 billing-api？") {
		t.Fatalf("删除前应确认: %q", h.tt.Texts())
	}
	h.click("删除")
	h.tt.Frame()
	if h.host("billing-api").ID != "" || h.host("billing-api 副本").ID == "" {
		t.Fatalf("hosts=%+v", h.a.hosts)
	}
	for _, x := range h.onDisk() {
		if x.ID == original.ID {
			t.Fatal("删除应写进保险库文件")
		}
	}
}

func TestFormValidation(t *testing.T) {
	h := newHarness(t)
	h.a.openAdd()
	h.a.form.address, h.a.form.user, h.a.form.name = "1.1.1.1", "u", "dev"
	h.tt.Frame()
	h.click("添加")
	if !strings.Contains(h.a.form.err, "已经有一台叫 dev") {
		t.Fatalf("err=%q", h.a.form.err)
	}
	h.a.form.name, h.a.form.auth = "k", 2
	h.tt.Frame()
	if !h.tt.HasText("还没有私钥。在侧栏的“密钥”里添加私钥文件或生成新密钥，就能在这里选择。") {
		t.Errorf("没有私钥时应指引去添加: %q", h.tt.Texts())
	}
	h.click("添加")
	if !strings.Contains(h.a.form.err, "选一把私钥") {
		t.Fatalf("私钥登录没选私钥应报错: %q", h.a.form.err)
	}
}

// 改名时同步更新其他主机 ProxyJump 中的名称引用，并在表单里提示影响的主机数。
func TestRenameHostUpdatesJumpReferences(t *testing.T) {
	h := newHarness(t,
		hosts.Host{ID: "b", Name: "bastion", Address: "10.0.0.1", Port: 22, User: "ops"},
		hosts.Host{ID: "w1", Name: "web-1", Address: "10.0.0.2", Port: 22, User: "ops", ProxyJump: "bastion"},
		hosts.Host{ID: "w2", Name: "web-2", Address: "10.0.0.3", Port: 22, User: "ops", ProxyJump: "bastion,gw:2222"},
	)
	h.a.openEdit(h.host("bastion"))
	h.tt.Frame()
	if h.tt.HasText("将同时更新") {
		t.Fatal("还没改名时不应显示更新跳板机提示")
	}

	h.a.form.name = "bastion-prod"
	h.tt.Frame()
	if !h.tt.HasText("将同时更新 2 台主机的跳板机设置。") {
		t.Fatalf("表单应提示将更新 2 台主机: %q", h.tt.Texts())
	}
	h.click("保存")
	h.tt.Frame()

	if got := h.host("web-1").ProxyJump; got != "bastion-prod" {
		t.Fatalf("web-1 ProxyJump = %q，期望 bastion-prod", got)
	}
	if got := h.host("web-2").ProxyJump; got != "bastion-prod,gw:2222" {
		t.Fatalf("web-2 ProxyJump = %q，期望 bastion-prod,gw:2222", got)
	}
	route, err := hosts.ResolveRoute(h.host("web-1"), h.a.hosts, h.a.localUser)
	if err != nil || len(route) != 1 || route[0].ID != "b" || route[0].Name != "bastion-prod" {
		t.Fatalf("改名后应能正常解析跳板机链路: route=%+v err=%v", route, err)
	}
}

// 转发 ssh-agent 默认关闭；勾选后能看到风险提示，保存后写进主机记录，再次编辑仍然勾选。
func TestForwardAgentOptionWarnsAndPersists(t *testing.T) {
	h := newHarness(t)
	h.a.openEdit(h.devHost())
	h.tt.Frame()
	if h.a.form.forwardAgent {
		t.Fatal("转发 ssh-agent 应默认关闭")
	}
	if h.tt.HasText("只在信任这台机器时保持开启。") {
		t.Fatal("未开启时不应展示风险提示")
	}
	if box, ok := h.tt.Find("协议"); ok {
		h.tt.Scroll(box.X+50, box.Y+50, 0, 400)
		h.tt.Frame()
	}
	h.click("转发 ssh-agent")
	h.tt.Frame()
	if !h.a.form.forwardAgent || !h.tt.HasText("只在信任这台机器时保持开启。") {
		t.Fatalf("勾选后应打开选项并提示风险: on=%v texts=%q", h.a.form.forwardAgent, h.tt.Texts())
	}
	h.click("保存")
	h.tt.Frame()
	if h.a.form.open {
		t.Fatalf("保存失败: %s", h.a.form.err)
	}
	if !h.devHost().ForwardAgent {
		t.Fatal("转发选项应写入主机记录")
	}
	h.a.openEdit(h.devHost())
	h.tt.Frame()
	if !h.a.form.forwardAgent || !h.tt.HasText("只在信任这台机器时保持开启。") {
		t.Fatal("再次编辑应保持开启并继续提示风险")
	}
}

func TestHostFormDistroPersistsAndDisplays(t *testing.T) {
	h := newHarness(t)
	h.a.openEdit(h.devHost())
	h.tt.Frame()
	h.a.form.distro = hosts.DistroUbuntu
	h.click("保存")
	h.tt.Frame()
	if h.a.form.open {
		t.Fatalf("保存失败: %s", h.a.form.err)
	}
	saved := h.devHost()
	if saved.Distro != hosts.DistroUbuntu {
		t.Fatalf("Distro 未正确写入主机: %+v", saved)
	}
	h.a.openEdit(saved)
	h.tt.Frame()
	if h.a.form.distro != hosts.DistroUbuntu {
		t.Fatalf("再次编辑时 Distro 未保留: %q", h.a.form.distro)
	}
}

func TestHostFormProtocolTelnetAndSerial(t *testing.T) {
	h := newHarness(t)

	// 1. 添加 Telnet 主机
	h.click("新建")
	h.tt.Frame()
	h.click("添加主机")
	h.tt.Frame()
	f := &h.a.form
	f.protoIdx = 1
	f.name = "my-telnet"
	f.address = "127.0.0.1"
	f.port = "" // 留空应默认为 23
	h.tt.Frame()
	h.click("添加")
	h.tt.Frame()
	if h.a.form.open {
		t.Fatalf("Telnet 保存失败: %s", h.a.form.err)
	}
	telnetHost := h.host("my-telnet")
	if telnetHost.Protocol != hosts.ProtocolTelnet || telnetHost.Port != 23 {
		t.Fatalf("Telnet 属性异常: %+v", telnetHost)
	}

	// 编辑 Telnet 主机，确认表单回显与安全提示
	h.a.openEdit(telnetHost)
	h.tt.Frame()
	if h.a.form.protoIdx != 1 {
		t.Fatalf("编辑时 protoIdx 应为 1, 得到 %d", h.a.form.protoIdx)
	}
	if !h.tt.HasText("Telnet 不加密") {
		t.Fatal("选择 Telnet 时应显示明文安全提示")
	}
	h.click("取消")
	h.tt.Frame()

	// 2. 添加串口主机并设置高级参数 7E2
	h.click("新建")
	h.tt.Frame()
	h.click("添加主机")
	h.tt.Frame()
	f = &h.a.form
	f.protoIdx = 2
	f.name = "my-serial"
	f.address = "/dev/ttyUSB0"
	f.baudRateText = "57600"
	h.tt.Frame()
	h.click("▸ 高级串口参数")
	h.tt.Frame()
	f.dataBitsText = "7"
	f.parityText = parityOption("E")
	f.stopBitsText = "2"
	h.tt.Frame()
	h.click("添加")
	h.tt.Frame()
	if h.a.form.open {
		t.Fatalf("Serial 保存失败: %s", h.a.form.err)
	}
	serialHost := h.host("my-serial")
	if serialHost.Protocol != hosts.ProtocolSerial || serialHost.BaudRate != 57600 ||
		serialHost.DataBits != 7 || serialHost.Parity != "E" || serialHost.StopBits != 2 {
		t.Fatalf("Serial 属性异常: %+v", serialHost)
	}

	// 编辑 Serial 主机，确认表单回显
	h.a.openEdit(serialHost)
	h.tt.Frame()
	if h.a.form.protoIdx != 2 || h.a.form.baudRateText != "57600" ||
		h.a.form.dataBitsText != "7" || h.a.form.stopBitsText != "2" {
		t.Fatalf("编辑时 Serial 回显异常: %+v", h.a.form)
	}
}

// TestNonSSHTabCapabilitiesSafety 验证非 SSH（Telnet/串口）标签不会因拆分崩溃，且隐藏文件/监控/拆分按钮。
func TestNonSSHTabCapabilitiesSafety(t *testing.T) {
	h := newHarness(t)
	h.click("新建")
	h.tt.Frame()
	h.click("本地终端")
	h.tt.Frame()
	if len(h.a.tabs) != 1 {
		t.Fatalf("预期 1 个标签，实际 %d", len(h.a.tabs))
	}
	tb := h.a.tabs[0]
	// 模拟 Telnet/串口会话（非本地、无 SSH client）
	tb.local = false
	tb.client = nil
	tb.host = hosts.Host{ID: "telnet-1", Name: "switch-telnet", Address: "10.0.0.1", Port: 23, Protocol: hosts.ProtocolTelnet}
	h.tt.Frame()

	for _, txt := range h.tt.Texts() {
		if strings.Contains(txt, "向右拆分") || strings.Contains(txt, "向下拆分") ||
			strings.Contains(txt, "文件（") || strings.Contains(txt, "服务器监控") {
			t.Fatalf("非 SSH 标签不应显示拆分、文件或监控按钮，界面出现: %q", txt)
		}
	}

	// 直接调用 split、toggleFiles、toggleMonitor 不得崩溃或打开无效抽屉
	h.a.split(tb, false)
	if tb.root.leaves() != 1 {
		t.Fatalf("非 SSH 标签不应拆分窗格: leaves=%d", tb.root.leaves())
	}
	h.a.toggleFiles()
	if h.a.filesOpen {
		t.Fatal("非 SSH 标签不应打开文件抽屉")
	}
	h.a.toggleMonitor(tb)
	if tb.monitorOpen {
		t.Fatal("非 SSH 标签不应开启服务器监控")
	}
}
