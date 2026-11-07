package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"serval/core/vault"

	"github.com/egoist/mygo/ui"
)

// 从侧栏和命令面板打开设置、切换外观，重启后仍然生效。
func TestSettingsAppearancePersistsAcrossRestart(t *testing.T) {
	h := newBareHarness(t)
	h.tt.Frame()

	// 从侧栏打开设置面板，切换为深色。
	h.click("设置")
	h.tt.Frame()
	if !h.a.settingsPanel.open || !h.tt.HasText("外观") {
		t.Fatalf("设置面板没有打开: %q", h.tt.Texts())
	}
	h.click("深色")
	h.tt.Frame()
	if h.a.settings.Appearance != appearanceDark || !h.a.isDark(false) {
		t.Fatalf("appearance=%q isDark(false)=%v", h.a.settings.Appearance, h.a.isDark(false))
	}
	h.click("完成")
	h.tt.Frame()
	if h.a.settingsPanel.open {
		t.Fatal("点击完成后应关闭设置面板")
	}

	// 未加密保险库重启后，外观保持深色。
	h.reopen()
	if h.a.settings.Appearance != appearanceDark || !h.a.isDark(false) {
		t.Fatalf("重启后外观丢失: %q", h.a.settings.Appearance)
	}

	// 从命令面板打开设置，切换为浅色。
	h.click("搜索主机和操作")
	h.a.pal.query = "设置"
	h.tt.Frame()
	h.click("打开设置")
	h.tt.Frame()
	if !h.a.settingsPanel.open {
		t.Fatal("命令面板未能打开设置")
	}
	h.click("浅色")
	h.tt.Frame()
	if h.a.settings.Appearance != appearanceLight || h.a.isDark(true) {
		t.Fatalf("appearance=%q isDark(true)=%v", h.a.settings.Appearance, h.a.isDark(true))
	}

	// 设置主密码并记住本机密钥，重启后自动解锁并保持浅色。
	if err := h.a.vault.Protect(masterPassword); err != nil {
		t.Fatal(err)
	}
	h.a.rememberOnDevice(true)
	h.reopen()
	if h.a.settings.Appearance != appearanceLight || h.a.isDark(true) {
		t.Fatalf("加密保险库重启后外观丢失: %q", h.a.settings.Appearance)
	}
}

