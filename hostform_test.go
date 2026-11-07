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
	if !strings.Contains(h.a.unlock.err, "不正确") || h.a.unlock.password != "" {
		t.Fatalf("err=%q", h.a.unlock.err)
	}
	h.a.unlock.password = "first master pw"
	h.tt.Frame()
	h.click("解锁")
	h.tt.Frame()
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
