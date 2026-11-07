package keys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestGenerateAndParse(t *testing.T) {
	k, err := Generate(Ed25519, "笔记本", "me@laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	if k.Type != ssh.KeyAlgoED25519 || k.Label() != "ED25519" || !strings.HasSuffix(k.PublicKey, " me@laptop") ||
		!strings.HasPrefix(k.Fingerprint, "SHA256:") {
		t.Fatalf("k=%+v", k)
	}
	again, err := Parse("x", []byte(k.PrivateKey), "")
	if err != nil || again.Fingerprint != k.Fingerprint {
		t.Fatalf("重新解析应得到同一把钥匙: %v %v", again.Fingerprint, err)
	}
}

func TestEncryptedKeyNeedsPassphrase(t *testing.T) {
	k, err := Generate(Ed25519, "加密", "", "kp")
	if err != nil {
		t.Fatal(err)
	}
	if !Encrypted([]byte(k.PrivateKey)) {
		t.Fatal("生成时给了口令，私钥应是加密的")
	}
	if _, err := Parse("x", []byte(k.PrivateKey), ""); !errors.Is(err, ErrPassphraseNeeded) {
		t.Fatalf("err=%v", err)
	}
	if _, err := Parse("x", []byte(k.PrivateKey), "wrong"); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("err=%v", err)
	}
	got, err := Parse("x", []byte(k.PrivateKey), "kp")
	if err != nil || got.Fingerprint != k.Fingerprint || got.Passphrase != "kp" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestParseRejectsPublicKeyAndGarbage(t *testing.T) {
	k, _ := Generate(Ed25519, "", "", "")
	if _, err := Parse("x", []byte(k.PublicKey), ""); !errors.Is(err, ErrNotPrivateKey) {
		t.Fatalf("粘贴公钥应提示选错了文件: %v", err)
	}
	if _, err := Parse("x", []byte("hello"), ""); !errors.Is(err, ErrNotPrivateKey) {
		t.Fatalf("err=%v", err)
	}
	if _, err := Parse("x", []byte("  "), ""); err == nil {
		t.Fatal("空内容应报错")
	}
}

func TestRSAAndECDSALabels(t *testing.T) {
	if testing.Short() {
		t.Skip("生成 RSA 4096 较慢")
	}
	k, err := Generate(RSA4096, "", "", "")
	if err != nil || k.Label() != "RSA 4096" {
		t.Fatalf("label=%q err=%v", k.Label(), err)
	}
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	e, err := Parse("e", pem.EncodeToMemory(block), "")
	if err != nil || e.Label() != "ECDSA 256" {
		t.Fatalf("label=%q err=%v", e.Label(), err)
	}
}
