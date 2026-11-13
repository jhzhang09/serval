package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"serval/core/hosts"
	"serval/core/keys"
	"serval/core/serial"
	"serval/core/totp"
	"serval/core/vault"
	"serval/design"
	"serval/locales"

	"github.com/egoist/mygo/ui"
)

// defaultGroup 是没填分组的主机所在的分组。
const defaultGroup = "我的主机"

// noKey 是私钥下拉框里“不用私钥”的选项。
// 选项文字同时是表单里的哨兵值，渲染和比较都走这里，不能在 init 时钉死语言。
func noKey() string { return locales.HostNoKey() }

// 表单里认证方式的顺序，和 Segmented 的选项一一对应。
var authModes = []string{hosts.AuthAuto, hosts.AuthPassword, hosts.AuthKey}

// serialBaudRates 是串口常见波特率选项。
var serialBaudRates = []string{"9600", "19200", "38400", "57600", "115200", "230400"}
var serialDataBits = []string{"8", "7", "6", "5"}
var serialParities = []string{"N (无校验)", "O (奇校验)", "E (偶校验)"}
var serialStopBits = []string{"1", "2"}

// hostForm 是添加、编辑和复制主机共用的表单。
type hostForm struct {
	open, fresh bool
	// editing 是正在编辑的主机 ID，新建（包括复制）时为空。
	editing string
	// copyOf 是复制来源的 ID：没填新密码时，沿用来源保存的密码。
	copyOf string
	// tempHostID 是当前正在保存的临时主机 ID。
	tempHostID string

	name, address, port, user, group string
	tagsText                         string   // 逗号分隔的标签字符串
	logonScript                      string   // 登录成功后自动执行的初始化命令
	distro                           string   // 操作系统或发行版标识
	protoIdx                         int      // 0: SSH, 1: Telnet, 2: 串口
	baudRateText                     string   // 串口波特率字符串
	dataBitsText                     string   // 数据位: 5/6/7/8
	parityText                       string   // 校验位: N/O/E 选项文本
	stopBitsText                     string   // 停止位: 1/2
	showSerialAdv                    bool     // 是否展开高级串口参数
	serialPorts                      []string // 扫描到的可用串口
	serialPortPick                   string   // 下拉框当前选中的串口
	auth                             int      // authModes 的下标
	keyName                          string   // 选中的私钥名称，noKey 表示不用
	production                       bool
	forwardAgent                     bool

	// jumps 是按顺序选择的跳板机（主机名称；旧记录里直接填写的地址原样保留）。
	// 默认不显示，点“添加跳板机”后才出现。
	jumps     []string
	showJumps bool
	// proxyName 是选中的代理名称；legacyProxy 是旧记录里直接填写的代理地址。
	// 代理同样默认不显示，点“使用代理”后才出现。
	proxyName   string
	legacyProxy string
	showProxy   bool

	// password 是新输入的密码，留空表示保持保存的密码不变。
	password string
	// passwordSaved 表示已经保存了密码；clearPassword 表示用户要求清除。
	passwordSaved, clearPassword bool

	// totp 是新粘贴的验证码密钥或 otpauth 链接，留空表示保持已保存的密钥。
	totp string
	// totpSaved 表示已经保存了密钥；clearTOTP 表示用户要求清除。
	totpSaved, clearTOTP bool

	err string
}

func parityOption(p string) string {
	switch strings.ToUpper(strings.TrimSpace(p)) {
	case "O":
		return serialParities[1]
	case "E":
		return serialParities[2]
	default:
		return serialParities[0]
	}
}

func parseParityOption(opt string) string {
	switch {
	case strings.HasPrefix(opt, "O"):
		return "O"
	case strings.HasPrefix(opt, "E"):
		return "E"
	default:
		return "N"
	}
}

// openAdd 打开空白的添加主机表单。
func (a *app) openAdd() {
	ports := serial.ListPorts()
	a.form = hostForm{
		open: true, fresh: true, keyName: noKey(),
		baudRateText: "115200", dataBitsText: "8", parityText: serialParities[0], stopBitsText: "1",
		serialPorts: ports, serialPortPick: "手动输入",
	}
	if len(a.keys) == 1 {
		a.form.keyName = a.keys[0].Name // 只有一把私钥时直接选上，添加完私钥就能用
	}
}