// 两份保险库合并时，设置记录按版本号取较新的一方；更新版本客户端加的未知字段在修改外观时保留。
func TestSettingsMergeTakesNewerAndPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	v1, err := vault.Open(filepath.Join(dir, "a.json"), "dev-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := v1.Protect(masterPassword); err != nil {
		t.Fatal(err)
	}
	if err := v1.Apply([]vault.Change{settingsChange(settings{Appearance: appearanceLight})}); err != nil {
		t.Fatal(err)
	}

	// 另一台设备从同一份保险库出发，随后把外观改成深色，并写入一个未来版本的字段。
	initial, err := v1.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	bPath := filepath.Join(dir, "b.json")
	if err := os.WriteFile(bPath, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	v2, err := vault.Open(bPath, "dev-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := v2.Unlock(masterPassword); err != nil {
		t.Fatal(err)
	}
	type futureSettings struct {
		Appearance   string `json:"appearance,omitempty"`
		TerminalFont string `json:"terminalFont,omitempty"`
	}
	ch, err := vault.JSONChange(kindSettings, settingsID, futureSettings{
		Appearance:   appearanceDark,
		TerminalFont: "Iosevka",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := v2.Apply([]vault.Change{ch}); err != nil {
		t.Fatal(err)
	}
	remote, err := v2.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	// 合并回第一台设备：应采用较新的深色设置。
	changed, err := v1.Merge(remote)
	if err != nil || !changed {
		t.Fatalf("Merge changed=%v err=%v", changed, err)
	}
	var got settings
	if err := decodeSettings(v1, &got); err != nil {
		t.Fatal(err)
	}
	if got.Appearance != appearanceDark {
		t.Fatalf("合并后应取较新的深色，实际 %q", got.Appearance)
	}

	// 第一台设备（旧版本）把外观改回跟随系统，未知字段 terminalFont 仍应保留。
	if err := v1.Apply([]vault.Change{settingsChange(settings{Appearance: appearanceSystem})}); err != nil {
		t.Fatal(err)
	}
	r, ok, err := v1.Get(settingsID)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	var merged futureSettings
	if err := json.Unmarshal(r.Data, &merged); err != nil {
		t.Fatal(err)
	}
	if merged.Appearance != appearanceSystem || merged.TerminalFont != "Iosevka" {
		t.Fatalf("未知字段未保留或外观未更新: %+v", merged)
	}
}

// 本机设置保存在 device.json，重启后保留，但绝不写进 vault.json。
func TestDeviceSettingsStayOutOfVault(t *testing.T) {
	h := newHarness(t)
	h.connectDev()

	if !slices.Equal(h.a.local.RecentHosts, []string{"dev"}) {
		t.Fatalf("连接后应记录最近使用的主机: %+v", h.a.local)
	}
	if h.vaultFileContains("recentHosts") {
		t.Fatal("本机设置不应出现在 vault.json 里")
	}
	if r, ok, _ := h.a.vault.Get(settingsID); ok && strings.Contains(string(r.Data), "recentHosts") {
		t.Fatal("本机设置不应写进保险库的 settings 记录")
	}
	raw, err := os.ReadFile(filepath.Join(h.dir, "device.json"))
	if err != nil || !strings.Contains(string(raw), `"recentHosts"`) {
		t.Fatalf("device.json 应保存本机设置: err=%v raw=%q", err, string(raw))
	}

	// 重启后本机设置仍在（即使保险库尚未解锁，本机设置也独立存在）。
	h.reopen()
	if !slices.Equal(h.a.local.RecentHosts, []string{"dev"}) {
		t.Fatalf("重启后本机设置丢失: %+v", h.a.local)
	}
	if err := h.a.vault.Unlock(masterPassword); err != nil {
		t.Fatal(err)
	}
	h.a.reload()

	// 删除主机时同步清理最近使用列表。
	h.a.deleteHost(h.devHost())
	if len(h.a.local.RecentHosts) != 0 {
		t.Fatalf("删除主机后应从最近使用中移除: %+v", h.a.local.RecentHosts)
	}
}

func TestSettingsNarrowWindowAndCorruptDeviceFile(t *testing.T) {
	h := newHarness(t)
	h.tt = ui.NewTester(h.a.view, 760, 420)
	h.tt.Frame()
	if box, ok := h.tt.Find("设置"); !ok || box.X < 0 || box.Y < 0 || box.X+box.W > 760 || box.Y+box.H > 420 {
		t.Fatalf("最小窗口下侧栏设置按钮溢出: ok=%v box=%+v", ok, box)
	}
	h.click("设置")
	h.tt.Frame()
	if box, ok := h.tt.Find("完成"); !ok || box.X+box.W > 760 || box.Y+box.H > 420 {
		t.Fatalf("最小窗口下设置面板内容溢出: ok=%v box=%+v", ok, box)
	}
	h.click("完成")

	// 损坏的 device.json 应给出面向用户的中文提示，而不是崩溃。
	if err := os.WriteFile(filepath.Join(h.dir, "device.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.a.hostsError = ""
	h.a.loadLocal()
	if !strings.Contains(h.a.hostsError, "本机设置文件已损坏") {
		t.Fatalf("损坏文件应提示: %q", h.a.hostsError)
	}
}

// 已知主机列表与保险库一致，显示地址、密钥类型、指纹与添加时间；删除后下次连接重新核对指纹。
func TestKnownHostsListAndDelete(t *testing.T) {
	h := newHarness(t)
	h.click("设置")
	h.tt.Frame()
	if !h.tt.HasText("还没有已信任的主机密钥。首次连接服务器并确认指纹后，记录会显示在这里。") {
		t.Fatalf("未连接时已知主机应为空: %q", h.tt.Texts())
	}
	h.click("完成")

	h.connectDev()
	h.a.closeAll()
	h.tt.Frame()

	known := h.a.knownHostsList()
	if len(known) != 1 {
		t.Fatalf("保险库应有 1 条已知主机记录，实际 %d", len(known))
	}
	addr := trustedAddress(h.devHost())
	sums := known[0].keySummaries()
	if known[0].Host != addr || len(sums) != 1 || !strings.HasPrefix(sums[0], "ssh-ed25519  SHA256:") || known[0].Added.IsZero() {
		t.Fatalf("knownHost=%+v sums=%v", known[0], sums)
	}

	h.click("设置")
	h.tt.Frame()
	for _, want := range []string{addr, sums[0], known[0].Added.Local().Format("2006-01-02 15:04")} {
		if !h.tt.HasText(want) {
			t.Errorf("已知主机列表缺少 %q: %q", want, h.tt.Texts())
		}
	}

	// 删除该条已知主机记录（即使主机本身还在或已删都能独立删除）。
	h.click("删除")
	h.tt.Frame()
	if len(h.a.knownHostsList()) != 0 || h.tt.HasText(sums[0]) || !h.tt.HasText("还没有已信任的主机密钥。首次连接服务器并确认指纹后，记录会显示在这里。") {
		t.Fatalf("删除后已知主机列表应为空: %+v texts=%q", h.a.knownHostsList(), h.tt.Texts())
	}
	h.click("完成")
	h.tt.Frame()

	// 再次连接 dev 时应重新核对指纹。
	h.click("dev")
	h.pump("重新核对指纹", func() bool { return len(h.a.prompts) > 0 })
	h.click("信任并连接")
	h.pump("会话建立", func() bool { return len(h.a.tabs) == 1 })
}

// 修改主密码流程：旧密码错误提示、新密码两次不一致提示、修改成功后旧密码不能解锁、新密码可以解锁。
func TestChangeMasterPasswordFlow(t *testing.T) {
	h := newHarness(t)
	h.tt.Frame()

	h.click("设置")
	h.tt.Frame()
	h.click("修改主密码…")
	h.tt.Frame()
	if !h.a.settingsPanel.changePwOpen {
		t.Fatal("修改主密码表单没有打开")
	}

	// 1. 旧主密码错误
	h.a.settingsPanel.currentPassword = "wrong-password"
	h.a.settingsPanel.newPassword = "brand-new-password"
	h.a.settingsPanel.confirmPassword = "brand-new-password"
	h.tt.Frame()
	h.click("保存新密码")
	h.tt.Frame()
	if !strings.Contains(h.a.settingsPanel.changePwErr, "当前主密码不正确") {
		t.Fatalf("应提示当前主密码不正确: %q", h.a.settingsPanel.changePwErr)
	}

	// 2. 两次新密码不一致
	h.a.settingsPanel.currentPassword = masterPassword
	h.a.settingsPanel.confirmPassword = "mismatched-password"
	h.tt.Frame()
	h.click("保存新密码")
	h.tt.Frame()
	if !strings.Contains(h.a.settingsPanel.changePwErr, "不一致") {
		t.Fatalf("应提示两次输入不一致: %q", h.a.settingsPanel.changePwErr)
	}

	// 3. 修改成功
	h.a.settingsPanel.confirmPassword = "brand-new-password"
	h.tt.Frame()
	h.click("保存新密码")
	h.tt.Frame()
	if h.a.settingsPanel.changePwOpen {
		t.Fatal("修改成功后应收起修改密码表单")
	}

	// 4. 手动锁定后验证解锁
	h.a.lockVault()
	h.tt.Frame()
	if h.a.vault.State() != vault.Locked {
		t.Fatal("锁定后状态应为 Locked")
	}
	if len(h.a.hosts) != 0 || len(h.a.keys) != 0 {
		t.Fatalf("锁定后内存中的数据应清空: hosts=%d keys=%d", len(h.a.hosts), len(h.a.keys))
	}

	// 尝试用旧密码解锁
	h.a.unlock.password = masterPassword
	h.tt.Frame()
	h.click("解锁")
	h.tt.Frame()
	if h.a.vault.State() != vault.Locked || !strings.Contains(h.a.unlock.err, "不正确") {
		t.Fatalf("旧密码解锁应失败: state=%v err=%q", h.a.vault.State(), h.a.unlock.err)
	}

	// 用新密码解锁
	h.a.unlock.password = "brand-new-password"
	h.tt.Frame()
	h.click("解锁")
	h.tt.Frame()
	if h.a.vault.State() != vault.Unlocked {
		t.Fatalf("新密码解锁失败: state=%v err=%q", h.a.vault.State(), h.a.unlock.err)
	}
	if len(h.a.hosts) == 0 {
		t.Fatal("解锁后应恢复主机列表")
	}
}

// 命令面板手动锁定：有活跃会话时二次确认并关闭会话，锁定后清除内存数据。
func TestManualLockWithOpenSessions(t *testing.T) {
	h := newHarness(t)
	h.connectDev()
	if len(h.a.tabs) != 1 {
		t.Fatalf("tabs=%d", len(h.a.tabs))
	}

	// 从命令面板触发锁定
	h.click("搜索主机和操作")
	h.a.pal.query = "锁定"
	h.tt.Frame()
	h.click("锁定保险库")
	h.tt.Frame()

	// 弹窗提示有 1 个会话将关闭
	if !h.a.askingLock || !h.tt.HasText("锁定将关闭当前打开的 1 个会话") {
		t.Fatalf("未提示会话将被关闭: askingLock=%v texts=%q", h.a.askingLock, h.tt.Texts())
	}

	// 取消锁定
	h.click("取消")
	h.tt.Frame()
	if h.a.askingLock || h.a.vault.State() != vault.Unlocked || len(h.a.tabs) != 1 {
		t.Fatalf("取消后应保持解锁和会话: state=%v tabs=%d", h.a.vault.State(), len(h.a.tabs))
	}

	// 再次触发并确认锁定
	h.click("搜索主机和操作")
	h.a.pal.query = "锁定"
	h.tt.Frame()
	h.click("锁定保险库")
	h.tt.Frame()
	h.click("锁定")
	h.tt.Frame()

	if h.a.vault.State() != vault.Locked {
		t.Fatalf("确认后应锁定: state=%v", h.a.vault.State())
	}
	if len(h.a.tabs) != 0 {
		t.Fatalf("锁定后所有会话应关闭: tabs=%d", len(h.a.tabs))
	}
	if len(h.a.hosts) != 0 || len(h.a.keys) != 0 {
		t.Fatalf("锁定后数据应清空: hosts=%d keys=%d", len(h.a.hosts), len(h.a.keys))
	}
	if !h.tt.HasText("Serval 已锁定") {
		t.Fatalf("锁定后应显示锁定界面: %q", h.tt.Texts())
	}
}

// 自动锁定：在设定的闲置时间后自动关闭全部会话并锁定保险库。
func TestAutoLockWithInjectedClock(t *testing.T) {
	h := newHarness(t)
	h.connectDev()

	now := time.Now()
	h.a.now = func() time.Time { return now }
	h.a.touchActive()

	// 设置 15 分钟自动锁定
	h.click("设置")
	h.tt.Frame()
	h.click("15分钟")
	h.tt.Frame()
	h.click("完成")
	h.tt.Frame()
	if h.a.local.AutoLockMinutes != 15 {
		t.Fatalf("AutoLockMinutes=%d", h.a.local.AutoLockMinutes)
	}

	// 经过 10 分钟：未达到阈值，保持解锁
	now = now.Add(10 * time.Minute)
	h.a.checkAutoLock()
	if h.a.vault.State() != vault.Unlocked || len(h.a.tabs) != 1 {
		t.Fatalf("10分钟时不应锁定: state=%v tabs=%d", h.a.vault.State(), len(h.a.tabs))
	}

	// 用户活动：重置闲置计时
	h.a.touchActive()

	// 再经过 10 分钟：距上次活动只有 10 分钟，不应锁定
	now = now.Add(10 * time.Minute)
	h.a.checkAutoLock()
	if h.a.vault.State() != vault.Unlocked {
		t.Fatal("活动后重新计时，不应锁定")
	}

	// 再经过 6 分钟（累计 16 分钟未活动）：触发自动锁定
	now = now.Add(6 * time.Minute)
	h.a.checkAutoLock()
	if h.a.vault.State() != vault.Locked {
		t.Fatalf("超过15分钟闲置应自动锁定: state=%v", h.a.vault.State())
	}
	if len(h.a.tabs) != 0 {
		t.Fatalf("自动锁定后会话应关闭: tabs=%d", len(h.a.tabs))
	}
}

// 设置中关闭“在这台设备上记住”后，安全存储中的密钥被删除，重启需输入主密码。
func TestRememberOnDeviceToggleInSettings(t *testing.T) {
	h := newHarness(t)
	h.a.rememberOnDevice(true)
	if !h.a.isRememberedOnDevice() {
		t.Fatal("初始应已记住主密码")
	}

	// 打开设置，取消勾选“在这台设备上记住主密码”
	h.click("设置")
	h.tt.Frame()
	h.click("在这台设备上记住主密码")
	h.tt.Frame()
	if h.a.isRememberedOnDevice() {
		t.Fatal("点击后应清除记住的密码")
	}
	h.click("完成")
	h.tt.Frame()

	// 重启后不能自动解锁，展示锁定界面
	h.reopen()
	if h.a.vault.State() != vault.Locked {
		t.Fatalf("清除后重启应为锁定状态: state=%v", h.a.vault.State())
	}
	if !h.tt.HasText("Serval 已锁定") {
		t.Fatalf("应显示锁定界面: %q", h.tt.Texts())
	}

	// 解锁（不勾选在这台设备上记住）
	h.a.unlock.password = masterPassword
	h.a.unlock.remember = false
	h.tt.Frame()
	h.click("解锁")
	h.tt.Frame()
	if h.a.vault.State() != vault.Unlocked {
		t.Fatalf("解锁失败: state=%v", h.a.vault.State())
	}
	if h.a.isRememberedOnDevice() {
		t.Fatal("未勾选记住时解锁，不应记在安全存储中")
	}

	// 再次在设置里打开“记住”
	h.click("设置")
	h.tt.Frame()
	h.click("在这台设备上记住主密码")
	h.tt.Frame()
	if !h.a.isRememberedOnDevice() {
		t.Fatal("重新勾选后应记在安全存储中")
	}
}
