package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"serval/core/hosts"
	"serval/core/vault"
	"serval/design"
	"serval/internal/devsshd"
	"serval/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
)

const (
	testPassword = "test-only-password"
	// masterPassword 是测试保险库的主密码。
	masterPassword = "test master password"
)

func init() {
	// 测试里用很低的派生成本，正式参数见 vault.DefaultKDF。
	vault.DefaultKDF = vault.KDF{Alg: "argon2id", Time: 1, MemoryKiB: 1024, Threads: 1}
	// 界面测试按简体中文文案点击。正式运行跟随系统语言；这里钉住目录语言，
	// 避免英文系统上 App.Locale 选到 en 后把现有点击测试全部点空。
	// 只影响测试二进制（main_test.go 不会编进应用）。
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
}

// harness 让视图在没有窗口的情况下跑完真实的 SSH 连接流程。
// post 把后台结果放入队列，由 pump 在测试线程（相当于主线程）里执行。
type harness struct {
	t      *testing.T
	a      *app
	tt     *ui.Tester
	mu     sync.Mutex
	queue  []func()
	server *devsshd.Server
	device *memDevice
	dir    string
}

// memDevice 是测试用的系统安全存储。
type memDevice struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memDevice) Get(key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return v, nil
}

func (s *memDevice) Set(key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = value
	return nil
}

func (s *memDevice) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}

// newHarness 启动开发服务器，保险库里放一台指向它的主机 dev，以及 extra 里的主机。
// 保险库已经设置好主密码，和日常使用时一样；要测第一次设置主密码，用 newBareHarness。
func newHarness(t *testing.T, extra ...hosts.Host) *harness {
	t.Helper()
	h := newBareHarness(t, extra...)
	if err := h.a.vault.Protect(masterPassword); err != nil {
		t.Fatal(err)
	}
	return h
}