// formFrom 用已有主机的信息填好表单。
func (a *app) formFrom(h hosts.Host) hostForm {
	ports := serial.ListPorts()
	pick := "手动输入"
	if slices.Contains(ports, h.Address) {
		pick = h.Address
	}
	f := hostForm{
		open: true, fresh: true,
		name: h.Name, address: h.Address, user: h.User, group: h.Group,
		tagsText:      formatTags(h.Tags),
		logonScript:   h.LogonScript,
		distro:        h.Distro,
		production:    h.Production,
		forwardAgent:  h.ForwardAgent,
		passwordSaved: h.Password != "", keyName: noKey(),
		totpSaved:      h.TOTPSecret != "",
		jumps:          hosts.SplitJumps(h.ProxyJump),
		baudRateText:   strconv.Itoa(h.EffectiveBaudRate()),
		dataBitsText:   strconv.Itoa(h.EffectiveDataBits()),
		parityText:     parityOption(h.EffectiveParity()),
		stopBitsText:   strconv.Itoa(h.EffectiveStopBits()),
		serialPorts:    ports,
		serialPortPick: pick,
	}
	if h.DataBits != 0 && h.DataBits != 8 || (h.Parity != "" && h.Parity != "N") || (h.StopBits != 0 && h.StopBits != 1) {
		f.showSerialAdv = true
	}
	switch h.EffectiveProtocol() {
	case hosts.ProtocolTelnet:
		f.protoIdx = 1
	case hosts.ProtocolSerial:
		f.protoIdx = 2
	default:
		f.protoIdx = 0
	}
	f.showJumps = len(f.jumps) > 0
	if p, ok := a.proxyByID(h.ProxyID); ok {
		f.proxyName, f.showProxy = p.Name, true
	} else if h.ProxyID != "" {
		f.proxyName, f.showProxy = pickProxy, true // 引用的代理已删除：展开让用户重新选择
	} else if h.Proxy != "" {
		f.legacyProxy, f.showProxy = h.Proxy, true
		f.proxyName = legacyProxyLabel(h.Proxy)
	}
	if k, ok := a.keyByID(h.KeyID); ok {
		f.keyName = k.Name
	}
	if h.Port != 0 && h.Port != 22 {
		f.port = strconv.Itoa(h.Port)
	}
	f.auth = max(slices.Index(authModes, h.Auth), 0)
	return f
}

// openEdit 编辑主机。
func (a *app) openEdit(h hosts.Host) {
	a.form = a.formFrom(h)
	a.form.editing = h.ID
}

// openDuplicate 以 h 为模板新建主机。
func (a *app) openDuplicate(h hosts.Host) {
	a.form = a.formFrom(h)
	a.form.copyOf = h.ID
	a.form.name = a.uniqueName(locales.HostCopyName(h.Name))
}

// openSaveHost 从临时快速连接的主机打开添加主机表单。
func (a *app) openSaveHost(h hosts.Host) {
	a.form = a.formFrom(h)
	a.form.editing = ""
	a.form.copyOf = ""
	a.form.tempHostID = h.ID
	a.form.name = a.uniqueName(h.Name)
}

// uniqueName 在 name 已被占用时加上序号。跳板机按名称查找主机，名称必须唯一。
func (a *app) uniqueName(name string) string {
	candidate := name
	for i := 2; a.nameTaken(candidate, ""); i++ {
		candidate = fmt.Sprintf("%s %d", name, i)
	}
	return candidate
}

func (a *app) nameTaken(name, exceptID string) bool {
	return slices.ContainsFunc(a.hosts, func(h hosts.Host) bool { return h.Name == name && h.ID != exceptID })
}

