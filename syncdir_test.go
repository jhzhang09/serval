package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"serval/core/hosts"
	"serval/core/vault"
)

func TestConflictCopyName(t *testing.T) {
	yes := []string{
		"vault.sync-conflict-20261109-120000-ABCDEF.json",
		"vault (conflicted copy 2026-11-09).json",
		"Vault (Conflicted Copy from Mac).json",
	}
	no := []string{"vault.json", ".vault.json", "notes.json", "vault.sync-conflict.txt", ""}
	for _, name := range yes {
		if !conflictCopyName(name) {
			t.Fatalf("%s 应识别为冲突副本", name)
		}
	}
	for _, name := range no {
		if conflictCopyName(name) {
			t.Fatalf("%s 不是冲突副本", name)
		}
	}
}

func TestSyncDirExportsAndMergesFolder(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	h.a.syncDirEvery = 20 * time.Millisecond
	h.a.chooseFolder = func() (string, error) { return dir, nil }
	t.Cleanup(h.a.stopSyncDir)

	h.click("设置")
	h.tt.Frame()
	h.click("同步")
	h.tt.Frame()
	h.click("选择同步文件夹")
	h.tt.Frame()
	if h.a.local.SyncDir != dir {
		t.Fatalf("同步文件夹 = %q", h.a.local.SyncDir)
	}
	raw, err := os.ReadFile(h.a.devicePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"syncDir"`) {
		t.Fatalf("路径应记在本机设置: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "vault.json")); err != nil {
		t.Fatal(err)
	}

	if err := h.a.apply(hostChange(hosts.Host{
		ID: "from-local", Name: "from-local", Address: "10.4.4.4", Port: 22, User: "u", Group: defaultGroup,
	})); err != nil {
		t.Fatal(err)
	}
	if !remoteHasHost(t, dir, h.a.vault.Key(), "from-local") {
		t.Fatal("本地修改应写进同步文件夹")
	}

	edited := editFolderVault(t, filepath.Join(dir, "vault.json"), h.a.vault.Key(), "from-folder", `{"name":"from-folder","address":"10.5.5.5","port":22,"user":"u","group":"我的主机"}`)
	if err := writePrivateFile(filepath.Join(dir, "vault.json"), edited); err != nil {
		t.Fatal(err)
	}
	h.pump("合并文件夹里的修改", func() bool { return h.tt.HasText("from-folder") })
	if !remoteHasHost(t, dir, h.a.vault.Key(), "from-local") || !remoteHasHost(t, dir, h.a.vault.Key(), "from-folder") {
		t.Fatal("合并后文件夹里应同时有两边的主机")
	}
}

func TestSyncDirConflictCopyNeedsConfirm(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	h.a.syncDirEvery = 20 * time.Millisecond
	h.a.chooseFolder = func() (string, error) { return dir, nil }
	t.Cleanup(h.a.stopSyncDir)
	if err := h.a.setSyncDir(dir); err != nil {
		t.Fatal(err)
	}

	conflictPath := filepath.Join(dir, "vault.sync-conflict-20261109-120000-ABCDEF.json")
	body := editFolderVault(t, filepath.Join(dir, "vault.json"), h.a.vault.Key(), "conflict-box", `{"name":"conflict-box","address":"10.6.6.6","port":22,"user":"u","group":"我的主机"}`)
	if err := writePrivateFile(conflictPath, body); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.pump("冲突副本确认", func() bool { return h.tt.HasText("发现冲突副本") })
	h.click("取消")
	h.tt.Frame()
	if _, err := os.Stat(conflictPath); err != nil {
		t.Fatal("取消后不应删除冲突副本")
	}
	for range 8 {
		h.mu.Lock()
		queue := h.queue
		h.queue = nil
		h.mu.Unlock()
		for _, fn := range queue {
			fn()
		}
		h.tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if h.tt.HasText("发现冲突副本") {
		t.Fatal("取消后不应立刻再次询问")
	}

	// 文件变了才再问。
	if err := writePrivateFile(conflictPath, append(body, '\n')); err != nil {
		t.Fatal(err)
	}
	h.pump("再次询问", func() bool { return h.tt.HasText("发现冲突副本") })
	h.click("合并并删除")
	h.pump("合并冲突副本", func() bool { return h.tt.HasText("conflict-box") })
	if _, err := os.Stat(conflictPath); !os.IsNotExist(err) {
		t.Fatalf("确认后应删除冲突副本: %v", err)
	}
}

func editFolderVault(t *testing.T, src string, key []byte, id, data string) []byte {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "edit.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(path, "other-device")
	if err != nil {
		t.Fatal(err)
	}
	if err := v.UnlockWithKey(key); err != nil {
		t.Fatal(err)
	}
	if err := v.Put("host", id, []byte(data)); err != nil {
		t.Fatal(err)
	}
	out, err := v.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return out
}
