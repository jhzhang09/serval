package hosts

import (
	"fmt"
	"net"
	"strconv"
)

// 端口转发类型，对应 OpenSSH 的 -L、-R、-D。
const (
	ForwardLocal   = "local"
	ForwardRemote  = "remote"
	ForwardDynamic = "dynamic"
)

// Forward 是一条端口转发规则，加密后存进保险库。规则里没有密码。
type Forward struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// HostID 非空时，连上这台主机就自动开启，最后一条相关会话断开后关闭。
	HostID string `json:"hostId,omitempty"`
	// BindHost 是监听地址。本地和动态转发听在本机，远程转发听在服务器上。空值按 127.0.0.1。
	BindHost string `json:"bindHost,omitempty"`
	BindPort int    `json:"bindPort"`
	// TargetHost 和 TargetPort 是转发到的地址。动态转发不使用。
	TargetHost string `json:"targetHost,omitempty"`
	TargetPort int    `json:"targetPort,omitempty"`
}

// ListenHost 返回实际使用的监听地址。没填时只监听本机回环，避免端口暴露到局域网。
func (f Forward) ListenHost() string {
	if f.BindHost == "" {
		return "127.0.0.1"
	}
	return f.BindHost
}

// Summary 返回列表里的一行说明。
func (f Forward) Summary() string {
	bind := net.JoinHostPort(f.ListenHost(), strconv.Itoa(f.BindPort))
	switch f.Kind {
	case ForwardDynamic:
		return "动态 " + bind
	case ForwardRemote:
		return fmt.Sprintf("远程 %s → %s", bind, net.JoinHostPort(f.TargetHost, strconv.Itoa(f.TargetPort)))
	default:
		return fmt.Sprintf("本地 %s → %s", bind, net.JoinHostPort(f.TargetHost, strconv.Itoa(f.TargetPort)))
	}
}
