package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"serval/core/syncer"
	"serval/core/vault"
	"serval/design"

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

// normalizeSyncURL 规范化用户填写的 WebDAV 或 S3 同步目标地址。
func normalizeSyncURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("请填写同步地址")
	}
	if syncer.IsS3URL(s) {
		target, err := syncer.ParseS3URL(s)
		if err != nil {
			return "", err
		}
		return target.FormatURL(), nil
	}
	return syncer.FileURL(s)
}

// newSyncRemote 根据地址与凭据构造 Remote 客户端。
func newSyncRemote(fileURL, user, password string) (syncer.Remote, error) {
	if syncer.IsS3URL(fileURL) {
		return syncer.NewS3ClientFromURL(fileURL, user, password)
	}
	return &syncer.Client{URL: fileURL, User: user, Password: password}, nil
}

// probeSyncRemote 测试远端是否能连接。
func probeSyncRemote(ctx context.Context, fileURL, user, password string) error {
	remote, err := newSyncRemote(fileURL, user, password)
	if err != nil {
		return err
	}
	if probeable, ok := remote.(interface{ Probe(context.Context) error }); ok {
		return probeable.Probe(ctx)
	}
	_, _, err = remote.Download(ctx)
	if errors.Is(err, syncer.ErrNotFound) {
		return nil
	}
	return err
}

// buildSyncURLFromDraft 根据草稿面板字段构建规范的同步地址及账号、密码。
func (p *settingsPanel) buildDraftConfig() (fileURL string, user string, password string, err error) {
	subDir := strings.TrimSpace(p.syncSubDir)
	kind := strings.TrimSpace(p.syncKind)
	if kind == "S3 兼容存储" || syncer.IsS3URL(p.syncURL) {
		bucket := strings.TrimSpace(p.s3Bucket)
		if bucket == "" {
			return "", "", "", errors.New("请填写存储桶 (Bucket)")
		}
		cleanSub := strings.Trim(subDir, "/")
		key := "vault.json"
		if cleanSub != "" {
			key = cleanSub + "/vault.json"
		}
		target := syncer.S3Target{
			Endpoint: strings.TrimSpace(p.s3Endpoint),
			Region:   strings.TrimSpace(p.s3Region),
			Bucket:   bucket,
			Key:      key,
		}
		fileURL = target.FormatURL()
		user = strings.TrimSpace(p.s3AccessID)
		password = p.s3Secret
		return fileURL, user, password, nil
	}

	// WebDAV
	server := strings.TrimSpace(p.webdavServer)
	if server == "" {
		server = strings.TrimSpace(p.syncURL)
	}
	if server == "" {
		return "", "", "", errors.New("请填写 WebDAV 服务器地址")
	}
	fileURL, err = syncer.FileURLWithDir(server, subDir)
	if err != nil {
		return "", "", "", err
	}
	user = strings.TrimSpace(p.webdavUser)
	if user == "" {
		user = strings.TrimSpace(p.syncUser)
	}
	password = p.webdavPassword
	if password == "" {
		password = p.syncPassword
	}
	return fileURL, user, password, nil
}

