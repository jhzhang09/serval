package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"serval/core/vault"

	"github.com/egoist/mygo/ui"
)

const defaultSyncDirEvery = 5 * time.Second

// syncConflict 是同步软件留下的冲突副本。body 在后台读好，合并发生在主线程。
type syncConflict struct {
	name string
	path string
	body []byte
	size int64
}

type syncDirSnap struct {
	vault   []byte
	missing bool
	err     error
	files   []syncConflict
}

// conflictCopyName 识别 Dropbox / Nextcloud 的 “conflicted copy” 和 Syncthing 的 .sync-conflict-。
// 不把普通的 vault.json、点文件或无关 json 当成冲突副本。
func conflictCopyName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") {
		return false
	}
	lower := strings.ToLower(name)
	if !strings.HasSuffix(lower, ".json") {
		return false
	}
	return strings.Contains(lower, ".sync-conflict-") || strings.Contains(lower, "conflicted copy")
}

func (a *app) startSyncDir() {
	if strings.TrimSpace(a.local.SyncDir) == "" || !a.vaultOpen() || a.post == nil || a.syncDirStop != nil {
		return
	}
	// 启用前另一台设备可能已经把更新的保险库放进文件夹了。
	// 必须先读进来合并，再导出本机状态；顺序反了会用本机旧状态覆盖掉远端的新数据。
	// consumeSyncDir 对读不到、读失败和“是另一个保险库”的情况都不会写文件。
	a.consumeSyncDir(readSyncDir(a.local.SyncDir))
	every := a.syncDirEvery
	if every <= 0 {
		every = defaultSyncDirEvery
	}
	dir := a.local.SyncDir
	ctx, cancel := context.WithCancel(context.Background())
	a.syncDirStop = cancel
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				snap := readSyncDir(dir)
				if a.post == nil {
					continue
				}
				a.post(func() {
					if a.local.SyncDir != dir || !a.vaultOpen() {
						return
					}
					a.consumeSyncDir(snap)
				})
			}
		}
	}()
}

func (a *app) stopSyncDir() {
	if a.syncDirStop != nil {
		a.syncDirStop()
		a.syncDirStop = nil
	}
	a.askConflicts = false
	a.pendingConflicts = nil
}

// setSyncDir 记住用户选的同步文件夹。空字符串表示停用。路径只留在本机。
func (a *app) setSyncDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir != "" {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return errors.New("请选择一个存在的文件夹")
		}
	}
	next := a.local
	next.SyncDir = dir
	if err := a.saveLocal(next); err != nil {
		return err
	}
	a.ignoredConflicts = nil
	a.stopSyncDir()
	if dir != "" {
		a.startSyncDir()
	}
	return nil
}

func (a *app) chooseSyncDir() {
	if a.chooseFolder == nil {
		a.settingsPanel.err = "这台设备不能选择文件夹。"
		return
	}
	dir, err := a.chooseFolder()
	if err != nil {
		a.settingsPanel.err = "没能打开文件夹选择框，请再试一次。"
		return
	}
	if dir == "" {
		return
	}
	if err := a.setSyncDir(dir); err != nil {
		a.settingsPanel.err = err.Error()
		return
	}
	a.settingsPanel.err = ""
	a.toast(ui.Toast{Title: "已使用同步文件夹"})
}

