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
	requireDevShell(t)
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
	requireDevShell(t)
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
	requireDevShell(t)
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
	waitProcessGone(t, pid)
}

// 一条连接上要能同时开多个终端和 SFTP，互不影响。
func TestConcurrentShellsAndFiles(t *testing.T) {
	requireDevShell(t)
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

// Exited 区分“远端 shell 自己退出”和“连接中断”。
func TestShellExitedDistinguishesExitFromDisconnect(t *testing.T) {
	requireDevShell(t)
	server := startServer(t, devsshd.Options{Password: testPassword})
	client := mustDial(t, dialConfig(server, acceptAll(t)))

	shell, err := client.OpenShell(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, shell)
	fmt.Fprintf(shell, "exit 3\n")
	if !shell.Exited(5 * time.Second) {
		t.Fatal("输入 exit 后应判定为远端自己退出（退出码非 0 也算）")
	}

	other, err := client.OpenShell(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, other)
	server.Close() // 服务器消失：没有退出状态
	if other.Exited(5 * time.Second) {
		t.Fatal("连接中断不应判定为远端自己退出")
	}
}

func TestStreamingTransferMemory(t *testing.T) {
	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))
	files, err := client.OpenFiles()
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()

	dir := t.TempDir()
	largeFile := filepath.Join(dir, "large.dat")
	f, err := os.Create(largeFile)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1024*1024)
	for i := 0; i < 100; i++ {
		if _, err := f.Write(chunk); err != nil {
			f.Close()
			t.Fatal(err)
		}
	}
	f.Close()

	remoteFile := filepath.Join(t.TempDir(), "remote.dat")
	downloadedFile := filepath.Join(dir, "downloaded.dat")

	var uploadProgress int64
	err = files.Upload(context.Background(), largeFile, remoteFile, func(transferred, total int64) {
		uploadProgress = transferred
	})
	if err != nil {
		t.Fatalf("Upload failed: %v", err)
	}
	if uploadProgress != 100*1024*1024 {
		t.Fatalf("Upload progress = %d, want 100 MB", uploadProgress)
	}

	var downloadProgress int64
	err = files.Download(context.Background(), remoteFile, downloadedFile, func(transferred, total int64) {
		downloadProgress = transferred
	})
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}
	if downloadProgress != 100*1024*1024 {
		t.Fatalf("Download progress = %d, want 100 MB", downloadProgress)
	}

	info, err := os.Stat(downloadedFile)
	if err != nil || info.Size() != 100*1024*1024 {
		t.Fatalf("下载的文件尺寸不匹配: %v", err)
	}
}

// 取消上传或下载后，本地和服务器上都不留下写了一半的文件；已有的同名文件保持原样。
func TestCancelledTransferLeavesNoPartialFiles(t *testing.T) {
	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))
	files, err := client.OpenFiles()
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()

	local := t.TempDir()
	remote := t.TempDir() // 开发服务器的 SFTP 直接操作本机文件系统
	big := filepath.Join(local, "big.dat")
	if err := os.WriteFile(big, make([]byte, 8<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(remote, "big.dat")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	cancelAfterFirstChunk := func() (context.Context, func(int64, int64)) {
		ctx, cancel := context.WithCancel(context.Background())
		return ctx, func(int64, int64) { cancel() }
	}
	ctx, progress := cancelAfterFirstChunk()
	if err := files.Upload(ctx, big, existing, progress); !errors.Is(err, context.Canceled) {
		t.Fatalf("上传应被取消: %v", err)
	}
	if data, _ := os.ReadFile(existing); string(data) != "old" {
		t.Fatal("取消上传不应改动服务器上已有的同名文件")
	}
	target := filepath.Join(local, "copy.dat")
	if err := os.WriteFile(filepath.Join(remote, "big2.dat"), make([]byte, 8<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, progress = cancelAfterFirstChunk()
	if err := files.Download(ctx, filepath.Join(remote, "big2.dat"), target, progress); !errors.Is(err, context.Canceled) {
		t.Fatalf("下载应被取消: %v", err)
	}
	for _, dir := range []string{local, remote} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.Contains(e.Name(), "part") || e.Name() == "copy.dat" {
				t.Errorf("%s 里留下了残留文件 %s", dir, e.Name())
			}
		}
	}
	if ok, err := files.Exists(existing); !ok || err != nil {
		t.Fatalf("Exists(已有文件) = %v %v", ok, err)
	}
	if ok, err := files.Exists(filepath.Join(remote, "none")); ok || err != nil {
		t.Fatalf("Exists(不存在) = %v %v", ok, err)
	}
}

func TestChallengeMultiQuestions(t *testing.T) {
	server := startServer(t, devsshd.Options{
		KeyboardInteractive: true,
		KeyboardQuestions:   []string{"OTP: ", "PIN: "},
		KeyboardAnswers:     []string{"123456", "9999"},
	})
	cfg := dialConfig(server, acceptAll(t))
	cfg.Password = "not-used" // 服务器只接受 keyboard-interactive，密码方式会被拒绝
	called := false
	cfg.Challenge = func(name, instruction string, questions []Question) ([]string, error) {
		called = true
		if len(questions) != 2 {
			t.Fatalf("questions count = %d, want 2", len(questions))
		}
		if questions[0].Text != "OTP: " || questions[1].Text != "PIN: " {
			t.Fatalf("unexpected questions: %+v", questions)
		}
		return []string{"123456", "9999"}, nil
	}

	client, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Dial with Challenge failed: %v", err)
	}
	defer client.Close()
	if !called {
		t.Fatal("Challenge callback was not called")
	}
}

