package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"serval/core/hosts"
	"serval/core/vault"

	"github.com/egoist/mygo/ui"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// kindKnownHost 是保险库里主机信任记录的类型。信任记录放在保险库里而不是本机文件，
// 是为了随同步到其他设备：在一台设备上确认过的服务器，其他设备不用再确认；
// 服务器换了密钥时，所有设备都会拒绝。
const kindKnownHost = "knownhost"

// knownHost 是一台服务器的信任记录。
type knownHost struct {
	// Host 是 knownhosts.Normalize 之后的地址，例如 example.com 或 [example.com]:2222。
	Host string `json:"host"`
	// Keys 是 authorized_keys 格式的公钥（一台服务器可能有多种算法的密钥）。
	Keys  []string  `json:"keys"`
	Added time.Time `json:"added"`
}

// knownHostID 由地址算出记录 ID。两台设备各自信任同一台服务器时得到同一个 ID，
// 同步时合并成一条记录，而不是两条重复的。
func knownHostID(host string) string {
	sum := sha256.Sum256([]byte(host))
	return "kh-" + hex.EncodeToString(sum[:16])
}

// vaultTrust 用保险库实现 sshconn.TrustedKeys。它在连接的 goroutine 里调用，
// 只碰保险库（并发安全），不碰界面状态。
type vaultTrust struct{ v *vault.Vault }

func (t vaultTrust) load(host string) (knownHost, bool, error) {
	r, ok, err := t.v.Get(knownHostID(host))
	if err != nil || !ok {
		return knownHost{}, false, err
	}
	var k knownHost
	if err := json.Unmarshal(r.Data, &k); err != nil {
		return knownHost{}, false, err
	}
	return k, true, nil
}

func (t vaultTrust) Lookup(host string) ([]ssh.PublicKey, error) {
	k, ok, err := t.load(host)
	if err != nil || !ok {
		return nil, err
	}
	var keys []ssh.PublicKey
	for _, line := range k.Keys {
		if pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err == nil {
			keys = append(keys, pub)
		}
	}
	return keys, nil
}

func (t vaultTrust) Trust(host string, key ssh.PublicKey) error {
	k, ok, err := t.load(host)
	if err != nil {
		return err
	}
	if !ok {
		k = knownHost{Host: host, Added: time.Now()}
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	if !slices.Contains(k.Keys, line) {
		k.Keys = append(k.Keys, line)
	}
	ch, err := vault.JSONChange(kindKnownHost, knownHostID(host), k)
	if err != nil {
		return err
	}
	return t.v.Apply([]vault.Change{ch})
}

// trustedAddress 返回主机在信任记录里的地址。
func trustedAddress(h hosts.Host) string {
	port := h.Port
	if port == 0 {
		port = 22
	}
	return knownhosts.Normalize(net.JoinHostPort(h.Address, strconv.Itoa(port)))
}

// hasTrustRecord 报告这台主机有没有保存的信任记录。
func (a *app) hasTrustRecord(h hosts.Host) bool {
	_, ok, _ := a.vault.Get(knownHostID(trustedAddress(h)))
	return ok
}

// resetHostKey 删除主机的信任记录。服务器确实重装、换了密钥时用；下次连接重新核对指纹。
func (a *app) resetHostKey(h hosts.Host) {
	if err := a.vault.Delete(knownHostID(trustedAddress(h))); err != nil {
		a.notice = err.Error()
		return
	}
	a.notice = ""
	a.toast(ui.Toast{Title: "已重置 " + h.Name + " 的主机密钥", Description: "下次连接时会重新请你核对指纹。"})
}

// knownHostsList 返回保险库里保存的全部主机信任记录。
func (a *app) knownHostsList() []knownHost {
	var list []knownHost
	_ = decodeAll(a.vault, kindKnownHost, &list)
	return list
}

// keySummaries 返回这台主机已信任公钥的类型和 SHA-256 指纹。
func (k knownHost) keySummaries() []string {
	var out []string
	for _, line := range k.Keys {
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			continue
		}
		out = append(out, pub.Type()+"  "+ssh.FingerprintSHA256(pub))
	}
	return out
}

// deleteKnownHost 从保险库里删除单条已知主机记录。
func (a *app) deleteKnownHost(host string) {
	if err := a.vault.Delete(knownHostID(host)); err != nil {
		a.settingsPanel.err = err.Error()
		return
	}
	a.notice = ""
	a.toast(ui.Toast{Title: "已删除 " + host + " 的主机密钥", Description: "下次连接时会重新请你核对指纹。"})
}
