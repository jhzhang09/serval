package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"serval/core/hosts"
	"serval/core/keys"
	"serval/core/sshconn"
	"serval/core/totp"
	"serval/core/vault"
	"serval/design"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// app 是窗口显示的全部状态。所有字段只在主线程读写，后台 goroutine 必须通过 post 回到主线程。
type app struct {
	post func(func())

	// vault 是全部用户数据（主机、私钥、密码、代码片段、同步设置）的唯一来源，
	// hosts、keys、snippets、settings 是它解密后的副本。
	vault      *vault.Vault
	hosts      []hosts.Host
	keys       []keys.Key
	proxies    []hosts.Proxy
	snippets   []snippet
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

	// pingInterval 和 reconnectBaseDelay 用于测试调节保活和重连退避时间，零值使用默认值。
	pingInterval time.Duration
	// restoreActive 是恢复会话时上次激活的标签位置（从 1 开始），见 insertRestoredTab。
	restoreActive      int
	reconnectBaseDelay time.Duration

	sidebarOpen bool
	filesOpen   bool
	// hostFilter 是侧栏过滤框的内容，只留在内存里，重启后清空。
	hostFilter string

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

	fileOp     fileOpState
	fileDelete *fileDeleteState

	auth authSheet
	form hostForm
	// deleting 是等待确认删除的主机。
	deleting      *hosts.Host
	keyPanel      keyPanel
	proxyPanel    proxyPanel
	snippetPanel  snippetPanel
	settingsPanel settingsPanel
	setup         setupSheet
	unlock        unlockState
	pal           palette
	askingLock    bool
	// idleTime 返回用户多久没有使用键盘和鼠标（系统级，正式运行时是 mygo.Power.IdleTime）。
	// 自动锁定按它判断：不能用“界面最后一次重绘”的时间，终端输出、动画、定时器都会触发重绘。
	idleTime func() time.Duration
	// 系统文件选择框，返回选中的路径（取消时为空）。正式运行时由 installSystemDialogs 设置，测试里替换掉。
	// chooseKeyFile 选私钥（默认打开 ~/.ssh）；chooseUploadFile 选要上传的文件；chooseSavePath 选下载保存的位置。
	chooseKeyFile    func() (string, error)
	chooseUploadFile func() (string, error)
	// chooseUploadFiles 选要用 rz 上传的文件（可多选）；chooseFolder 选 sz 收到的文件存放的文件夹。
	chooseUploadFiles func() ([]string, error)
	chooseFolder      func() (string, error)
	chooseSavePath    func(defaultName string) (string, error)
	// pendingUploads 是等待确认替换远端同名文件的上传，一次拖进多个文件时逐个询问。
	pendingUploads []*pendingUpload
	// pendingFolders 是等用户决定如何处理同名文件的文件夹上传。
	pendingFolders []*folderPlan
	// challenges 是等待用户回答的多步验证（keyboard-interactive）询问，依次显示。
	// 经跳板机连接时，跳板机和目标主机可能先后都要求验证码。
	challenges []*challengeSheet
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

// challengeSheet 是服务器要求多步验证时的弹窗状态。
type challengeSheet struct {
	open        bool
	fresh       bool
	host        hosts.Host
	name        string
	instruction string
	questions   []sshconn.Question
	answers     []string
	answerChan  chan challengeResult
}

type challengeResult struct {
	answers []string
	err     error
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
	// local 为真表示运行在本机伪终端的本地 shell。
	local bool

	// restoreSlot 是恢复会话时这个标签在上次会话里的位置（从 1 开始），普通标签为 0。
	restoreSlot int

	reconnecting     bool
	reconnectAttempt int
	reconnectCancel  context.CancelFunc

	files      *sshconn.Files
	filesBusy  bool
	filesError string
	// filesWaiting 是等文件通道打开后再执行的操作（例如通道还没打开时拖进来的文件）。
	filesWaiting []func()
	dir          string
	entries      []sshconn.Entry
	followed     string
	// listGen 用来丢弃过期的目录结果：用户连续切换目录时，只有最后一次请求生效。
	listGen int

	transfers []*fileTransfer
}

// pane 是窗格树的节点：叶子节点持有一个终端，内部节点持有两个子窗格。
type pane struct {
	id    int
	term  *terminal.Terminal
	ended bool
	// shell 是这个窗格的远程 shell（本地终端为 nil），用来在终端结束时判断是断线还是远端自己退出。
	shell *sshconn.Shell
	// zconn 位于 shell 和终端之间，处理 rz/sz；zmodem 是正在进行的 rz/sz 传输，界面显示进度。
	zconn  *zmodemConn
	zmodem *zmodemTransfer

	searchOpen  bool
	searchQuery string
	// focusSearch、focusTerm 让下一帧把键盘焦点交给查找框或终端：打开查找后要立刻能输入搜索词，
	// 否则按 ⌘F 之后打的字会落进终端、发给远端 shell；关闭查找后焦点回到终端。
	focusSearch, focusTerm bool
	searchStatus           terminal.SearchStatus
	// fileDrag 表示上一帧有文件从别的程序拖到这个窗格上方，这时铺一层接收拖放的透明层。
	fileDrag bool

	first, second *pane
	vertical      bool
	percent       float32
}

func newApp() *app {
	a := &app{
		sidebarOpen: true,
		connecting:  map[string]context.CancelFunc{},
		arrived:     map[int]bool{},
	}
	return a
}

// checkAutoLock 在用户闲置超过设定时间后锁定保险库。由 startAutoLock 定时调用。
func (a *app) checkAutoLock() {
	if a.local.AutoLockMinutes <= 0 || a.idleTime == nil || a.vault == nil || a.vault.State() != vault.Unlocked {
		return
	}
	if a.idleTime() >= time.Duration(a.local.AutoLockMinutes)*time.Minute {
		a.lockVault()
	}
}

func (a *app) askLockVault() {
	if a.vault == nil || a.vault.State() != vault.Unlocked {
		return
	}
	if len(a.tabs) > 0 {
		a.askingLock = true
		return
	}
	a.lockVault()
}

// lockVault 关闭全部会话并锁定保险库。内存里不留下任何秘密和主机信息：
// 包括还没提交的密码询问（a.auth 里可能有用户刚输入的密码）、表单、待确认的操作。
func (a *app) lockVault() {
	remembered := a.isRememberedOnDevice()
	a.closeAll()
	if a.vault != nil {
		a.vault.Lock()
	}
	a.hosts = nil
	a.keys = nil
	a.proxies = nil
	a.snippets = nil
	a.proxyPanel = proxyPanel{}
	a.snippetPanel = snippetPanel{}
	a.settings = settings{}
	a.hostsError = ""
	a.notice = ""
	a.auth = authSheet{}
	a.form = hostForm{}
	a.deleting = nil
	a.prompts, a.asking = nil, false
	// 验证码对话框里可能有用户输了一半的答案；逐个取消，让等待的连接 goroutine 立即结束。
	for _, ch := range a.challenges {
		ch.answerChan <- challengeResult{err: errChallengeCancelled} // channel 有 1 个缓冲，每个询问只发送一次
	}
	a.challenges = nil
	a.keyPanel = keyPanel{}
	a.setup = setupSheet{}
	a.settingsPanel = settingsPanel{}
	a.pal = palette{}
	a.unlock = unlockState{remember: remembered}
	a.askingLock = false
	a.toast(ui.Toast{Title: "保险库已锁定"})
}

func (a *app) startAutoLock() {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if a.post != nil {
				a.post(func() {
					a.checkAutoLock()
				})
			}
		}
	}()
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

