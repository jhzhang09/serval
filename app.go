package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"serval/core/hosts"
	"serval/core/keys"
	"serval/core/sshconn"
	"serval/core/vault"
	"serval/design"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// app 是窗口显示的全部状态。所有字段只在主线程读写，后台 goroutine 必须通过 post 回到主线程。
type app struct {
	post func(func())

	// vault 是全部用户数据（主机、私钥、密码、同步设置）的唯一来源，hosts、keys、settings 是它解密后的副本。
	vault      *vault.Vault
	hosts      []hosts.Host
	keys       []keys.Key
	settings   settings
	hostsError string
	// local 是只属于本机的设置（device.json），不进保险库、不参与同步。
	local      deviceSettings
	devicePath string
	// device 是系统安全存储，只用来“在这台设备上记住”保险库密钥。
	device     deviceStore
	knownHosts []string
	// localUser 是本机用户名。主机和跳板机没写用户名时用它，和 OpenSSH 一致。
	localUser string

	sidebarOpen bool
	filesOpen   bool

	tabs   []*tab
	active int
	nextID int
	// arrived 记录刚出现的窗格，它们的卡片下一帧从起始姿态弹入（见 design.Arrival）。
	arrived map[int]bool

	// connecting 记录正在连接的主机和取消函数，同一台主机不重复发起连接。
	connecting map[string]context.CancelFunc
	// notice 显示最近一次连接失败的原因。
	notice string

	// toasts 是等待显示的提示。事件发生在主线程的各个地方，而显示提示需要视图的
	// Context，所以先排队，下一次构建视图时统一交给 MyGo。
	toasts []ui.Toast

	prompts []*hostKeyPrompt
	asking  bool

	auth authSheet
	form hostForm
	// deleting 是等待确认删除的主机。
	deleting      *hosts.Host
	keyPanel      keyPanel
	settingsPanel settingsPanel
	setup         setupSheet
	unlock        unlockState
	pal           palette
	// pickFile 弹出文件选择框，返回选中的路径（取消时为空）。测试里替换掉。
	pickFile func() (string, error)
}

// hostKeyPrompt 是等待用户回答的未知主机询问。
type hostKeyPrompt struct {
	info   sshconn.HostKeyInfo
	answer chan bool
}

// credential 是链路上某一台机器的密码和私钥口令，只在一次连接尝试中有效。
// 内容可能是用户刚输入的，也可能是保险库里保存的（stored 为 true）。
type credential struct {
	password, passphrase string
	// storedPassword、storedPassphrase 表示内容来自保险库，失败时要告诉用户“保存的密码已失效”。
	storedPassword, storedPassphrase bool
	// savePassword、savePassphrase 表示用户勾选了记住，连接成功后再存进保险库。
	// 不在输入时就存：输错的密码不该留下来。
	savePassword, savePassphrase bool
}

// authSheet 是向用户询问密码或私钥口令的面板。
//
// 经跳板机连接时，链路上每台机器的密码可能都不同，所以要记住是在为哪一跳询问（hop），
// 以及前面几跳已经输入过的内容（creds），重试时不必让用户再输一遍。
type authSheet struct {
	open bool
	// target 是要连接的主机，host 是这次询问的对象（目标主机本身，或者途中的跳板机）。
	target hosts.Host
	host   hosts.Host
	// hop 是 host 在链路中的位置：跳板机按顺序为 0、1、…，目标主机等于跳板机数量。
	hop    int
	via    bool
	creds  map[int]credential
	secret string
	// passphrase 为 true 时询问的是私钥口令，否则是登录密码。
	// 两者不能混用：把私钥口令当密码发给服务器会造成泄露。
	passphrase bool
	message    string
	// remember 对应“记住密码”勾选框，默认勾选。
	remember bool
	// fresh 表示面板刚打开，这一帧从起始姿态弹出。
	fresh bool
}