// configureSync 保存本机的同步设置。password 为空时保留安全存储里已有的密码。
// 地址会规范成 vault.json 的 URL。开启并且保险库未锁定、窗口已就绪时立刻同步一次。
func (a *app) configureSync(rawURL, user, password string, enabled bool) error {
	var fileURL string
	if strings.TrimSpace(rawURL) != "" {
		var err error
		fileURL, err = normalizeSyncURL(rawURL)
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
	next.SyncSubDir = strings.TrimSpace(a.settingsPanel.syncSubDir)
	next.SyncVendor = strings.TrimSpace(a.settingsPanel.syncVendor)
	next.SyncKind = strings.TrimSpace(a.settingsPanel.syncKind)
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

// saveSyncSettings 把设置页里的草稿写入本机。密码留空时不覆盖系统安全存储里已有的密码。
// 已有数据的设备第一次开启同步时，先说明会按记录合并，不会整份覆盖。
func (a *app) saveSyncSettings() {
	p := &a.settingsPanel
	p.probeMsg = ""
	if p.syncOn && !a.local.SyncOn && a.vault != nil && !a.vault.Empty() && !p.allowMerge {
		p.askSyncMerge = true
		return
	}
	p.allowMerge = false

	fileURL, user, password, err := p.buildDraftConfig()
	if err != nil && p.syncOn {
		p.err = err.Error()
		return
	}
	if err == nil {
		p.syncURL = fileURL
		p.syncUser = user
		if password != "" {
			p.syncPassword = password
		}
	}

	if err := a.configureSync(p.syncURL, p.syncUser, p.syncPassword, p.syncOn); err != nil {
		p.err = err.Error()
		return
	}
	p.err = ""
	p.syncPassword = ""
	p.webdavPassword = ""
	p.s3Secret = ""
	p.syncURL = a.local.SyncURL
	p.syncUser = a.local.SyncUser
	p.syncOn = a.local.SyncOn
	a.toast(ui.Toast{Title: "已保存同步设置"})
}

// testSyncFromPanel 用当前草稿试一次连接，不保存、不上传。密码留空时用已保存的密码。
func (a *app) testSyncFromPanel() {
	p := &a.settingsPanel
	if p.probeBusy {
		return
	}
	fileURL, user, password, err := p.buildDraftConfig()
	if err != nil {
		// 回退兼容老字段
		rawURL := strings.TrimSpace(p.syncURL)
		if rawURL != "" {
			fileURL, err = normalizeSyncURL(rawURL)
			user = strings.TrimSpace(p.syncUser)
			password = p.syncPassword
		}
	}
	if err != nil {
		p.probeOK = false
		p.probeMsg = err.Error()
		p.err = ""
		return
	}
	if password == "" {
		password = a.syncPassword()
	}
	if a.post == nil {
		p.probeOK = false
		p.probeMsg = "窗口还没准备好，稍后再试。"
		return
	}
	p.probeBusy = true
	p.probeOK = false
	p.probeMsg = ""
	p.err = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := probeSyncRemote(ctx, fileURL, user, password)
		msg := "可以连接。"
		ok := err == nil
		if err != nil {
			msg = syncUserMessage(err)
		}
		a.post(func() {
			if !a.settingsPanel.open {
				return
			}
			a.settingsPanel.probeBusy = false
			a.settingsPanel.probeOK = ok
			a.settingsPanel.probeMsg = msg
		})
	}()
}

func formatSyncTime(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04")
}

func (a *app) openRestore() {
	a.settingsPanel = settingsPanel{}
	a.restore = restorePanel{open: true, fresh: true}
}

// restoreDialog 让空设备下载已有的保险库，再用原来的主密码解锁。
func (a *app) restoreDialog(c *ui.Context, colors design.Colors) {
	p := &a.restore
	if !p.open || a.setup.open {
		return
	}
	sheet(c, colors, &p.open, &p.fresh, 460, 22, func() {
		ui.Column(c).Gap(14).Children(func() {
			ui.Text(c, "恢复保险库").FontSize(design.TextHeading).FontWeight(600)
			ui.Text(c, "从同步服务下载已有的保险库。下载完成后输入原来的主密码解锁。").
				FontSize(design.TextSmall).TextColor(c.Theme().TextMuted)
			userLabel := "账号"
			passLabel := "同步密码"
			userDesc := "可选"
			if syncer.IsS3URL(p.url) {
				userLabel = "AccessKey"
				passLabel = "SecretKey"
				userDesc = "S3 访问密钥 ID"
			}
			ui.Field(c, "服务地址", func() {
				c.Key("restoreURL")
				ui.TextInput(c, &p.url).Placeholder("WebDAV 地址或 s3://bucket/vault.json").AutoFocus()
			}).Description("支持 WebDAV 或 S3 对象存储（如 s3://my-bucket/vault.json）。")
			ui.Field(c, userLabel, func() {
				c.Key("restoreUser")
				ui.TextInput(c, &p.user).Placeholder(userDesc)
			})
			ui.Field(c, passLabel, func() {
				c.Key("restorePassword")
				ui.TextInput(c, &p.password).Password()
			}).Description("凭据只存放在这台设备的系统安全存储里。")
			if p.err != "" {
				ui.Text(c, p.err).Key("restoreError").FontSize(design.TextSmall).TextColor(colors.Danger).Selectable()
			}
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				ui.Button(c, "取消").Key("restoreCancel").Disabled(p.busy).OnClick(func() {
					a.restore = restorePanel{}
				})
				label := "开始恢复"
				if p.busy {
					label = "正在恢复…"
				}
				ui.PrimaryButton(c, label).Key("restoreSubmit").Disabled(p.busy).OnClick(a.submitRestore)
			})
		})
	})
	if !p.open {
		a.restore = restorePanel{}
	}
}

