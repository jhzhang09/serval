package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"serval/core/hosts"
	"serval/core/keys"
	"serval/internal/devsshd"

	"golang.org/x/crypto/ssh"
)

// keyServer 生成一把私钥写到磁盘，并启动只认这把钥匙的开发服务器。
func keyServer(t *testing.T, passphrase string) (keyPath string, server *devsshd.Server, k keys.Key) {
	t.Helper()
	k, err := keys.Generate(keys.Ed25519, "", "", passphrase)
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	keyPath = filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, []byte(k.PrivateKey), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err = devsshd.Start(devsshd.Options{AuthorizedKey: pub})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	return keyPath, server, k
}

// addHostWithKey 用表单添加一台用私钥登录的主机。
func (h *harness) addHostWithKey(name string, server *devsshd.Server, keyName string) {
	h.t.Helper()
	h.a.openAdd()
	f := &h.a.form
	f.address, f.port, f.user, f.name, f.auth, f.keyName = "127.0.0.1", fmt.Sprint(server.Addr().Port), "tester", name, 2, keyName
	h.tt.Frame()
	h.click("添加")
	if h.a.form.open {
		h.t.Fatalf("添加主机失败: %q", h.a.form.err)
	}
}

// 选择私钥文件导入（带口令），再在主机里选它：连接时不问任何东西。
func TestImportKeyFileAndConnect(t *testing.T) {
	keyPath, server, generated := keyServer(t, "kp")
	h := newHarness(t)
	h.a.pickFile = func() (string, error) { return keyPath, nil }

	h.click("密钥")
	h.tt.Frame()
	if !h.tt.HasText("还没有私钥") {
		t.Fatalf("空密钥库应有说明: %q", h.tt.Texts())
	}
	h.click("添加私钥")
	h.click("选择文件…")
	h.pump("读入私钥文件", func() bool { return h.a.keyPanel.pem != "" })
	if h.a.keyPanel.name != "id_ed25519" {
		t.Errorf("名称应默认用文件名，实际 %q", h.a.keyPanel.name)
	}
	h.tt.Frame()
	if !h.tt.HasText("保存口令（不保存时，每次连接都询问）") {
		t.Fatalf("加了口令的私钥应出现口令输入框: %q", h.tt.Texts())
	}
	h.click("添加")
	if !strings.Contains(h.a.keyPanel.err, "口令") {
		t.Fatalf("没填口令应提示: %q", h.a.keyPanel.err)
	}
	h.a.keyPanel.passphrase = "wrong"
	h.tt.Frame()
	h.click("添加")
	if !strings.Contains(h.a.keyPanel.err, "口令不正确") {
		t.Fatalf("口令错误应提示: %q", h.a.keyPanel.err)
	}
	h.a.keyPanel.passphrase = "kp"
	h.tt.Frame()
	h.click("添加")
	if len(h.a.keys) != 1 || h.a.keys[0].Fingerprint != generated.Fingerprint || h.a.keys[0].Passphrase != "kp" {
		t.Fatalf("keys=%+v err=%q", h.a.keys, h.a.keyPanel.err)
	}
	if h.vaultFileContains("PRIVATE KEY") || h.vaultFileContains("kp\"") {
		t.Fatal("私钥和口令不应以明文出现在保险库文件里")
	}
	// 原文件删掉也不影响使用：私钥已经在保险库里。
	os.Remove(keyPath)
	h.click("完成")

	h.addHostWithKey("keyed", server, "id_ed25519")
	if got := h.host("keyed"); got.KeyID != h.a.keys[0].ID || got.Auth != hosts.AuthKey {
		t.Fatalf("host=%+v", got)
	}
	h.click("keyed")
	h.trustAndWait(0)
}

// 只有一把私钥时，添加主机的表单直接选上它。
func TestSingleKeyIsPreselected(t *testing.T) {
	h := newHarness(t)
	k, _ := keys.Generate(keys.Ed25519, "only", "", "")
	k.ID = "k1"
	h.a.apply(keyChange(k))
	h.a.openAdd()
	if h.a.form.keyName != "only" {
		t.Fatalf("keyName=%q", h.a.form.keyName)
	}
}

// 口令不保存：连接时询问，勾选记住后存到私钥上，下次不再问。
func TestUnsavedPassphraseIsAskedAndRemembered(t *testing.T) {
	_, server, generated := keyServer(t, "kp")
	h := newHarness(t)
	generated.ID, generated.Name, generated.Passphrase = "k1", "work", ""
	h.a.apply(keyChange(generated))
	h.addHostWithKey("keyed", server, "work")

	h.click("keyed")
	h.pump("口令询问", func() bool { return h.a.auth.open })
	if !h.a.auth.passphrase || !h.tt.HasText("记住口令（加密保存在保险库里）") {
		t.Fatalf("应询问私钥口令: %q", h.tt.Texts())
	}
	h.a.auth.secret = "kp"
	h.tt.Frame()
	h.click("连接")
	h.trustAndWait(0)
	if k, _ := h.a.keyByID("k1"); k.Passphrase != "kp" {
		t.Fatal("勾选记住后应把口令保存到私钥上")
	}
	if h.host("keyed").Password != "" {
		t.Error("私钥登录不应保存密码")
	}
}