// isSavedHost 判断主机是否已保存在保险库中。临时快速连接的主机不在保险库里。
func (a *app) isSavedHost(id string) bool {
	_, ok := a.hostByID(id)
	return ok
}

// open 打开主机：已有会话时切换过去，否则新建连接。
func (a *app) open(h hosts.Host) {
	for i, t := range a.tabs {
		if t.host.ID == h.ID {
			a.active = i
			if _, ok := a.hostByID(h.ID); ok {
				a.recordRecentHost(h.ID)
			}
			a.saveSessionLayout()
			return
		}
	}
	a.connect(h, nil)
}

// connect 在后台建立连接，有跳板机时逐跳连接。creds 是用户刚输入的密码或口令，
// 按链路位置（见 authSheet.hop）索引，只用于这一次连接尝试；没输入的部分用保险库里保存的。
func (a *app) connect(h hosts.Host, creds map[int]credential) {
	a.connectWithLayout(h, creds, nil, 0)
}

// connectWithLayout 在后台建立连接，并按 layout 恢复窗格结构（为 nil 时打开单个窗格）。
// slot 是恢复会话时这个标签在上次会话里的位置（从 1 开始），普通连接为 0。
func (a *app) connectWithLayout(h hosts.Host, creds map[int]credential, layout *savedPane, slot int) {
	if _, busy := a.connecting[h.ID]; busy {
		return
	}
	a.notice = ""
	ctx, cancel := context.WithCancel(context.Background())
	cfg, route, used, chain, err := a.prepareDialConfig(ctx, h, creds)
	if err != nil {
		cancel()
		a.notice = err.Error()
		return
	}
	a.connecting[h.ID] = cancel

	leafCount := 1
	if layout != nil {
		leafCount = layout.leaves()
	}
	if leafCount < 1 {
		leafCount = 1
	}

	// cancel 只能在回到主线程之后调用：connectFailed 靠 ctx 区分“用户主动取消”和
	// 真正的连接失败，提前调用会把失败误判成取消，错误提示就丢了。
	go func() {
		client, err := sshconn.Dial(ctx, cfg)
		var shells []*sshconn.Shell
		if err == nil {
			shells = make([]*sshconn.Shell, leafCount)
			for i := 0; i < leafCount; i++ {
				shells[i], err = client.OpenShell(80, 24)
				if err != nil {
					for j := 0; j < i; j++ {
						shells[j].Close()
					}
					client.Close()
					shells = nil
					break
				}
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
					for _, s := range shells {
						s.Close()
					}
					client.Close()
				}()
				return
			}
			a.rememberCredentials(chain, used)
			if layout != nil {
				a.addTabWithLayout(h, client, shells, layout, slot)
			} else {
				a.addTab(h, client, shells[0])
			}
		})
	}()
}

