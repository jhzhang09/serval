package sshconn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"serval/internal/devsshd"

	"golang.org/x/crypto/ssh/agent"
)

const agentForwardMarker = "serval-forward-marker"

// 没有本机 agent 时，开启转发的连接直接失败，不连上之后再悄悄忽略。
func TestForwardAgentRequiresLocalAgent(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	server := startServer(t, devsshd.Options{})
	cfg := dialConfig(server, acceptAll(t))
	cfg.ForwardAgent = true
	_, err := Dial(context.Background(), cfg)
	if !errors.Is(err, ErrAgentUnavailable) {
		t.Fatalf("应返回 ErrAgentUnavailable，得到 %v", err)
	}
}

// 经跳板机连上目标后，目标机上的 ssh-add -l 能看到本机 agent 里的钥匙。
// 不转发时同一条命令联系不上 agent，证明结果不是开发服务器继承了测试进程的套接字。
func TestAgentForwardingVisibleThroughJump(t *testing.T) {
	requireDevShell(t)
	if _, err := exec.LookPath("ssh-add"); err != nil {
		t.Skip("本机没有 ssh-add，跳过转发互通测试")
	}
	t.Setenv("SSH_AUTH_SOCK", serveTestAgent(t))

	jump := startServer(t, devsshd.Options{})
	target := startServer(t, devsshd.Options{})
	policy := acceptAll(t)

	cfg := hopConfig(target, testPassword, policy)
	cfg.ForwardAgent = true
	cfg.Jumps = []Config{hopConfig(jump, testPassword, policy)}
	client := mustDial(t, cfg)
	shell, err := client.OpenShell(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	out := collect(shell)
	fmt.Fprintf(shell, "ssh-add -l; echo SERVAL_AGENT_RC:$?\n")
	out.waitFor(t, agentForwardMarker)
	out.waitFor(t, "SERVAL_AGENT_RC:0")

	plain := mustDial(t, hopConfig(target, testPassword, policy))
	shell2, err := plain.OpenShell(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer shell2.Close()
	out2 := collect(shell2)
	fmt.Fprintf(shell2, "ssh-add -l; echo SERVAL_AGENT_RC:$?\n")
	out2.waitFor(t, "SERVAL_AGENT_RC:2")
}

// serveTestAgent 在短路径上起一个只含一把测试钥匙的 ssh-agent。
func serveTestAgent(t *testing.T) string {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: private, Comment: agentForwardMarker}); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "sa-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				agent.ServeAgent(keyring, conn)
				conn.Close()
			}()
		}
	}()
	return sock
}
