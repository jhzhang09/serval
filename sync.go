package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"serval/core/syncer"
	"serval/core/vault"

	"github.com/egoist/mygo/ui"
)

// syncSecretName 是系统安全存储里 WebDAV 密码的名字。地址和账号在 device.json，密码不进文件。
const syncSecretName = "sync-password"

// 自动同步的默认节奏。测试可以改 app.syncDelay 和 app.syncEvery。
const (
	defaultSyncDelay = 3 * time.Second
	defaultSyncEvery = 5 * time.Minute
)

func (a *app) syncConfigured() bool {
	return a.local.SyncOn && strings.TrimSpace(a.local.SyncURL) != ""
}

func (a *app) vaultOpen() bool {
	return a.vault != nil && a.vault.State() != vault.Locked
}

// configureSync 保存本机的同步设置。password 为空时保留安全存储里已有的密码。
// 地址会规范成 vault.json 的 URL。开启并且保险库未锁定、窗口已就绪时立刻同步一次。
func (a *app) configureSync(rawURL, user, password string, enabled bool) error {
	var fileURL string
	if strings.TrimSpace(rawURL) != "" {
		var err error
		fileURL, err = syncer.FileURL(rawURL)
		if err != nil {
			return err
		}
	} else if enabled {
		return errors.New("请填写同步地址")
	}
	if password != "" {
		if a.device == nil {
			return errors.New("这台设备没有安全存储，无法保存同步密码")
		}
		if err := a.device.Set(syncSecretName, []byte(password)); err != nil {
			return errors.New("没能把同步密码放进系统安全存储")
		}
	}
	next := a.local
	next.SyncURL = fileURL
	next.SyncUser = strings.TrimSpace(user)
	next.SyncOn = enabled
	if err := a.saveLocal(next); err != nil {
		return err
	}
	if enabled && a.vaultOpen() && a.post != nil {
		a.onVaultReady()
	} else if !enabled {
		a.stopSyncLoop()
	}
	return nil
}

func (a *app) syncPassword() string {
	if a.device == nil {
		return ""
	}
	b, err := a.device.Get(syncSecretName)
	if err != nil {
		return ""
	}
	return string(b)
}

// onVaultReady 在保险库解锁并且本机设置已经加载后调用：开了同步就先对齐一次，之后每几分钟再对齐。
func (a *app) onVaultReady() {
	if !a.syncConfigured() || !a.vaultOpen() || a.post == nil {
		return
	}
	a.startSyncLoop()
	a.syncNow(false)
}

// noteVaultChanged 在本地写入成功后调用。短时间里的连续修改合成一次同步。
func (a *app) noteVaultChanged() {
	if !a.syncConfigured() || !a.vaultOpen() {
		return
	}
	delay := a.syncDelay
	if delay <= 0 {
		delay = defaultSyncDelay
	}
	if a.syncTimer != nil {
		a.syncTimer.Stop()
	}
	a.syncTimer = time.AfterFunc(delay, func() {
		if a.post == nil {
			return
		}
		a.post(func() { a.syncNow(false) })
	})
}

func (a *app) startSyncLoop() {
	if a.syncLoopStop != nil {
		return
	}
	every := a.syncEvery
	if every <= 0 {
		every = defaultSyncEvery
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.syncLoopStop = cancel
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if a.post == nil {
					continue
				}
				a.post(func() { a.syncNow(false) })
			}
		}
	}()
}

func (a *app) stopSyncLoop() {
	if a.syncTimer != nil {
		a.syncTimer.Stop()
		a.syncTimer = nil
	}
	if a.syncLoopStop != nil {
		a.syncLoopStop()
		a.syncLoopStop = nil
	}
	a.syncAgain = false
	a.syncManual = false
}

// syncNow 立刻同步一次。manual 为真时把结果告诉用户；自动同步失败只记下错误，避免反复弹窗。
func (a *app) syncNow(manual bool) {
	if !a.vaultOpen() {
		if manual {
			a.toast(ui.Toast{Title: "请先解锁保险库", Description: "锁定时不能同步。"})
		}
		return
	}
	if !a.syncConfigured() {
		if manual {
			a.toast(ui.Toast{Title: "还没有开启同步", Description: "同步地址和账号记在这台设备上，密码只放在系统安全存储里。"})
		}
		return
	}
	if a.post == nil {
		return
	}
	if a.syncing {
		a.syncAgain = true
		if manual {
			a.syncManual = true
		}
		return
	}
	a.syncing = true
	fileURL, user, pass := a.local.SyncURL, a.local.SyncUser, a.syncPassword()
	v := a.vault
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		err := syncer.Sync(ctx, v, &syncer.Client{URL: fileURL, User: user, Password: pass})
		a.post(func() {
			a.syncing = false
			if !a.vaultOpen() || a.vault != v {
				return
			}
			if err != nil {
				a.syncErr = syncUserMessage(err)
				if manual {
					a.toast(ui.Toast{Title: "没能同步", Description: a.syncErr, Type: "error"})
				}
			} else {
				a.syncErr = ""
				a.lastSync = time.Now()
				a.reload()
				if manual {
					a.toast(ui.Toast{Title: "已同步"})
				}
			}
			if a.syncAgain {
				againManual := a.syncManual
				a.syncAgain, a.syncManual = false, false
				a.syncNow(againManual)
			}
		})
	}()
}

func syncUserMessage(err error) string {
	switch {
	case errors.Is(err, syncer.ErrAuth):
		return "同步账号或密码不正确。"
	case errors.Is(err, syncer.ErrUnavailable):
		return "连不上同步服务，本机的修改还在，恢复后再试。"
	case errors.Is(err, syncer.ErrConflict):
		return "同步时和其他设备改到了一起，请稍后再试。"
	case errors.Is(err, syncer.ErrNoFolder):
		return "同步地址下还没有这个文件夹，请先在服务器上建好。"
	case errors.Is(err, syncer.ErrNoETag):
		return "同步服务没有返回文件版本，为避免覆盖已取消上传。"
	case errors.Is(err, vault.ErrDifferentVault):
		return "远端是另一个保险库，不能合并到这台设备上。"
	case errors.Is(err, vault.ErrLocked):
		return "保险库已锁定，解锁后再同步。"
	default:
		return "没能完成同步，本机的修改还在。"
	}
}
