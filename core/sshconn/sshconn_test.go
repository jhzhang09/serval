package sshconn

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"serval/internal/devsshd"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const testPassword = "test-only-password"

// acceptAll 返回一个信任所有未知主机的策略，用于不关注主机密钥的用例。
func acceptAll(t *testing.T) *HostKeyPolicy {
	t.Helper()
	return &HostKeyPolicy{
		TrustStore: filepath.Join(t.TempDir(), "known_hosts"),
		Confirm:    func(HostKeyInfo) bool { return true },
	}
}

func startServer(t *testing.T, opts devsshd.Options) *devsshd.Server {
	t.Helper()
	if opts.Password == "" && opts.AuthorizedKey == nil {
		opts.Password = testPassword
	}
	server, err := devsshd.Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	return server
}

func dialConfig(server *devsshd.Server, policy *HostKeyPolicy) Config {
	return Config{
		Host:     "127.0.0.1",
		Port:     server.Addr().Port,
		User:     "tester",
		Password: testPassword,
		HostKeys: policy,
		Timeout:  5 * time.Second,
	}
}

func mustDial(t *testing.T, cfg Config) *Client {
	t.Helper()
	client, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestPasswordAuth(t *testing.T) {
	server := startServer(t, devsshd.Options{})
	mustDial(t, dialConfig(server, acceptAll(t)))

	cfg := dialConfig(server, acceptAll(t))
	cfg.Password = "wrong"
	if _, err := Dial(context.Background(), cfg); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("错误密码应返回 ErrAuthFailed，得到 %v", err)
	}

	cfg.Password = ""
	if _, err := Dial(context.Background(), cfg); !errors.Is(err, ErrNoAuthMethod) {
		t.Fatalf("没有认证方式应返回 ErrNoAuthMethod，得到 %v", err)
	}
}

func TestPing(t *testing.T) {
	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))
	rtt, err := client.Ping(context.Background())
	if err != nil || rtt <= 0 {
		t.Fatalf("rtt=%v err=%v", rtt, err)
	}
	client.Close()
	if _, err := client.Ping(context.Background()); err == nil {
		t.Fatal("连接关闭后 Ping 应返回错误")
	}
}

func TestKeyAuth(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPublic, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	server := startServer(t, devsshd.Options{AuthorizedKey: sshPublic})

	writeKey := func(passphrase string) string {
		var block *pem.Block
		var err error
		if passphrase == "" {
			block, err = ssh.MarshalPrivateKey(private, "")
		} else {
			block, err = ssh.MarshalPrivateKeyWithPassphrase(private, "", []byte(passphrase))
		}
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "id_ed25519")
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	cfg := dialConfig(server, acceptAll(t))
	cfg.Password = ""
	cfg.KeyPath = writeKey("")
	mustDial(t, cfg)

	cfg.KeyPath = writeKey("secret")
	if _, err := Dial(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "口令") {
		t.Fatalf("未提供口令时应提示输入口令，得到 %v", err)
	}
	cfg.KeyPassphrase = "wrong"
	if _, err := Dial(context.Background(), cfg); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("口令错误应返回 ErrBadPassphrase，得到 %v", err)
	}
	cfg.KeyPassphrase = "secret"
	mustDial(t, cfg)

	// 私钥内容直接来自保险库时不需要文件。
	data, err := os.ReadFile(cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.KeyPath, cfg.KeyPEM = "", data
	mustDial(t, cfg)
}