func TestChallengeCancelled(t *testing.T) {
	server := startServer(t, devsshd.Options{
		KeyboardInteractive: true,
		KeyboardQuestions:   []string{"OTP: "},
		KeyboardAnswers:     []string{"123456"},
	})
	cfg := dialConfig(server, acceptAll(t))
	cfg.Password = "not-used" // 服务器只接受 keyboard-interactive，密码方式会被拒绝
	userCancelled := errors.New("user cancelled")
	cfg.Challenge = func(name, instruction string, questions []Question) ([]string, error) {
		return nil, userCancelled
	}

	_, err := Dial(context.Background(), cfg)
	if err == nil {
		t.Fatal("Dial should fail when user cancels challenge")
	}
	if !strings.Contains(err.Error(), "user cancelled") && !errors.Is(err, userCancelled) {
		t.Fatalf("expected user cancelled error, got: %v", err)
	}
}

// 等用户回答验证码期间握手超时暂停：回答比 Timeout 慢也能连上。
func TestChallengeWaitDoesNotTimeOut(t *testing.T) {
	server := startServer(t, devsshd.Options{
		KeyboardInteractive: true,
		KeyboardQuestions:   []string{"OTP: "},
		KeyboardAnswers:     []string{"123456"},
	})
	cfg := dialConfig(server, acceptAll(t))
	cfg.Password = "not-used"
	cfg.Timeout = 200 * time.Millisecond
	cfg.Challenge = func(name, instruction string, questions []Question) ([]string, error) {
		time.Sleep(600 * time.Millisecond) // 用户掏手机看验证码
		return []string{"123456"}, nil
	}
	client, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatalf("回答验证码比超时慢也应能连上: %v", err)
	}
	client.Close()
}

// 基于 PAM 的服务器用 keyboard-interactive 要密码：保存的密码自动填写，不打扰用户。
func TestKeyboardInteractivePasswordAutoAnswered(t *testing.T) {
	server := startServer(t, devsshd.Options{
		KeyboardInteractive: true,
		KeyboardQuestions:   []string{"Password: "},
		KeyboardAnswers:     []string{testPassword},
	})
	cfg := dialConfig(server, acceptAll(t))
	cfg.Challenge = func(string, string, []Question) ([]string, error) {
		t.Error("密码问题应自动回答，不应交给用户")
		return nil, errors.New("不应调用")
	}
	client, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()

	// 密码不对：直接认证失败（界面会提示密码不正确），不把密码问题当验证码弹给用户。
	cfg.Password = "wrong"
	if _, err := Dial(context.Background(), cfg); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("密码错误应返回 ErrAuthFailed，得到 %v", err)
	}
}

// 只问验证码的服务器：即使保存了密码，也不能拿密码去填验证码。
func TestOTPPromptIsNotAnsweredWithPassword(t *testing.T) {
	server := startServer(t, devsshd.Options{
		KeyboardInteractive: true,
		KeyboardQuestions:   []string{"Verification code: "},
		KeyboardAnswers:     []string{"654321"},
		Password:            "server-side-only", // 让密码方式失败，迫使走 keyboard-interactive
	})
	cfg := dialConfig(server, acceptAll(t))
	asked := false
	cfg.Challenge = func(_, _ string, qs []Question) ([]string, error) {
		asked = true
		return []string{"654321"}, nil
	}
	client, err := Dial(context.Background(), cfg)
	if err != nil || !asked {
		t.Fatalf("验证码应交给用户回答: asked=%v err=%v", asked, err)
	}
	client.Close()
}

