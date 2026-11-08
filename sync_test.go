package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"serval/core/hosts"
	"serval/core/vault"

	"golang.org/x/net/webdav"
)

func TestSyncNowWithoutConfigExplains(t *testing.T) {
	h := newHarness(t)
	h.click("搜索主机和操作")
	h.a.pal.query = "立即同步"
	h.tt.Frame()
	h.click("立即同步")
	h.tt.Frame()
	if !h.tt.HasText("还没有开启同步") {
		t.Fatalf("没配置时应说明还没开启: %q", h.tt.Texts())
	}
}

func TestSyncUploadsAfterEditAndPasswordStaysOutOfFile(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	srv := httptest.NewServer(&webdav.Handler{
		FileSystem: webdav.Dir(dir),
		LockSystem: webdav.NewMemLS(),
	})
	t.Cleanup(func() {
		h.a.stopSyncLoop()
		srv.Close()
	})
	const pass = "dav-password-not-in-file"
	if err := h.a.configureSync(srv.URL+"/vault.json", "alice", pass, true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(h.a.devicePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), pass) {
		t.Fatal("同步密码不应出现在 device.json")
	}
	if !strings.Contains(string(raw), `"syncURL"`) || !strings.Contains(string(raw), `"syncUser": "alice"`) {
		t.Fatalf("地址和账号应在本机设置里: %s", raw)
	}
	if h.vaultFileContains(pass) || h.vaultFileContains("alice") {
		t.Fatal("同步账号和密码不应写进保险库文件")
	}

	h.a.syncDelay = 20 * time.Millisecond
	if err := h.a.apply(hostChange(hosts.Host{
		ID: "synced", Name: "synced-box", Address: "10.1.1.1", Port: 22, User: "u", Group: defaultGroup,
	})); err != nil {
		t.Fatal(err)
	}
	h.pump("上传新主机", func() bool { return remoteHasHost(t, dir, h.a.vault.Key(), "synced") })
}

func TestSyncSettingsSaveProbeAndStatus(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	const pass = "dav-password-not-in-file"
	dav := &webdav.Handler{FileSystem: webdav.Dir(dir), LockSystem: webdav.NewMemLS()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "alice" || p != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="serval"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		dav.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		h.a.stopSyncLoop()
		srv.Close()
	})

	h.click("设置")
	h.tt.Frame()
	h.click("同步")
	h.tt.Frame()
	if !h.tt.HasText("服务地址") || !h.tt.HasText("同步密码") {
		t.Fatalf("同步页没有打开: %q", h.tt.Texts())
	}
	h.a.settingsPanel.syncURL = srv.URL
	h.a.settingsPanel.syncUser = "alice"
	h.a.settingsPanel.syncPassword = "wrong-password"
	h.tt.Frame()
	h.click("启用同步")
	h.tt.Frame()
	if !h.a.settingsPanel.syncOn {
		t.Fatal("勾选后应启用同步")
	}
	h.click("测试连接")
	h.pump("错误密码的测试结果", func() bool { return h.tt.HasText("同步账号或密码不正确。") })

	h.a.settingsPanel.syncPassword = pass
	h.tt.Frame()
	h.click("测试连接")
	h.pump("正确密码的测试结果", func() bool { return h.tt.HasText("可以连接。") })

	h.click("保存同步设置")
	h.tt.Frame()
	if h.a.settingsPanel.syncPassword != "" {
		t.Fatal("保存后应清掉表单里的密码")
	}
	if !h.a.local.SyncOn || !strings.HasSuffix(h.a.local.SyncURL, "/vault.json") || h.a.local.SyncUser != "alice" {
		t.Fatalf("本机设置 = %+v", h.a.local)
	}
	raw, err := os.ReadFile(h.a.devicePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), pass) || !strings.Contains(string(raw), `"syncUser": "alice"`) {
		t.Fatalf("device.json 不应含密码，且应记下账号: %s", raw)
	}
	got, err := h.device.Get(syncSecretName)
	if err != nil || string(got) != pass {
		t.Fatalf("安全存储里的密码 = %q, err=%v", got, err)
	}
	if h.vaultFileContains(pass) {
		t.Fatal("同步密码不应写进保险库")
	}
	h.pump("侧栏同步状态", func() bool { return h.tt.HasText("同步正常") })
	if h.a.lastSync.IsZero() {
		t.Fatal("同步成功后应记下时间")
	}

	h.click("完成")
	h.tt.Frame()
	h.click("同步状态")
	h.tt.Frame()
	if !h.a.settingsPanel.open || h.a.settingsPanel.tab != settingsTabSync {
		t.Fatalf("点状态图标应打开同步页: open=%v tab=%d", h.a.settingsPanel.open, h.a.settingsPanel.tab)
	}
	if h.a.settingsPanel.syncPassword != "" {
		t.Fatal("再次打开时不应把已存密码填回输入框")
	}
	h.click("保存同步设置")
	h.tt.Frame()
	got, err = h.device.Get(syncSecretName)
	if err != nil || string(got) != pass {
		t.Fatalf("空密码应保留已存密码，得到 %q err=%v", got, err)
	}
	h.pump("再次同步结束", func() bool { return !h.a.syncing && h.tt.HasText("同步正常") })

	h.click("启用同步")
	h.tt.Frame()
	h.click("保存同步设置")
	h.tt.Frame()
	if h.a.local.SyncOn || h.a.syncLoopStop != nil {
		t.Fatal("取消启用后应停用同步")
	}
	if h.tt.HasText("同步正常") || h.tt.HasText("同步失败") {
		t.Fatalf("停用后侧栏不应再显示同步状态: %q", h.tt.Texts())
	}
}

func remoteHasHost(t *testing.T, dir string, key []byte, id string) bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "vault.json"))
	if err != nil || len(raw) == 0 {
		return false
	}
	path := filepath.Join(dir, "readback.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(path, "reader")
	if err != nil {
		return false
	}
	if err := v.UnlockWithKey(key); err != nil {
		return false
	}
	_, ok, err := v.Get(id)
	return err == nil && ok
}