func TestUnknownHostKey(t *testing.T) {
	server := startServer(t, devsshd.Options{})
	store := filepath.Join(t.TempDir(), "sub", "known_hosts")

	// 用户拒绝：不能连接，也不能写入信任文件。
	var asked HostKeyInfo
	_, err := Dial(context.Background(), dialConfig(server, &HostKeyPolicy{
		TrustStore: store,
		Confirm:    func(info HostKeyInfo) bool { asked = info; return false },
	}))
	if !errors.Is(err, ErrHostKeyRejected) {
		t.Fatalf("应返回 ErrHostKeyRejected，得到 %v", err)
	}
	if !strings.HasPrefix(asked.Fingerprint, "SHA256:") {
		t.Fatalf("确认信息缺少指纹: %+v", asked)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("拒绝后不应写入信任文件: %v", err)
	}

	// 没有确认回调：默认拒绝。
	if _, err := Dial(context.Background(), dialConfig(server, &HostKeyPolicy{TrustStore: store})); !errors.Is(err, ErrHostKeyRejected) {
		t.Fatalf("没有确认回调时应拒绝，得到 %v", err)
	}

	// 用户接受一次后，再次连接不再询问。
	mustDial(t, dialConfig(server, &HostKeyPolicy{TrustStore: store, Confirm: func(HostKeyInfo) bool { return true }}))
	mustDial(t, dialConfig(server, &HostKeyPolicy{TrustStore: store, Confirm: func(HostKeyInfo) bool {
		t.Error("已信任的主机不应再次询问")
		return false
	}}))
}

func TestChangedHostKeyIsRejected(t *testing.T) {
	first, err := devsshd.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	server := startServer(t, devsshd.Options{HostKeys: []ssh.Signer{first}})
	store := filepath.Join(t.TempDir(), "known_hosts")
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(server.Addr().Port))
	other, err := devsshd.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(addr)}, other.PublicKey())
	if err := os.WriteFile(store, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = Dial(context.Background(), dialConfig(server, &HostKeyPolicy{
		TrustStore: store,
		Confirm: func(HostKeyInfo) bool {
			t.Error("密钥变化时不应询问用户")
			return true
		},
	}))
	var changed *HostKeyChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("应返回 HostKeyChangedError，得到 %v", err)
	}
}

// 服务器同时有 ed25519 和 ECDSA 密钥而本地只记录了 ECDSA 时，
// 必须协商已记录的算法，不能误判为密钥变化。
func TestNegotiatesKnownAlgorithm(t *testing.T) {
	ed, err := devsshd.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ec, err := ssh.NewSignerFromKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	server := startServer(t, devsshd.Options{HostKeys: []ssh.Signer{ed, ec}})
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(server.Addr().Port))
	readOnly := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{knownhosts.Normalize(addr)}, ec.PublicKey())
	if err := os.WriteFile(readOnly, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(readOnly)
	if err != nil {
		t.Fatal(err)
	}

	mustDial(t, dialConfig(server, &HostKeyPolicy{ReadOnlyFiles: []string{readOnly}}))

	after, err := os.ReadFile(readOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("只读 known_hosts 不应被修改")
	}
}

func TestCancelWhileConfirmingHostKey(t *testing.T) {
	server := startServer(t, devsshd.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	defer close(release)

	result := make(chan error, 1)
	go func() {
		_, err := Dial(ctx, dialConfig(server, &HostKeyPolicy{Confirm: func(HostKeyInfo) bool {
			cancel()
			<-release
			return true
		}}))
		result <- err
	}()
	// Confirm 阻塞在握手内部，真实界面会在取消时同时让它返回。
	time.Sleep(50 * time.Millisecond)
	release <- struct{}{}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("取消后不应连接成功")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 Dial 未返回")
	}
}

func TestConnectionRefused(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	_, err = Dial(context.Background(), Config{
		Host: "127.0.0.1", Port: port, User: "u", Password: "p", HostKeys: acceptAll(t), Timeout: time.Second,
	})
	if err == nil {
		t.Fatal("连接已关闭的端口应失败")
	}
}

