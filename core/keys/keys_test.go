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
	if again.PublicKey != k.PublicKey {
		t.Fatalf("导入时应保留公钥注释: got=%q want=%q", again.PublicKey, k.PublicKey)
	}
}

func TestParsePreservesComment(t *testing.T) {
	// 带口令的 Ed25519 私钥
	enc, err := Generate(Ed25519, "工作", "alice@work", "kp")
	if err != nil {
		t.Fatal(err)
	}
	gotEnc, err := Parse("工作", []byte(enc.PrivateKey), "kp")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotEnc.PublicKey, " alice@work") {
		t.Fatalf("加密私钥导入后未保留注释: %q", gotEnc.PublicKey)
	}

	// ECDSA 带注释，以及无注释时不附加多余空格
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	withComment, _ := ssh.MarshalPrivateKey(priv, "ec@host")
	gotEC, err := Parse("ec", pem.EncodeToMemory(withComment), "")
	if err != nil || !strings.HasSuffix(gotEC.PublicKey, " ec@host") {
		t.Fatalf("ECDSA 注释未保留: got=%q err=%v", gotEC.PublicKey, err)
	}

	withoutComment, _ := ssh.MarshalPrivateKey(priv, "")
	gotNoComment, err := Parse("ec-plain", pem.EncodeToMemory(withoutComment), "")
	if err != nil || strings.Contains(gotNoComment.PublicKey, "\n") || strings.HasSuffix(gotNoComment.PublicKey, " ") {
		t.Fatalf("无注释公钥格式异常: got=%q err=%v", gotNoComment.PublicKey, err)
	}

	// 注释里的换行/控制字符应被清理，保持 authorized_keys 单行格式
	dirty, _ := ssh.MarshalPrivateKey(priv, "user@host\ninjected")
	gotDirty, err := Parse("dirty", pem.EncodeToMemory(dirty), "")
	if err != nil || strings.ContainsAny(gotDirty.PublicKey, "\r\n") || !strings.HasSuffix(gotDirty.PublicKey, " user@hostinjected") {
		t.Fatalf("控制字符未清理: got=%q err=%v", gotDirty.PublicKey, err)
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
	k, err := Generate(RSA4096, "", "rsa@host", "")
	if err != nil || k.Label() != "RSA 4096" {
		t.Fatalf("label=%q err=%v", k.Label(), err)
	}
	parsedRSA, err := Parse("rsa", []byte(k.PrivateKey), "")
	if err != nil || !strings.HasSuffix(parsedRSA.PublicKey, " rsa@host") {
		t.Fatalf("RSA 注释未保留: got=%q err=%v", parsedRSA.PublicKey, err)
	}
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	e, err := Parse("e", pem.EncodeToMemory(block), "")
	if err != nil || e.Label() != "ECDSA 256" {
		t.Fatalf("label=%q err=%v", e.Label(), err)
	}
}