// prepareDialConfig 准备连接一台主机所需的全部跳板机和认证配置。必须在主线程调用。
func (a *app) prepareDialConfig(ctx context.Context, h hosts.Host, creds map[int]credential) (sshconn.Config, []hosts.Host, map[int]credential, []hosts.Host, error) {
	// 调用方手里的可能是旧副本（例如标签里的主机，之后又保存了密码），以保险库里的为准。
	if fresh, ok := a.hostByID(h.ID); ok {
		h = fresh
	}
	route, err := hosts.ResolveRoute(h, a.hosts, a.localUser)
	if err != nil {
		return sshconn.Config{}, nil, nil, nil, fmt.Errorf("无法连接 %s：%w", h.Name, err)
	}
	// 代理只用于第一跳。目标主机没设代理时，沿用第一台跳板机自己的代理设置：
	// 那台跳板机单独连接时要走这个代理，经由它连接时自然也要。
	proxyOwner := h
	if !hasProxy(h) && len(route) > 0 {
		proxyOwner = route[0]
	}
	proxy, err := a.dialProxy(proxyOwner)
	if err != nil {
		return sshconn.Config{}, nil, nil, nil, fmt.Errorf("无法连接 %s：%w", h.Name, err)
	}

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
			return sshconn.Config{}, nil, nil, nil, fmt.Errorf("%s 选用的私钥已经删除，请编辑这台主机，重新选择私钥", hh.Name)
		}
		c := creds[i]
		fillSaved(hh, key, hasKey, &c)
		used[i] = c
		configs[i] = hopConfig(hh, key, hasKey, c, a.localUser, policy)
		who := hh
		configs[i].Challenge = func(name, instruction string, questions []sshconn.Question) ([]string, error) {
			return a.askChallenge(ctx, who, name, instruction, questions)
		}
	}
	cfg := configs[len(route)]
	cfg.Jumps = configs[:len(route)]
	cfg.Proxy = proxy
	return cfg, route, used, chain, nil
}

// hasProxy 报告主机是否设置了代理（引用的代理配置或旧版直接填写的地址）。
func hasProxy(h hosts.Host) bool { return h.ProxyID != "" || h.Proxy != "" }

// dialProxy 把主机的代理设置转成连接用的配置。没有代理时返回 nil。
// 引用的代理已被删除时报错，而不是悄悄直连：用户可能就是靠代理才能出网。
func (a *app) dialProxy(h hosts.Host) (*sshconn.Proxy, error) {
	if h.ProxyID != "" {
		p, ok := a.proxyByID(h.ProxyID)
		if !ok {
			return nil, fmt.Errorf("%s 选用的代理已经删除，请编辑这台主机，重新选择代理", h.Name)
		}
		kind := p.Kind
		if kind == "" {
			kind = hosts.ProxySOCKS5
		}
		return &sshconn.Proxy{Kind: kind, Addr: net.JoinHostPort(p.Address, strconv.Itoa(p.Port)),
			User: p.User, Password: p.Password}, nil
	}
	proxy, err := sshconn.ParseProxy(h.Proxy)
	if err != nil {
		return nil, fmt.Errorf("代理设置有误，%w", err)
	}
	return proxy, nil
}

// hopConfig 生成连接链路上一台机器的配置。认证方式决定尝试哪些登录方法。
func hopConfig(h hosts.Host, key keys.Key, hasKey bool, c credential, localUser string, policy *sshconn.HostKeyPolicy) sshconn.Config {
	user := h.User
	if user == "" {
		user = localUser
	}
	cfg := sshconn.Config{Host: h.Address, Port: h.Port, User: user, UseAgent: true,
		ForwardAgent: h.ForwardAgent,
		HostKeys:     policy, Password: c.password, KeyPassphrase: c.passphrase}
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
	case errors.Is(err, errChallengeCancelled):
		a.notice = fmt.Sprintf("已取消连接 %s：没有完成多步验证。", who.Name)
	case errors.Is(err, sshconn.ErrNoAuthMethod):
		a.askSecret(h, who, hop, creds, false, "")
	case errors.Is(err, sshconn.ErrAgentUnavailable):
		a.notice = fmt.Sprintf("没有连接 %s：这台主机开启了转发 ssh-agent，但本机没有可用的 ssh-agent。先启动 ssh-agent，或编辑这台主机关掉转发。", who.Name)
	case errors.Is(err, sshconn.ErrAgentForwardDenied):
		a.notice = fmt.Sprintf("%s 拒绝转发 ssh-agent。请在主机设置里关掉“转发 ssh-agent”，或换一台允许转发的服务器。", who.Name)
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
	go a.watchLatency(ctx, t, t.client)
	if a.filesOpen {
		a.ensureFiles(t)
	}
	a.saveSessionLayout()
}

