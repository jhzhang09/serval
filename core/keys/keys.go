// Package keys 解析、生成 SSH 私钥。私钥内容存进保险库，所以这里只处理内存里的数据，不读写文件。
package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Key 是保险库里的一把私钥。
type Key struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Type 是公钥算法，例如 ssh-ed25519、ssh-rsa、ecdsa-sha2-nistp256。
	Type string `json:"type"`
	// Bits 是 RSA 和 ECDSA 的位数，ed25519 为 0。
	Bits int `json:"bits,omitempty"`
	// PrivateKey 是 PEM 格式的私钥原文。加了口令的私钥保持加密状态存放。
	PrivateKey string `json:"privateKey"`
	// PublicKey 是 authorized_keys 格式的一行公钥。
	PublicKey   string `json:"publicKey"`
	Fingerprint string `json:"fingerprint"`
	// Passphrase 是私钥口令。为空且私钥加了口令时，连接时再询问。
	Passphrase string    `json:"passphrase,omitempty"`
	Created    time.Time `json:"created"`
}

var (
	// ErrPassphraseNeeded 表示私钥加了口令，解析需要提供口令。
	ErrPassphraseNeeded = errors.New("这把私钥加了口令，请输入口令")
	// ErrBadPassphrase 表示口令不对。
	ErrBadPassphrase = errors.New("私钥口令不正确")
	// ErrNotPrivateKey 表示内容不是私钥。
	ErrNotPrivateKey = errors.New("这不是 SSH 私钥。请选择私钥文件（例如 id_ed25519），而不是 .pub 公钥")
)

// Encrypted 报告 PEM 私钥是否加了口令。
func Encrypted(pemText []byte) bool {
	_, err := ssh.ParseRawPrivateKey(pemText)
	var missing *ssh.PassphraseMissingError
	return errors.As(err, &missing)
}

// Parse 校验私钥并提取公钥和指纹。加了口令的私钥必须提供正确的口令才能导入：
// 这样导入时就能发现口令记错了，而不是等到连接时才失败。
func Parse(name string, pemText []byte, passphrase string) (Key, error) {
	text := strings.TrimSpace(string(pemText))
	if text == "" {
		return Key{}, errors.New("私钥内容为空")
	}
	if strings.HasPrefix(text, "ssh-") || strings.HasPrefix(text, "ecdsa-") {
		return Key{}, ErrNotPrivateKey
	}
	data := []byte(text + "\n")
	var signer ssh.Signer
	var err error
	if passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(data)
	}
	var missing *ssh.PassphraseMissingError
	switch {
	case errors.As(err, &missing):
		return Key{}, ErrPassphraseNeeded
	case errors.Is(err, x509.IncorrectPasswordError):
		return Key{}, ErrBadPassphrase
	case err != nil:
		if !strings.Contains(text, "PRIVATE KEY") {
			return Key{}, ErrNotPrivateKey
		}
		return Key{}, fmt.Errorf("无法解析私钥: %w", err)
	}
	return describe(Key{Name: strings.TrimSpace(name), PrivateKey: string(data), Passphrase: passphrase}, signer.PublicKey()), nil
}

func describe(k Key, pub ssh.PublicKey) Key {
	k.Type = pub.Type()
	k.PublicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	k.Fingerprint = ssh.FingerprintSHA256(pub)
	if crypto, ok := pub.(ssh.CryptoPublicKey); ok {
		if p, ok := crypto.CryptoPublicKey().(*rsa.PublicKey); ok {
			k.Bits = p.N.BitLen()
		}
	}
	if strings.HasPrefix(k.Type, "ecdsa-sha2-nistp") {
		fmt.Sscanf(strings.TrimPrefix(k.Type, "ecdsa-sha2-nistp"), "%d", &k.Bits)
	}
	if k.Created.IsZero() {
		k.Created = time.Now()
	}
	return k
}

// 可生成的密钥类型。
const (
	Ed25519 = "ed25519"
	RSA4096 = "rsa-4096"
)

// Generate 生成一把新私钥，OpenSSH 格式。passphrase 非空时私钥本身也用口令加密
// （导出到别处时仍受保护）。comment 写进公钥末尾，通常是“用户@设备”。
func Generate(kind, name, comment, passphrase string) (Key, error) {
	var private any
	switch kind {
	case Ed25519:
		_, p, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return Key{}, err
		}
		private = p
	case RSA4096:
		p, err := rsa.GenerateKey(rand.Reader, 4096)
		if err != nil {
			return Key{}, err
		}
		private = p
	default:
		return Key{}, fmt.Errorf("不支持的密钥类型 %q", kind)
	}
	var block *pem.Block
	var err error
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(private, comment, []byte(passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(private, comment)
	}
	if err != nil {
		return Key{}, err
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return Key{}, err
	}
	k := describe(Key{Name: strings.TrimSpace(name), PrivateKey: string(pem.EncodeToMemory(block)), Passphrase: passphrase}, signer.PublicKey())
	if comment != "" {
		k.PublicKey += " " + comment
	}
	return k, nil
}

// Label 返回给人看的类型说明，例如“ED25519”“RSA 4096”。
func (k Key) Label() string {
	switch {
	case k.Type == ssh.KeyAlgoED25519:
		return "ED25519"
	case k.Type == ssh.KeyAlgoRSA:
		return fmt.Sprintf("RSA %d", k.Bits)
	case strings.HasPrefix(k.Type, "ecdsa-"):
		return fmt.Sprintf("ECDSA %d", k.Bits)
	}
	return strings.ToUpper(k.Type)
}
