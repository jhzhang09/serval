package sshconn

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ErrHostKeyRejected 表示用户拒绝信任未知主机，或没有提供确认回调。
var ErrHostKeyRejected = errors.New("未信任该主机的密钥")

// HostKeyChangedError 表示主机密钥与已保存的记录不一致，可能存在中间人攻击，
// 这种情况一律拒绝连接，不提供“继续”的选项。
type HostKeyChangedError struct {
	Host        string
	Fingerprint string
}

func (e *HostKeyChangedError) Error() string {
	return fmt.Sprintf("%s 的主机密钥已变化（当前 %s），已拒绝连接", e.Host, e.Fingerprint)
}

// HostKeyInfo 是需要用户确认的未知主机信息。
type HostKeyInfo struct {
	Host        string
	KeyType     string
	Fingerprint string
}

// TrustedKeys 是应用自己保存的主机信任记录（Serval 存在保险库里，随同步到其他设备）。
// host 是 knownhosts.Normalize 之后的形式，例如 example.com 或 [example.com]:2222。
// 方法会在连接的 goroutine 里调用，实现必须并发安全。
type TrustedKeys interface {
	Lookup(host string) ([]ssh.PublicKey, error)
	Trust(host string, key ssh.PublicKey) error
}

// HostKeyPolicy 按 known_hosts 校验服务器身份。
//
// ReadOnlyFiles（通常是 ~/.ssh/known_hosts）只读取，不会写入，避免改动用户
// 现有的 OpenSSH 配置。用户确认的新密钥保存到 Trusted；没有 Trusted 时保存到 TrustStore 文件。
type HostKeyPolicy struct {
	ReadOnlyFiles []string
	TrustStore    string
	Trusted       TrustedKeys
	// Confirm 询问用户是否信任未知主机，可以阻塞。为 nil 时拒绝所有未知主机。
	Confirm func(HostKeyInfo) bool
}

// storeMu 保护同一进程内对 TrustStore 的并发追加。
var storeMu sync.Mutex

func (p *HostKeyPolicy) files() []string {
	var files []string
	for _, f := range append(slices.Clone(p.ReadOnlyFiles), p.TrustStore) {
		if f == "" {
			continue
		}
		if _, err := os.Stat(f); err == nil {
			files = append(files, f)
		}
	}
	return files
}

// callback 返回握手用的校验函数和应优先协商的主机密钥算法。
// onWaiting 在开始和结束等待用户确认时被调用。
func (p *HostKeyPolicy) callback(addr string, onWaiting func(bool)) (ssh.HostKeyCallback, []string, error) {
	check := func(string, net.Addr, ssh.PublicKey) error {
		return &knownhosts.KeyError{}
	}
	if files := p.files(); len(files) > 0 {
		cb, err := knownhosts.New(files...)
		if err != nil {
			return nil, nil, fmt.Errorf("读取 known_hosts 失败: %w", err)
		}
		check = cb
	}
	if p.Trusted != nil {
		check = p.withTrusted(check)
	}

	callback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := check(hostname, remote, key)
		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			return err // nil（已信任）或吊销等其他错误
		}
		fingerprint := ssh.FingerprintSHA256(key)
		if len(keyErr.Want) > 0 {
			return &HostKeyChangedError{Host: knownhosts.Normalize(hostname), Fingerprint: fingerprint}
		}
		if p.Confirm == nil {
			return ErrHostKeyRejected
		}
		onWaiting(true)
		defer onWaiting(false)
		if !p.Confirm(HostKeyInfo{Host: hostname, KeyType: key.Type(), Fingerprint: fingerprint}) {
			return ErrHostKeyRejected
		}
		return p.trust(hostname, key)
	}
	return callback, knownAlgorithms(check, addr), nil
}

// knownAlgorithms 查出已保存的该主机密钥类型，优先协商这些算法。
//
// 否则服务器可能选用另一种同样合法的密钥（比如记录的是 RSA，协商到 ed25519），
// 被误判为“密钥已变化”。这里用一把临时生成的密钥探测：它不可能匹配任何记录，
// knownhosts 会在错误中列出该主机的全部已知密钥。
func knownAlgorithms(check ssh.HostKeyCallback, addr string) []string {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return nil
	}
	remote := &net.TCPAddr{IP: net.IPv4zero}
	var keyErr *knownhosts.KeyError
	if !errors.As(check(addr, remote, signer.PublicKey()), &keyErr) {
		return nil
	}
	var algorithms []string
	for _, known := range keyErr.Want {
		switch t := known.Key.Type(); t {
		case ssh.KeyAlgoRSA:
			algorithms = append(algorithms, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
		default:
			algorithms = append(algorithms, t)
		}
	}
	return algorithms
}

// withTrusted 在文件校验之外再查 Trusted 里的记录，结果合成 knownhosts 的错误格式：
// 有记录但都不匹配时 Want 非空（密钥已变化），完全没有记录时 Want 为空（未知主机）。
func (p *HostKeyPolicy) withTrusted(fileCheck ssh.HostKeyCallback) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		saved, err := p.Trusted.Lookup(knownhosts.Normalize(hostname))
		if err != nil {
			return fmt.Errorf("读取已信任的主机密钥失败: %w", err)
		}
		for _, k := range saved {
			if bytes.Equal(k.Marshal(), key.Marshal()) {
				return nil
			}
		}
		fileErr := fileCheck(hostname, remote, key)
		var keyErr *knownhosts.KeyError
		if !errors.As(fileErr, &keyErr) {
			if fileErr == nil {
				return nil // ~/.ssh/known_hosts 里已有记录
			}
			return fileErr
		}
		combined := &knownhosts.KeyError{Want: keyErr.Want}
		for _, k := range saved {
			combined.Want = append(combined.Want, knownhosts.KnownKey{Key: k, Filename: "Serval"})
		}
		return combined
	}
}

func (p *HostKeyPolicy) trust(hostname string, key ssh.PublicKey) error {
	if p.Trusted != nil {
		if err := p.Trusted.Trust(knownhosts.Normalize(hostname), key); err != nil {
			return fmt.Errorf("保存主机密钥失败: %w", err)
		}
		return nil
	}
	if p.TrustStore == "" {
		return nil // 只信任本次连接
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(p.TrustStore), 0o700); err != nil {
		return fmt.Errorf("保存主机密钥失败: %w", err)
	}
	f, err := os.OpenFile(p.TrustStore, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("保存主机密钥失败: %w", err)
	}
	defer f.Close()
	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
	if _, err := fmt.Fprintln(f, line); err != nil {
		return fmt.Errorf("保存主机密钥失败: %w", err)
	}
	return nil
}