func (a *app) openSearch(t *tab) {
	if t == nil || t.focus == nil {
		return
	}
	p := t.focus
	p.searchOpen, p.focusSearch = true, true
	if p.searchQuery != "" {
		p.term.Search(p.searchQuery)
	}
}

func (a *app) closeSearch(p *pane) {
	p.searchOpen, p.focusTerm = false, true
	p.term.EndSearch()
}

func (a *app) terminalFont() terminal.Font {
	return design.TerminalFontWith(
		a.settings.TerminalFontFamily,
		a.settings.TerminalFontSize,
		a.settings.TerminalLineHeight,
	)
}

func (a *app) terminalCursor() terminal.CursorStyle {
	switch a.settings.TerminalCursor {
	case "bar":
		return terminal.CursorBar
	case "underline":
		return terminal.CursorUnderline
	default:
		return terminal.CursorBlock
	}
}

// terminalScrollback 返回终端插件的 Scrollback 选项（单位是字节；0 表示插件默认的 10 MB）。
func (a *app) terminalScrollback() int {
	if mb := a.settings.TerminalScrollbackMB; mb > 0 {
		return mb << 20
	}
	return 0
}

func (a *app) addTabWithLayout(h hosts.Host, client *sshconn.Client, shells []*sshconn.Shell, layout *savedPane, slot int) {
	a.nextID++
	ctx, stop := context.WithCancel(context.Background())
	t := &tab{id: a.nextID, host: h, client: client, stop: stop, restoreSlot: slot}
	idx := 0
	root, err := a.restorePaneNode(t, shells, &idx, layout)
	if err != nil {
		stop()
		for _, s := range shells {
			s.Close()
		}
		client.Close()
		a.notice = fmt.Sprintf("启动终端失败: %v", err)
		return
	}
	t.root = root
	t.focus = firstLeaf(root)
	root.each(func(p *pane) {
		a.arrived[p.id] = true
	})
	a.insertRestoredTab(t)
	if _, ok := a.hostByID(h.ID); ok {
		a.recordRecentHost(h.ID)
	}
	go a.watchLatency(ctx, t, t.client)
	if a.filesOpen {
		a.ensureFiles(t)
	}
	a.saveSessionLayout()
}

// insertRestoredTab 把标签放进标签栏。恢复会话的连接是并行建立的，先连上的不一定排在前面：
// 按上次会话里的位置插入，只有上次激活的那个标签连上时才切换过去，
// 免得恢复过程中用户刚切换的标签被抢走。普通连接（slot 为 0）照常放到最后并切换过去。
func (a *app) insertRestoredTab(t *tab) {
	if t.restoreSlot == 0 {
		a.tabs = append(a.tabs, t)
		a.active = len(a.tabs) - 1
		return
	}
	at := 0
	for _, other := range a.tabs {
		if other.restoreSlot > 0 && other.restoreSlot < t.restoreSlot {
			at++
		}
	}
	current := a.current()
	a.tabs = slices.Insert(a.tabs, at, t)
	switch {
	case t.restoreSlot == a.restoreActive || current == nil:
		a.active = at
	default:
		a.active = slices.Index(a.tabs, current)
	}
}

func (a *app) restorePaneNode(t *tab, shells []*sshconn.Shell, idx *int, layout *savedPane) (*pane, error) {
	if layout == nil || (layout.First == nil && layout.Second == nil) {
		if *idx >= len(shells) {
			return nil, errors.New("shell 数量不足")
		}
		shell := shells[*idx]
		*idx++
		return a.newPane(t, shell)
	}
	first, err := a.restorePaneNode(t, shells, idx, layout.First)
	if err != nil {
		return nil, err
	}
	second, err := a.restorePaneNode(t, shells, idx, layout.Second)
	if err != nil {
		first.each(func(p *pane) { p.term.Close() })
		return nil, err
	}
	pct := layout.Percent
	if pct <= 0 || pct >= 100 {
		pct = 50
	}
	a.nextID++
	return &pane{
		id:       a.nextID,
		first:    first,
		second:   second,
		vertical: layout.Vertical,
		percent:  pct,
	}, nil
}

// openLocalTerminal 打开本机终端标签。
func (a *app) openLocalTerminal() {
	a.nextID++
	t := &tab{
		id:    a.nextID,
		local: true,
		// 标签名不用“本地终端”：侧栏的按钮已经叫这个名字，同一界面里可点击的文字不能重复。
		host: hosts.Host{
			ID:   "local",
			Name: "本机",
		},
	}
	pane, err := a.newPane(t, nil)
	if err != nil {
		a.notice = fmt.Sprintf("启动本地终端失败: %v", err)
		return
	}
	t.root = pane
	t.focus = pane
	a.tabs = append(a.tabs, t)
	a.active = len(a.tabs) - 1
	a.arrived[pane.id] = true
}

