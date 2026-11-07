package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"serval/core/hosts"
	"serval/core/vault"
	"serval/design"
	"serval/internal/devsshd"

	"github.com/egoist/mygo"
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
	server, err := devsshd.Start(devsshd.Options{Password: testPassword})
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
	h.a.pickFile = func() (string, error) { return "", errors.New("测试没有设置 pickFile") }
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

func TestAddHostValidatesJumpsAndProxy(t *testing.T) {
	h := newHarness(t)
	h.click("添加主机")
	d := &h.a.form
	d.address, d.user, d.name = "10.2.2.2", "ops", "inner"
	for _, c := range []struct{ jumps, proxy, want string }{
		{"gw:abc", "", "跳板机"},
		{"inner", "", "循环"},
		{"", "ftp://x:1", "代理"},
		{"", "socks5://u:secret@127.0.0.1:1080", "不能带密码"},
	} {
		d.jumps, d.proxy = c.jumps, c.proxy
		h.tt.Frame()
		h.click("添加")
		if !strings.Contains(d.err, c.want) {
			t.Errorf("jumps=%q proxy=%q 错误 = %q，期望包含 %q", c.jumps, c.proxy, d.err, c.want)
		}
	}
	d.jumps, d.proxy = " dev , ops@gw:2222 ", "socks5://127.0.0.1:1080"
	h.tt.Frame()
	h.click("添加")
	if saved := h.host("inner"); saved.ProxyJump != "dev,ops@gw:2222" || saved.Proxy != "socks5://127.0.0.1:1080" {
		t.Fatalf("saved=%+v add.err=%q", saved, d.err)
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
	if !h.tt.HasText("连接已结束") {
		t.Fatalf("界面未显示连接已结束，界面文字: %q", h.tt.Texts())
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
