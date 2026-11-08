package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