// createTerminal 创建终端控件。shell 为 nil 时是本地终端：插件在伪终端里运行本机 shell。
func (a *app) createTerminal(t *tab, p *pane, shell *sshconn.Shell) (*terminal.Terminal, error) {
	opts := terminal.Options{
		Font:       a.terminalFont(),
		Theme:      a.terminalTheme(false),
		DarkTheme:  a.terminalTheme(true),
		Cursor:     a.terminalCursor(),
		Scrollback: a.terminalScrollback(),
		Padding:    terminal.Padding{X: 10, Y: 8},
		OnExit: func(int) {
			a.post(func() { a.paneEnded(t, p, shell) })
		},
		OnCopy: func(_ string, by terminal.CopySource) {
			a.post(func() { a.copied(by) })
		},
		OnSearch: func(s terminal.SearchStatus) {
			a.post(func() { p.searchStatus = s })
		},
	}
	// 只在有远程 shell 时设置 Conn：值为 nil 的 *Shell 放进接口后接口并不是 nil，
	// 终端插件会以为有连接，本地终端就打不开了。远程 shell 外面套一层 zmodemConn 处理 rz/sz。
	if shell != nil {
		var z *zmodemConn
		z = newZmodemConn(shell, func(s *zmodemSession) {
			a.post(func() { a.startZmodem(t, p, z, s) })
		})
		opts.Conn = z
		p.zconn = z
	}
	return terminal.New(opts)
}

func (a *app) newPane(t *tab, shell *sshconn.Shell) (*pane, error) {
	a.nextID++
	p := &pane{id: a.nextID}
	term, err := a.createTerminal(t, p, shell)
	if err != nil {
		return nil, err
	}
	p.term = term
	p.shell = shell
	return p, nil
}

// terminalTheme 返回 windowDark 外观的窗口里终端应使用的配色。
// 用户手动指定外观时，无论窗口实际是明是暗都用同一套。
func (a *app) terminalTheme(windowDark bool) *terminal.Theme {
	dark := windowDark
	switch a.settings.Appearance {
	case appearanceLight:
		dark = false
	case appearanceDark:
		dark = true
	}
	themeName := a.settings.TerminalThemeLight
	if dark {
		themeName = a.settings.TerminalThemeDark
	}
	if themeName != "" {
		if th, err := terminal.GhosttyTheme(themeName); err == nil && th != nil {
			return th
		}
	}
	return design.Terminal(dark)
}

// refreshTerminals 让所有运行中的终端应用最新的字体与配色设置。
func (a *app) refreshTerminals() {
	font := a.terminalFont()
	light := a.terminalTheme(false)
	dark := a.terminalTheme(true)
	for _, t := range a.tabs {
		t.root.each(func(p *pane) {
			p.term.SetTheme(light, dark)
			p.term.SetFont(font)
		})
	}
}