// newBareHarness 和 newHarness 一样，只是保险库还没有设置主密码。
func newBareHarness(t *testing.T, extra ...hosts.Host) *harness {
	t.Helper()
	// 不让测试用到开发者自己的 ssh-agent。
	t.Setenv("SSH_AUTH_SOCK", "")
	design.RegisterFonts()
	serverDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(serverDir, "go.mod"), []byte("module serval\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, err := devsshd.Start(devsshd.Options{Password: testPassword, Dir: serverDir})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	h := &harness{t: t, a: newApp(), server: server, device: &memDevice{m: map[string][]byte{}}, dir: dir}
	h.a.devicePath = filepath.Join(dir, "device.json")
	h.a.device = h.device
	h.a.localUser = "tester"
	if h.a.vault, err = vault.Open(filepath.Join(dir, "vault.json"), "test"); err != nil {
		t.Fatal(err)
	}
	dev := hosts.Host{ID: "dev", Name: "dev", Address: "127.0.0.1", Port: server.Addr().Port, User: "tester", Group: defaultGroup}
	changes := []vault.Change{hostChange(dev)}
	for _, x := range extra {
		if x.Group == "" {
			x.Group = defaultGroup
		}
		changes = append(changes, hostChange(x))
	}
	if err := h.a.apply(changes...); err != nil {
		t.Fatal(err)
	}
	h.a.loadLocal()
	h.a.unlock.remember = true
	h.a.chooseKeyFile = func() (string, error) { return "", errors.New("测试没有设置 chooseKeyFile") }
	h.a.chooseSavePath = func(string) (string, error) { return "", errors.New("测试没有设置 chooseSavePath") }
	h.a.chooseUploadFile = func() (string, error) { return "", errors.New("测试没有设置 chooseUploadFile") }
	h.a.chooseUploadFiles = func() ([]string, error) { return nil, errors.New("测试没有设置 chooseUploadFiles") }
	h.a.chooseFolder = func() (string, error) { return "", errors.New("测试没有设置 chooseFolder") }
	h.a.post = func(fn func()) {
		h.mu.Lock()
		h.queue = append(h.queue, fn)
		h.mu.Unlock()
	}
	h.tt = ui.NewTester(h.a.view, 1180, 720)
	t.Cleanup(func() {
		h.a.closeAll()
		server.Close()
	})
	return h
}

// reopen 模拟重启：从磁盘重新打开保险库，像 main 一样先尝试本机记住的密钥。
func (h *harness) reopen() {
	h.t.Helper()
	h.a.closeAll()
	v, err := vault.Open(filepath.Join(h.dir, "vault.json"), "test")
	if err != nil {
		h.t.Fatal(err)
	}
	h.a.vault, h.a.hosts, h.a.keys, h.a.settings, h.a.local = v, nil, nil, settings{}, deviceSettings{}
	if !h.a.unlockWithRemembered() {
		h.a.reload()
	}
	h.a.loadLocal()
	if h.a.vault.State() != vault.Locked {
		h.a.restoreSession()
	}
	h.tt.Frame()
}

// onDisk 从磁盘重新读出保险库里的主机，确认修改确实写进了文件。
func (h *harness) onDisk() []hosts.Host {
	h.t.Helper()
	v, err := vault.Open(filepath.Join(h.dir, "vault.json"), "test")
	if err != nil {
		h.t.Fatal(err)
	}
	if v.State() == vault.Locked {
		if err := v.UnlockWithKey(h.a.vault.Key()); err != nil {
			h.t.Fatal(err)
		}
	}
	var list []hosts.Host
	if err := decodeAll(v, kindHost, &list); err != nil {
		h.t.Fatal(err)
	}
	return list
}

// host 按名称查找主机，找不到时返回零值。
func (h *harness) host(name string) hosts.Host {
	for _, x := range h.a.hosts {
		if x.Name == name {
			return x
		}
	}
	return hosts.Host{}
}

// pump 执行后台结果并重绘，直到 done 为真。
func (h *harness) pump(what string, done func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		queue := h.queue
		h.queue = nil
		h.mu.Unlock()
		for _, fn := range queue {
			fn()
		}
		h.tt.Frame()
		if done() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("等待%s超时，界面文字: %q，提示: %q", what, h.tt.Texts(), h.a.notice)
}

func (h *harness) click(label string) {
	h.t.Helper()
	if err := h.tt.Click(label); err != nil {
		h.t.Fatalf("点击 %q 失败: %v，界面文字: %q", label, err, h.tt.Texts())
	}
}

// connectDev 走完首次连接的完整流程：选择主机、输入密码、信任指纹。
//
// 没有私钥和 agent 时没有可尝试的认证方式，所以先问密码再握手，省掉一次注定失败的连接。
// 密码要等用户信任指纹、握手完成后才会发给服务器。
func (h *harness) connectDev() *tab {
	h.t.Helper()
	before := len(h.a.tabs)
	h.enterPassword(testPassword)
	h.pump("主机密钥询问", func() bool { return len(h.a.prompts) > 0 })
	h.click("信任并连接")
	h.pump("会话建立", func() bool { return len(h.a.tabs) == before+1 })
	return h.a.tabs[before]
}

func (h *harness) enterPassword(password string) {
	h.t.Helper()
	h.click("dev")
	h.pump("密码询问", func() bool { return h.a.auth.open })
	h.a.auth.secret = password
	h.tt.Frame()
	h.click("连接")
}

// TestEnglishCatalogRenders 确认英文目录真的画到界面上，并且简体中文原文没被改掉。
// 其他测试把语言钉在 zh-Hans，这里临时改成 en，结束时恢复。
func TestEnglishCatalogRenders(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	t.Cleanup(func() { i18n.SetLocaleFunc(func() string { return "zh-Hans" }) })
	if got := locales.In("zh-Hans").ViewConnectHost(); got != "连接一台主机" {
		t.Fatalf("简体中文被改成了 %q", got)
	}
	if got := locales.In("en").AddHost(); got != "Add host" {
		t.Fatalf("英文添加主机是 %q", got)
	}
	h := newBareHarness(t)
	for _, want := range []string{"Connect to a host", "Add host", "Local terminal", "Settings"} {
		if _, ok := h.tt.Find(want); !ok {
			t.Fatalf("英文界面没有 %q，实际：%v", want, h.tt.Texts())
		}
	}
	if _, ok := h.tt.Find("连接一台主机"); ok {
		t.Fatal("英文界面仍显示中文空状态")
	}
}

func TestSidebarListsVaultHosts(t *testing.T) {
	h := newHarness(t, hosts.Host{ID: "p", Name: "prod-db", Address: "10.0.0.9", Port: 22, User: "admin"})
	h.tt.Frame()
	for _, text := range []string{"dev", "tester@127.0.0.1:", "prod-db", "admin@10.0.0.9", defaultGroup, "连接一台主机"} {
		found := false
		for _, s := range h.tt.Texts() {
			if strings.Contains(s, text) {
				found = true
			}
		}
		if !found {
			t.Errorf("界面上没有 %q: %q", text, h.tt.Texts())
		}
	}
}

func TestConnectFlowAndPasswordIsNotKept(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()

	if h.a.auth.secret != "" {
		t.Error("连接后不应在内存里保留密码")
	}
	tb.focus.term.Send([]byte("echo roundtrip-$((40+2))\n"))
	h.pump("终端输出", func() bool { return strings.Contains(tb.focus.term.Text(), "roundtrip-42") })
	h.pump("延迟数据", func() bool { return tb.latency > 0 })
	h.tt.Frame()
	if !h.tt.HasText(formatLatency(tb.latency)) {
		t.Errorf("卡片标题行没有显示延迟: %q", h.tt.Texts())
	}

	// 再次点击同一台主机只切换过去，不重复连接。
	h.click("dev")
	h.tt.Frame()
	if len(h.a.tabs) != 1 || len(h.a.connecting) != 0 {
		t.Fatalf("重复点击不应新建连接，标签数 %d", len(h.a.tabs))
	}
}

func TestWrongPasswordAsksAgain(t *testing.T) {
	h := newHarness(t)
	h.enterPassword("wrong")
	h.pump("主机密钥询问", func() bool { return len(h.a.prompts) > 0 })
	h.click("信任并连接")
	h.pump("再次询问", func() bool { return h.a.auth.open && h.a.auth.message != "" })
	if !strings.Contains(h.a.auth.message, "密码不正确") {
		t.Errorf("提示 = %q", h.a.auth.message)
	}
	if h.a.auth.secret != "" {
		t.Error("重新询问时输入框应为空")
	}
}

func TestRejectingHostKeyExplainsWhy(t *testing.T) {
	h := newHarness(t)
	h.enterPassword(testPassword)
	h.pump("主机密钥询问", func() bool { return len(h.a.prompts) > 0 })
	h.click("取消")
	h.pump("连接结束", func() bool { return len(h.a.connecting) == 0 })
	if len(h.a.tabs) != 0 || !strings.Contains(h.a.notice, "未信任") {
		t.Fatalf("标签数 %d，提示 %q", len(h.a.tabs), h.a.notice)
	}
	if h.a.hasTrustRecord(h.devHost()) {
		t.Fatal("拒绝后不应保存主机密钥")
	}
}

func TestChangedHostKeyIsExplained(t *testing.T) {
	h := newHarness(t)
	// 先正常连接一次，记下服务器的密钥。
	h.connectDev()
	h.a.closeAll()
	h.tt.Frame()

	// 换一台使用不同密钥、但地址相同的服务器，模拟被冒充。
	port := h.server.Addr().Port
	h.server.Close()
	impostor, err := devsshd.Start(devsshd.Options{Addr: fmt.Sprintf("127.0.0.1:%d", port), Password: testPassword})
	if err != nil {
		t.Skipf("端口被占用，无法重新监听: %v", err)
	}
	defer impostor.Close()

	// 第一次连接时记住了密码，这次直接握手，在核对主机密钥时被拒绝。
	h.click("dev")
	h.pump("拒绝连接", func() bool { return len(h.a.connecting) == 0 && h.a.notice != "" })
	if !strings.Contains(h.a.notice, "主机密钥和上次不同") || strings.Contains(h.a.notice, "handshake failed") {
		t.Fatalf("提示应面向用户而不是底层错误: %q", h.a.notice)
	}
	if len(h.a.tabs) != 0 {
		t.Fatal("密钥变化时不应建立会话")
	}
	if !strings.Contains(h.a.notice, "重置主机密钥") {
		t.Errorf("提示应告诉用户怎么处理: %q", h.a.notice)
	}

	// 服务器确实重装了：右键“重置主机密钥”，下次连接重新核对指纹。
	h.tt.Frame()
	if err := h.tt.RightClick("dev"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("重置主机密钥"); err != nil {
		t.Fatalf("%v 菜单: %q", err, h.tt.Menu())
	}
	if h.a.hasTrustRecord(h.devHost()) {
		t.Fatal("重置后不应还有信任记录")
	}
	h.tt.Frame()
	h.click("dev")
	h.pump("重新核对指纹", func() bool { return len(h.a.prompts) > 0 })
	h.click("信任并连接")
	h.pump("会话建立", func() bool { return len(h.a.tabs) == 1 })
}

// 主机信任记录存在保险库里（随同步到其他设备），并且是加密的。
func TestTrustRecordLivesInVault(t *testing.T) {
	h := newHarness(t)
	h.connectDev()
	if !h.a.hasTrustRecord(h.devHost()) {
		t.Fatal("信任记录应保存在保险库里")
	}
	if h.vaultFileContains("ssh-ed25519") || !h.vaultFileContains(`"kind": "knownhost"`) {
		t.Fatal("信任记录应以加密形式存在保险库文件里")
	}
	// 另一台设备拿到同一个保险库（重启模拟），连接时不再询问指纹。
	h.reopen()
	if err := h.a.vault.Unlock(masterPassword); err != nil {
		t.Fatal(err)
	}
	h.a.reload()
	h.tt.Frame()
	h.click("dev")
	h.trustAndWait(0)
}

// 经跳板机连接：先为跳板机要密码，再为目标主机要密码，前一跳的密码不用重输；
// 两台机器的指纹分别确认。
func TestConnectThroughJumpHost(t *testing.T) {
	bastion, err := devsshd.Start(devsshd.Options{Password: "bastion-pass"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bastion.Close() })
	h := newHarness(t, hosts.Host{ID: "bastion", Name: "bastion", Address: "127.0.0.1", Port: bastion.Addr().Port, User: "tester"})
	if err := h.a.apply(hostChange(hosts.Host{ID: "inner", Name: "inner", Address: "127.0.0.1", Port: h.server.Addr().Port,
		User: "tester", ProxyJump: "bastion", Group: defaultGroup})); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	if !h.tt.HasText("经 bastion") {
		t.Errorf("侧栏应显示途经的跳板机: %q", h.tt.Texts())
	}

	h.click("inner")
	h.pump("跳板机密码询问", func() bool { return h.a.auth.open })
	if h.a.auth.host.ID != "bastion" || !h.a.auth.via {
		t.Fatalf("应先为跳板机询问密码，实际询问 %q", h.a.auth.host.Name)
	}
	h.tt.Frame()
	if !h.tt.HasText("这是连接 inner 途中的跳板机。") {
		t.Errorf("面板应说明这是跳板机: %q", h.tt.Texts())
	}

	// 先输错一次，确认错误提示落在跳板机上。
	h.a.auth.secret = "wrong"
	h.tt.Frame()
	h.click("连接")
	h.pump("跳板机指纹", func() bool { return len(h.a.prompts) > 0 })
	h.click("信任并连接")
	h.pump("再次询问跳板机", func() bool { return h.a.auth.open && h.a.auth.message != "" })
	if h.a.auth.host.ID != "bastion" || !strings.Contains(h.a.auth.message, "密码不正确") {
		t.Fatalf("询问对象 %q，提示 %q", h.a.auth.host.Name, h.a.auth.message)
	}

	h.a.auth.secret = "bastion-pass"
	h.tt.Frame()
	h.click("连接")
	h.pump("目标主机密码询问", func() bool { return h.a.auth.open })
	if h.a.auth.host.ID != "inner" || h.a.auth.via || h.a.auth.creds[0].password != "bastion-pass" {
		t.Fatalf("跳板机通过后应询问目标主机，并保留跳板机密码: host=%q via=%v", h.a.auth.host.Name, h.a.auth.via)
	}
	h.a.auth.secret = testPassword
	h.tt.Frame()
	h.click("连接")
	h.pump("目标主机指纹", func() bool { return len(h.a.prompts) > 0 })
	h.click("信任并连接")
	h.pump("会话建立", func() bool { return len(h.a.tabs) == 1 })

	if h.a.auth.creds != nil || h.a.auth.secret != "" {
		t.Error("连接完成后不应在内存里保留任何密码")
	}
	tb := h.a.tabs[0]
	tb.focus.term.Send([]byte("echo via-$((20+1))\n"))
	h.pump("终端输出", func() bool { return strings.Contains(tb.focus.term.Text(), "via-21") })
}

func TestJumpCycleIsReportedNotAttempted(t *testing.T) {
	h := newHarness(t,
		hosts.Host{ID: "a", Name: "a", Address: "10.0.0.1", Port: 22, User: "u", ProxyJump: "b"},
		hosts.Host{ID: "b", Name: "b", Address: "10.0.0.2", Port: 22, User: "u", ProxyJump: "a"})
	h.click("a")
	h.tt.Frame()
	if len(h.a.connecting) != 0 || !strings.Contains(h.a.notice, "循环") {
		t.Fatalf("connecting=%d notice=%q", len(h.a.connecting), h.a.notice)
	}
}

func TestPaletteFindsHostAndRunsActions(t *testing.T) {
	h := newHarness(t,
		hosts.Host{ID: "w", Name: "web-01", Address: "10.0.0.3", Port: 22, User: "u"},
		hosts.Host{ID: "m", Name: "my-new-box", Address: "10.0.0.4", Port: 22, User: "u"})
	h.click("搜索主机和操作")
	if !h.a.pal.open {
		t.Fatal("命令面板没有打开")
	}
	h.a.pal.query = "web"
	h.tt.Frame()
	items := h.a.paletteItems("web")
	if len(items) == 0 || items[0].title != "web-01" {
		t.Fatalf("“web” 应把 web-01 排第一: %+v", items)
	}

	h.a.pal.query = "深色"
	h.tt.Frame()
	h.click("外观：深色")
	h.tt.Frame()
	if h.a.settings.Appearance != appearanceDark || h.a.pal.open {
		t.Fatalf("appearance=%q open=%v", h.a.settings.Appearance, h.a.pal.open)
	}
}

func TestSplitAndClosePanes(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	first := tb.focus

	h.click("向右拆分（" + shortcutText("D") + "）")
	h.pump("拆分", func() bool { return tb.root.leaves() == 2 })
	if tb.focus == first {
		t.Error("新窗格应获得焦点")
	}
	// 两个窗格共用一条连接，各自是独立的 shell。
	tb.focus.term.Send([]byte("echo pane-$((1+1))\n"))
	h.pump("新窗格输出", func() bool { return strings.Contains(tb.focus.term.Text(), "pane-2") })
	if strings.Contains(first.term.Text(), "pane-2") {
		t.Error("输入不应出现在另一个窗格")
	}

	h.a.closeFocused(tb)
	h.tt.Frame()
	if tb.root != first || tb.focus != first {
		t.Fatal("关闭后应回到原来的窗格")
	}
	h.a.closeFocused(tb)
	h.tt.Frame()
	if len(h.a.tabs) != 0 {
		t.Fatal("关闭最后一个窗格应关闭标签")
	}
}

// 回归：点一次拆分只能多一个窗格。真实窗口里曾出现过点一次变成四个窗格。
func TestOneClickSplitsOnce(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	h.click("向右拆分（" + shortcutText("D") + "）")
	h.pump("拆分", func() bool { return tb.root.leaves() >= 2 })
	settle := time.Now().Add(1500 * time.Millisecond)
	h.pump("稳定", func() bool { return time.Now().After(settle) })
	if n := tb.root.leaves(); n != 2 {
		t.Fatalf("点一次拆分后有 %d 个窗格", n)
	}
}

// 连续触发拆分时，上一个还没完成就忽略后面的，不会一下子多出好几个窗格。
func TestRapidSplitRequestsOpenOnePane(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	for range 3 {
		h.a.split(tb, false)
	}
	h.pump("拆分", func() bool { return !tb.splitting })
	if n := tb.root.leaves(); n != 2 {
		t.Fatalf("连续触发三次后有 %d 个窗格，期望 2", n)
	}
	h.a.split(tb, false)
	h.pump("第二次拆分", func() bool { return tb.root.leaves() == 3 })
}

// 新窗格的弹入标记只用一次：播放后要清掉，否则每次重建都会重新弹一遍。
func TestArrivalPoseIsUsedOnce(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	h.tt.Frame()
	if len(h.a.arrived) != 0 {
		t.Fatalf("首个窗格的弹入标记没有清掉: %v", h.a.arrived)
	}
	h.a.split(tb, false)
	h.pump("拆分", func() bool { return tb.root.leaves() == 2 })
	h.tt.Frame()
	if len(h.a.arrived) != 0 {
		t.Fatalf("拆分出的窗格弹入标记没有清掉: %v", h.a.arrived)
	}
}

// 回归：窗格拆分后，旧窗格的退出回调仍要标记到它自己身上。
func TestPaneExitAfterSplitMarksTheRightPane(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	first := tb.focus
	h.a.split(tb, true)
	h.pump("拆分", func() bool { return tb.root.leaves() == 2 })
	first.term.Send([]byte("exit\n"))
	h.pump("旧窗格结束", func() bool { return first.ended })
	if tb.focus.ended {
		t.Error("新窗格不应被标记为结束")
	}
}

func TestFilesDrawer(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	h.click("文件（" + shortcutText("J") + "）")
	// 服务器的工作目录就是本包目录。
	h.pump("文件列表", func() bool { return h.tt.HasText("go.mod") })
	// 抽屉有滑入动画，Tester 的时钟不会自己走。
	h.tt.Advance(time.Second)
	h.tt.Frame()
	home := tb.dir
	h.click("上一级")
	h.pump("上级目录", func() bool { return tb.dir == filepath.Dir(home) })
}

func TestManyTabsStayReachable(t *testing.T) {
	h := newHarness(t)
	h.connectDev()
	for range 11 {
		before := len(h.a.tabs)
		// 同一台主机再开会话：直接调用 connect，已信任的主机不再询问指纹。
		h.a.connect(h.a.hosts[0], map[int]credential{0: {password: testPassword}})
		h.pump("新会话", func() bool { return len(h.a.tabs) == before+1 })
	}
	if len(h.a.tabs) != 12 {
		t.Fatalf("标签数 %d", len(h.a.tabs))
	}
	h.a.active = 0
	h.tt.Frame()
	if box, ok := h.tt.Find("向右拆分（" + shortcutText("D") + "）"); !ok || box.X+box.W > 1180 {
		t.Errorf("标签过多时把工具按钮挤出了窗口: %+v", box)
	}
}

func TestAddHost(t *testing.T) {
	h := newHarness(t)
	h.click("添加主机")
	if !h.a.form.open {
		t.Fatal("添加面板没有打开")
	}
	h.a.form.address, h.a.form.user, h.a.form.port = "10.1.1.1", "ops", "70000"
	h.tt.Frame()
	h.click("添加")
	if !strings.Contains(h.a.form.err, "端口") {
		t.Fatalf("错误 = %q", h.a.form.err)
	}
	h.a.form.port, h.a.form.name, h.a.form.production = "", "billing", true
	h.tt.Frame()
	h.click("添加")
	h.tt.Frame()
	if !h.tt.HasText("已添加 billing") {
		t.Errorf("添加后应有提示: %q", h.tt.Texts())
	}
	if h.a.form.open || !h.tt.HasText("billing") {
		t.Fatalf("添加后应关闭面板并出现在列表里: %q", h.tt.Texts())
	}
	var saved hosts.Host
	for _, x := range h.onDisk() {
		if x.Name == "billing" {
			saved = x
		}
	}
	if !saved.Production || saved.Port != 22 || saved.Address != "10.1.1.1" {
		t.Fatalf("saved=%+v", saved)
	}
}

// 跳板机和代理默认收起；跳板机按顺序选择已保存的主机，代理从独立管理的列表里选。
func TestAddHostChoosesJumpsAndProxy(t *testing.T) {
	h := newHarness(t,
		hosts.Host{ID: "gw", Name: "gw", Address: "10.0.0.9", Port: 22, User: "ops"})
	if err := h.a.apply(proxyChange(hosts.Proxy{ID: "px", Name: "office", Kind: hosts.ProxySOCKS5,
		Address: "127.0.0.1", Port: 1080})); err != nil {
		t.Fatal(err)
	}
	h.click("添加主机")
	h.tt.Frame()
	if h.tt.HasText("选择主机…") || h.tt.HasText("不使用代理") {
		t.Fatalf("跳板机和代理默认不应展开: %q", h.tt.Texts())
	}
	d := &h.a.form
	d.address, d.user, d.name = "10.2.2.2", "ops", "inner"

	h.click("添加跳板机")
	h.tt.Frame()
	h.click("添加")
	if !strings.Contains(d.err, "还没有选择主机") {
		t.Fatalf("没选跳板机应提示: %q", d.err)
	}
	if opts := h.a.jumpOptions(d, 0); !slices.Contains(opts, "dev") || !slices.Contains(opts, "gw") {
		t.Fatalf("跳板机应能选已保存的主机: %q", opts)
	}
	d.jumps[0] = "inner" // 选自己（名称相同）会形成循环
	h.click("再加一台")
	d.jumps[1] = "gw"
	h.tt.Frame()
	h.click("添加")
	if !strings.Contains(d.err, "循环") {
		t.Fatalf("跳板机循环应提示: %q", d.err)
	}
	h.click("移除第 1 台跳板机")
	h.tt.Frame()

	h.click("使用代理")
	h.tt.Frame()
	if d.proxyName != "office" {
		t.Fatalf("只有一个代理时应直接选上: %q", d.proxyName)
	}
	h.click("添加")
	saved := h.host("inner")
	if saved.ProxyJump != "gw" || saved.ProxyID != "px" || saved.Proxy != "" {
		t.Fatalf("saved=%+v err=%q", saved, d.err)
	}

	// 编辑时已有的设置自动展开；“不使用代理”后保存即去掉代理。
	h.a.openEdit(saved)
	h.tt.Frame()
	if !h.tt.HasText("不使用代理") || len(h.a.form.jumps) != 1 {
		t.Fatalf("编辑时应展开已有设置: %q", h.tt.Texts())
	}
	h.click("不使用代理")
	h.click("移除第 1 台跳板机")
	h.click("保存")
	if got := h.host("inner"); got.ProxyID != "" || got.ProxyJump != "" {
		t.Fatalf("got=%+v", got)
	}
}

// 旧版直接填写的代理和跳板机地址：编辑表单原样保留，连接仍然有效。
func TestLegacyProxyAndJumpAreKept(t *testing.T) {
	h := newHarness(t,
		hosts.Host{ID: "old", Name: "old", Address: "10.0.0.3", Port: 22, User: "u",
			ProxyJump: "ops@gw:2222", Proxy: "socks5://127.0.0.1:1080"})
	h.a.openEdit(h.host("old"))
	h.tt.Frame()
	h.click("保存")
	if got := h.host("old"); got.ProxyJump != "ops@gw:2222" || got.Proxy != "socks5://127.0.0.1:1080" {
		t.Fatalf("旧设置被改动: %+v err=%q", got, h.a.form.err)
	}
}

// 在表单里新建代理：表单让位、内容保留，保存后回到表单并选中新代理。
func TestCreateProxyFromHostForm(t *testing.T) {
	h := newHarness(t)
	h.click("添加主机")
	h.a.form.address, h.a.form.user, h.a.form.name = "10.3.3.3", "ops", "via-proxy"
	h.tt.Frame()
	h.click("使用代理")
	h.tt.Frame()
	if !h.tt.HasText("还没有代理，先新建一个。") {
		t.Fatalf("没有代理时应指引新建: %q", h.tt.Texts())
	}
	h.click("新建代理")
	h.tt.Frame()
	p := &h.a.proxyPanel
	p.kind, p.address, p.port, p.name = 1, "127.0.0.1", "7890", "clash"
	h.tt.Frame()
	h.click("添加")
	h.tt.Frame()
	if h.a.proxyPanel.open || !h.a.form.open || h.a.form.address != "10.3.3.3" || h.a.form.proxyName != "clash" {
		t.Fatalf("应回到表单并选中新代理: panel=%v form=%+v", h.a.proxyPanel.open, h.a.form)
	}
	h.click("添加")
	got := h.host("via-proxy")
	px, ok := h.a.proxyByID(got.ProxyID)
	if !ok || px.Kind != hosts.ProxyHTTP || px.Port != 7890 {
		t.Fatalf("host=%+v proxy=%+v", got, px)
	}
	cfg, _, _, _, err := h.a.prepareDialConfig(context.Background(), got, nil)
	if err != nil || cfg.Proxy == nil || cfg.Proxy.Kind != "http" || cfg.Proxy.Addr != "127.0.0.1:7890" {
		t.Fatalf("连接配置应使用选中的代理: %+v err=%v", cfg.Proxy, err)
	}
}

// 代理面板：带密码的代理先设置主密码、加密保存；删除时使用它的主机改为直连。
func TestProxyPanelPasswordAndDelete(t *testing.T) {
	h := newBareHarness(t)
	h.click("代理")
	h.tt.Frame()
	h.click("添加代理")
	h.tt.Frame()
	p := &h.a.proxyPanel
	p.address, p.port, p.user, p.password = "10.8.8.8", "1080", "pu", "proxy-secret"
	h.tt.Frame()
	h.click("添加")
	h.tt.Frame()
	if !h.a.setup.open || len(h.a.proxies) != 0 {
		t.Fatal("保存代理密码前应先设置主密码")
	}
	h.a.setup.password, h.a.setup.confirm = "master pw 1", "master pw 1"
	h.tt.Frame()
	h.click("设置并继续")
	h.pump("设置主密码完成", func() bool { return !h.a.setup.open })
	if len(h.a.proxies) != 1 || h.a.proxies[0].Password != "proxy-secret" || h.a.proxies[0].Name != "10.8.8.8:1080" {
		t.Fatalf("proxies=%+v err=%q", h.a.proxies, h.a.proxyPanel.err)
	}
	if h.vaultFileContains("proxy-secret") {
		t.Fatal("保险库文件里不应出现代理密码明文")
	}
	px := h.a.proxies[0]
	dev := h.devHost()
	dev.ProxyID = px.ID
	h.a.apply(hostChange(dev))

	h.a.deleteProxy(px)
	if len(h.a.proxies) != 0 || h.devHost().ProxyID != "" {
		t.Fatalf("删除后主机应改为直连: %+v", h.devHost())
	}
}

// 引用的代理不存在时报错，而不是悄悄直连。
func TestMissingProxyIsReported(t *testing.T) {
	h := newHarness(t, hosts.Host{ID: "x", Name: "x", Address: "10.0.0.1", Port: 22, User: "u", ProxyID: "gone"})
	h.click("x")
	h.tt.Frame()
	if len(h.a.connecting) != 0 || !strings.Contains(h.a.notice, "代理已经删除") {
		t.Fatalf("notice=%q", h.a.notice)
	}
}

func TestVibrancyFollowsReduceTransparency(t *testing.T) {
	if vibrancyFor(true) != mygo.VibrancyNone {
		t.Error("开启“减少透明度”时不应使用毛玻璃")
	}
	if vibrancyFor(false) != mygo.VibrancySidebar {
		t.Error("默认应使用侧栏毛玻璃")
	}
}

// 会话自己结束时提示并给出“重新连接”；点它会重新发起连接。
func TestEndedSessionOffersReconnect(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	tb.focus.term.Send([]byte("exit\n"))
	h.pump("会话结束提示", func() bool { return h.tt.HasText(tb.host.Name + " 的会话已结束") })
	h.click("重新连接")
	// 密码第一次连接时已记住，重新连接不再询问，直接换成新的会话。
	h.pump("重新连接", func() bool { return len(h.a.tabs) == 1 && h.a.tabs[0] != tb })
	if h.a.auth.open {
		t.Error("已记住密码时重新连接不应再询问")
	}
}

// 用户主动关闭标签时，终端也会退出，但不应因此弹出“会话已结束”。
func TestClosingTabDoesNotToast(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	h.a.closeTab(tb)
	time.Sleep(300 * time.Millisecond)
	h.pump("关闭完成", func() bool { return true })
	for _, toast := range h.a.toasts {
		t.Errorf("主动关闭后不应有提示，得到 %q", toast.Title)
	}
	if h.tt.HasText(tb.host.Name + " 的会话已结束") {
		t.Error("主动关闭后界面上出现了会话结束提示")
	}
}

func TestFuzzyScore(t *testing.T) {
	if fuzzyScore("", "abc") != 0 || fuzzyScore("xyz", "abc") != -1 {
		t.Fatal("空查询应匹配，不相关的应不匹配")
	}
	if fuzzyScore("web", "web-01") <= fuzzyScore("web", "my-new-box") {
		t.Error("从开头连续匹配的分数应更高")
	}
	if fuzzyScore("db", "prod-db") <= fuzzyScore("db", "dashboard") {
		t.Error("在分隔符之后匹配的分数应更高")
	}
	if fuzzyScore("生产", "生产数据库") < 0 || fuzzyScore("数据", "生产数据库") < 0 {
		t.Error("应支持中文")
	}
	// 从第一个 d 贪心匹配只得 8+1=9 分；从 “-db” 的 d 开始是 5+6=11 分。
	if got := fuzzyScore("db", "d-x-db"); got != 11 {
		t.Errorf("应找到最好的起点，得分 %d，期望 11", got)
	}
}

func TestFormatting(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KB", 1 << 30: "1.0 GB"} {
		if got := formatSize(n); got != want {
			t.Errorf("formatSize(%d) = %q", n, got)
		}
	}
	if got := shortPath("/a/b/c/d/e"); got != "…/c/d/e" {
		t.Errorf("shortPath = %q", got)
	}
	if got := shortPath("/a/b"); got != "/a/b" {
		t.Errorf("shortPath = %q", got)
	}
	if got := formatLatency(42 * time.Millisecond); got != "42 ms" {
		t.Errorf("formatLatency = %q", got)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{"": "", "/a/b": "/a/b", "~/x": filepath.Join(home, "x"), "~other/x": "~other/x"} {
		if got, err := expandHome(in); err != nil || got != want {
			t.Errorf("expandHome(%q) = %q, %v", in, got, err)
		}
	}
}

func TestAutoReconnectOnServerRestart(t *testing.T) {
	h := newHarness(t)
	tab := h.connectDev()
	h.a.pingInterval = 50 * time.Millisecond
	h.a.reconnectBaseDelay = 50 * time.Millisecond

	addr := h.server.Addr().String()
	hostKeys := h.server.HostKeys()

	h.server.Close()

	h.pump("观察到正在重连", func() bool {
		return tab.reconnecting && h.tt.HasText("正在重连")
	})

	newServer, err := devsshd.Start(devsshd.Options{
		Addr:     addr,
		Password: testPassword,
		HostKeys: hostKeys,
	})
	if err != nil {
		t.Fatalf("重启 devsshd 失败: %v", err)
	}
	defer newServer.Close()
	h.server = newServer

	h.pump("自动重连完成", func() bool {
		return !tab.reconnecting && tab.client != nil && !tab.root.ended
	})

	if tab.reconnecting {
		t.Fatal("仍然处于重连状态")
	}
	if tab.client == nil {
		t.Fatal("重连后客户端为空")
	}
}

func TestCancelAutoReconnect(t *testing.T) {
	h := newHarness(t)
	tab := h.connectDev()
	h.a.pingInterval = 50 * time.Millisecond
	h.a.reconnectBaseDelay = 50 * time.Millisecond

	h.server.Close()

	h.pump("观察到正在重连", func() bool {
		return tab.reconnecting && h.tt.HasText("正在重连")
	})

	h.click("取消")
	h.tt.Frame()

	if tab.reconnecting {
		t.Fatal("取消后重连标志未清除")
	}
	if !tab.root.ended {
		t.Fatal("取消后窗格未标记为已结束")
	}
	if !h.tt.HasText("会话已结束") {
		t.Fatalf("界面未显示会话已结束，界面文字: %q", h.tt.Texts())
	}
}

func TestSessionRestoreAcrossRestart(t *testing.T) {
	h := newHarness(t)
	h.a.rememberOnDevice(true)
	tab := h.connectDev()

	h.a.split(tab, true)
	h.pump("拆分窗格完成", func() bool {
		return tab.root.leaves() == 2
	})

	h.reopen()

	h.pump("会话恢复完成", func() bool {
		return len(h.a.tabs) == 1 && h.a.tabs[0].root.leaves() == 2 && h.a.tabs[0].client != nil
	})

	if len(h.a.tabs) != 1 {
		t.Fatalf("期望恢复 1 个标签，实际: %d", len(h.a.tabs))
	}
	if leaves := h.a.tabs[0].root.leaves(); leaves != 2 {
		t.Fatalf("期望恢复 2 个窗格，实际: %d", leaves)
	}
}

func TestDisableSessionRestore(t *testing.T) {
	h := newHarness(t)
	h.a.rememberOnDevice(true)
	h.connectDev()

	h.a.openSettings()
	h.tt.Frame()
	if !h.tt.HasText("会话恢复") {
		t.Fatalf("设置中未找到会话恢复，文字: %q", h.tt.Texts())
	}
	h.click("启动时恢复上次打开的标签和窗格")
	h.tt.Frame()
	if !h.a.local.DisableRestoreSession {
		t.Fatal("禁用会话恢复未生效")
	}

	h.reopen()
	h.tt.Frame()

	if len(h.a.tabs) != 0 {
		t.Fatalf("禁用恢复后不应恢复会话，实际有 %d 个标签", len(h.a.tabs))
	}
}

func TestParseQuickConnect(t *testing.T) {
	cases := []struct {
		in   string
		user string
		addr string
		port int
		ok   bool
	}{
		{"root@10.0.0.1", "root", "10.0.0.1", 22, true},
		{"admin@example.com:2222", "admin", "example.com", 2222, true},
		{"tester@[::1]:22", "tester", "::1", 22, true},
		{"a@b@host", "a@b", "host", 22, true},
		{"user@host:invalid", "", "", 0, false},
		{"not-a-target", "", "", 0, false},
		{"@emptyuser", "", "", 0, false},
		{"user@", "", "", 0, false},
		{"user with spaces@host", "", "", 0, false},
	}
	for _, c := range cases {
		u, a, p, ok := parseQuickConnect(c.in)
		if ok != c.ok || u != c.user || a != c.addr || p != c.port {
			t.Errorf("parseQuickConnect(%q) = (%q, %q, %d, %v), want (%q, %q, %d, %v)",
				c.in, u, a, p, ok, c.user, c.addr, c.port, c.ok)
		}
	}
}

func TestQuickConnectAndSaveAsHost(t *testing.T) {
	h := newHarness(t)
	h.click("搜索主机和操作")
	if !h.a.pal.open {
		t.Fatal("命令面板没有打开")
	}
	quickTarget := fmt.Sprintf("tester@127.0.0.1:%d", h.server.Addr().Port)
	h.a.pal.query = quickTarget
	h.tt.Frame()
	items := h.a.paletteItems(quickTarget)
	if len(items) == 0 || items[0].title != "连接 "+quickTarget {
		t.Fatalf("应该出现快速连接条目: %+v", items)
	}

	beforeTabs := len(h.a.tabs)
	h.click("连接 " + quickTarget)
	h.tt.Frame()

	h.pump("密码询问", func() bool { return h.a.auth.open })
	h.tt.Frame()
	if h.tt.HasText("记住密码（加密保存在保险库里）") {
		t.Fatal("临时主机没有地方保存密码，不应显示“记住密码”")
	}
	h.a.auth.secret = testPassword
	h.tt.Frame()
	h.click("连接")

	h.pump("主机密钥询问", func() bool { return len(h.a.prompts) > 0 })
	h.click("信任并连接")

	h.pump("会话建立", func() bool { return len(h.a.tabs) == beforeTabs+1 })
	tab := h.a.tabs[beforeTabs]
	if h.a.isSavedHost(tab.host.ID) {
		t.Fatal("快速连接的主机不应是已保存主机")
	}

	// 验证临时主机的密码没有被存进保险库
	for _, dh := range h.onDisk() {
		if dh.ID == tab.host.ID {
			t.Fatalf("临时主机不应存在于磁盘保险库中: %+v", dh)
		}
	}

	// 点击保存为主机
	h.click("保存为主机")
	h.tt.Frame()
	if !h.a.form.open {
		t.Fatal("主机表单没有打开")
	}
	if h.a.form.address != "127.0.0.1" || h.a.form.user != "tester" {
		t.Fatalf("表单内容不正确: address=%s user=%s", h.a.form.address, h.a.form.user)
	}

	// 提交表单保存
	h.click("添加")
	h.tt.Frame()
	if h.a.form.open {
		t.Fatalf("保存主机失败: %s", h.a.form.err)
	}
	if !h.a.isSavedHost(tab.host.ID) {
		t.Fatal("保存后标签关联的主机应该已被保存到保险库")
	}

	saved := false
	for _, dh := range h.onDisk() {
		if dh.ID == tab.host.ID {
			saved = true
			break
		}
	}
	if !saved {
		t.Fatal("保存后的主机未写入磁盘保险库")
	}
}

func TestLocalTerminal(t *testing.T) {
	h := newHarness(t)
	before := len(h.a.tabs)
	h.click("本地终端")
	h.tt.Frame()
	if len(h.a.tabs) != before+1 {
		t.Fatalf("应该打开本地终端标签，tabs=%d", len(h.a.tabs))
	}
	tab := h.a.tabs[before]
	if !tab.local || tab.host.Name != "本机" {
		t.Fatalf("标签属性不正确: local=%v name=%s", tab.local, tab.host.Name)
	}
	texts := h.tt.Texts()
	foundLocal := false
	for _, txt := range texts {
		if txt == "本地" {
			foundLocal = true
			break
		}
	}
	if !foundLocal {
		t.Fatalf("窗格状态应该显示‘本地’，当前文字: %q", texts)
	}

	for _, txt := range texts {
		if strings.Contains(txt, "文件（") || txt == "保存为主机" {
			t.Fatalf("本地终端不应提供文件抽屉和“保存为主机”: %q", texts)
		}
	}
	// 标签名和侧栏按钮不能同名（测试和读屏软件按文字找按钮）。
	if n := strings.Count(strings.Join(texts, "\n"), "本地终端"); n > 2 { // 侧栏按钮的标签和文字各一次
		t.Fatalf("“本地终端”出现了 %d 次，标签和侧栏按钮重名: %q", n, texts)
	}

	h.click("向右拆分（⌘D）")
	h.pump("本地终端拆分", func() bool { return tab.root.leaves() == 2 })
	if tab.root.leaves() != 2 {
		t.Fatalf("本地终端应能正常拆分窗格，leaves=%d", tab.root.leaves())
	}

	h.a.closeFocused(tab)
	h.tt.Frame()
	if tab.root.leaves() != 1 {
		t.Fatalf("关闭后应剩 1 个窗格，leaves=%d", tab.root.leaves())
	}

	h.click("搜索主机和操作")
	h.tt.Frame()
	h.a.pal.query = "新建本地终端"
	h.tt.Frame()
	h.click("新建本地终端")
	h.tt.Frame()
	if len(h.a.tabs) != before+2 {
		t.Fatalf("应该从命令面板打开第二个本地终端，tabs=%d", len(h.a.tabs))
	}
}

func TestTerminalSearch(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()

	// 写入测试内容
	tb.focus.term.Feed([]byte("first line of text\r\nsecond hello world\r\nthird line with hello\r\n"))
	h.tt.Frame()

	// 触发搜索
	h.click("查找（⌘F）")
	h.tt.Frame()
	if !tb.focus.searchOpen {
		t.Fatal("查找工具条应该处于打开状态")
	}

	// 输入搜索词
	tb.focus.searchQuery = "hello"
	h.tt.Frame()
	h.pump("终端搜索匹配", func() bool {
		return tb.focus.term.SearchStatus().Total >= 2
	})

	status := tb.focus.term.SearchStatus()
	if status.Total != 2 {
		t.Fatalf("应该找到 2 处 hello，实际: %+v", status)
	}

	// 导航到下一个
	beforeSel := status.Selected
	h.click("下一个")
	h.tt.Frame()
	afterSel := tb.focus.term.SearchStatus().Selected
	if afterSel == 0 {
		t.Fatalf("查找下一个后选中项不应为 0: %+v", tb.focus.term.SearchStatus())
	}
	_ = beforeSel

	// 关闭查找
	h.click("关闭查找")
	h.tt.Frame()
	if tb.focus.searchOpen {
		t.Fatal("关闭后查找工具条应该关闭")
	}
}

// 按 ⌘F 后查找框立刻获得焦点：否则接着输入的搜索词会落进终端、发给远端 shell。
// 关闭查找后焦点回到终端；清空查找框后高亮消失。
func TestSearchFocusAndClear(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	tb.focus.term.Send([]byte("for i in 1 2 3; do echo needle; done\n"))
	h.pump("输出", func() bool { return strings.Count(tb.focus.term.Text(), "needle") >= 3 })

	h.tt.Key(chromeShortcut(), ui.KeyF)
	h.tt.Frame()
	h.tt.Frame()
	if !tb.focus.searchOpen || !h.tt.Focused("查找输入框") {
		t.Fatalf("⌘F 后查找框应打开并获得焦点: open=%v focused=%v", tb.focus.searchOpen, h.tt.Focused("查找输入框"))
	}
	h.tt.Type("needle")
	h.pump("搜索结果", func() bool { return tb.focus.searchStatus.Total >= 3 })
	if tb.focus.searchQuery != "needle" {
		t.Fatalf("输入应进入查找框，实际查询 %q", tb.focus.searchQuery)
	}

	tb.focus.searchQuery = ""
	h.pump("清空后结束搜索", func() bool { return tb.focus.searchStatus.Total == 0 && tb.focus.searchStatus.Text == "" })

	h.click("关闭查找")
	h.tt.Frame()
	h.tt.Frame()
	if tb.focus.searchOpen || h.tt.Focused("查找输入框") {
		t.Fatal("关闭后查找框应收起")
	}
}

func TestFileUploadDownloadAndCancel(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()

	// 打开文件抽屉
	h.click("文件（⌘J）")
	h.pump("打开文件列表", func() bool { return tb.dir != "" })

	// 准备本地待上传文件
	tmpDir := t.TempDir()
	localUploadPath := filepath.Join(tmpDir, "hello-upload.txt")
	content := []byte("hello sftp streaming transfer")
	if err := os.WriteFile(localUploadPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	h.a.chooseUploadFile = func() (string, error) {
		return localUploadPath, nil
	}

	// 点击上传按钮
	h.click("上传文件")
	h.pump("上传完成", func() bool {
		return len(tb.transfers) > 0 && tb.transfers[0].done.Load()
	})

	if tb.transfers[0].err != "" {
		t.Fatalf("上传失败: %s", tb.transfers[0].err)
	}

	// 刷新文件列表确认文件已上传
	h.pump("列表中出现上传文件", func() bool {
		for _, e := range tb.entries {
			if e.Name == "hello-upload.txt" {
				return true
			}
		}
		return false
	})

	// 下载该文件
	localDownloadPath := filepath.Join(tmpDir, "downloaded-hello.txt")
	h.a.chooseSavePath = func(string) (string, error) {
		return localDownloadPath, nil
	}

	h.tt.Frame()
	if err := h.tt.RightClick("hello-upload.txt"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("下载"); err != nil {
		t.Fatalf("右键选择下载失败: %v, menu=%q", err, h.tt.Menu())
	}

	h.pump("下载完成", func() bool {
		return len(tb.transfers) > 1 && tb.transfers[1].done.Load()
	})

	if tb.transfers[1].err != "" {
		t.Fatalf("下载失败: %s", tb.transfers[1].err)
	}

	data, err := os.ReadFile(localDownloadPath)
	if err != nil || string(data) != string(content) {
		t.Fatalf("下载文件内容不匹配: %q, err=%v", string(data), err)
	}

	// 测试取消传输
	slowLocalPath := filepath.Join(tmpDir, "slow-upload.txt")
	slowContent := make([]byte, 64*1024*1024)
	if err := os.WriteFile(slowLocalPath, slowContent, 0o644); err != nil {
		t.Fatal(err)
	}
	h.a.chooseUploadFile = func() (string, error) {
		return slowLocalPath, nil
	}

	// 上传前要先在后台检查远端有没有同名文件，传输晚一个回合才出现：一出现就取消。
	h.click("上传文件")
	var cancelTr *fileTransfer
	h.pump("取消测试的传输开始", func() bool {
		if len(tb.transfers) >= 3 && cancelTr == nil {
			cancelTr = tb.transfers[2]
			h.a.cancelTransfer(cancelTr)
		}
		return cancelTr != nil
	})
	h.pump("传输取消", func() bool {
		return cancelTr.done.Load()
	})
	if cancelTr.err != "已取消" {
		t.Fatalf("传输取消状态不正确: %q", cancelTr.err)
	}
}

// 系统对话框只在 installSystemDialogs 里设置；漏接一个，正式运行时就会调用 nil 函数崩溃。
func TestSystemDialogsAreWired(t *testing.T) {
	a := newApp()
	installSystemDialogs(a)
	if a.chooseKeyFile == nil || a.chooseUploadFile == nil || a.chooseSavePath == nil ||
		a.chooseUploadFiles == nil || a.chooseFolder == nil {
		t.Fatalf("有系统对话框没有接上: key=%v upload=%v save=%v uploadFiles=%v folder=%v",
			a.chooseKeyFile != nil, a.chooseUploadFile != nil, a.chooseSavePath != nil,
			a.chooseUploadFiles != nil, a.chooseFolder != nil)
	}
}

// 远端已有同名文件时，上传前先确认替换；取消就不传，确认才替换。
func TestUploadAsksBeforeReplacing(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	h.click("文件（⌘J）")
	h.pump("打开文件列表", func() bool { return tb.dir != "" })

	name := fmt.Sprintf("replace-%d.txt", time.Now().UnixNano())
	remote := filepath.Join(tb.dir, name) // 开发服务器的 SFTP 直接操作本机文件系统
	if err := os.WriteFile(remote, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(remote) })
	local := filepath.Join(t.TempDir(), name)
	os.WriteFile(local, []byte("new"), 0o600)
	h.a.chooseUploadFile = func() (string, error) { return local, nil }

	h.click("上传文件")
	h.pump("替换确认", func() bool { return len(h.a.pendingUploads) > 0 })
	h.tt.Frame()
	if !h.tt.HasText("替换 " + name + "？") {
		t.Fatalf("应询问是否替换: %q", h.tt.Texts())
	}
	h.click("取消")
	h.tt.Frame()
	if len(tb.transfers) != 0 || len(h.a.pendingUploads) > 0 {
		t.Fatal("取消后不应上传")
	}
	if data, _ := os.ReadFile(remote); string(data) != "old" {
		t.Fatal("取消后远端文件不应改变")
	}

	h.click("上传文件")
	h.pump("替换确认", func() bool { return len(h.a.pendingUploads) > 0 })
	h.tt.Frame()
	h.click("替换")
	h.pump("上传完成", func() bool { return len(tb.transfers) == 1 && tb.transfers[0].done.Load() })
	if data, _ := os.ReadFile(remote); string(data) != "new" || tb.transfers[0].err != "" {
		t.Fatalf("确认后应替换远端文件: data=%q err=%q", data, tb.transfers[0].err)
	}
}

func TestFileOperations(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()

	// 打开文件抽屉
	h.click("文件（⌘J）")
	h.pump("打开文件列表", func() bool { return tb.dir != "" })

	// 1. 新建文件夹
	h.click("新建文件夹")
	h.tt.Frame()
	if !h.a.fileOp.open || h.a.fileOp.kind != fileOpNewFolder {
		t.Fatal("新建文件夹弹窗未打开")
	}
	h.a.fileOp.input = "test-folder"
	h.tt.Frame()
	h.click("创建")

	h.pump("列表中出现新建文件夹", func() bool {
		for _, e := range tb.entries {
			if e.Name == "test-folder" && e.IsDir {
				return true
			}
		}
		return false
	})

	// 2. 复制路径
	if err := h.tt.RightClick("test-folder"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("复制路径"); err != nil {
		t.Fatalf("选择复制路径失败: %v", err)
	}
	h.tt.Frame()
	expectedPath := path.Join(tb.dir, "test-folder")
	if h.tt.Clipboard() != expectedPath {
		t.Fatalf("复制路径不匹配: got %q, want %q", h.tt.Clipboard(), expectedPath)
	}

	// 3. 修改权限
	if err := h.tt.RightClick("test-folder"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("修改权限"); err != nil {
		t.Fatalf("选择修改权限失败: %v", err)
	}
	h.tt.Frame()
	if !h.a.fileOp.open || h.a.fileOp.kind != fileOpPermissions {
		t.Fatal("修改权限弹窗未打开")
	}
	h.a.fileOp.input = "0700"
	h.tt.Frame()
	h.click("修改权限")

	h.pump("权限修改成功", func() bool {
		for _, e := range tb.entries {
			if e.Name == "test-folder" && e.Mode.Perm() == 0700 {
				return true
			}
		}
		return false
	})

	// 4. 重命名
	if err := h.tt.RightClick("test-folder"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("重命名"); err != nil {
		t.Fatalf("选择重命名失败: %v", err)
	}
	h.tt.Frame()
	if !h.a.fileOp.open || h.a.fileOp.kind != fileOpRename {
		t.Fatal("重命名弹窗未打开")
	}
	h.a.fileOp.input = "renamed-folder"
	h.tt.Frame()
	h.click("重命名")

	h.pump("重命名成功", func() bool {
		foundNew := false
		foundOld := false
		for _, e := range tb.entries {
			if e.Name == "renamed-folder" {
				foundNew = true
			}
			if e.Name == "test-folder" {
				foundOld = true
			}
		}
		return foundNew && !foundOld
	})

	// 5. 删除
	if err := h.tt.RightClick("renamed-folder"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("删除"); err != nil {
		t.Fatalf("选择删除失败: %v", err)
	}
	h.tt.Frame()
	if h.a.fileDelete == nil {
		t.Fatal("删除确认弹窗未打开")
	}
	h.click("删除")

	h.pump("删除成功", func() bool {
		for _, e := range tb.entries {
			if e.Name == "renamed-folder" {
				return false
			}
		}
		return true
	})
}

// 名称里带 / 或者是 ..，会把文件建到或移到别的目录，必须拒绝。
func TestFileNameValidation(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "../x", "a/b", "x\x00"} {
		if validateFileName(bad) == nil {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
	for _, ok := range []string{"a.txt", "新建文件夹", ".bashrc", "a..b"} {
		if err := validateFileName(ok); err != nil {
			t.Errorf("%q 应被接受: %v", ok, err)
		}
	}

	h := newHarness(t)
	tb := h.connectDev()
	h.click("文件（⌘J）")
	h.pump("打开文件列表", func() bool { return tb.dir != "" })
	h.click("新建文件夹")
	h.tt.Frame()
	h.a.fileOp.input = "../escape"
	h.tt.Frame()
	h.click("创建")
	h.tt.Frame()
	if !strings.Contains(h.a.fileOp.err, "/") || h.a.fileOp.busy {
		t.Fatalf("应拒绝带 / 的名称: err=%q", h.a.fileOp.err)
	}
}

func TestKeyboardInteractiveChallengeFlow(t *testing.T) {
	otpServer, err := devsshd.Start(devsshd.Options{
		KeyboardInteractive: true,
		KeyboardQuestions:   []string{"OTP 验证码: "},
		KeyboardAnswers:     []string{"654321"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { otpServer.Close() })

	host := hosts.Host{
		ID:      "otp-host",
		Name:    "otp-server",
		Address: "127.0.0.1",
		Port:    otpServer.Addr().Port,
		User:    "tester",
		Group:   defaultGroup,
	}
	h := newHarness(t, host)

	// 没有保存任何凭据：先像普通主机一样问密码（服务器拒绝密码方式），再弹出验证码。
	connect := func() {
		h.click("otp-server")
		for step := 0; h.a.currentChallenge() == nil; step++ {
			if step > 3 {
				t.Fatal("没有出现多步验证")
			}
			h.pump("下一步", func() bool { return h.a.auth.open || len(h.a.prompts) > 0 || h.a.currentChallenge() != nil })
			switch {
			case len(h.a.prompts) > 0:
				h.click("信任并连接")
			case h.a.auth.open:
				h.a.auth.secret = "irrelevant"
				h.tt.Frame()
				h.click("连接")
			}
			h.tt.Frame()
		}
	}

	// 第一次：用户取消验证码，提示已取消，不弹密码错误。
	connect()
	h.click("取消")
	h.pump("取消提示", func() bool { return strings.Contains(h.a.notice, "已取消连接 otp-server") })
	if h.a.auth.open || h.a.currentChallenge() != nil || len(h.a.tabs) != 0 {
		t.Fatalf("取消后不应再有弹窗或会话: auth=%v tabs=%d", h.a.auth.open, len(h.a.tabs))
	}

	// 第二次：填写验证码后连上。
	connect()
	ch := h.a.currentChallenge()
	if len(ch.questions) != 1 || ch.questions[0].Text != "OTP 验证码: " {
		t.Fatalf("unexpected challenge questions: %+v", ch.questions)
	}
	ch.answers[0] = "654321"
	h.tt.Frame()
	h.click("验证")
	h.tt.Frame()
	h.pump("会话建立", func() bool { return len(h.a.tabs) > 0 })
	if h.a.tabs[0].host.Name != "otp-server" {
		t.Fatalf("连接未成功建立: %+v", h.a.tabs)
	}
}

// 在远程标签打开文件抽屉后切到本地终端：抽屉说明原因，不是一块空白。
func TestFilesDrawerOnLocalTabExplains(t *testing.T) {
	h := newHarness(t)
	h.connectDev()
	h.click("文件（" + shortcutText("J") + "）")
	h.pump("文件列表", func() bool { return h.tt.HasText("go.mod") })
	h.click("本地终端")
	h.tt.Frame()
	if !h.tt.HasText("文件抽屉用于浏览远程主机的文件") {
		t.Fatalf("本地标签的文件抽屉应说明原因: %q", h.tt.Texts())
	}
}
