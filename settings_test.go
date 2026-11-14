package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"serval/core/vault"

	"github.com/egoist/mygo/plugins/terminal"
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
	h.click("安全")
	h.tt.Frame()
	if !h.tt.HasText("还没有已信任的主机密钥。首次连接服务器并确认指纹后，记录会显示在这里。") {
		t.Fatalf("未连接时已知主机应为空: %q", h.tt.Texts())
	}
	h.click("完成")

	h.connectDev()
	h.a.closeAll()
	h.tt.Frame()

	known, _ := h.a.knownHostsList()
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
	h.click("安全")
	h.tt.Frame()
	for _, want := range []string{addr, sums[0], known[0].Added.Local().Format("2006-01-02 15:04")} {
		if !h.tt.HasText(want) {
			t.Errorf("已知主机列表缺少 %q: %q", want, h.tt.Texts())
		}
	}

	// 删除该条已知主机记录（即使主机本身还在或已删都能独立删除）。
	if box, ok := h.tt.Find("自动锁定"); ok {
		h.tt.Scroll(box.X+50, box.Y+50, 0, 400)
		h.tt.Frame()
	}
	h.click("删除")
	h.tt.Frame()
	if known, _ := h.a.knownHostsList(); len(known) != 0 || h.tt.HasText(sums[0]) || !h.tt.HasText("还没有已信任的主机密钥。首次连接服务器并确认指纹后，记录会显示在这里。") {
		t.Fatalf("删除后已知主机列表应为空: %+v texts=%q", known, h.tt.Texts())
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
	h.click("安全")
	h.tt.Frame()
	if box, ok := h.tt.Find("自动锁定"); ok {
		h.tt.Scroll(box.X+50, box.Y+50, 0, 300)
		h.tt.Frame()
	}
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
	h.pump("修改主密码结果", func() bool { return !h.a.settingsPanel.changePwBusy })
	if !strings.Contains(h.a.settingsPanel.changePwErr, "当前主密码不正确") {
		t.Fatalf("应提示当前主密码不正确: %q", h.a.settingsPanel.changePwErr)
	}

	// 2. 两次新密码不一致
	h.a.settingsPanel.currentPassword = masterPassword
	h.a.settingsPanel.confirmPassword = "mismatched-password"
	h.tt.Frame()
	h.click("保存新密码")
	h.pump("修改主密码结果", func() bool { return !h.a.settingsPanel.changePwBusy })
	if !strings.Contains(h.a.settingsPanel.changePwErr, "不一致") {
		t.Fatalf("应提示两次输入不一致: %q", h.a.settingsPanel.changePwErr)
	}

	// 3. 修改成功
	h.a.settingsPanel.confirmPassword = "brand-new-password"
	h.tt.Frame()
	h.click("保存新密码")
	h.pump("修改主密码结果", func() bool { return !h.a.settingsPanel.changePwBusy })
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
	h.pump("解锁结果", func() bool { return !h.a.unlock.busy })
	if h.a.vault.State() != vault.Locked || !strings.Contains(h.a.unlock.err, "不正确") {
		t.Fatalf("旧密码解锁应失败: state=%v err=%q", h.a.vault.State(), h.a.unlock.err)
	}

	// 用新密码解锁
	h.a.unlock.password = "brand-new-password"
	h.tt.Frame()
	h.click("解锁")
	h.pump("解锁结果", func() bool { return !h.a.unlock.busy })
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
func TestAutoLockFollowsSystemIdleTime(t *testing.T) {
	h := newHarness(t)
	h.connectDev()
	idle := time.Duration(0)
	h.a.idleTime = func() time.Duration { return idle }

	// 设置 15 分钟自动锁定
	h.click("设置")
	h.tt.Frame()
	h.click("安全")
	h.tt.Frame()
	h.click("15分钟")
	h.tt.Frame()
	h.click("完成")
	h.tt.Frame()
	if h.a.local.AutoLockMinutes != 15 {
		t.Fatalf("AutoLockMinutes=%d", h.a.local.AutoLockMinutes)
	}

	// 真实运行时，定时检查之间界面会不断重绘（终端输出、动画、定时器本身的 post）。
	// 重绘不能算作用户活动：这里每次检查前都渲染一帧。
	check := func(d time.Duration) {
		idle = d
		h.tt.Frame()
		h.a.checkAutoLock()
		h.tt.Frame()
	}
	check(10 * time.Minute)
	if h.a.vault.State() != vault.Unlocked || len(h.a.tabs) != 1 {
		t.Fatalf("闲置 10 分钟不应锁定: state=%v tabs=%d", h.a.vault.State(), len(h.a.tabs))
	}
	check(30 * time.Second) // 用户动了一下键盘或鼠标，系统闲置时间归零后重新累计
	if h.a.vault.State() != vault.Unlocked {
		t.Fatal("用户活动后不应锁定")
	}

	// 锁定前留一个填了一半的密码询问，锁定后内存里不应留下。
	h.a.auth = authSheet{open: true, secret: "half-typed"}
	check(16 * time.Minute)
	if h.a.vault.State() != vault.Locked || len(h.a.tabs) != 0 {
		t.Fatalf("闲置超过 15 分钟应锁定并关闭会话: state=%v tabs=%d", h.a.vault.State(), len(h.a.tabs))
	}
	if h.a.auth.secret != "" || h.a.auth.open || len(h.a.hosts) != 0 {
		t.Fatal("锁定后不应在内存里留下密码询问或主机信息")
	}
	if !h.tt.HasText("Serval 已锁定") {
		t.Fatalf("应显示锁定页面: %q", h.tt.Texts())
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
	h.click("安全")
	h.tt.Frame()
	if box, ok := h.tt.Find("自动锁定"); ok {
		h.tt.Scroll(box.X+50, box.Y+50, 0, 300)
		h.tt.Frame()
	}
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
	h.pump("解锁结果", func() bool { return !h.a.unlock.busy })
	if h.a.vault.State() != vault.Unlocked {
		t.Fatalf("解锁失败: state=%v", h.a.vault.State())
	}
	if h.a.isRememberedOnDevice() {
		t.Fatal("未勾选记住时解锁，不应记在安全存储中")
	}

	// 再次在设置里打开“记住”
	h.click("设置")
	h.tt.Frame()
	h.click("安全")
	h.tt.Frame()
	if box, ok := h.tt.Find("自动锁定"); ok {
		h.tt.Scroll(box.X+50, box.Y+50, 0, 300)
		h.tt.Frame()
	}
	h.click("在这台设备上记住主密码")
	h.tt.Frame()
	if !h.a.isRememberedOnDevice() {
		t.Fatal("重新勾选后应记在安全存储中")
	}
}

// 终端设置（字号、行高、光标样式、回滚行数、配色主题）修改后已打开终端立即生效，且重启后保留。
func TestTerminalSettingsPersistAndApplyLive(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	h.tt.Frame()

	// 初始默认值
	initFont := h.a.terminalFont()
	if initFont.Size != 13 || initFont.LineHeight != 1.15 {
		t.Fatalf("初始字体设置异常: %+v", initFont)
	}

	// 打开设置面板并切换到“终端”标签
	h.click("设置")
	h.tt.Frame()
	if !h.a.settingsPanel.open {
		t.Fatal("设置面板未打开")
	}
	h.click("终端")
	h.tt.Frame()

	// 修改字号为 15
	h.click("15")
	h.tt.Frame()
	if h.a.settings.TerminalFontSize != 15 {
		t.Fatalf("字号未更新: %v", h.a.settings.TerminalFontSize)
	}

	// 修改行高为 1.3
	h.click("1.3")
	h.tt.Frame()
	if h.a.settings.TerminalLineHeight != 1.3 {
		t.Fatalf("行高未更新: %v", h.a.settings.TerminalLineHeight)
	}

	// 修改光标样式为下划线
	h.click("下划线")
	h.tt.Frame()
	if h.a.settings.TerminalCursor != "underline" {
		t.Fatalf("光标样式未更新: %v", h.a.settings.TerminalCursor)
	}

	// 修改回滚缓冲区为 100 MB（终端插件按字节计）
	h.click("100 MB")
	h.tt.Frame()
	if h.a.settings.TerminalScrollbackMB != 100 || h.a.terminalScrollback() != 100<<20 {
		t.Fatalf("回滚缓冲区未更新: %v", h.a.settings.TerminalScrollbackMB)
	}

	// 修改深色与浅色配色主题前先向下滚，保证配色块都在视口内
	if box, ok := h.tt.Find("终端字号"); ok {
		h.tt.Scroll(box.X+50, box.Y+50, 0, 220)
		h.tt.Frame()
	}

	// 修改深色配色主题为 Dracula
	h.click("Dracula")
	h.tt.Frame()
	if h.a.settings.TerminalThemeDark != "Dracula" {
		t.Fatalf("深色主题未更新: %v", h.a.settings.TerminalThemeDark)
	}

	// 修改浅色配色主题为 GitHub
	h.click("GitHub")
	h.tt.Frame()
	if h.a.settings.TerminalThemeLight != "GitHub Light Default" {
		t.Fatalf("浅色主题未更新: %v", h.a.settings.TerminalThemeLight)
	}

	// 验证运行中终端立即应用了新字体和主题
	curFont := h.a.terminalFont()
	if curFont.Size != 15 || curFont.LineHeight != 1.3 {
		t.Fatalf("应用运行中字体未更新: %+v", curFont)
	}
	if tb.focus.term == nil {
		t.Fatal("焦点窗格终端为空")
	}

	h.click("完成")
	h.tt.Frame()

	// 记住本机密钥后重启应用，验证设置持久化保存在保险库中
	h.a.rememberOnDevice(true)
	h.reopen()
	if h.a.settings.TerminalFontSize != 15 ||
		h.a.settings.TerminalLineHeight != 1.3 ||
		h.a.settings.TerminalCursor != "underline" ||
		h.a.settings.TerminalScrollbackMB != 100 ||
		h.a.settings.TerminalThemeDark != "Dracula" ||
		h.a.settings.TerminalThemeLight != "GitHub Light Default" {
		t.Fatalf("重启后终端设置未持久化保留: %+v", h.a.settings)
	}
}

// 预设配色必须在 Ghostty 主题表里存在，否则选了没有任何效果（验收时发现 4 个名字写错）。
func TestTerminalThemePresetsExist(t *testing.T) {
	for _, list := range [][]string{popularDarkThemes, popularLightThemes} {
		for _, name := range list {
			if name == "" {
				continue
			}
			if _, err := terminal.GhosttyTheme(name); err != nil {
				t.Errorf("预设配色 %q 不存在: %v", name, err)
			}
			if themeNote(name) != "" {
				t.Errorf("存在的配色不应提示找不到: %q", name)
			}
		}
	}
	if themeNote("No Such Theme") == "" {
		t.Error("找不到的配色应提示正在使用默认配色")
	}
}

// 选中排在默认项前面的选项（11 号字、1.0 行高）时，高亮的仍是它自己。
func TestSettingIndexKeepsEarlierChoices(t *testing.T) {
	if got := indexOr(fontSizes, 11, 2); got != 0 {
		t.Errorf("11 号字的下标 = %d，期望 0", got)
	}
	if got := indexOr(lineHeights, 1.0, 1); got != 0 {
		t.Errorf("1.0 行高的下标 = %d，期望 0", got)
	}
	if got := indexOr(fontSizes, 99, 2); got != 2 {
		t.Errorf("找不到时应返回默认下标 2，得到 %d", got)
	}

	h := newHarness(t)
	h.click("设置")
	h.tt.Frame()
	h.click("终端")
	h.tt.Frame()
	h.click("11")
	h.tt.Frame()
	h.tt.Frame() // 再渲染几帧：错误的高亮下标会通过 OnChange 把设置改回去
	if h.a.settings.TerminalFontSize != 11 {
		t.Fatalf("选 11 号字后设置变成了 %v", h.a.settings.TerminalFontSize)
	}
}

// 本机设置写不进去时要告诉用户，不能悄悄丢掉最近使用记录。
func TestRecentHostSaveFailureIsReported(t *testing.T) {
	h := newHarness(t)
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.a.devicePath = filepath.Join(blocker, "device.json") // 父路径是文件，目录建不出来
	h.a.recordRecentHost("dev")
	if !strings.Contains(h.a.notice, "保存最近使用的主机失败") {
		t.Fatalf("应提示保存失败: %q", h.a.notice)
	}
	if slices.Contains(h.a.local.RecentHosts, "dev") {
		t.Fatal("没有保存成功，内存里的设置不应改变")
	}
}

// 换了应用标识符后，系统安全存储按新标识符找不到原来“记住”的钥匙：
// 显示锁定页面，用主密码解锁并重新记住后，下次启动又能自动解锁，保险库数据完整。
func TestRememberedKeyMissingAfterIdentifierChange(t *testing.T) {
	h := newHarness(t)
	h.a.rememberOnDevice(true)
	hostsBefore := len(h.a.hosts)

	h.device = &memDevice{m: map[string][]byte{}} // 新标识符对应一个空的安全存储
	h.a.device = h.device
	h.reopen()
	if h.a.vault.State() != vault.Locked || !h.tt.HasText("Serval 已锁定") {
		t.Fatalf("找不到记住的钥匙时应显示锁定页面: state=%v", h.a.vault.State())
	}

	h.a.unlock.password = masterPassword
	h.a.unlock.remember = true
	h.tt.Frame()
	h.click("解锁")
	h.pump("解锁结果", func() bool { return !h.a.unlock.busy })
	if h.a.vault.State() != vault.Unlocked || len(h.a.hosts) != hostsBefore {
		t.Fatalf("主密码应能解锁且数据完整: state=%v hosts=%d→%d", h.a.vault.State(), hostsBefore, len(h.a.hosts))
	}
	if !h.a.isRememberedOnDevice() {
		t.Fatal("勾选“记住”后应写入新的安全存储")
	}

	h.reopen()
	if h.a.vault.State() != vault.Unlocked {
		t.Fatalf("重新记住后，下次启动应自动解锁: state=%v", h.a.vault.State())
	}
}

func TestSettingsShortcutOpensSettings(t *testing.T) {
	h := newHarness(t)
	h.tt.Key(chromeShortcut(), ui.KeyComma)
	h.tt.Frame()
	if !h.a.settingsPanel.open {
		t.Fatal("按设置快捷键后应打开设置面板")
	}

	// 已打开时再按快捷键，不应清空正在编辑的草稿
	h.a.settingsPanel.aiModel = "custom-model"
	h.tt.Key(chromeShortcut(), ui.KeyComma)
	h.tt.Frame()
	if h.a.settingsPanel.aiModel != "custom-model" {
		t.Fatalf("重复按设置快捷键不应重置草稿: %q", h.a.settingsPanel.aiModel)
	}
}

func TestTerminalCopyOnSelectAndRightClickPersist(t *testing.T) {
	h := newHarness(t)
	h.connectDev()
	h.tt.Frame()

	if h.a.terminalCopyOnSelect() != terminal.CopyOnSelectNone || h.a.terminalRightClick() != terminal.RightClickContextMenu {
		t.Fatalf("默认终端交互设置异常: copy=%v rightClick=%v", h.a.terminalCopyOnSelect(), h.a.terminalRightClick())
	}

	h.click("设置")
	h.tt.Frame()
	h.click("终端")
	h.tt.Frame()

	// 向下滚一点保证选中复制与右键行为在视口内
	if box, ok := h.tt.Find("字体大小"); ok {
		h.tt.Scroll(box.X+50, box.Y+50, 0, 200)
		h.tt.Frame()
	}
	h.click("选中文本时自动复制到剪贴板")
	h.tt.Frame()
	h.click("直接粘贴")
	h.tt.Frame()

	if !h.a.settings.TerminalCopyOnSelect || h.a.terminalCopyOnSelect() != terminal.CopyOnSelectClipboard {
		t.Fatalf("选中自动复制未生效: %+v", h.a.settings)
	}
	if h.a.settings.TerminalRightClick != "paste" || h.a.terminalRightClick() != terminal.RightClickPaste {
		t.Fatalf("右键行为未生效: %+v", h.a.settings)
	}

	h.click("完成")
	h.tt.Frame()
	h.a.rememberOnDevice(true)
	h.reopen()
	if !h.a.settings.TerminalCopyOnSelect || h.a.settings.TerminalRightClick != "paste" {
		t.Fatalf("重启后终端交互设置丢失: %+v", h.a.settings)
	}
}

type unsupportedDevice struct{}

func (unsupportedDevice) Get(string) ([]byte, error) { return nil, errors.ErrUnsupported }
func (unsupportedDevice) Set(string, []byte) error   { return errors.ErrUnsupported }
func (unsupportedDevice) Delete(string) error        { return errors.ErrUnsupported }

func TestUnsupportedDeviceSecretsGracefulOnLinux(t *testing.T) {
	h := newBareHarness(t)
	h.a.device = unsupportedDevice{}
	h.a.probeDeviceSecrets()
	h.tt.Frame()

	if h.a.deviceAvailable() {
		t.Fatal("系统安全存储返回 ErrUnsupported 时应标记为不可用")
	}

	// 打开主密码设置弹窗时不应显示“在这台设备上记住主密钥”
	h.a.setup.open = true
	h.a.setup.fresh = true
	h.tt.Frame()
	if h.tt.HasText("在这台设备上记住主密钥") {
		t.Fatalf("系统安全存储不可用时 setupDialog 不应提供记住选项: %q", h.tt.Texts())
	}
	h.a.setup.open = false
	h.tt.Frame()

	// 打开设置面板并切换到安全页时不应显示“在这台设备上记住主密钥”
	h.click("设置")
	h.tt.Frame()
	h.click("安全")
	h.tt.Frame()
	if h.tt.HasText("在这台设备上记住主密钥") {
		t.Fatalf("系统安全存储不可用时设置安全页不应提供记住选项: %q", h.tt.Texts())
	}
	h.click("完成")
	h.tt.Frame()

	// 即使强行调用 rememberOnDevice(true) 也不应崩溃或写入
	h.a.rememberOnDevice(true)
	if h.a.isRememberedOnDevice() {
		t.Fatal("ErrUnsupported 下不应误认为已记住")
	}
}

func TestSecurityTabPaletteAndNarrowLayout(t *testing.T) {
	h := newHarness(t)
	h.tt = ui.NewTester(h.a.view, 760, 420)
	h.tt.Frame()

	// 命令面板选“已知主机”应直接跳转到设置面板的“安全”分页
	h.click("搜索主机和操作")
	h.a.pal.query = "已知主机"
	h.tt.Frame()
	h.click("已知主机")
	h.tt.Frame()
	if !h.a.settingsPanel.open || h.a.settingsPanel.tab != settingsTabSecurity {
		t.Fatalf("命令面板已知主机应打开安全设置分页: open=%v tab=%v", h.a.settingsPanel.open, h.a.settingsPanel.tab)
	}
	for _, tabLabel := range []string{"通用", "安全", "终端", "同步", "AI"} {
		box, ok := h.tt.Find(tabLabel)
		if !ok || box.X < 0 || box.Y < 0 || box.X+box.W > 760 || box.Y+box.H > 420 {
			t.Fatalf("窄窗口下设置分页标签 %q 布局异常: ok=%v box=%+v", tabLabel, ok, box)
		}
	}
	if !h.tt.HasText("自动锁定") {
		t.Fatalf("安全分页应显示自动锁定设置: %q", h.tt.Texts())
	}
}

func TestPasswordRevealToggleAndReset(t *testing.T) {
	h := newHarness(t)
	h.tt.Frame()

	// 1. 设置 -> 安全 -> 修改主密码表单的密码显隐开关，取消后应重置
	h.click("设置")
	h.tt.Frame()
	h.click("安全")
	h.tt.Frame()
	h.click("修改主密码…")
	h.tt.Frame()
	h.click("显示当前主密码")
	h.tt.Frame()
	if !h.a.settingsPanel.showCurrentPw {
		t.Fatal("点击显示当前主密码后应展开明文开关")
	}
	h.click("隐藏当前主密码")
	h.tt.Frame()
	if h.a.settingsPanel.showCurrentPw {
		t.Fatal("点击隐藏当前主密码后应恢复密文")
	}
	h.click("显示新主密码")
	h.tt.Frame()
	if !h.a.settingsPanel.showNewPw {
		t.Fatal("点击显示新主密码后应展开明文开关")
	}
	h.click("取消")
	h.tt.Frame()
	if h.a.settingsPanel.showNewPw {
		t.Fatal("取消修改主密码后应重置明文开关")
	}

	// 2. AI 设置页 API Key 显隐开关，关闭设置后清空
	h.click("AI")
	h.tt.Frame()
	if box, ok := h.tt.Find("API 端点 (Base URL)"); ok {
		h.tt.Scroll(box.X+50, box.Y+50, 0, 260)
		h.tt.Frame()
	}
	h.click("显示API Key")
	h.tt.Frame()
	if !h.a.settingsPanel.showAIKey {
		t.Fatal("点击显示API Key后应展开明文开关")
	}
	h.click("完成")
	h.tt.Frame()
	if h.a.settingsPanel.showAIKey {
		t.Fatal("关闭设置后应重置 AI Key 明文开关")
	}

	// 3. 添加主机表单里的密码显隐开关，取消后重置
	h.click("新建")
	h.tt.Frame()
	h.click("添加主机")
	h.tt.Frame()
	if box, ok := h.tt.Find("名称"); ok {
		h.tt.Scroll(box.X+50, box.Y+50, 0, 220)
		h.tt.Frame()
	}
	h.click("密码")
	h.tt.Frame()
	if box, ok := h.tt.Find("认证方式"); ok {
		h.tt.Scroll(box.X+50, box.Y+50, 0, 120)
		h.tt.Frame()
	}
	h.click("显示密码")
	h.tt.Frame()
	if !h.a.form.showPassword {
		t.Fatal("点击显示密码后应展开主机表单密码明文开关")
	}
	h.click("取消")
	h.tt.Frame()
	if h.a.form.showPassword {
		t.Fatal("关闭主机表单后应重置密码明文开关")
	}

	// 4. 认证弹窗显隐开关，锁定保险库后重置；解锁页显隐开关正常切换
	h.a.auth = authSheet{open: true, fresh: true, host: h.devHost(), secret: "s3cret"}
	h.tt.Frame()
	h.click("显示密码")
	h.tt.Frame()
	if !h.a.auth.showSecret {
		t.Fatal("点击显示密码后应展开认证弹窗明文开关")
	}
	h.a.lockVault()
	h.tt.Frame()
	if h.a.auth.showSecret || h.a.auth.secret != "" {
		t.Fatal("锁定后应清除认证弹窗密码与明文开关")
	}

	h.click("显示主密码")
	h.tt.Frame()
	if !h.a.unlock.showPassword {
		t.Fatal("点击显示主密码后应展开解锁页明文开关")
	}
	h.click("隐藏主密码")
	h.tt.Frame()
	if h.a.unlock.showPassword {
		t.Fatal("点击隐藏主密码后应恢复解锁页密文状态")
	}
}