// saveForm 校验表单并保存主机。表单里填了密码而保险库还没有主密码时，先设置主密码。
func (a *app) saveForm() {
	f := &a.form
	address := strings.TrimSpace(f.address)
	if address == "" {
		if f.protoIdx == 2 {
			f.err = "请填写串口设备路径"
		} else {
			f.err = locales.HostAddressRequired()
		}
		return
	}

	var proto string
	port := 22
	baudRate := 115200
	if text := strings.TrimSpace(f.baudRateText); text != "" {
		if parsed, err := strconv.Atoi(text); err == nil && parsed > 0 {
			baudRate = parsed
		}
	}

	switch f.protoIdx {
	case 1:
		proto = hosts.ProtocolTelnet
		port = 23
		if text := strings.TrimSpace(f.port); text != "" {
			parsed, err := strconv.Atoi(text)
			if err != nil || parsed < 1 || parsed > 65535 {
				f.err = locales.HostPortInvalid()
				return
			}
			port = parsed
		}
	case 2:
		proto = hosts.ProtocolSerial
		port = 0
	default:
		proto = hosts.ProtocolSSH
		if text := strings.TrimSpace(f.port); text != "" {
			parsed, err := strconv.Atoi(text)
			if err != nil || parsed < 1 || parsed > 65535 {
				f.err = locales.HostPortInvalid()
				return
			}
			port = parsed
		}
	}

	user := strings.TrimSpace(f.user)
	if f.protoIdx == 0 && user == "" {
		f.err = locales.HostUserRequired()
		return
	}
	if f.protoIdx == 1 && user == "" {
		user = "telnet"
	}
	if f.protoIdx == 2 && user == "" {
		user = "serial"
	}

	auth := authModes[f.auth]
	keyID := ""
	if f.protoIdx == 0 {
		if auth != hosts.AuthPassword && f.keyName != noKey() {
			i := slices.IndexFunc(a.keys, func(k keys.Key) bool { return k.Name == f.keyName })
			if i < 0 {
				f.err = locales.HostKeyGone()
				return
			}
			keyID = a.keys[i].ID
		}
		if auth == hosts.AuthKey && keyID == "" {
			f.err = locales.HostKeyRequired()
			return
		}
	}

	name := strings.TrimSpace(f.name)
	if name == "" {
		name = address
	}
	if a.nameTaken(name, f.editing) {
		f.err = locales.HostNameTaken(name)
		return
	}
	group := strings.TrimSpace(f.group)
	if group == "" {
		group = defaultGroup
	}
	proxyID, legacyProxy, err := a.formProxy(f)
	if err != nil {
		f.err = err.Error()
		return
	}
	var jumps []string
	if f.showJumps && f.protoIdx == 0 {
		for i, j := range f.jumps {
			if j == pickHost() || strings.TrimSpace(j) == "" {
				f.err = locales.RouteJumpMissing(i + 1)
				return
			}
			jumps = append(jumps, j)
		}
	}

	dataBits, _ := strconv.Atoi(f.dataBitsText)
	if dataBits < 5 || dataBits > 8 {
		dataBits = 8
	}
	stopBits, _ := strconv.Atoi(f.stopBitsText)
	if stopBits != 2 {
		stopBits = 1
	}
	parity := parseParityOption(f.parityText)

	h := hosts.Host{
		Name: name, Address: address, Port: port, User: user, Auth: auth, KeyID: keyID,
		Group: group, Production: f.production, ForwardAgent: f.forwardAgent,
		ProxyJump: strings.Join(jumps, ","), ProxyID: proxyID, Proxy: legacyProxy,
		Distro:   f.distro,
		Protocol: proto, BaudRate: baudRate,
		DataBits: dataBits, Parity: parity, StopBits: stopBits,
		Tags:        parseTags(f.tagsText),
		LogonScript: strings.TrimRight(f.logonScript, "\r\n "),
	}
	// 密码：新输入的覆盖；要求清除的清除；密码以外的登录方式也保留已有密码（自动模式会用到），
	// 只有“私钥”模式用不上密码，清掉。复制来的主机沿用来源的密码。
	var previous hosts.Host
	if f.editing != "" {
		previous, _ = a.hostByID(f.editing)
	} else if f.copyOf != "" {
		previous, _ = a.hostByID(f.copyOf)
	}
	switch {
	case auth == hosts.AuthKey || f.clearPassword:
		h.Password = ""
	case f.password != "":
		h.Password = f.password
	default:
		h.Password = previous.Password
	}
	switch {
	case f.clearTOTP:
		h.TOTPSecret, h.TOTPDigits, h.TOTPPeriod, h.TOTPAlgorithm = "", 0, 0, ""
	case strings.TrimSpace(f.totp) != "":
		parsed, err := totp.Parse(f.totp)
		if err != nil {
			f.err = err.Error()
			return
		}
		h.TOTPSecret, h.TOTPDigits, h.TOTPPeriod, h.TOTPAlgorithm = parsed.Secret, parsed.Digits, parsed.Period, parsed.Algorithm
	default:
		h.TOTPSecret, h.TOTPDigits, h.TOTPPeriod, h.TOTPAlgorithm = previous.TOTPSecret, previous.TOTPDigits, previous.TOTPPeriod, previous.TOTPAlgorithm
	}
	if f.editing != "" {
		h.ID = f.editing
		h.SortOrder = previous.SortOrder
		h.Bookmarks = slices.Clone(previous.Bookmarks)
		if h.Distro == "" {
			h.Distro = previous.Distro
		}
	} else {
		h.ID = vault.NewID()
	}
	all := slices.DeleteFunc(slices.Clone(a.hosts), func(x hosts.Host) bool { return x.ID == h.ID })
	changes := []vault.Change{hostChange(h)}
	if f.editing != "" && previous.Name != "" && previous.Name != h.Name {
		for i := range all {
			if updated, ok := hosts.RenameJump(all[i].ProxyJump, previous.Name, h.Name); ok {
				all[i].ProxyJump = updated
				changes = append(changes, hostChange(all[i]))
			}
		}
	}
	// 保存前就把链路完整解析一遍：写错的地址、互相引用成环，在这里说清楚，
	// 而不是等到连接时才失败。
	if _, err := hosts.ResolveRoute(h, append(all, h), a.localUser); err != nil {
		f.err = locales.RouteJumpError(err.Error())
		return
	}
	if (h.Password != "" || h.TOTPSecret != "") && a.vault.State() == vault.Unprotected {
		a.requireProtection(a.saveForm)
		return
	}
	if err := a.apply(changes...); err != nil {
		f.err = err.Error()
		return
	}
	if f.tempHostID != "" {
		for _, tab := range a.tabs {
			if tab.host.ID == f.tempHostID {
				tab.host = h
			}
		}
	}
	title := locales.HostAdded(name)
	if f.editing != "" {
		title = locales.HostSaved(name)
	}
	a.form = hostForm{}
	a.toast(ui.Toast{Title: title})
}

