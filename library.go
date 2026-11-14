package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"serval/core/hosts"
	"serval/core/keys"
	"serval/core/vault"
	"serval/locales"

	"github.com/egoist/mygo/ui"
)

// 保险库里的记录类型。
const (
	kindHost     = "host"
	kindKey      = "key"
	kindSettings = "settings"
	kindProxy    = "proxy"
	kindSnippet  = "snippet"
	kindForward  = "forward"

	// settingsID 是随同步设置记录的固定 ID，多台设备修改后按同一条记录的 rev 合并。
	settingsID = "settings"
)

// deviceStore 是系统安全存储（正式运行时是 mygo.Secrets，测试里是内存实现）。
// 它只用来“在这台设备上记住”保险库密钥，不存放任何用户数据：用户数据都在保险库里，才能同步。
type deviceStore interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
}

func rememberedKeyName(vaultID string) string { return "vault-key/" + vaultID }

// reload 从保险库重新读出主机、私钥、代码片段和随同步设置。保险库是唯一的数据来源，界面上的状态只是它的副本。
func (a *app) reload() {
	a.hosts, a.keys, a.proxies, a.snippets, a.forwards, a.settings, a.hostsError = nil, nil, nil, nil, nil, settings{}, ""
	if err := decodeAll(a.vault, kindHost, &a.hosts); err != nil {
		a.hostsError = err.Error()
	}
	if err := decodeAll(a.vault, kindProxy, &a.proxies); err != nil && a.hostsError == "" {
		a.hostsError = err.Error()
	}
	if err := decodeAll(a.vault, kindForward, &a.forwards); err != nil && a.hostsError == "" {
		a.hostsError = err.Error()
	}
	if err := decodeAll(a.vault, kindKey, &a.keys); err != nil && a.hostsError == "" {
		a.hostsError = err.Error()
	}
	if err := decodeAll(a.vault, kindSnippet, &a.snippets); err != nil && a.hostsError == "" {
		a.hostsError = err.Error()
	}
	if err := decodeSettings(a.vault, &a.settings); err != nil && a.hostsError == "" {
		a.hostsError = err.Error()
	}
	a.customGroups = slices.Clone(a.settings.CustomGroups)
	a.dropStaleTunnels()
	a.refreshTerminals()
}

func decodeAll[T any](v *vault.Vault, kind string, out *[]T) error {
	records, err := v.List(kind)
	if err != nil {
		return err
	}
	for _, r := range records {
		var item T
		if err := json.Unmarshal(r.Data, &item); err != nil {
			return fmt.Errorf("%s%w", locales.LibRecordUnreadable(r.ID), err)
		}
		*out = append(*out, item)
	}
	return nil
}

func decodeSettings(v *vault.Vault, out *settings) error {
	r, ok, err := v.Get(settingsID)
	if err != nil || !ok {
		return err
	}
	if err := json.Unmarshal(r.Data, out); err != nil {
		return fmt.Errorf("%s%w", locales.LibRecordUnreadable(r.ID), err)
	}
	return nil
}

// hostChange、keyChange、settingsChange 把主机、私钥或设置转成保险库的修改。用 JSONChange 写入：
// 只替换本版本认识的字段，其他设备上更新版本的 Serval 加的字段原样保留。
func hostChange(h hosts.Host) vault.Change { return mustJSONChange(kindHost, h.ID, h) }

func keyChange(k keys.Key) vault.Change { return mustJSONChange(kindKey, k.ID, k) }

func settingsChange(s settings) vault.Change { return mustJSONChange(kindSettings, settingsID, s) }

func proxyChange(p hosts.Proxy) vault.Change { return mustJSONChange(kindProxy, p.ID, p) }

func snippetChange(s snippet) vault.Change { return mustJSONChange(kindSnippet, s.ID, s) }

func forwardChange(f hosts.Forward) vault.Change { return mustJSONChange(kindForward, f.ID, f) }

func (a *app) proxyByID(id string) (hosts.Proxy, bool) {
	i := slices.IndexFunc(a.proxies, func(p hosts.Proxy) bool { return p.ID == id })
	if i < 0 || id == "" {
		return hosts.Proxy{}, false
	}
	return a.proxies[i], true
}