// watchLatency 定期保活并记录延迟，直到会话关闭。
//
// 每次通知界面都会重建并重绘整个窗口，所以只在显示出来的内容真正变化时才通知：
// 实测 2 个会话每 5 秒各通知一次，空闲 CPU 从 0.15% 涨到 1.15%，而延迟显示其实一直没变。
//
// client 由调用方在主线程上传入：t.client 会在重连时被主线程替换，后台不能读它。
func (a *app) watchLatency(ctx context.Context, t *tab, client *sshconn.Client) {
	interval := 5 * time.Second
	if a.pingInterval > 0 {
		interval = a.pingInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	shown, wasStalled, first := "", false, true
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 2*interval)
		rtt, err := client.Ping(pingCtx)
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
				if ctx.Err() != nil {
					return // 这路保活已被取消（会话关闭或重连换了新连接），结果作废
				}
				t.stalled = stalled
				if !stalled {
					t.latency = rtt
				}
				if stalled && !t.reconnecting {
					a.startAutoReconnect(t)
				}
			})
		}
		if stalled {
			return
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
		var shell *sshconn.Shell
		var err error
		if !t.local {
			shell, err = t.client.OpenShell(80, 24)
		}
		a.post(func() {
			t.splitting = false
			if err != nil {
				a.notice = err.Error()
				return
			}
			if !slices.Contains(a.tabs, t) {
				if shell != nil {
					shell.Close()
				}
				return
			}
			leaf, err := a.newPane(t, shell)
			if err != nil {
				if shell != nil {
					shell.Close()
				}
				a.notice = fmt.Sprintf("启动终端失败: %v", err)
				return
			}
			// 叶子节点的指针必须保持不变：它的退出回调捕获了这个指针。
			a.nextID++
			target := t.focus
			t.replace(target, &pane{id: a.nextID, first: target, second: leaf, vertical: vertical, percent: 50})
			t.focus = leaf
			a.arrived[leaf.id] = true
			a.saveSessionLayout()
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
	a.saveSessionLayout()
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
	a.saveSessionLayout()
	// 断开网络连接可能阻塞，不放在主线程。
	go t.close()
}

func (t *tab) close() {
	for _, tr := range t.transfers {
		if tr.cancel != nil {
			tr.cancel()
		}
	}
	if t.reconnectCancel != nil {
		t.reconnectCancel()
	}
	if t.stop != nil {
		t.stop()
	}
	t.root.each(func(p *pane) { p.term.Close() })
	if t.files != nil {
		t.files.Close()
	}
	if t.client != nil {
		t.client.Close()
	}
}

// closeAll 在窗口关闭时取消进行中的连接并断开所有会话。
func (a *app) closeAll() {
	a.saveSessionLayout()
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
	if t.local {
		a.closeTab(t)
		a.openLocalTerminal()
		return
	}
	h := t.host
	a.closeTab(t)
	a.connect(h, nil)
}

func (a *app) reconnectDelay(attempt int) time.Duration {
	if a.reconnectBaseDelay > 0 {
		return a.reconnectBaseDelay
	}
	sec := 1
	if attempt > 1 {
		sec = 1 << (attempt - 1)
	}
	if sec > 30 {
		sec = 30
	}
	return time.Duration(sec) * time.Second
}

func (a *app) cancelReconnect(t *tab) {
	if t.reconnectCancel != nil {
		t.reconnectCancel()
		t.reconnectCancel = nil
	}
	t.reconnecting = false
	t.reconnectAttempt = 0
	t.root.each(func(p *pane) {
		p.ended = true
	})
}

func (a *app) startAutoReconnect(t *tab) {
	if t.reconnecting || !slices.Contains(a.tabs, t) {
		return
	}
	t.reconnecting = true
	t.reconnectAttempt = 1
	ctx, cancel := context.WithCancel(context.Background())
	t.reconnectCancel = cancel
	a.retryReconnect(ctx, t, 1)
}

// reconnectRetryable 报告这个错误值不值得自动重试。只有网络类错误会自己好起来；
// 认证失败、主机密钥变化（可能有人冒充服务器）、用户拒绝信任，重试只会重复失败，
// 主机密钥变化时一直重试还会掩盖安全问题。
func reconnectRetryable(err error) bool {
	var changed *sshconn.HostKeyChangedError
	switch {
	case errors.As(err, &changed),
		errors.Is(err, sshconn.ErrHostKeyRejected),
		errors.Is(err, sshconn.ErrAuthFailed),
		errors.Is(err, sshconn.ErrNoAuthMethod),
		errors.Is(err, sshconn.ErrKeyEncrypted),
		errors.Is(err, sshconn.ErrBadPassphrase):
		return false
	}
	return true
}

// stopReconnect 停止自动重连，把原因告诉用户。会话保持“已结束”，用户可以点“重新连接”，
// 走完整的连接流程（重新输入密码、核对指纹）。
func (a *app) stopReconnect(t *tab, err error) {
	a.cancelReconnect(t)
	var changed *sshconn.HostKeyChangedError
	if errors.As(err, &changed) {
		a.notice = fmt.Sprintf("%s 的主机密钥和上次不同，可能有人冒充这台服务器，已停止自动重连。"+
			"如果服务器确实重装过，右键 %s 选择“重置主机密钥”，再重新连接。", t.host.Name, t.host.Name)
		return
	}
	a.notice = fmt.Sprintf("%s 自动重连已停止：%v。点窗格上的“重新连接”可以重新输入密码或核对指纹。", t.host.Name, err)
}

func (a *app) retryReconnect(ctx context.Context, t *tab, attempt int) {
	if ctx.Err() != nil || !t.reconnecting || !slices.Contains(a.tabs, t) {
		return
	}
	t.reconnectAttempt = attempt
	cfg, _, _, _, err := a.prepareDialConfig(ctx, t.host, nil)
	if err != nil {
		// 主机配置本身有问题（私钥被删、跳板机写错），重试不会好转。
		a.stopReconnect(t, err)
		return
	}
	go func() {
		client, err := sshconn.Dial(ctx, cfg)
		a.post(func() {
			if ctx.Err() != nil || !t.reconnecting || !slices.Contains(a.tabs, t) {
				if client != nil {
					client.Close()
				}
				return
			}
			if err != nil {
				if !reconnectRetryable(err) {
					a.stopReconnect(t, err)
					return
				}
				delay := a.reconnectDelay(attempt)
				time.AfterFunc(delay, func() {
					a.post(func() { a.retryReconnect(ctx, t, attempt+1) })
				})
				return
			}
			a.finishReconnect(t, client)
		})
	}()
}

func (a *app) finishReconnect(t *tab, client *sshconn.Client) {
	t.reconnecting = false
	t.reconnectAttempt = 0
	t.stalled = false
	if t.reconnectCancel != nil {
		t.reconnectCancel = nil
	}
	oldClient := t.client
	t.client = client
	if oldClient != nil {
		go oldClient.Close()
	}
	if t.files != nil {
		oldFiles := t.files
		t.files = nil
		go oldFiles.Close()
	}
	if a.filesOpen && a.current() == t {
		a.ensureFiles(t)
	}
	t.stop()
	ctx, stop := context.WithCancel(context.Background())
	t.stop = stop
	go a.watchLatency(ctx, t, t.client)

	t.root.each(func(p *pane) {
		cols, rows := 80, 24
		if p.term != nil {
			if c, r := p.term.Size(); c > 0 && r > 0 {
				cols, rows = c, r
			}
		}
		shell, err := client.OpenShell(cols, rows)
		if err != nil {
			p.ended = true
			return
		}
		oldTerm := p.term
		term, err := a.createTerminal(t, p, shell)
		if err != nil {
			shell.Close()
			p.ended = true
			return
		}
		if oldTerm != nil {
			go oldTerm.Close()
		}
		p.term = term
		p.shell = shell
		p.ended = false
	})
	a.saveSessionLayout()
}

func savePane(p *pane) *savedPane {
	if p == nil {
		return nil
	}
	if p.term != nil {
		return &savedPane{}
	}
	return &savedPane{
		Vertical: p.vertical,
		Percent:  p.percent,
		First:    savePane(p.first),
		Second:   savePane(p.second),
	}
}

func (a *app) saveSessionLayout() {
	if a.local.DisableRestoreSession {
		return
	}
	if len(a.tabs) == 0 {
		if a.local.SavedSession != nil {
			next := a.local
			next.SavedSession = nil
			_ = a.saveLocal(next)
		}
		return
	}
	tabs := make([]savedTab, 0, len(a.tabs))
	for _, t := range a.tabs {
		tabs = append(tabs, savedTab{
			HostID: t.host.ID,
			Root:   savePane(t.root),
		})
	}
	next := a.local
	next.SavedSession = &savedSession{
		Tabs:   tabs,
		Active: a.active,
	}
	_ = a.saveLocal(next)
}

func (a *app) restoreSession() {
	if a.local.DisableRestoreSession || a.local.SavedSession == nil || len(a.local.SavedSession.Tabs) == 0 {
		return
	}
	// 各台主机并行连接。需要密码或核对指纹的主机会各自弹出询问。
	session := a.local.SavedSession
	a.restoreActive = session.Active + 1
	for i, st := range session.Tabs {
		h, ok := a.hostByID(st.HostID)
		if !ok {
			continue
		}
		a.connectWithLayout(h, nil, st.Root, i+1)
	}
}

func firstLeaf(p *pane) *pane {
	if p == nil || p.term != nil {
		return p
	}
	return firstLeaf(p.first)
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

// ErrChallengeCancelled 表示用户取消了多步验证。
var errChallengeCancelled = errors.New("已取消多步验证")

// askChallenge 在连接 goroutine 上阻塞，直到用户回答多步验证或连接被取消。
// 询问排队显示：经跳板机时跳板机和目标主机可能先后都要验证码，后来的不能覆盖前面的。
func (a *app) askChallenge(ctx context.Context, host hosts.Host, name, instruction string, questions []sshconn.Question) ([]string, error) {
	// 验证码能全部算出来时不弹面板：连接 goroutine 上就能回答，不必回到主线程。
	if answers, ok := autoTOTP(host, questions, time.Now()); ok {
		return answers, nil
	}
	ch := make(chan challengeResult, 1)
	prompt := &challengeSheet{
		open:        true,
		fresh:       true,
		host:        host,
		name:        name,
		instruction: instruction,
		questions:   questions,
		answers:     make([]string, len(questions)),
		answerChan:  ch,
	}
	a.post(func() { a.challenges = append(a.challenges, prompt) })
	select {
	case res := <-ch:
		return res.answers, res.err
	case <-ctx.Done():
		a.post(func() { a.removeChallenge(prompt) })
		return nil, ctx.Err()
	}
}

// autoTOTP 在每个问题都像验证码、并且主机保存了密钥时，算出全部答案。
// 有一题不像验证码，或者密钥无效，就交给用户，避免把验证码填进不相干的问题。
func autoTOTP(host hosts.Host, questions []sshconn.Question, now time.Time) ([]string, bool) {
	if host.TOTPSecret == "" || len(questions) == 0 {
		return nil, false
	}
	answers := make([]string, len(questions))
	for i, q := range questions {
		if !looksLikeOTP(q.Text) {
			return nil, false
		}
		code, err := totp.Code(totp.Params{
			Secret: host.TOTPSecret, Digits: host.TOTPDigits,
			Period: host.TOTPPeriod, Algorithm: host.TOTPAlgorithm,
		}, now)
		if err != nil {
			return nil, false
		}
		answers[i] = code
	}
	return answers, true
}

// looksLikeOTP 判断多步验证的问题是不是在要一次性验证码。
func looksLikeOTP(q string) bool {
	q = strings.ToLower(q)
	if strings.Contains(q, "password") || strings.Contains(q, "密码") {
		return false
	}
	for _, hint := range []string{
		"otp", "totp", "验证码", "动态口令", "one-time", "one time",
		"authenticator", "verification code", "2fa", "two-factor", "two factor",
	} {
		if strings.Contains(q, hint) {
			return true
		}
	}
	return false
}

// currentChallenge 返回正在显示的多步验证询问。
func (a *app) currentChallenge() *challengeSheet {
	if len(a.challenges) == 0 {
		return nil
	}
	return a.challenges[0]
}

func (a *app) removeChallenge(c *challengeSheet) {
	a.challenges = slices.DeleteFunc(a.challenges, func(x *challengeSheet) bool { return x == c })
}

// submitChallenge 交出当前询问的回答。答案只经 channel 交给连接 goroutine，不留在界面状态里。
func (a *app) submitChallenge() {
	ch := a.currentChallenge()
	if ch == nil {
		return
	}
	a.removeChallenge(ch)
	ch.answerChan <- challengeResult{answers: ch.answers}
}

func (a *app) cancelChallenge() {
	ch := a.currentChallenge()
	if ch == nil {
		return
	}
	a.removeChallenge(ch)
	ch.answerChan <- challengeResult{err: errChallengeCancelled}
}

// toggleFiles 打开或收起文件抽屉。SFTP 通道到需要时才打开，不用文件的会话不占这份资源。
func (a *app) toggleFiles() {
	a.filesOpen = !a.filesOpen
	if t := a.current(); a.filesOpen && t != nil {
		a.ensureFiles(t)
	}
}

func (a *app) ensureFiles(t *tab) {
	if t.local || t.client == nil || t.files != nil || t.filesBusy {
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
			waiting := t.filesWaiting
			t.filesWaiting = nil
			if err != nil {
				t.filesError = err.Error()
				if len(waiting) > 0 {
					a.toast(ui.Toast{Title: "无法上传", Description: "打不开文件传输通道：" + err.Error(), Type: "error"})
				}
				return
			}
			if !slices.Contains(a.tabs, t) {
				go files.Close()
				return
			}
			t.files = files
			a.openDir(t, home)
			for _, fn := range waiting {
				fn()
			}
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

// groupCollapsed 报告这个分组在本机侧栏里是否折叠。正在过滤时不折叠，否则匹配的主机会被藏起来。
func (a *app) groupCollapsed(name string) bool {
	return strings.TrimSpace(a.hostFilter) == "" && slices.Contains(a.local.CollapsedGroups, name)
}

// toggleGroup 折叠或展开一个分组，并把结果写入本机设置。
func (a *app) toggleGroup(name string) {
	next := a.local
	if slices.Contains(a.local.CollapsedGroups, name) {
		next.CollapsedGroups = slices.DeleteFunc(slices.Clone(a.local.CollapsedGroups), func(g string) bool { return g == name })
	} else {
		next.CollapsedGroups = append(slices.Clone(a.local.CollapsedGroups), name)
	}
	if err := a.saveLocal(next); err != nil {
		a.notice = "保存分组折叠状态失败：" + err.Error()
	}
}

// hostVisible 判断主机是否匹配侧栏过滤。空过滤匹配全部。名称、用户、地址和分组都参与，不区分大小写。
func hostVisible(h hosts.Host, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	for _, field := range []string{h.Name, h.User, h.Address, h.Group, h.Target()} {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

// hostsInGroup 返回这个分组里通过过滤的主机，按名称排序（不区分大小写）。
// 同名时保持保险库里的相对顺序。手动拖动排序仍待决定，这里不做。
func (a *app) hostsInGroup(group string) []hosts.Host {
	var list []hosts.Host
	for _, h := range a.hosts {
		if h.Group == group && hostVisible(h, a.hostFilter) {
			list = append(list, h)
		}
	}
	slices.SortStableFunc(list, func(x, y hosts.Host) int {
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	return list
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
//
// shell 是退出的那个终端所连的 shell。重连成功后旧终端才被关闭，它的退出事件会迟到，
// 这时窗格里已经换成了新 shell，必须忽略，否则会把正常工作的新会话当成断线再重连一次。
func (a *app) paneEnded(t *tab, p *pane, shell *sshconn.Shell) {
	if !slices.Contains(a.tabs, t) || !t.root.contains(p) || p.shell != shell {
		return
	}
	p.ended = true
	if t.reconnecting {
		return
	}
	allEnded := true
	t.root.each(func(q *pane) { allEnded = allEnded && q.ended })
	if !allEnded {
		a.toast(ui.Toast{Title: t.host.Name + " 的一个窗格已结束"})
		return
	}
	// 保活已经失败，肯定是断线。否则看远端有没有发来退出状态：用户输入 exit 时有，断线时没有
	// （见 sshconn.Shell.Exited）。在后台等，不阻塞界面；不靠 ping，高延迟链路上 ping 慢不代表断线。
	if t.stalled {
		a.startAutoReconnect(t)
		return
	}
	if shell == nil {
		a.showSessionEnded(t)
		return
	}
	go func() {
		exited := shell.Exited(3 * time.Second)
		a.post(func() {
			if !slices.Contains(a.tabs, t) || t.reconnecting {
				return
			}
			if !exited {
				a.startAutoReconnect(t)
				return
			}
			a.showSessionEnded(t)
		})
	}()
}

// showSessionEnded 提示远端会话已经结束，提供重新连接。
func (a *app) showSessionEnded(t *tab) {
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