// askDelete 请用户确认删除主机。
func (a *app) askDelete(h hosts.Host) { a.deleting = &h }

// dependents 返回经 h 跳转的主机名称。
func (a *app) dependents(h hosts.Host) []string {
	var names []string
	for _, other := range a.hosts {
		if other.ID != h.ID && slices.Contains(hosts.SplitJumps(other.ProxyJump), h.Name) {
			names = append(names, other.Name)
		}
	}
	return names
}

// renamingJumpCount 返回编辑表单改名时将同步更新跳板机引用的主机数量。
func (a *app) renamingJumpCount(f *hostForm) int {
	if f.editing == "" {
		return 0
	}
	previous, ok := a.hostByID(f.editing)
	if !ok || previous.Name == "" {
		return 0
	}
	next := strings.TrimSpace(f.name)
	if next == "" {
		next = strings.TrimSpace(f.address)
	}
	if next == "" || next == previous.Name {
		return 0
	}
	return len(a.dependents(previous))
}

// deleteHost 删除主机（连同保存的密码）。已打开的会话不受影响。
func (a *app) deleteHost(h hosts.Host) {
	if err := a.apply(vault.Change{ID: h.ID, Delete: true}); err != nil {
		a.notice = err.Error()
		return
	}
	a.removeRecentHost(h.ID)
	a.toast(ui.Toast{Title: locales.HostDeleted(h.Name)})
}

// hostMenu 是主机的右键菜单，侧栏行尾的“更多”按钮打开同一个菜单。
func (a *app) hostMenu(c *ui.Context, h hosts.Host) func(m *ui.Menu) {
	return func(m *ui.Menu) {
		if m.Item(locales.Connect()).Chosen() {
			a.open(h)
		}
		if m.Item(locales.ConnectNewTab()).Chosen() {
			a.connect(h, nil)
		}
		if h.EffectiveProtocol() == hosts.ProtocolSSH {
			if m.Item("向右分屏打开").Chosen() {
				a.openSplit(h, false)
			}
			if m.Item("向下分屏打开").Chosen() {
				a.openSplit(h, true)
			}
			if m.Item("仅打开 SFTP").Chosen() {
				a.openFilesForHost(h)
			}
		}
		m.Separator()
		if m.Item(locales.Edit()).Chosen() {
			a.openEdit(h)
		}
		if m.Item(locales.HostDuplicate()).Chosen() {
			a.openDuplicate(h)
		}
		if m.Item("复制 SSH 命令").Chosen() {
			cmd := sshCommandForHost(h, a.hosts, a.localUser)
			c.WriteClipboard(cmd)
			a.toast(ui.Toast{ID: "copy", Title: "已复制连接命令", Description: cmd})
		}
		if h.EffectiveProtocol() == hosts.ProtocolSSH && len(a.keys) > 0 {
			m.Submenu("部署公钥到服务器", func(sub *ui.Menu) {
				for _, k := range a.keys {
					key := k
					if sub.Item(key.Name).Chosen() {
						a.deployKeyToHost(h, key)
					}
				}
			})
		}
		if m.Item("检查连通性").Chosen() {
			a.probeOneHost(h)
		}
		groupHosts := a.allHostsInGroup(h.Group)
		idx := slices.IndexFunc(groupHosts, func(x hosts.Host) bool { return x.ID == h.ID })
		if len(groupHosts) > 1 {
			m.Separator()
			if m.Item(locales.HostMoveUp()).Disabled(idx <= 0).Chosen() {
				if err := a.moveHostStep(h, -1); err != nil {
					a.notice = "调整主机排序失败：" + err.Error()
				}
			}
			if m.Item(locales.HostMoveDown()).Disabled(idx < 0 || idx >= len(groupHosts)-1).Chosen() {
				if err := a.moveHostStep(h, 1); err != nil {
					a.notice = "调整主机排序失败：" + err.Error()
				}
			}
		}
		otherGroups := slices.DeleteFunc(slices.Clone(a.groups()), func(g string) bool { return g == h.Group })
		if len(otherGroups) > 0 {
			m.Submenu(locales.HostMoveToGroup(), func(sub *ui.Menu) {
				for _, g := range otherGroups {
					if sub.Item(g).Chosen() {
						if err := a.moveHostToGroup(h, g); err != nil {
							a.notice = "移动主机分组失败：" + err.Error()
						}
					}
				}
			})
		}
		// 服务器重装、换了密钥时用。没有信任记录时没什么可重置的。
		if m.Item(locales.HostResetKey()).Disabled(!a.hasTrustRecord(h)).Chosen() {
			a.resetHostKey(h)
		}
		m.Separator()
		if m.Item(locales.Delete()).Danger().Chosen() {
			a.askDelete(h)
		}
	}
}

