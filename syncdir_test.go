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
	h.pump("选择同步文件夹", func() bool { return h.a.local.SyncDir == dir })
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

// 回归：另一台设备先把更新的数据放进同步文件夹，本机后来才启用同步时，
// 必须先把文件夹里的数据合并进来，而不是用本机旧状态直接覆盖它。
func TestSyncDirEnableMergesExistingFolderData(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	h.a.syncDirEvery = 20 * time.Millisecond

	// 先启用一次，把本机保险库放进文件夹，再停掉，模拟另一台设备接着往里写。
	if err := h.a.setSyncDir(dir); err != nil {
		t.Fatal(err)
	}
	h.a.stopSyncDir()
	edited := editFolderVault(t, filepath.Join(dir, "vault.json"), h.a.vault.Key(), "folder-first", `{"name":"folder-first","address":"10.7.7.7","port":22,"user":"u","group":"我的主机"}`)
	if err := writePrivateFile(filepath.Join(dir, "vault.json"), edited); err != nil {
		t.Fatal(err)
	}

	if err := h.a.setSyncDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.a.stopSyncDir)
	h.pump("合并文件夹里别的设备写的数据", func() bool { return h.tt.HasText("folder-first") })
	h.pump("把合并结果导回文件夹", func() bool { return remoteHasHost(t, dir, h.a.vault.Key(), "folder-first") })
}

// 回归：用户确认冲突副本期间，同步软件又更新了同一个文件。
// 这时不能按当初读到的旧内容合并并删除，否则最新一版会丢。
func TestSyncDirConflictCopyChangedDuringConfirm(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	// 轮询放慢，手动喂快照，保证确认时手里拿的是哪一版是确定的。
	h.a.syncDirEvery = time.Hour
	if err := h.a.setSyncDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.a.stopSyncDir)

	conflictPath := filepath.Join(dir, "vault.sync-conflict-20261109-130000-ABCDEF.json")
	old := editFolderVault(t, filepath.Join(dir, "vault.json"), h.a.vault.Key(), "conflict-old", `{"name":"conflict-old","address":"10.8.8.8","port":22,"user":"u","group":"我的主机"}`)
	if err := writePrivateFile(conflictPath, old); err != nil {
		t.Fatal(err)
	}
	h.a.consumeSyncDir(readSyncDir(dir))
	h.pump("冲突副本确认", func() bool { return h.tt.HasText("发现冲突副本") })

	// 确认期间同步软件写了一版新的。
	newer := editFolderVault(t, filepath.Join(dir, "vault.json"), h.a.vault.Key(), "conflict-new", `{"name":"conflict-new","address":"10.9.9.9","port":22,"user":"u","group":"我的主机"}`)
	if err := writePrivateFile(conflictPath, newer); err != nil {
		t.Fatal(err)
	}
	h.click("合并并删除")
	h.tt.Frame()
	if _, err := os.Stat(conflictPath); err != nil {
		t.Fatalf("内容已变化，不应删除冲突副本: %v", err)
	}
	if h.tt.HasText("conflict-old") {
		t.Fatal("旧内容不应被合并")
	}

	// 下一轮应带着最新内容重新询问，确认后合并的才是新内容。
	h.a.consumeSyncDir(readSyncDir(dir))
	h.pump("重新询问", func() bool { return h.tt.HasText("发现冲突副本") })
	h.click("合并并删除")
	h.pump("合并最新内容", func() bool { return h.tt.HasText("conflict-new") })
	if _, err := os.Stat(conflictPath); !os.IsNotExist(err) {
		t.Fatalf("确认后应删除冲突副本: %v", err)
	}
}

// 回归：“不再问”按内容记忆。同样大小但内容不同的冲突副本，更新后要重新问。
func TestSyncDirIgnoredConflictUsesContentNotSize(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	h.a.syncDirEvery = time.Hour
	if err := h.a.setSyncDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.a.stopSyncDir)

	conflictPath := filepath.Join(dir, "vault.sync-conflict-20261109-140000-ABCDEF.json")
	body := editFolderVault(t, filepath.Join(dir, "vault.json"), h.a.vault.Key(), "conflict-ask", `{"name":"conflict-ask","address":"10.10.0.1","port":22,"user":"u","group":"我的主机"}`)
	if err := writePrivateFile(conflictPath, body); err != nil {
		t.Fatal(err)
	}
	h.a.consumeSyncDir(readSyncDir(dir))
	h.pump("冲突副本确认", func() bool { return h.tt.HasText("发现冲突副本") })
	h.click("取消")
	h.tt.Frame()
	h.a.consumeSyncDir(readSyncDir(dir))
	h.tt.Frame()
	if h.tt.HasText("发现冲突副本") {
		t.Fatal("内容没变，不应再问")
	}

	// 内容变了一个字节，大小不变。
	mutated := append([]byte(nil), body...)
	mutated[len(mutated)-1] ^= 1
	if err := writePrivateFile(conflictPath, mutated); err != nil {
		t.Fatal(err)
	}
	h.a.consumeSyncDir(readSyncDir(dir))
	h.pump("内容变了应再问", func() bool { return h.tt.HasText("发现冲突副本") })
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
