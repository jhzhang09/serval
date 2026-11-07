package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"serval/core/hosts"
	"serval/core/keys"
	"serval/core/vault"

	"github.com/egoist/mygo/ui"
)

// 保险库里的记录类型。
const (
	kindHost = "host"
	kindKey  = "key"
)

// deviceStore 是系统安全存储（正式运行时是 mygo.Secrets，测试里是内存实现）。
// 它只用来“在这台设备上记住”保险库密钥，不存放任何用户数据：用户数据都在保险库里，才能同步。
type deviceStore interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
}

func rememberedKeyName(vaultID string) string { return "vault-key/" + vaultID }

// reload 从保险库重新读出主机和私钥。保险库是唯一的数据来源，界面上的列表只是它的副本。
func (a *app) reload() {
	a.hosts, a.keys, a.hostsError = nil, nil, ""
	if err := decodeAll(a.vault, kindHost, &a.hosts); err != nil {
		a.hostsError = err.Error()
	}
	if err := decodeAll(a.vault, kindKey, &a.keys); err != nil && a.hostsError == "" {
		a.hostsError = err.Error()
	}
}

func decodeAll[T any](v *vault.Vault, kind string, out *[]T) error {
	records, err := v.List(kind)
	if err != nil {
		return err
	}
	for _, r := range records {
		var item T
		if err := json.Unmarshal(r.Data, &item); err != nil {
			return fmt.Errorf("保险库里有一条无法读取的记录（%s）: %w", r.ID, err)
		}
		*out = append(*out, item)
	}
	return nil
}

// hostChange、keyChange 把一条主机或私钥转成保险库的修改。用 JSONChange 写入：
// 只替换本版本认识的字段，其他设备上更新版本的 Serval 加的字段原样保留。
func hostChange(h hosts.Host) vault.Change { return mustJSONChange(kindHost, h.ID, h) }

func keyChange(k keys.Key) vault.Change { return mustJSONChange(kindKey, k.ID, k) }

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
	err               string
	// then 是设置成功后继续执行的操作（保存表单、添加私钥、带着“记住密码”重新连接）。
	then func()
}

// requireProtection 保证保险库已加密后再执行 then。还没设置主密码时先请用户设置，
// 取消则什么都不做，原来的面板保持打开，用户可以去掉要保存的秘密。
func (a *app) requireProtection(then func()) {
	if a.vault.State() != vault.Unprotected {
		then()
		return
	}
	a.setup = setupSheet{open: true, fresh: true, remember: a.device != nil, then: then}
}

func (a *app) submitSetup() {
	s := &a.setup
	if s.password != s.confirm {
		s.err = "两次输入的主密码不一致。"
		return
	}
	if err := a.vault.Protect(s.password); err != nil {
		s.err = err.Error()
		return
	}
	a.rememberOnDevice(s.remember)
	then := s.then
	a.setup = setupSheet{}
	a.toast(ui.Toast{Title: "已设置主密码"})
	if then != nil {
		then()
	}
}

// rememberOnDevice 按用户的选择在系统安全存储里保存或删除保险库密钥。
func (a *app) rememberOnDevice(remember bool) {
	if a.device == nil {
		return
	}
	name := rememberedKeyName(a.vault.ID())
	if !remember {
		a.device.Delete(name) // 本来就没有也不算错
		return
	}
	if err := a.device.Set(name, a.vault.Key()); err != nil {
		a.toast(ui.Toast{Title: "没能在这台设备上记住主密码", Description: err.Error(), Type: "error"})
	}
}

// unlockWithRemembered 启动时用本机记住的密钥自动解锁。没有记住或者密钥失效时返回 false，
// 由界面请用户输入主密码。
func (a *app) unlockWithRemembered() bool {
	if a.vault.State() != vault.Locked || a.device == nil {
		return false
	}
	key, err := a.device.Get(rememberedKeyName(a.vault.ID()))
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
	err      string
}

func (a *app) submitUnlock() {
	u := &a.unlock
	err := a.vault.Unlock(u.password)
	u.password = ""
	if errors.Is(err, vault.ErrWrongPassword) {
		u.err = "主密码不正确。"
		return
	}
	if err != nil {
		u.err = err.Error()
		return
	}
	a.rememberOnDevice(u.remember)
	a.unlock = unlockState{}
	a.reload()
}