// deleteDialog 确认删除。和主机指纹确认一样用系统风格的警告框，默认按钮是“取消”。
func (a *app) deleteDialog(c *ui.Context) {
	if a.deleting == nil {
		return
	}
	h := *a.deleting
	message := locales.HostDeleteBody()
	if names := a.dependents(h); len(names) > 0 {
		message += locales.HostDeleteJumps(strings.Join(names, locales.NameSeparator()))
	}
	open := true
	choice := ui.AlertDialog(c, &open, locales.HostDeleteTitle(h.Name), message, locales.Delete(), locales.Cancel())
	if choice == -1 && open {
		return
	}
	a.deleting = nil
	if choice == 0 {
		a.deleteHost(h)
	}
}

func formChipButton(c *ui.Context, colors design.Colors, key, label, text string, selected bool, onClick func()) {
	t := c.Theme()
	fg, bg := t.TextMuted, colors.Chip
	if selected {
		fg, bg = t.Accent, t.Accent.Alpha(0.14)
	}
	b := ui.ButtonBase(c).Key(key).Label(label).Height(22).Padding(0, 8).Radius(design.RadiusChip).
		AlignItems(ui.Center).Background(bg).Transition(design.Hover)
	if !selected && b.Hovered() {
		b.Background(colors.Hover)
	}
	b.Children(func() {
		ui.Text(c, text).FontSize(design.TextSmall - 1).FontWeight(500).TextColor(fg).SingleLine()
	}).OnClick(onClick)
}

// importKeyFromHostForm 在添加/编辑主机时直接从文件导入一把私钥并自动选中。
func (a *app) importKeyFromHostForm() {
	pick := a.chooseKeyFile
	if pick == nil {
		a.keyPanel = keyPanel{open: true, fresh: true, page: keyAdd, savePassphrase: true, fromHostForm: true}
		return
	}
	go func() {
		filePath, err := pick()
		if a.post == nil {
			return
		}
		a.post(func() {
			if err != nil {
				a.form.err = err.Error()
				return
			}
			if filePath == "" {
				return
			}
			info, err := os.Stat(filePath)
			if err == nil && info.Size() > maxKeyFile {
				err = errors.New(locales.KeyFileTooLarge(filepath.Base(filePath)))
			}
			var data []byte
			if err == nil {
				data, err = os.ReadFile(filePath)
			}
			if err != nil {
				a.form.err = err.Error()
				return
			}
			base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
			name := a.defaultKeyName(base)
			if keys.Encrypted(data) {
				a.keyPanel = keyPanel{
					open: true, fresh: true, page: keyAdd,
					name: name, pem: string(data), savePassphrase: true, fromHostForm: true,
				}
				return
			}
			k, err := keys.Parse(name, data, "")
			if err != nil {
				a.keyPanel = keyPanel{
					open: true, fresh: true, page: keyAdd,
					name: name, pem: string(data), savePassphrase: true, fromHostForm: true, err: err.Error(),
				}
				return
			}
			k.ID = vault.NewID()
			a.requireProtection(func() {
				if err := a.apply(keyChange(k)); err != nil {
					a.form.err = err.Error()
					return
				}
				a.form.keyName = k.Name
				if a.form.auth == 1 {
					a.form.auth = 0
				}
				a.form.err = ""
				a.toast(ui.Toast{Title: locales.KeyAdded(k.Name)})
			})
		})
	}()
}