// tab 是一个会话标签：一条 SSH 连接，加上其上的终端窗格和文件抽屉。
type tab struct {
	id     int
	host   hosts.Host
	client *sshconn.Client
	root   *pane
	focus  *pane
	stop   context.CancelFunc
	// splitting 表示有一个拆分请求正在打开新的 shell。
	splitting bool

	// latency 是最近一次保活的往返时间；stalled 表示服务器长时间没有响应。
	latency time.Duration
	stalled bool

	files      *sshconn.Files
	filesBusy  bool
	filesError string
	dir        string
	entries    []sshconn.Entry
	followed   string
	// listGen 用来丢弃过期的目录结果：用户连续切换目录时，只有最后一次请求生效。
	listGen int
}

// pane 是窗格树的节点：叶子节点持有一个终端，内部节点持有两个子窗格。
type pane struct {
	id    int
	term  *terminal.Terminal
	ended bool

	first, second *pane
	vertical      bool
	percent       float32
}

func newApp() *app {
	return &app{sidebarOpen: true, connecting: map[string]context.CancelFunc{}, arrived: map[int]bool{}}
}

func (a *app) current() *tab {
	if a.active < 0 || a.active >= len(a.tabs) {
		return nil
	}
	return a.tabs[a.active]
}

func (a *app) isDark(systemDark bool) bool {
	switch a.settings.Appearance {
	case appearanceLight:
		return false
	case appearanceDark:
		return true
	}
	return systemDark
}

// open 打开主机：已有会话时切换过去，否则新建连接。
func (a *app) open(h hosts.Host) {
	for i, t := range a.tabs {
		if t.host.ID == h.ID {
			a.active = i
			if _, ok := a.hostByID(h.ID); ok {
				a.recordRecentHost(h.ID)
			}
			return
		}
	}
	a.connect(h, nil)
}