// syncMergeDialog 在已有数据的设备开启同步前说明合并规则。默认按钮是取消。
func (a *app) syncMergeDialog(c *ui.Context) {
	p := &a.settingsPanel
	if !p.askSyncMerge {
		return
	}
	choice := ui.AlertDialog(c, &p.askSyncMerge, "与已有数据合并？",
		"这台设备上已经有数据。同步会按记录合并，不会用一边覆盖另一边。",
		"合并并同步", "取消")
	if choice == 0 {
		p.allowMerge = true
		a.saveSyncSettings()
	}
}

// submitRestore 下载远端保险库。本地是空的才整份换上；已有数据时拒绝覆盖。
func (a *app) submitRestore() {
	p := &a.restore
	if p.busy {
		return
	}
	fileURL, err := normalizeSyncURL(p.url)
	if err != nil {
		p.err = err.Error()
		return
	}
	if a.vault == nil || !a.vault.Empty() {
		p.err = "这台设备已有数据，不能整份替换。请在设置的“同步”里开启同步，两边会按记录合并。"
		return
	}
	if a.post == nil {
		p.err = "窗口还没准备好，稍后再试。"
		return
	}
	user, password := strings.TrimSpace(p.user), p.password
	p.busy = true
	p.err = ""
	v := a.vault
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		remote, err := newSyncRemote(fileURL, user, password)
		if err != nil {
			a.post(func() {
				if a.vault == v && p.open {
					p.busy = false
					p.err = err.Error()
				}
			})
			return
		}
		body, _, err := remote.Download(ctx)
		a.post(func() {
			p := &a.restore
			p.busy = false
			if a.vault != v || !p.open {
				return
			}
			if err != nil {
				if errors.Is(err, syncer.ErrNotFound) {
					p.err = "远端还没有保险库。如果这是第一台设备，请在设置里开启同步，把本机数据传上去。"
					return
				}
				p.err = syncUserMessage(err)
				return
			}
			if err := v.Adopt(body); err != nil {
				p.err = err.Error()
				return
			}
			if err := a.configureSync(fileURL, user, password, true); err != nil {
				p.err = err.Error()
				return
			}
			a.restore = restorePanel{}
			a.hosts, a.keys, a.proxies = nil, nil, nil
			a.settings = settings{}
			if a.vault.State() == vault.Locked {
				a.toast(ui.Toast{Title: "已下载保险库", Description: "输入原来的主密码即可解锁。"})
				return
			}
			a.reload()
			a.toast(ui.Toast{Title: "已从同步恢复"})
		})
	}()
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
	if !a.vaultOpen() || a.post == nil {
		return
	}
	if a.syncConfigured() {
		a.startSyncLoop()
		a.syncNow(false)
	}
	a.startSyncDir()
}

// noteVaultChanged 在本地写入成功后调用。短时间里的连续修改合成一次同步。
func (a *app) noteVaultChanged() {
	if !a.vaultOpen() {
		return
	}
	a.exportSyncDir()
	if !a.syncConfigured() {
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
		remote, err := newSyncRemote(fileURL, user, pass)
		if err == nil {
			err = syncer.Sync(ctx, v, remote)
		}
		a.post(func() {
			a.syncing = false
			if !a.vaultOpen() || a.vault != v {
				return
			}
			if err != nil {
				a.syncErr = syncUserMessage(err)
				if errors.Is(err, vault.ErrDifferentVault) && a.vault.Empty() {
					a.syncErr = "远端是另一个保险库。这台设备还是空的，请用侧栏的“从同步恢复”。"
				}
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