// reader 持续收集 Shell 输出，供用例等待特定内容。
type reader struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func collect(r io.Reader) *reader {
	out := &reader{}
	go func() {
		chunk := make([]byte, 32*1024)
		for {
			n, err := r.Read(chunk)
			out.mu.Lock()
			out.buf.Write(chunk[:n])
			out.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return out
}

func (r *reader) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		found := strings.Contains(r.buf.String(), want)
		r.mu.Unlock()
		if found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("等待 %q 超时，当前输出: %q", want, r.buf.String())
}

func (r *reader) waitForPattern(t *testing.T, pattern string) {
	t.Helper()
	re := regexp.MustCompile(pattern)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		found := re.MatchString(r.buf.String())
		r.mu.Unlock()
		if found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待模式 %q 超时", pattern)
}

func TestShellRoundTripAndResize(t *testing.T) {
	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))
	shell, err := client.OpenShell(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	out := collect(shell)

	// 用算术结果判断，避免把回显的命令文本误认为输出。
	fmt.Fprintf(shell, "echo marker-$((40+2)); stty size\n")
	out.waitFor(t, "marker-42")
	out.waitFor(t, "24 80")

	if err := shell.Resize(132, 43); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(shell, "stty size\n")
	out.waitFor(t, "43 132")
}

func TestShellEOFOnExitAndDoubleClose(t *testing.T) {
	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))
	shell, err := client.OpenShell(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(shell, "exit 3\n")

	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, shell)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("远端退出应以 EOF 结束读取，得到 %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("远端退出后读取未结束")
	}
	if err := shell.Close(); err != nil {
		t.Fatalf("远端已退出时 Close 应返回 nil，得到 %v", err)
	}
	if err := shell.Close(); err != nil {
		t.Fatalf("重复 Close 应返回 nil，得到 %v", err)
	}
}

// 回归：关闭会话后远端 shell 必须结束，开发服务器不能留下孤儿进程。
func TestClosingShellEndsRemoteProcess(t *testing.T) {
	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))
	shell, err := client.OpenShell(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	out := collect(shell)
	fmt.Fprintf(shell, "echo pid-$$-end\n")
	out.waitFor(t, "-end")
	// 回显的命令里是字面量 $$，真实输出才是数字。
	match := regexp.MustCompile(`pid-(\d+)-end`).FindStringSubmatch(func() string {
		out.waitForPattern(t, `pid-\d+-end`)
		out.mu.Lock()
		defer out.mu.Unlock()
		return out.buf.String()
	}())
	pid, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatal(err)
	}

	if err := shell.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return // 进程已退出
		}
		time.Sleep(20 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("关闭会话后 shell 进程 %d 仍在运行", pid)
}

// 一条连接上要能同时开多个终端和 SFTP，互不影响。
func TestConcurrentShellsAndFiles(t *testing.T) {
	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))
	files, err := client.OpenFiles()
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()

	var wg sync.WaitGroup
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			shell, err := client.OpenShell(80, 24)
			if err != nil {
				t.Error(err)
				return
			}
			defer shell.Close()
			out := collect(shell)
			fmt.Fprintf(shell, "echo shell-$((%d+100))\n", i)
			out.waitFor(t, fmt.Sprintf("shell-%d", i+100))
			if _, err := files.List("/"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestListDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.txt", "A.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("hello"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"zdir", "Mdir"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "zdir"), filepath.Join(dir, "link-to-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "broken")); err != nil {
		t.Fatal(err)
	}

	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))
	files, err := client.OpenFiles()
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()

	if home, err := files.Home(); err != nil || home == "" {
		t.Fatalf("Home() = %q, %v", home, err)
	}
	entries, err := files.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	want := "link-to-dir,Mdir,zdir,A.txt,b.txt,broken"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("排序错误\n得到 %s\n期望 %s", got, want)
	}
	if !entries[0].IsDir {
		t.Fatal("指向目录的符号链接应视为目录")
	}
	for _, e := range entries {
		if e.Name == "A.txt" && e.Size != 5 {
			t.Fatalf("A.txt 大小 = %d", e.Size)
		}
	}
	if _, err := files.List(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("读取不存在的目录应失败")
	}
}

func TestDevServerRefusesNonLoopback(t *testing.T) {
	if _, err := devsshd.Start(devsshd.Options{Addr: "0.0.0.0:0", Password: "x"}); err == nil {
		t.Fatal("开发服务器不应监听非回环地址")
	}
}