// 生成新密钥后直接显示公钥，复制到剪贴板。
func TestGenerateKeyAndCopyPublicKey(t *testing.T) {
	h := newHarness(t)
	h.a.openKeys()
	h.tt.Frame()
	h.click("添加私钥")
	h.click("生成新密钥")
	h.a.keyPanel.name = "laptop"
	h.tt.Frame()
	h.click("生成")
	h.pump("生成完成", func() bool { return h.a.keyPanel.page == keyDetail })
	if len(h.a.keys) != 1 || h.a.keys[0].Name != "laptop" || h.a.keys[0].Type != ssh.KeyAlgoED25519 {
		t.Fatalf("keys=%+v", h.a.keys)
	}
	k := h.a.keys[0]
	if !h.tt.HasText(k.PublicKey) {
		t.Fatalf("生成后应显示公钥: %q", h.tt.Texts())
	}
	h.click("复制公钥")
	if h.tt.Clipboard() != k.PublicKey {
		t.Fatalf("剪贴板 = %q", h.tt.Clipboard())
	}
	if !strings.HasSuffix(k.PublicKey, " tester@"+strings.TrimSuffix(hostname(t), ".local")) {
		t.Errorf("公钥末尾应带“用户@主机名”: %q", k.PublicKey)
	}

	// 重命名
	h.a.keyPanel.rename = "laptop-2026"
	h.tt.Frame()
	h.click("保存")
	if h.a.keys[0].Name != "laptop-2026" || h.a.keyPanel.page != keyList {
		t.Fatalf("重命名失败: %+v", h.a.keys)
	}
}

func hostname(t *testing.T) string {
	name, err := os.Hostname()
	if err != nil {
		t.Skip(err)
	}
	return name
}

// 删除正在使用的私钥：先确认，删除后这些主机改回“自动”认证，不会指向不存在的私钥。
func TestDeleteKeyInUse(t *testing.T) {
	h := newHarness(t)
	k, _ := keys.Generate(keys.Ed25519, "old", "", "")
	k.ID = "k1"
	h.a.apply(keyChange(k), hostChange(hosts.Host{ID: "x", Name: "x", Address: "10.0.0.1", Port: 22, User: "u",
		Auth: hosts.AuthKey, KeyID: "k1", Group: defaultGroup}))
	h.a.openKeys()
	h.tt.Frame()
	if !h.tt.HasText("ED25519  1 台主机在用") {
		t.Errorf("列表应显示使用情况: %q", h.tt.Texts())
	}
	h.a.showKey("k1")
	h.tt.Frame()
	h.click("删除私钥")
	h.tt.Frame()
	if !h.tt.HasText("删除私钥 old？") {
		t.Fatalf("删除前应确认: %q", h.tt.Texts())
	}
	h.click("删除")
	h.tt.Frame()
	if len(h.a.keys) != 0 {
		t.Fatal("私钥应已删除")
	}
	if got := h.host("x"); got.KeyID != "" || got.Auth != hosts.AuthAuto {
		t.Fatalf("主机应改回自动认证: %+v", got)
	}
}

// 第一次添加私钥时设置主密码，私钥不会写进未加密的保险库。
func TestFirstKeyAsksForMasterPassword(t *testing.T) {
	h := newBareHarness(t)
	k, _ := keys.Generate(keys.Ed25519, "", "", "")
	h.a.openKeys()
	h.tt.Frame()
	h.click("添加私钥")
	h.a.keyPanel.pem = k.PrivateKey
	h.tt.Frame()
	h.click("添加")
	h.tt.Frame()
	if !h.a.setup.open || len(h.a.keys) != 0 {
		t.Fatal("添加私钥前应先设置主密码")
	}
	h.a.setup.password, h.a.setup.confirm = "some master", "some master"
	h.tt.Frame()
	h.click("设置并继续")
	if len(h.a.keys) != 1 || h.a.keys[0].Name != "ed25519" {
		t.Fatalf("设置主密码后应继续添加私钥: %+v", h.a.keys)
	}
	if h.vaultFileContains("PRIVATE KEY") {
		t.Fatal("私钥不应以明文写入")
	}
}

func TestPastingPublicKeyIsExplained(t *testing.T) {
	h := newHarness(t)
	k, _ := keys.Generate(keys.Ed25519, "", "", "")
	h.a.openKeys()
	h.tt.Frame()
	h.click("添加私钥")
	h.a.keyPanel.pem = k.PublicKey
	h.tt.Frame()
	h.click("添加")
	if !strings.Contains(h.a.keyPanel.err, ".pub") {
		t.Fatalf("err=%q", h.a.keyPanel.err)
	}
}
