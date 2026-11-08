package main

import (
	"strings"
	"testing"
	"time"

	"serval/core/hosts"
	"serval/core/totp"
	"serval/internal/devsshd"
)

func TestLooksLikeOTP(t *testing.T) {
	for _, q := range []string{"OTP 验证码: ", "Verification code: ", "Authenticator code"} {
		if !looksLikeOTP(q) {
			t.Errorf("%q 应识别为验证码", q)
		}
	}
	for _, q := range []string{"Password: ", "请输入密码", "你的宠物叫什么？"} {
		if looksLikeOTP(q) {
			t.Errorf("%q 不应识别为验证码", q)
		}
	}
}

// 保存 otpauth 链接时解析出密钥，界面不再回显，保险库文件里也没有明文。
func TestSaveOTPAuthLinkHidesSecret(t *testing.T) {
	h := newHarness(t)
	h.a.openEdit(h.devHost())
	h.a.form.totp = "otpauth://totp/dev?secret=JBSWY3DPEHPK3PXP&digits=8&period=45"
	h.tt.Frame()
	h.click("保存")
	h.tt.Frame()
	if h.a.form.open {
		t.Fatalf("保存失败: %s", h.a.form.err)
	}
	got := h.devHost()
	if got.TOTPSecret != "JBSWY3DPEHPK3PXP" || got.TOTPDigits != 8 || got.TOTPPeriod != 45 {
		t.Fatalf("主机记录 %+v", got)
	}
	if h.vaultFileContains("JBSWY3DPEHPK3PXP") || h.vaultFileContains("otpauth://") {
		t.Fatal("验证码密钥不应以明文出现在保险库文件里")
	}
	h.a.openEdit(got)
	h.tt.Frame()
	if !h.a.form.totpSaved || h.a.form.totp != "" {
		t.Fatalf("再次编辑不应回显密钥: saved=%v totp=%q", h.a.form.totpSaved, h.a.form.totp)
	}
}

// 未加密保险库里保存验证码密钥前，先要求设置主密码，并且先不写入。
func TestSaveTOTPRequiresProtection(t *testing.T) {
	h := newBareHarness(t)
	h.a.openEdit(h.devHost())
	h.a.form.totp = "JBSWY3DPEHPK3PXP"
	h.tt.Frame()
	h.click("保存")
	h.tt.Frame()
	if !h.a.setup.open || h.a.form.totp == "" {
		t.Fatalf("应先设置主密码并保留表单: setup=%v totp空=%v", h.a.setup.open, h.a.form.totp == "")
	}
	if h.vaultFileContains("JBSWY3DPEHPK3PXP") {
		t.Fatal("设置主密码之前不应写入验证码密钥")
	}
}

// 主机保存了验证码密钥时，多步验证里的验证码问题自动填写，不再弹面板。
func TestTOTPAutoFillsChallenge(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"
	code, err := totp.Code(totp.Params{Secret: secret, Period: 3600}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server, err := devsshd.Start(devsshd.Options{
		KeyboardInteractive: true,
		KeyboardQuestions:   []string{"Verification code: "},
		KeyboardAnswers:     []string{code},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })

	h := newHarness(t, hosts.Host{
		ID: "otp-host", Name: "otp-auto", Address: "127.0.0.1", Port: server.Addr().Port,
		User: "tester", Group: defaultGroup, TOTPSecret: secret, TOTPPeriod: 3600,
	})
	h.click("otp-auto")
	for step := 0; len(h.a.tabs) == 0 && h.a.notice == ""; step++ {
		if step > 6 {
			t.Fatalf("连接没有完成: notice=%q texts=%q", h.a.notice, h.tt.Texts())
		}
		h.pump("下一步", func() bool {
			return h.a.auth.open || len(h.a.prompts) > 0 || len(h.a.tabs) > 0 || h.a.notice != "" || h.a.currentChallenge() != nil
		})
		if h.a.currentChallenge() != nil {
			t.Fatal("保存了验证码密钥时不应再询问验证码")
		}
		switch {
		case len(h.a.prompts) > 0:
			h.click("信任并连接")
		case h.a.auth.open:
			h.a.auth.secret = "irrelevant"
			h.tt.Frame()
			h.click("连接")
		}
		h.tt.Frame()
	}
	if len(h.a.tabs) != 1 {
		t.Fatalf("应自动填写验证码并连上: notice=%q", h.a.notice)
	}
	if strings.Contains(h.a.notice, secret) {
		t.Fatal("提示里不应出现验证码密钥")
	}
}