// connect 在后台建立连接，有跳板机时逐跳连接。creds 是用户刚输入的密码或口令，
// 按链路位置（见 authSheet.hop）索引，只用于这一次连接尝试；没输入的部分用保险库里保存的。
func (a *app) connect(h hosts.Host, creds map[int]credential) {
	if _, busy := a.connecting[h.ID]; busy {
		return
	}
	// 调用方手里的可能是旧副本（例如标签里的主机，之后又保存了密码），以保险库里的为准。
	if fresh, ok := a.hostByID(h.ID); ok {
		h = fresh
	}
	a.notice = ""
	route, err := hosts.ResolveRoute(h, a.hosts, a.localUser)
	if err != nil {
		a.notice = fmt.Sprintf("无法连接 %s：%v", h.Name, err)
		return
	}
	// 代理只用于第一跳。目标主机没设代理时，沿用第一台跳板机自己的代理设置：
	// 那台跳板机单独连接时要走这个代理，经由它连接时自然也要。
	proxySpec := h.Proxy
	if proxySpec == "" && len(route) > 0 {
		proxySpec = route[0].Proxy
	}
	proxy, err := sshconn.ParseProxy(proxySpec)
	if err != nil {
		a.notice = fmt.Sprintf("无法连接 %s：代理设置有误，%v", h.Name, err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.connecting[h.ID] = cancel
	// 所有跳共用一个主机密钥策略：每台机器都要核对指纹，信任记录也存在同一处。
	policy := &sshconn.HostKeyPolicy{
		ReadOnlyFiles: a.knownHosts,
		Trusted:       vaultTrust{a.vault},
		Confirm:       func(info sshconn.HostKeyInfo) bool { return a.confirmHostKey(ctx, info) },
	}
	chain := append(slices.Clone(route), h) // 链路上的全部机器，最后一台是目标
	used := make(map[int]credential, len(chain))
	configs := make([]sshconn.Config, len(chain))
	for i, hh := range chain {
		key, hasKey := a.keyByID(hh.KeyID)
		if hh.Auth == hosts.AuthKey && !hasKey {
			cancel()
			delete(a.connecting, h.ID)
			a.notice = fmt.Sprintf("%s 选用的私钥已经删除，请编辑这台主机，重新选择私钥。", hh.Name)
			return
		}
		c := creds[i]
		fillSaved(hh, key, hasKey, &c)
		used[i] = c
		configs[i] = hopConfig(hh, key, hasKey, c, a.localUser, policy)
	}
	cfg := configs[len(route)]
	cfg.Jumps = configs[:len(route)]
	cfg.Proxy = proxy

	// cancel 只能在回到主线程之后调用：connectFailed 靠 ctx 区分“用户主动取消”和
	// 真正的连接失败，提前调用会把失败误判成取消，错误提示就丢了。
	go func() {
		client, err := sshconn.Dial(ctx, cfg)
		var shell *sshconn.Shell
		if err == nil {
			shell, err = client.OpenShell(80, 24)
			if err != nil {
				client.Close()
			}
		}
		a.post(func() {
			defer cancel()
			delete(a.connecting, h.ID)
			if err != nil {
				a.connectFailed(ctx, h, route, used, err)
				return
			}
			if ctx.Err() != nil {
				// 用户在连接刚好完成时取消了，不再打开会话。
				go func() {
					shell.Close()
					client.Close()
				}()
				return
			}
			a.rememberCredentials(chain, used)
			a.addTab(h, client, shell)
		})
	}()
}

// hopConfig 生成连接链路上一台机器的配置。认证方式决定尝试哪些登录方法。
func hopConfig(h hosts.Host, key keys.Key, hasKey bool, c credential, localUser string, policy *sshconn.HostKeyPolicy) sshconn.Config {
	user := h.User
	if user == "" {
		user = localUser
	}
	cfg := sshconn.Config{Host: h.Address, Port: h.Port, User: user, UseAgent: true,
		HostKeys: policy, Password: c.password, KeyPassphrase: c.passphrase}
	if hasKey && h.Auth != hosts.AuthPassword {
		cfg.KeyPEM = []byte(key.PrivateKey)
	}
	switch h.Auth {
	case hosts.AuthPassword:
		cfg.UseAgent, cfg.KeyPassphrase = false, ""
	case hosts.AuthKey:
		cfg.UseAgent, cfg.Password = false, ""
	}
	return cfg
}

// fillSaved 用保险库里保存的内容补上用户这次没输入的密码和口令。
func fillSaved(h hosts.Host, key keys.Key, hasKey bool, c *credential) {
	if c.password == "" && h.Auth != hosts.AuthKey && h.Password != "" {
		c.password, c.storedPassword = h.Password, true
	}
	if c.passphrase == "" && hasKey && h.Auth != hosts.AuthPassword && key.Passphrase != "" {
		c.passphrase, c.storedPassphrase = key.Passphrase, true
	}
}

// rememberCredentials 在连接成功后处理“记住密码”：勾选了的存进保险库（密码存在主机上，
// 口令存在私钥上）；这次手动输入却没勾选的，清掉保险库里的旧内容，取消勾选就是不想让它记住。
// 临时写在跳板机列表里的地址（不是保存的主机）没有地方存，跳过。
func (a *app) rememberCredentials(chain []hosts.Host, used map[int]credential) {
	var changes []vault.Change
	for i, hh := range chain {
		c := used[i]
		saved, isSaved := a.hostByID(hh.ID)
		if !isSaved {
			continue
		}
		if c.password != "" && !c.storedPassword {
			if next := choose(c.savePassword, c.password); next != saved.Password {
				saved.Password = next
				changes = append(changes, hostChange(saved))
			}
		}
		if key, ok := a.keyByID(saved.KeyID); ok && c.passphrase != "" && !c.storedPassphrase {
			if next := choose(c.savePassphrase, c.passphrase); next != key.Passphrase {
				key.Passphrase = next
				changes = append(changes, keyChange(key))
			}
		}
	}
	if len(changes) == 0 || a.vault.State() == vault.Unprotected {
		return // 保存秘密前一定已经设置了主密码（见 submitSecret），这里只是兜底，不把秘密写进未加密的保险库
	}
	if err := a.apply(changes...); err != nil {
		a.toast(ui.Toast{Title: "没能保存密码", Description: err.Error(), Type: "error"})
	}
}

func choose(keep bool, value string) string {
	if keep {
		return value
	}
	return ""
}

func (a *app) cancelConnect(id string) {
	if cancel, ok := a.connecting[id]; ok {
		cancel()
	}
}

func (a *app) connectFailed(ctx context.Context, h hosts.Host, route []hosts.Host, creds map[int]credential, err error) {
	if ctx.Err() != nil {
		return // 用户主动取消，无需提示
	}
	// 先弄清楚是哪一台机器出的错：跳板机的错误包在 HopError 里，其余都属于目标主机。
	hop, who := len(route), h
	var hopErr *sshconn.HopError
	if errors.As(err, &hopErr) && hopErr.Hop < len(route) {
		hop, who = hopErr.Hop, route[hopErr.Hop]
	}
	c := creds[hop]
	var changed *sshconn.HostKeyChangedError
	switch {
	case errors.As(err, &changed):
		// 最需要讲清楚的安全事件：说明风险，并给出确实需要时的处理办法。
		a.notice = fmt.Sprintf("%s 的主机密钥和上次不同，可能有人冒充这台服务器，已拒绝连接。"+
			"如果服务器确实重装过，右键 %s 选择“重置主机密钥”，下次连接时重新核对指纹"+
			"（记录来自 ~/.ssh/known_hosts 的，需要在那个文件里删掉 %s 的旧记录）。", who.Name, who.Name, changed.Host)
	case errors.Is(err, sshconn.ErrHostKeyRejected):
		a.notice = fmt.Sprintf("没有连接 %s：未信任它的主机密钥。", who.Name)
	case errors.Is(err, sshconn.ErrKeyEncrypted):
		a.askSecret(h, who, hop, creds, true, "这把私钥已加密，输入口令后继续。")
	case errors.Is(err, sshconn.ErrBadPassphrase):
		message := "口令不正确，请重新输入。"
		if c.storedPassphrase {
			message = "保存的口令不正确，请重新输入。"
		}
		a.askSecret(h, who, hop, creds, true, message)
	case errors.Is(err, sshconn.ErrNoAuthMethod):
		a.askSecret(h, who, hop, creds, false, "")
	case errors.Is(err, sshconn.ErrAuthFailed):
		// 这次带了密码还失败，说明是密码本身不对；否则只是还没输入过。
		message := ""
		switch {
		case c.storedPassword:
			message = "保存的密码不正确，请重新输入。"
		case c.password != "":
			message = "密码不正确，请重新输入。"
		}
		a.askSecret(h, who, hop, creds, false, message)
	default:
		a.notice = err.Error()
	}
}

// askSecret 为链路上的一台机器询问密码或口令。之前几跳的内容原样带着；
// 这一跳要问的那一项（已经证明不对或者缺失）丢掉，另一项保留。
func (a *app) askSecret(target, who hosts.Host, hop int, creds map[int]credential, passphrase bool, message string) {
	kept := maps.Clone(creds)
	c := kept[hop]
	if passphrase {
		c.passphrase, c.storedPassphrase = "", false
	} else {
		c.password, c.storedPassword = "", false
	}
	kept[hop] = c
	a.auth = authSheet{open: true, fresh: true, target: target, host: who, hop: hop, via: who.ID != target.ID,
		creds: kept, passphrase: passphrase, message: message, remember: true}
}

// submitSecret 用输入的密码或口令重新连接，然后立刻清空面板。勾选了“记住”而保险库还没有
// 主密码时，先设置主密码：秘密不能写进未加密的保险库。
func (a *app) submitSecret() {
	if _, saved := a.hostByID(a.auth.host.ID); a.auth.remember && saved && a.vault.State() == vault.Unprotected {
		a.requireProtection(a.submitSecret)
		return
	}
	creds := maps.Clone(a.auth.creds)
	if creds == nil {
		creds = map[int]credential{}
	}
	c := creds[a.auth.hop]
	if a.auth.passphrase {
		c.passphrase, c.savePassphrase = a.auth.secret, a.auth.remember
	} else {
		c.password, c.savePassword = a.auth.secret, a.auth.remember
	}
	creds[a.auth.hop] = c
	target := a.auth.target
	a.auth = authSheet{}
	a.connect(target, creds)
}

func (a *app) addTab(h hosts.Host, client *sshconn.Client, shell *sshconn.Shell) {
	a.nextID++
	ctx, stop := context.WithCancel(context.Background())
	t := &tab{id: a.nextID, host: h, client: client, stop: stop}
	leaf, err := a.newPane(t, shell)
	if err != nil {
		stop()
		shell.Close()
		client.Close()
		a.notice = fmt.Sprintf("启动终端失败: %v", err)
		return
	}
	t.root, t.focus = leaf, leaf
	a.arrived[leaf.id] = true
	a.tabs = append(a.tabs, t)
	a.active = len(a.tabs) - 1
	if _, ok := a.hostByID(h.ID); ok {
		a.recordRecentHost(h.ID)
	}
	go a.watchLatency(ctx, t)
	if a.filesOpen {
		a.ensureFiles(t)
	}
}

func (a *app) newPane(t *tab, shell *sshconn.Shell) (*pane, error) {
	a.nextID++
	p := &pane{id: a.nextID}
	term, err := terminal.New(terminal.Options{
		Conn:      shell,
		Font:      design.TerminalFont(),
		Theme:     a.terminalTheme(false),
		DarkTheme: a.terminalTheme(true),
		Padding:   terminal.Padding{X: 10, Y: 8},
		OnExit: func(int) {
			a.post(func() { a.paneEnded(t, p) })
		},
		OnCopy: func(_ string, by terminal.CopySource) {
			a.post(func() { a.copied(by) })
		},
	})
	if err != nil {
		return nil, err
	}
	p.term = term
	return p, nil
}

// terminalTheme 返回 windowDark 外观的窗口里终端应使用的配色。
// 用户手动指定外观时，无论窗口实际是明是暗都用同一套。
func (a *app) terminalTheme(windowDark bool) *terminal.Theme {
	switch a.settings.Appearance {
	case appearanceLight:
		return design.Terminal(false)
	case appearanceDark:
		return design.Terminal(true)
	}
	return design.Terminal(windowDark)
}

// refreshTerminals 让所有运行中的终端应用最新的配色设置。
func (a *app) refreshTerminals() {
	for _, t := range a.tabs {
		t.root.each(func(p *pane) {
			p.term.SetTheme(a.terminalTheme(false), a.terminalTheme(true))
		})
	}
}

// watchLatency 定期保活并记录延迟，直到会话关闭。
//
// 每次通知界面都会重建并重绘整个窗口，所以只在显示出来的内容真正变化时才通知：
// 实测 2 个会话每 5 秒各通知一次，空闲 CPU 从 0.15% 涨到 1.15%，而延迟显示其实一直没变。
func (a *app) watchLatency(ctx context.Context, t *tab) {
	const interval = 5 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	shown, wasStalled, first := "", false, true
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 2*interval)
		rtt, err := t.client.Ping(pingCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		stalled := err != nil
		text := shown
		if !stalled {
			text = formatLatency(rtt)
		}
		if first || stalled != wasStalled || text != shown {
			first, shown, wasStalled = false, text, stalled
			a.post(func() {
				t.stalled = stalled
				if !stalled {
					t.latency = rtt
				}
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// each 遍历所有叶子窗格。
func (p *pane) each(fn func(*pane)) {
	if p.term != nil {
		fn(p)
		return
	}
	p.first.each(fn)
	p.second.each(fn)
}

// leaves 返回所有叶子窗格的数量。
func (p *pane) leaves() int {
	n := 0
	p.each(func(*pane) { n++ })
	return n
}

// split 在当前窗格旁边打开一个新终端，复用同一条 SSH 连接。
//
// 同一个标签同时只处理一个拆分请求：连点按钮不应一下子多出好几个窗格，
// 而且上一个新窗格还没出现时，“当前窗格”也还没确定。
func (a *app) split(t *tab, vertical bool) {
	if t.splitting {
		return
	}
	t.splitting = true
	go func() {
		shell, err := t.client.OpenShell(80, 24)
		a.post(func() {
			t.splitting = false
			if err != nil {
				a.notice = err.Error()
				return
			}
			if !slices.Contains(a.tabs, t) {
				shell.Close()
				return
			}
			leaf, err := a.newPane(t, shell)
			if err != nil {
				shell.Close()
				a.notice = fmt.Sprintf("启动终端失败: %v", err)
				return
			}
			// 叶子节点的指针必须保持不变：它的退出回调捕获了这个指针。
			a.nextID++
			target := t.focus
			t.replace(target, &pane{id: a.nextID, first: target, second: leaf, vertical: vertical, percent: 50})
			t.focus = leaf
			a.arrived[leaf.id] = true
		})
	}()
}

// replace 把树里的 old 换成 replacement。
func (t *tab) replace(old, replacement *pane) {
	if t.root == old {
		t.root = replacement
		return
	}
	var walk func(*pane)
	walk = func(p *pane) {
		if p.term != nil {
			return
		}
		switch old {
		case p.first:
			p.first = replacement
		case p.second:
			p.second = replacement
		default:
			walk(p.first)
			walk(p.second)
		}
	}
	walk(t.root)
}

// closeFocused 关闭当前窗格；最后一个窗格关闭时关闭整个标签。
func (a *app) closeFocused(t *tab) {
	if t.root == t.focus {
		a.closeTab(t)
		return
	}
	closing := t.focus
	var parent *pane
	var walk func(*pane)
	walk = func(p *pane) {
		if p.term != nil {
			return
		}
		if p.first == closing || p.second == closing {
			parent = p
			return
		}
		walk(p.first)
		walk(p.second)
	}
	walk(t.root)
	if parent == nil {
		return
	}
	sibling := parent.first
	if sibling == closing {
		sibling = parent.second
	}
	t.replace(parent, sibling)
	// 焦点落到兄弟分支的第一个叶子。
	t.focus = nil
	sibling.each(func(p *pane) {
		if t.focus == nil {
			t.focus = p
		}
	})
	term := closing.term
	go term.Close()
}

func (a *app) closeTab(t *tab) {
	i := slices.Index(a.tabs, t)
	if i < 0 {
		return
	}
	a.tabs = slices.Delete(a.tabs, i, i+1)
	if a.active >= len(a.tabs) {
		a.active = max(0, len(a.tabs)-1)
	}
	// 断开网络连接可能阻塞，不放在主线程。
	go t.close()
}

func (t *tab) close() {
	t.stop()
	t.root.each(func(p *pane) { p.term.Close() })
	if t.files != nil {
		t.files.Close()
	}
	t.client.Close()
}

// closeAll 在窗口关闭时取消进行中的连接并断开所有会话。
func (a *app) closeAll() {
	for _, cancel := range a.connecting {
		cancel()
	}
	for _, t := range a.tabs {
		go t.close()
	}
	a.tabs = nil
}

// reconnect 用同样的主机信息重新连接，替换已断开的标签。
func (a *app) reconnect(t *tab) {
	h := t.host
	a.closeTab(t)
	a.connect(h, nil)
}

// confirmHostKey 在连接 goroutine 上阻塞，直到用户回答或连接被取消。
func (a *app) confirmHostKey(ctx context.Context, info sshconn.HostKeyInfo) bool {
	answer := make(chan bool, 1)
	prompt := &hostKeyPrompt{info: info, answer: answer}
	a.post(func() { a.prompts = append(a.prompts, prompt) })
	select {
	case v := <-answer:
		return v
	case <-ctx.Done():
		a.post(func() {
			a.prompts = slices.DeleteFunc(a.prompts, func(p *hostKeyPrompt) bool { return p == prompt })
		})
		return false
	}
}

// toggleFiles 打开或收起文件抽屉。SFTP 通道到需要时才打开，不用文件的会话不占这份资源。
func (a *app) toggleFiles() {
	a.filesOpen = !a.filesOpen
	if t := a.current(); a.filesOpen && t != nil {
		a.ensureFiles(t)
	}
}

func (a *app) ensureFiles(t *tab) {
	if t.files != nil || t.filesBusy {
		return
	}
	t.filesBusy = true
	go func() {
		files, err := t.client.OpenFiles()
		home := ""
		if err == nil {
			home, err = files.Home()
			if err != nil {
				files.Close()
				files = nil
			}
		}
		a.post(func() {
			t.filesBusy = false
			if err != nil {
				t.filesError = err.Error()
				return
			}
			if !slices.Contains(a.tabs, t) {
				go files.Close()
				return
			}
			t.files = files
			a.openDir(t, home)
		})
	}()
}

// openDir 在后台读取目录，主线程只应用最后一次请求的结果。
func (a *app) openDir(t *tab, dir string) {
	if t.files == nil {
		return
	}
	t.listGen++
	gen := t.listGen
	files := t.files
	go func() {
		entries, err := files.List(dir)
		a.post(func() {
			if gen != t.listGen {
				return
			}
			if err != nil {
				t.filesError = err.Error()
				return
			}
			t.dir, t.entries, t.filesError = dir, entries, ""
		})
	}()
}

// followCwd 让文件抽屉跟随终端的工作目录（远端 shell 通过 OSC 7 上报）。
func (a *app) followCwd(t *tab) {
	if t.files == nil || t.focus == nil || t.focus.term == nil {
		return
	}
	cwd := t.focus.term.Dir()
	if cwd == "" || cwd == t.followed || !path.IsAbs(cwd) {
		return
	}
	t.followed = cwd
	a.openDir(t, cwd)
}

// groups 按主机出现的顺序返回分组。
func (a *app) groups() []string {
	var names []string
	for _, h := range a.hosts {
		if !slices.Contains(names, h.Group) {
			names = append(names, h.Group)
		}
	}
	return names
}

func (t *tab) title() string { return t.host.Name }

// shortPath 把远程路径缩短到适合状态栏显示的长度。
func shortPath(p string) string {
	const limit = 3
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) <= limit {
		return p
	}
	return "…/" + filepath.Join(parts[len(parts)-limit:]...)
}

func formatLatency(d time.Duration) string {
	if d < time.Millisecond {
		return "<1 ms"
	}
	return fmt.Sprintf("%d ms", d.Milliseconds())
}

func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, suffix := float64(n), "KMGTPE"
	i := -1
	for value >= unit && i < len(suffix)-1 {
		value /= unit
		i++
	}
	return fmt.Sprintf("%.1f %cB", value, suffix[i])
}

// toast 排队一条提示，下一次构建视图时显示。
func (a *app) toast(t ui.Toast) {
	a.toasts = append(a.toasts, t)
}

// flushToasts 把排队的提示交给 MyGo 显示。
func (a *app) flushToasts(c *ui.Context) {
	for _, t := range a.toasts {
		c.AddToast(t)
	}
	a.toasts = nil
}

// copied 在终端复制内容后给出提示。远端程序写剪贴板（OSC 52）要单独说明：
// 用户没有动手，剪贴板却变了，这件事应该让用户知道。
func (a *app) copied(by terminal.CopySource) {
	if by == terminal.CopyProgram {
		a.toast(ui.Toast{ID: "copy", Title: "远端程序写入了剪贴板"})
		return
	}
	a.toast(ui.Toast{ID: "copy", Title: "已复制"})
}

// paneEnded 处理窗格里的 shell 退出。
//
// 用户主动关闭窗格或标签时，终端也会退出，但这时窗格已经从树里移除，不应再弹提示；
// 只有“自己结束”的会话才提示。整个标签的窗格都结束时，提示里给出“重新连接”。
func (a *app) paneEnded(t *tab, p *pane) {
	if !slices.Contains(a.tabs, t) || !t.root.contains(p) {
		return
	}
	p.ended = true
	allEnded := true
	t.root.each(func(q *pane) { allEnded = allEnded && q.ended })
	if !allEnded {
		a.toast(ui.Toast{Title: t.host.Name + " 的一个窗格已结束"})
		return
	}
	a.toast(ui.Toast{
		ID:       fmt.Sprintf("ended:%d", t.id),
		Title:    t.host.Name + " 的会话已结束",
		Action:   "重新连接",
		OnAction: func() { a.reconnect(t) },
		Timeout:  8 * time.Second,
	})
}

// contains 报告 target 是否在这棵窗格树里。
func (p *pane) contains(target *pane) bool {
	found := false
	p.each(func(q *pane) { found = found || q == target })
	return found
}