// hostDialog 是添加、编辑、复制主机的表单面板。设置主密码的面板或密钥/代理子面板打开时让位，表单内容保留。
func (a *app) hostDialog(c *ui.Context, colors design.Colors) {
	// 从表单里新建代理、设置主密码或补充加密私钥口令时，子面板盖在上面，表单让位，内容保留。
	if !a.form.open || a.setup.open || a.proxyPanel.open || a.keyPanel.open {
		return
	}
	t := c.Theme()
	f := &a.form
	title, submit := locales.AddHost(), locales.Add()
	switch {
	case f.editing != "":
		title, submit = locales.HostEdit(), locales.Save()
	case f.copyOf != "":
		title = locales.HostDuplicateTitle()
	}
	half := func(fn func()) { ui.Column(c).Grow(1).Basis(0).MinWidth(0).Children(fn) }
	_, windowHeight := c.Size()
	sheet(c, colors, &f.open, &f.fresh, 440, 22, func() {
		ui.Column(c).Gap(12).Children(func() {
			ui.Text(c, title).FontSize(design.TextHeading).FontWeight(600)
			ui.Scroll(c).Key("hostFormScroll").MaxHeight(max(windowHeight-180, 120)).Children(func() {
				ui.Column(c).Gap(12).Padding(0, 4, 0, 0).Children(func() {
					ui.Field(c, "协议", func() {
						c.Key("addProtocol")
						ui.Segmented(c, &f.protoIdx, "SSH", "Telnet", locales.ProtoSerial())
					})

					if f.protoIdx == 1 {
						ui.Text(c, locales.ProtoTelnetWarning()).
							FontSize(design.TextSmall).TextColor(colors.Danger)
					}

					if f.protoIdx == 2 {
						// 串口
						portOpts := append([]string{"手动输入"}, f.serialPorts...)
						if !slices.Contains(portOpts, f.serialPortPick) {
							f.serialPortPick = "手动输入"
						}
						ui.Row(c).Gap(8).AlignItems(ui.End).Children(func() {
							ui.Column(c).Grow(1).MinWidth(0).Children(func() {
								ui.Field(c, "可用串口", func() {
									c.Key("addSerialPortPick")
									if ui.Select(c, &f.serialPortPick, portOpts).Changed() && f.serialPortPick != "手动输入" {
										f.address = f.serialPortPick
									}
								})
							})
							ui.Button(c, "刷新端口").Key("refreshSerialPorts").OnClick(func() {
								f.serialPorts = serial.ListPorts()
							})
						})
						ui.Row(c).Gap(10).Children(func() {
							ui.Column(c).Grow(1).MinWidth(0).Children(func() {
								ui.Field(c, "设备路径", func() {
									c.Key("addAddress")
									ui.TextInput(c, &f.address).Placeholder("/dev/ttyUSB0 或 COM1").AutoFocus()
								})
							})
							ui.Column(c).Width(104).Children(func() {
								ui.Field(c, "波特率", func() {
									c.Key("addBaudRate")
									ui.Select(c, &f.baudRateText, serialBaudRates)
								})
							})
						})
						advTitle := "▸ 高级串口参数"
						if f.showSerialAdv {
							advTitle = "▾ 高级串口参数"
						}
						ui.Button(c, advTitle).Key("toggleSerialAdv").Height(26).Padding(0, 8).OnClick(func() {
							f.showSerialAdv = !f.showSerialAdv
						})
						if f.showSerialAdv {
							ui.Row(c).Gap(8).Children(func() {
								ui.Column(c).Grow(1).MinWidth(0).Children(func() {
									ui.Field(c, "数据位", func() {
										c.Key("addDataBits")
										ui.Select(c, &f.dataBitsText, serialDataBits)
									})
								})
								ui.Column(c).Grow(1).MinWidth(0).Children(func() {
									ui.Field(c, "校验", func() {
										c.Key("addParity")
										ui.Select(c, &f.parityText, serialParities)
									})
								})
								ui.Column(c).Grow(1).MinWidth(0).Children(func() {
									ui.Field(c, "停止位", func() {
										c.Key("addStopBits")
										ui.Select(c, &f.stopBitsText, serialStopBits)
									})
								})
							})
						}
					} else {
						// 网络协议 (SSH 或 Telnet)
						defaultPort := "22"
						if f.protoIdx == 1 {
							defaultPort = "23"
						}
						ui.Row(c).Gap(10).Children(func() {
							ui.Column(c).Grow(1).MinWidth(0).Children(func() {
								ui.Field(c, locales.Address(), func() {
									c.Key("addAddress")
									ui.TextInput(c, &f.address).Placeholder("example.com").AutoFocus()
								})
							})
							ui.Column(c).Width(76).Children(func() {
								ui.Field(c, locales.Port(), func() {
									c.Key("addPort")
									ui.TextInput(c, &f.port).Placeholder(defaultPort)
								})
							})
						})
						ui.Field(c, locales.HostUsername(), func() {
							c.Key("addUser")
							placeholder := "deploy"
							if f.protoIdx == 1 {
								placeholder = "可选"
							}
							ui.TextInput(c, &f.user).Placeholder(placeholder)
						})
					}

					ui.Row(c).Gap(10).Children(func() {
						half(func() {
							nameField := ui.Field(c, locales.Name(), func() {
								c.Key("addName")
								ui.TextInput(c, &f.name).Placeholder(locales.HostNamePlaceholder())
							})
							if n := a.renamingJumpCount(f); n > 0 {
								nameField.Description(locales.HostRenameJumps(n))
							}
						})
						half(func() {
							ui.Field(c, locales.HostGroup(), func() {
								c.Key("addGroup")
								ui.TextInput(c, &f.group).Placeholder(defaultGroup)
							})
						})
					})
					if groups := a.groups(); len(groups) > 0 {
						ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
							ui.Text(c, "已有分组").FontSize(design.TextSmall - 1).TextColor(colors.TextFaint)
							for _, g := range groups {
								if g == "" {
									continue
								}
								groupName := g
								formChipButton(c, colors, "formGroupChip:"+groupName, "选择分组 "+groupName, groupName,
									strings.TrimSpace(f.group) == groupName, func() {
										f.group = groupName
									})
							}
						})
					}
					ui.Field(c, "标签", func() {
						c.Key("addTags")
						ui.TextInput(c, &f.tagsText).Placeholder("多个标签用逗号分隔，如 prod, web")
					}).Description("用于在侧栏按标签快速筛选主机。")
					if allTags := a.allTags(); len(allTags) > 0 {
						curTags := parseTags(f.tagsText)
						ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
							for _, tag := range allTags {
								tagName := tag
								selected := slices.Contains(curTags, tagName)
								formChipButton(c, colors, "formTagChip:"+tagName, "标签 "+tagName, "#"+tagName, selected, func() {
									next := parseTags(f.tagsText)
									if idx := slices.Index(next, tagName); idx >= 0 {
										next = slices.Delete(next, idx, idx+1)
									} else {
										next = append(next, tagName)
									}
									f.tagsText = formatTags(next)
								})
							}
						})
					}
					a.distroField(c, f)

					if f.protoIdx == 0 {
						descriptions := []string{
							locales.HostAuthAutoHint(),
							locales.HostAuthPasswordHint(),
							locales.HostAuthKeyHint(),
						}
						ui.Field(c, locales.HostAuth(), func() {
							c.Key("addAuth")
							ui.Segmented(c, &f.auth, locales.HostAuthAuto(), locales.Password(), locales.PrivateKey())
						}).Description(descriptions[f.auth])
						if f.auth == 1 {
							a.passwordField(c, f)
						} else {
							a.keyField(c, f)
						}

						a.routeFields(c, colors, f)
						a.totpField(c, f)
						ui.Column(c).Gap(2).Children(func() {
							c.Key("addProduction")
							ui.Checkbox(c, &f.production, locales.HostProduction())
							ui.Text(c, "开启后显示红色警示标记；加入广播前需二次确认；AI 执行高危命令前强制拦截提示。").
								FontSize(design.TextSmall).TextColor(t.TextMuted)
						})
						c.Key("addForwardAgent")
						ui.Checkbox(c, &f.forwardAgent, "转发 ssh-agent")
						if f.forwardAgent {
							ui.Text(c, "远程上的程序可以用本机 ssh-agent 里的钥匙再登录其他服务器。只在信任这台机器时保持开启。").
								FontSize(design.TextSmall).TextColor(colors.Danger)
						}
						ui.Field(c, "登录后自动执行脚本", func() {
							c.Key("addLogonScript")
							ui.TextArea(c, &f.logonScript).Height(68).Font(design.FontTerminal).FontSize(design.TextSmall).
								Placeholder("每行一条命令，建立 SSH 终端后自动发送（可选）")
						}).Description("适用于自动切换目录、激活环境或查看运行状态。")
					}
				})
			})
			if f.err != "" {
				ui.Text(c, f.err).Key("addError").TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				ui.Button(c, locales.Cancel()).Key("addCancel").OnClick(func() { a.form = hostForm{} })
				ui.PrimaryButton(c, submit).Key("addSubmit").OnClick(a.saveForm)
			})
		})
	})
	// 点击外部或按 Esc 关闭时，不把输入的密码留在内存里。
	if !a.form.open {
		a.form = hostForm{}
	}
}