// exportSyncDir 把当前保险库写进同步文件夹。文件夹里若是另一个保险库，不覆盖。
func (a *app) exportSyncDir() {
	dir := strings.TrimSpace(a.local.SyncDir)
	if dir == "" || !a.vaultOpen() {
		return
	}
	body, err := a.vault.Bytes()
	if err != nil {
		return
	}
	path := filepath.Join(dir, "vault.json")
	existing, err := os.ReadFile(path)
	if err == nil {
		if bytes.Equal(existing, body) {
			return
		}
		if id := fileVaultID(existing); id != "" && id != a.vault.ID() {
			a.syncErr = "同步文件夹里是另一个保险库，没有覆盖它。"
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		a.syncErr = "没能读取同步文件夹里的保险库。"
		return
	}
	if err := writePrivateFile(path, body); err != nil {
		a.syncErr = "没能把保险库写进同步文件夹。"
	}
}

func (a *app) consumeSyncDir(snap syncDirSnap) {
	if snap.err != nil {
		a.syncErr = "没能读取同步文件夹。"
		return
	}
	if snap.missing {
		a.exportSyncDir()
	} else if len(snap.vault) > 0 {
		local, err := a.vault.Bytes()
		if err == nil && !bytes.Equal(local, snap.vault) {
			changed, err := a.vault.Merge(snap.vault)
			if err != nil {
				if errors.Is(err, vault.ErrDifferentVault) {
					a.syncErr = "同步文件夹里是另一个保险库，没有合并。"
				} else {
					a.syncErr = "没能合并同步文件夹里的保险库，本机的修改还在。"
				}
			} else if changed {
				a.syncErr = ""
				a.lastSync = time.Now()
				a.reload()
			}
			// 读盘到合并之间，同步软件可能又写了一版。这时不能把旧快照的结果写回去。
			if err == nil && a.syncErr == "" {
				now, readErr := os.ReadFile(filepath.Join(a.local.SyncDir, "vault.json"))
				if readErr == nil && bytes.Equal(now, snap.vault) {
					a.exportSyncDir()
				}
			}
		}
	}
	if a.askConflicts {
		return
	}
	var fresh []syncConflict
	for _, f := range snap.files {
		if a.ignoredConflicts[f.path] == f.size {
			continue
		}
		fresh = append(fresh, f)
	}
	if len(fresh) > 0 {
		a.pendingConflicts = fresh
		a.askConflicts = true
	}
}

// syncConflictDialog 在删除冲突副本前请用户确认。默认按钮是取消。
func (a *app) syncConflictDialog(c *ui.Context) {
	if !a.askConflicts {
		return
	}
	names := make([]string, 0, len(a.pendingConflicts))
	for i, f := range a.pendingConflicts {
		if i == 5 {
			names = append(names, "等")
			break
		}
		names = append(names, f.name)
	}
	message := "同步软件留下了冲突副本。确认后会把它们合并进保险库，再删除这些文件。\n" + strings.Join(names, "\n")
	choice := ui.AlertDialog(c, &a.askConflicts, "发现冲突副本", message, "合并并删除", "取消")
	if choice == -1 && a.askConflicts {
		return
	}
	pending := a.pendingConflicts
	a.pendingConflicts = nil
	if choice != 0 {
		if a.ignoredConflicts == nil {
			a.ignoredConflicts = map[string]int64{}
		}
		for _, f := range pending {
			a.ignoredConflicts[f.path] = f.size
		}
		return
	}
	a.mergeConflictCopies(pending)
}

func (a *app) mergeConflictCopies(files []syncConflict) {
	if !a.vaultOpen() {
		return
	}
	merged := false
	for _, f := range files {
		// 从读盘到用户确认之间，同步软件可能又写了一版。这时不能按当初读到的
		// 旧内容合并删除，否则最新一版会丢：跳过它，等下一轮重新读、重新询问。
		if now, err := os.ReadFile(f.path); err != nil || !bytes.Equal(now, f.body) {
			a.syncErr = "有一份冲突副本刚被同步软件更新，没有合并，稍后会再问一次。"
			continue
		}
		if _, err := a.vault.Merge(f.body); err != nil {
			a.syncErr = "有一份冲突副本没能合并，文件还留在文件夹里。"
			continue
		}
		if err := os.Remove(f.path); err != nil {
			a.syncErr = "冲突副本已经合并，但没能删掉文件。"
			continue
		}
		merged = true
	}
	if merged {
		a.syncErr = ""
		a.lastSync = time.Now()
		a.reload()
		a.exportSyncDir()
	}
}

func readSyncDir(dir string) syncDirSnap {
	var snap syncDirSnap
	body, err := os.ReadFile(filepath.Join(dir, "vault.json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		snap.missing = true
	case err != nil:
		snap.err = err
		return snap
	case len(body) > 32<<20:
		snap.err = errors.New("文件过大")
		return snap
	default:
		snap.vault = body
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		snap.err = err
		return snap
	}
	for _, e := range entries {
		if e.IsDir() || !conflictCopyName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() > 32<<20 {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		snap.files = append(snap.files, syncConflict{name: e.Name(), path: path, body: b, size: info.Size()})
	}
	return snap
}

func fileVaultID(raw []byte) string {
	var head struct {
		VaultID string `json:"vaultId"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return ""
	}
	return head.VaultID
}

func writePrivateFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vault-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}