// saveSettings 把随同步的偏好设置写进保险库并刷新界面。
func (a *app) saveSettings(s settings) error {
	return a.apply(settingsChange(s))
}

// loadLocal 读取本机设置文件（device.json）。文件不存在时使用默认值。
func (a *app) loadLocal() {
	d, err := loadDeviceSettings(a.devicePath)
	if err != nil {
		if a.hostsError == "" {
			a.hostsError = err.Error()
		}
		return
	}
	a.local = d
	if d.SidebarWidth > 0 {
		a.sidebarWidth = min(max(d.SidebarWidth, 200), 360)
	}
	a.migrateAIKey()
}

// saveLocal 保存只属于本机的设置到 device.json，不写进保险库。
func (a *app) saveLocal(d deviceSettings) error {
	if err := saveDeviceSettings(a.devicePath, d); err != nil {
		return err
	}
	a.local = d
	return nil
}

func loadDeviceSettings(path string) (deviceSettings, error) {
	if path == "" {
		return deviceSettings{}, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return deviceSettings{}, nil
	}
	if err != nil {
		return deviceSettings{}, fmt.Errorf("%s%w", locales.LibReadDeviceSettingsFailed(), err)
	}
	var d deviceSettings
	if err := json.Unmarshal(data, &d); err != nil {
		return deviceSettings{}, fmt.Errorf("%s%w", locales.LibDeviceSettingsCorrupt(path), err)
	}
	return d, nil
}