// keyField 是从密钥库里选私钥的下拉框，同时支持直接从文件导入私钥。
func (a *app) keyField(c *ui.Context, f *hostForm) {
	t := c.Theme()
	if len(a.keys) == 0 {
		ui.Field(c, locales.PrivateKey(), func() {
			ui.Column(c).Gap(6).Children(func() {
				ui.Text(c, locales.HostNoPrivateKey()).
					FontSize(design.TextSmall).TextColor(t.TextMuted)
				ui.Row(c).Children(func() {
					ui.Button(c, "从文件导入私钥").Key("addHostImportKey").OnClick(a.importKeyFromHostForm)
				})
			})
		})
		return
	}
	values := []string{noKey()}
	labels := []string{noKey()}
	for _, k := range a.keys {
		values = append(values, k.Name)
		labels = append(labels, fmt.Sprintf("%s (%s)", k.Name, k.Label()))
	}
	if !slices.Contains(values, f.keyName) {
		f.keyName = noKey()
	}
	curIdx := indexOr(values, f.keyName, 0)
	selectedLabel := labels[curIdx]
	ui.Field(c, locales.PrivateKey(), func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				c.Key("addKey")
				if ui.Select(c, &selectedLabel, labels).Changed() {
					if idx := slices.Index(labels, selectedLabel); idx >= 0 {
						f.keyName = values[idx]
					}
				}
			})
			ui.Button(c, "导入").Key("addHostImportKey").OnClick(a.importKeyFromHostForm)
		})
	})
}