func TestExecContext(t *testing.T) {
	requireDevShell(t)
	server := startServer(t, devsshd.Options{Dir: t.TempDir()})
	client := mustDial(t, dialConfig(server, acceptAll(t)))

	// 1. 正常命令输出与退出码 0
	res, err := client.ExecContext(context.Background(), "printf 'out-ok'; printf 'err-ok' >&2", 4096)
	if err != nil {
		t.Fatalf("ExecContext failed: %v", err)
	}
	if res.ExitCode != 0 || res.Stdout != "out-ok" || res.Stderr != "err-ok" || res.Truncated {
		t.Fatalf("unexpected result: %+v", res)
	}

	// 2. 非零退出码不视为传输失败，而是如实返回 ExitCode
	res, err = client.ExecContext(context.Background(), "echo bad >&2; exit 42", 4096)
	if err != nil {
		t.Fatalf("non-zero exit should not return error: %v", err)
	}
	if res.ExitCode != 42 || !strings.Contains(res.Stderr, "bad") {
		t.Fatalf("unexpected non-zero result: %+v", res)
	}

	// 3. 输出截断
	res, err = client.ExecContext(context.Background(), "printf '0123456789abcdef'", 8)
	if err != nil {
		t.Fatalf("truncated exec failed: %v", err)
	}
	if !res.Truncated || res.Stdout != "01234567" {
		t.Fatalf("expected truncated 8 bytes, got %+v", res)
	}

	// 4. 超时取消
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err = client.ExecContext(ctx, "sleep 5", 4096)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

func TestExecContextCrossPlatform(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	srvCfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) == testPassword {
				return nil, nil
			}
			return nil, errors.New("bad password")
		},
	}
	srvCfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan struct{})
	var wg sync.WaitGroup
	// t.Cleanup 按 LIFO 执行：先关闭连接和监听，最后等待 goroutine 退出。
	t.Cleanup(wg.Wait)
	t.Cleanup(func() {
		close(serverDone)
		_ = ln.Close()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			rawConn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				sconn, chans, reqs, err := ssh.NewServerConn(c, srvCfg)
				if err != nil {
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for newCh := range chans {
					if newCh.ChannelType() != "session" {
						_ = newCh.Reject(ssh.UnknownChannelType, "unknown")
						continue
					}
					ch, reqs, err := newCh.Accept()
					if err != nil {
						continue
					}
					wg.Add(1)
					go func(ch ssh.Channel, reqs <-chan *ssh.Request) {
						defer wg.Done()
						defer ch.Close()
						for req := range reqs {
							if req.Type != "exec" {
								if req.WantReply {
									_ = req.Reply(false, nil)
								}
								continue
							}
							var payload struct{ Command string }
							if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
								_ = req.Reply(false, nil)
								return
							}
							_ = req.Reply(true, nil)
							switch payload.Command {
							case "ok":
								_, _ = io.WriteString(ch, "out-ok")
								_, _ = io.WriteString(ch.Stderr(), "err-ok")
								_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
								_ = ch.CloseWrite()
								return
							case "fail":
								_, _ = io.WriteString(ch.Stderr(), "bad\n")
								_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{42}))
								_ = ch.CloseWrite()
								return
							case "truncate":
								_, _ = io.WriteString(ch, "0123456789abcdef")
								_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
								_ = ch.CloseWrite()
								return
							case "hang":
								// 模拟长时间执行直到收到 signal、通道关闭或测试结束。
								for {
									select {
									case r, ok := <-reqs:
										if !ok || r.Type == "signal" {
											return
										}
										if r.WantReply {
											_ = r.Reply(false, nil)
										}
									case <-serverDone:
										return
									}
								}
							default:
								_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{1}))
								_ = ch.CloseWrite()
								return
							}
						}
					}(ch, reqs)
				}
			}(rawConn)
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	client, err := Dial(context.Background(), Config{
		Host:     "127.0.0.1",
		Port:     addr.Port,
		User:     "tester",
		Password: testPassword,
		HostKeys: acceptAll(t),
		Timeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	// 1. stdout/stderr 分离与退出码 0
	res, err := client.ExecContext(context.Background(), "ok", 4096)
	if err != nil {
		t.Fatalf("ExecContext(ok) failed: %v", err)
	}
	if res.ExitCode != 0 || res.Stdout != "out-ok" || res.Stderr != "err-ok" || res.Truncated {
		t.Fatalf("unexpected ok result: %+v", res)
	}

	// 2. 非零退出码不作为错误返回
	res, err = client.ExecContext(context.Background(), "fail", 4096)
	if err != nil {
		t.Fatalf("ExecContext(fail) should not error: %v", err)
	}
	if res.ExitCode != 42 || !strings.Contains(res.Stderr, "bad") {
		t.Fatalf("unexpected fail result: %+v", res)
	}

	// 3. 输出截断
	res, err = client.ExecContext(context.Background(), "truncate", 8)
	if err != nil {
		t.Fatalf("ExecContext(truncate) failed: %v", err)
	}
	if !res.Truncated || res.Stdout != "01234567" {
		t.Fatalf("unexpected truncated result: %+v", res)
	}

	// 4. 超时取消
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err = client.ExecContext(ctx, "hang", 4096)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}

	// 5. 空客户端与已取消上下文边界
	if _, err := (*Client)(nil).ExecContext(context.Background(), "ok", 4096); err == nil {
		t.Fatal("expected error for nil Client")
	}
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := client.ExecContext(canceled, "ok", 4096); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