func saveDeviceSettings(path string, d deviceSettings) error {
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%s%w", locales.LibSaveDeviceSettingsFailed(), err)
	}
	tmp, err := os.CreateTemp(dir, ".device-*")
	if err != nil {
		return fmt.Errorf("%s%w", locales.LibSaveDeviceSettingsFailed(), err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("%s%w", locales.LibSaveDeviceSettingsFailed(), err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("%s%w", locales.LibSaveDeviceSettingsFailed(), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("%s%w", locales.LibSaveDeviceSettingsFailed(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%s%w", locales.LibSaveDeviceSettingsFailed(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("%s%w", locales.LibSaveDeviceSettingsFailed(), err)
	}
	return nil
}

func mustJSONChange(kind, id string, v any) vault.Change {
	ch, err := vault.JSONChange(kind, id, v)
	if err != nil {
		panic(err) // 结构里只有基本类型，序列化不会失败；失败说明结构定义写错了
	}
	return ch
}

// apply 写入保险库并刷新列表。写入失败时列表保持原样。
func (a *app) apply(changes ...vault.Change) error {
	if err := a.vault.Apply(changes); err != nil {
		return err
	}
	a.reload()
	// 标签和卡片标题里的主机副本跟着更新（改名、改密码后重新连接要用新值）。
	for _, t := range a.tabs {
		if i := slices.IndexFunc(a.hosts, func(h hosts.Host) bool { return h.ID == t.host.ID }); i >= 0 {
			t.host = a.hosts[i]
		}
	}
	a.noteVaultChanged()
	return nil
}

func (a *app) keyByID(id string) (keys.Key, bool) {
	i := slices.IndexFunc(a.keys, func(k keys.Key) bool { return k.ID == id })
	if i < 0 {
		return keys.Key{}, false
	}
	return a.keys[i], true
}

func (a *app) hostByID(id string) (hosts.Host, bool) {
	i := slices.IndexFunc(a.hosts, func(h hosts.Host) bool { return h.ID == id })
	if i < 0 {
		return hosts.Host{}, false
	}
	return a.hosts[i], true
}

// setupSheet 是“设置主密码”面板。第一次保存密码或私钥时出现。
type setupSheet struct {
	open, fresh       bool
	password, confirm string
	remember          bool
	busy              bool
	err               string
	// then 是设置成功后继续执行的操作（保存表单、添加私钥、带着“记住密码”重新连接）。
	then func()
}

// deviceAvailable 报告系统安全存储当前是否可用（部分无 Secret Service 的 Linux 桌面会返回 ErrUnsupported）。
func (a *app) deviceAvailable() bool {
	return a.device != nil && a.deviceSupported
}

// probeDeviceSecrets 探测系统安全存储是否受当前桌面环境支持。
func (a *app) probeDeviceSecrets() {
	if a.device == nil {
		a.deviceSupported = false
		return
	}
	_, err := a.device.Get("serval-secrets-probe")
	a.deviceSupported = !errors.Is(err, errors.ErrUnsupported)
}

// requireProtection 保证保险库已加密后再执行 then。还没设置主密码时先请用户设置，
// 取消则什么都不做，原来的面板保持打开，用户可以去掉要保存的秘密。
func (a *app) requireProtection(then func()) {
	if a.vault.State() != vault.Unprotected {
		then()
		return
	}
	a.setup = setupSheet{open: true, fresh: true, remember: a.deviceAvailable(), then: then}
}

// submitSetup 在后台派生主密码并加密保险库，避免 Argon2id 阻塞主线程。
func (a *app) submitSetup() {
	s := &a.setup
	if s.busy {
		return
	}
	if s.password != s.confirm {
		s.err = locales.LibMasterPasswordMismatch()
		return
	}
	password := s.password
	s.busy, s.err = true, ""
	go func() {
		err := a.vault.Protect(password)
		a.post(func() {
			s := &a.setup
			s.busy = false
			if !s.open {
				return
			}
			if err != nil {
				s.err = err.Error()
				return
			}
			a.rememberOnDevice(s.remember)
			then := s.then
			a.setup = setupSheet{}
			a.migrateAIKey()
			a.noteVaultChanged()
			a.toast(ui.Toast{Title: locales.LibMasterPasswordSet()})
			if then != nil {
				then()
			}
		})
	}()
}

// rememberOnDevice 按用户的选择在系统安全存储里保存或删除保险库密钥。
func (a *app) rememberOnDevice(remember bool) {
	if !a.deviceAvailable() {
		return
	}
	name := rememberedKeyName(a.vault.ID())
	if !remember {
		if err := a.device.Delete(name); errors.Is(err, errors.ErrUnsupported) {
			a.deviceSupported = false
		}
		return
	}
	if err := a.device.Set(name, a.vault.Key()); err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			a.deviceSupported = false
			return
		}
		a.toast(ui.Toast{Title: locales.LibRememberMasterFailed(), Description: err.Error(), Type: "error"})
	}
}

// isRememberedOnDevice 检查这台设备的安全存储里是否记住了保险库密钥。
func (a *app) isRememberedOnDevice() bool {
	if !a.deviceAvailable() || a.vault == nil {
		return false
	}
	key, err := a.device.Get(rememberedKeyName(a.vault.ID()))
	if errors.Is(err, errors.ErrUnsupported) {
		a.deviceSupported = false
		return false
	}
	return err == nil && len(key) > 0
}

// unlockWithRemembered 启动时用本机记住的密钥自动解锁。没有记住或者密钥失效时返回 false，
// 由界面请用户输入主密码。
func (a *app) unlockWithRemembered() bool {
	if a.vault.State() != vault.Locked || !a.deviceAvailable() {
		return false
	}
	key, err := a.device.Get(rememberedKeyName(a.vault.ID()))
	if errors.Is(err, errors.ErrUnsupported) {
		a.deviceSupported = false
		return false
	}
	if err != nil || len(key) == 0 {
		return false
	}
	if err := a.vault.UnlockWithKey(key); err != nil {
		// 主密码在另一台设备上改过不会导致这里失败（VK 不变）；失败说明存储的内容坏了，删掉重来。
		a.device.Delete(rememberedKeyName(a.vault.ID()))
		return false
	}
	a.reload()
	return true
}

// unlockState 是锁定页面的输入。
type unlockState struct {
	password string
	remember bool
	busy     bool
	err      string
}

// submitUnlock 在后台派生主密码并解锁保险库，避免 Argon2id 阻塞主线程。
func (a *app) submitUnlock() {
	u := &a.unlock
	if u.busy {
		return
	}
	password := u.password
	u.password = ""
	u.busy, u.err = true, ""
	go func() {
		err := a.vault.Unlock(password)
		a.post(func() {
			u := &a.unlock
			u.busy = false
			if errors.Is(err, vault.ErrWrongPassword) {
				u.err = locales.LibMasterPasswordWrong()
				return
			}
			if err != nil {
				u.err = err.Error()
				return
			}
			a.rememberOnDevice(u.remember)
			a.unlock = unlockState{}
			a.reload()
			a.migrateAIKey()
			a.restoreSession()
			a.onVaultReady()
		})
	}()
}