// totpField 是验证码密钥。输入用密码框遮住，保存后不再回显，和登录密码一样。
func (a *app) totpField(c *ui.Context, f *hostForm) {
	placeholder := "粘贴 otpauth:// 链接或密钥"
	switch {
	case f.totpSaved && f.clearTOTP:
		placeholder = "保存后清除已保存的验证码密钥"
	case f.totpSaved:
		placeholder = "已保存，留空保持不变"
	}
	ui.Row(c).Gap(8).AlignItems(ui.End).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			ui.Field(c, "验证码", func() {
				c.Key("addTOTP")
				ui.TextInput(c, &f.totp).Password().Placeholder(placeholder)
			}).Description("多步验证问到验证码时自动填写。")
		})
		if f.totpSaved {
			text := "清除验证码"
			if f.clearTOTP {
				text = "撤销清除验证码"
			}
			ui.Button(c, text).Key("addTOTPClear").OnClick(func() {
				f.clearTOTP = !f.clearTOTP
				if f.clearTOTP {
					f.totp = ""
				}
			})
		}
	})
}

// passwordField 是密码输入框。已保存密码时，留空表示保持不变，并可以清除。
func (a *app) passwordField(c *ui.Context, f *hostForm) {
	placeholder := locales.HostPasswordPrompt()
	switch {
	case f.passwordSaved && f.clearPassword:
		placeholder = locales.HostClearPasswordPending()
	case f.passwordSaved:
		placeholder = locales.HostPasswordUnchanged()
	}
	ui.Row(c).Gap(8).AlignItems(ui.End).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			ui.Field(c, locales.Password(), func() {
				c.Key("addPassword")
				ui.TextInput(c, &f.password).Password().Placeholder(placeholder)
			})
		})
		if f.passwordSaved {
			text := locales.Clear()
			if f.clearPassword {
				text = locales.HostUndoClear()
			}
			ui.Button(c, text).Key("addPasswordClear").OnClick(func() {
				f.clearPassword = !f.clearPassword
				if f.clearPassword {
					f.password = ""
				}
			})
		}
	})
}

// distroField 是操作系统或发行版下拉选择框。
func (a *app) distroField(c *ui.Context, f *hostForm) {
	options := hosts.SupportedDistros()
	labels := make([]string, len(options))
	currentLabel := options[0].Label
	for i, opt := range options {
		labels[i] = opt.Label
		if opt.Key == f.distro {
			currentLabel = opt.Label
		}
	}
	ui.Field(c, "系统 / 发行版", func() {
		c.Key("addDistro")
		if ui.Select(c, &currentLabel, labels).Changed() {
			for _, opt := range options {
				if opt.Label == currentLabel {
					f.distro = opt.Key
					break
				}
			}
		}
	}).Description("用于展示系统徽标。留空或选择“自动识别”将在连接后自动探测。")
}
