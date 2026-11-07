// Package hosts 定义主机和跳板机链路。主机作为记录存放在加密保险库里（见 core/vault）。
package hosts

import "strconv"

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
	// Proxy 是连接第一跳时使用的代理，例如 socks5://127.0.0.1:1080。
	Proxy string `json:"proxy,omitempty"`
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
