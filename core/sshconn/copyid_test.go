package sshconn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"serval/internal/devsshd"

	"golang.org/x/crypto/ssh"
)

func testPubKey(t *testing.T, comment string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if comment != "" {
		line += " " + comment
	}
	return line
}

func TestDeployPublicKey(t *testing.T) {
	requireDevShell(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(srv, acceptAll(t)))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. 无效公钥应拒绝
	if _, err := DeployPublicKey(ctx, client, "not-a-valid-key\nrm -rf /"); !errors.Is(err, ErrInvalidPublicKey) {
		t.Fatalf("expected ErrInvalidPublicKey, got %v", err)
	}

	// 2. 首次部署：应创建 ~/.ssh(0700) 和 authorized_keys(0600)
	key1 := testPubKey(t, "alice@workstation")
	res, err := DeployPublicKey(ctx, client, key1)
	if err != nil {
		t.Fatalf("first deploy failed: %v", err)
	}
	if res.AlreadyPresent {
		t.Fatal("first deploy should not report AlreadyPresent")
	}

	sshDir := filepath.Join(home, ".ssh")
	authPath := filepath.Join(sshDir, "authorized_keys")
	dirInfo, err := os.Stat(sshDir)
	if err != nil {
		t.Fatalf("stat .ssh: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf(".ssh perm = %o, want 0700", dirInfo.Mode().Perm())
	}
	fileInfo, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("stat authorized_keys: %v", err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("authorized_keys perm = %o, want 0600", fileInfo.Mode().Perm())
	}

	// 3. 重复部署同一把公钥：幂等，不重复追加
	res2, err := DeployPublicKey(ctx, client, key1)
	if err != nil {
		t.Fatalf("second deploy failed: %v", err)
	}
	if !res2.AlreadyPresent {
		t.Fatal("second deploy should report AlreadyPresent=true")
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 || lines[0] != key1 {
		t.Fatalf("unexpected authorized_keys content: %q", string(data))
	}

	// 4. 追加第二把带特殊单引号注释的公钥，且原文件末尾不带换行符
	if err := os.WriteFile(authPath, []byte(key1), 0o600); err != nil {
		t.Fatal(err)
	}
	key2 := testPubKey(t, "bob's-laptop")
	res3, err := DeployPublicKey(ctx, client, key2)
	if err != nil {
		t.Fatalf("third deploy failed: %v", err)
	}
	if res3.AlreadyPresent {
		t.Fatal("new key should not report AlreadyPresent")
	}
	data, err = os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	lines = strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || lines[0] != key1 || lines[1] != key2 {
		t.Fatalf("unexpected authorized_keys lines: %q", lines)
	}
}
