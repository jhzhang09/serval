// Package hosts 定义主机和跳板机链路。主机作为记录存放在加密保险库里（见 core/vault）。
package hosts

import (
	"net"
	"strconv"
)

// Host 是一台可连接的主机。整条记录（包括密码）加密后存进保险库。
type Host struct {
	// ID 在保险库内唯一，也是生成斑纹的种子，所以改名、改地址不会改变主机的样子。
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	User    string `json:"user"`
	// Auth 是认证方式，见 AuthAuto 等常量。
	Auth string `json:"auth,omitempty"`
	// KeyID 引用保险库里的一把私钥（见 core/keys）。
	KeyID string `json:"keyId,omitempty"`
	// Password 是保存的登录密码，为空时需要密码就询问。
	Password string `json:"password,omitempty"`
	Group    string `json:"group,omitempty"`
	// Production 为 true 的主机会有明显的危险标记。
	Production bool `json:"production,omitempty"`
	// ProxyJump 是逗号分隔的跳板机列表（OpenSSH 的写法），每项是另一台主机的名称，
	// 或者 [user@]host[:port]。见 ResolveRoute。
	ProxyJump string `json:"proxyJump,omitempty"`
	// ProxyID 引用保险库里的一条代理配置（见 Proxy）。
	ProxyID string `json:"proxyId,omitempty"`
	// Proxy 是直接填写的代理地址（兼容旧记录），例如 socks5://127.0.0.1:1080。
	Proxy string `json:"proxy,omitempty"`
}

// Proxy 是一条独立管理的代理配置，供多台主机复用，整条记录（包括密码）加密存进保险库。
type Proxy struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind,omitempty"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	User    string `json:"user,omitempty"`
	// Password 是代理认证密码，属于秘密。
	Password string `json:"password,omitempty"`
}

// 代理协议类型。
const (
	ProxySOCKS5 = "socks5"
	ProxyHTTP   = "http"
)

// Label 返回界面展示的协议名称。
func (p Proxy) Label() string {
	if p.Kind == ProxyHTTP {
		return "HTTP"
	}
	return "SOCKS5"
}

// Target 返回 kind://[user@]host:port 形式的地址（不含密码），用于界面和日志展示。
func (p Proxy) Target() string {
	kind := p.Kind
	if kind == "" {
		kind = ProxySOCKS5
	}
	addr := p.Address
	if p.Port > 0 {
		addr = net.JoinHostPort(p.Address, strconv.Itoa(p.Port))
	}
	if p.User != "" {
		return kind + "://" + p.User + "@" + addr
	}
	return kind + "://" + addr
}

// 认证方式。
const (
	// AuthAuto 依次尝试选定的私钥（如果有）、ssh-agent，都不行再询问密码。和 OpenSSH 的默认行为一致。
	AuthAuto = ""
	// AuthPassword 只用密码登录，不碰 agent 和私钥：agent 里钥匙多时，
	// 服务器可能在轮到密码之前就因为失败次数过多断开连接。
	AuthPassword = "password"
	// AuthKey 只用选定的私钥登录。
	AuthKey = "key"
)

// Target 返回 user@host 形式的文字，端口不是 22 时带上端口。
func (h Host) Target() string {
	target := h.Address
	if h.User != "" {
		target = h.User + "@" + target
	}
	if h.Port != 0 && h.Port != 22 {
		target += ":" + strconv.Itoa(h.Port)
	}
	return target
}
