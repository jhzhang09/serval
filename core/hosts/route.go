package hosts

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// maxRoute 限制展开后的跳板机级数，和 sshconn 的上限一致。
const maxRoute = 8

// ParseJumpSpec 解析一个跳板机写法：[user@]host[:port]，IPv6 地址要写成 [::1]:22。
// 端口缺省为 22，用户名缺省为空（由调用方决定默认值）。
func ParseJumpSpec(spec string) (user, host string, port int, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", 0, errors.New("跳板机地址为空")
	}
	if at := strings.LastIndex(spec, "@"); at >= 0 {
		user, spec = spec[:at], spec[at+1:]
		if user == "" {
			return "", "", 0, fmt.Errorf("跳板机 %q 的用户名为空", spec)
		}
	}
	port = 22
	switch {
	case strings.HasPrefix(spec, "["):
		h, p, splitErr := net.SplitHostPort(spec)
		if splitErr != nil {
			if !strings.HasSuffix(spec, "]") {
				return "", "", 0, fmt.Errorf("跳板机地址 %q 格式不对", spec)
			}
			h, p = strings.Trim(spec, "[]"), ""
		}
		host = h
		if p != "" {
			if port, err = strconv.Atoi(p); err != nil {
				return "", "", 0, fmt.Errorf("跳板机端口 %q 不是数字", p)
			}
		}
	case strings.Count(spec, ":") == 1:
		h, p, _ := strings.Cut(spec, ":")
		host = h
		if port, err = strconv.Atoi(p); err != nil {
			return "", "", 0, fmt.Errorf("跳板机端口 %q 不是数字", p)
		}
	default:
		host = spec // 主机名，或不带端口的 IPv6 地址
	}
	if host == "" {
		return "", "", 0, errors.New("跳板机主机为空")
	}
	if port < 1 || port > 65535 {
		return "", "", 0, fmt.Errorf("跳板机端口 %d 超出范围", port)
	}
	return user, host, port, nil
}

// SplitJumps 拆分逗号分隔的跳板机列表，去掉空项。
func SplitJumps(list string) []string {
	var out []string
	for _, part := range strings.Split(list, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// RenameJump 把 ProxyJump 列表里按名称引用的 oldName 替换为 newName，并返回是否发生了替换。
func RenameJump(list, oldName, newName string) (string, bool) {
	if oldName == "" || newName == "" || oldName == newName {
		return list, false
	}
	jumps := SplitJumps(list)
	changed := false
	for i, j := range jumps {
		if j == oldName {
			jumps[i] = newName
			changed = true
		}
	}
	if !changed {
		return list, false
	}
	return strings.Join(jumps, ","), true
}

// ResolveRoute 返回连接 target 要依次经过的跳板机。
//
// 列表里的每一项可以是另一台主机的名称（使用那台主机的地址、用户名和私钥），
// 也可以直接写 [user@]host[:port]。和 OpenSSH 一样，跳板机自己配置的 ProxyJump
// 也会展开：target 经 b，b 经 a，链路就是 a → b → target。
// 直接写地址且没写用户名时，用 defaultUser（通常是本机用户名，与 OpenSSH 一致）。
func ResolveRoute(target Host, all []Host, defaultUser string) ([]Host, error) {
	byName := map[string]Host{}
	for _, h := range all {
		if _, dup := byName[h.Name]; !dup {
			byName[h.Name] = h
		}
	}
	var route []Host
	// visiting 记录正在展开的主机，用来发现 a 经 b、b 又经 a 这样的循环。
	visiting := map[string]bool{target.ID: true}
	var expand func(list string) error
	expand = func(list string) error {
		for _, token := range SplitJumps(list) {
			hop, known := byName[token]
			if !known {
				user, address, port, err := ParseJumpSpec(token)
				if err != nil {
					return err
				}
				if user == "" {
					user = defaultUser
				}
				hop = Host{ID: "jump:" + token, Name: token, Address: address, Port: port, User: user}
			}
			if visiting[hop.ID] {
				return fmt.Errorf("跳板机配置有循环：%s 又回到了自己", hop.Name)
			}
			visiting[hop.ID] = true
			if err := expand(hop.ProxyJump); err != nil {
				return err
			}
			visiting[hop.ID] = false
			route = append(route, hop)
			if len(route) > maxRoute {
				return fmt.Errorf("跳板机最多 %d 级", maxRoute)
			}
		}
		return nil
	}
	if err := expand(target.ProxyJump); err != nil {
		return nil, err
	}
	return route, nil
}
