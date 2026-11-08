package main

import (
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
