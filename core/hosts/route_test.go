package hosts

import (
	"strings"
	"testing"
)

func TestParseJumpSpec(t *testing.T) {
	cases := []struct {
		in         string
		user, host string
		port       int
	}{
		{"bastion", "", "bastion", 22},
		{"ops@bastion", "ops", "bastion", 22},
		{"ops@bastion:2222", "ops", "bastion", 2222},
		{"10.0.0.1:2200", "", "10.0.0.1", 2200},
		{"[::1]:2022", "", "::1", 2022},
		{"[fe80::1]", "", "fe80::1", 22},
		{"fe80::1", "", "fe80::1", 22},
		{"a@b@host", "a@b", "host", 22},
	}
	for _, c := range cases {
		user, host, port, err := ParseJumpSpec(c.in)
		if err != nil || user != c.user || host != c.host || port != c.port {
			t.Errorf("ParseJumpSpec(%q) = %q %q %d %v", c.in, user, host, port, err)
		}
	}
	for _, bad := range []string{"", "@host", "host:abc", "host:70000", "[::1", "ops@"} {
		if _, _, _, err := ParseJumpSpec(bad); err == nil {
			t.Errorf("%q 应报错", bad)
		}
	}
}

func names(route []Host) string {
	var n []string
	for _, h := range route {
		n = append(n, h.Name+"("+h.User+"@"+h.Address+")")
	}
	return strings.Join(n, " > ")
}

func TestResolveRoute(t *testing.T) {
	all := []Host{
		{ID: "a", Name: "a", Address: "10.0.0.1", User: "ua", Port: 22},
		{ID: "b", Name: "b", Address: "10.0.0.2", User: "ub", Port: 22, ProxyJump: "a"},
		{ID: "t", Name: "t", Address: "10.0.0.3", User: "ut", Port: 22, ProxyJump: "b"},
	}
	// target 经 b，b 又经 a：展开成 a > b。
	route, err := ResolveRoute(all[2], all, "me")
	if err != nil || names(route) != "a(ua@10.0.0.1) > b(ub@10.0.0.2)" {
		t.Fatalf("route=%s err=%v", names(route), err)
	}

	// 直接写的地址没有用户名时用本机用户名。
	direct := Host{ID: "x", Name: "x", ProxyJump: "a, gw.example.com:2222"}
	route, err = ResolveRoute(direct, all, "me")
	if err != nil || names(route) != "a(ua@10.0.0.1) > gw.example.com:2222(me@gw.example.com)" || route[1].Port != 2222 {
		t.Fatalf("route=%s err=%v", names(route), err)
	}

	if route, err := ResolveRoute(Host{ID: "n"}, all, "me"); err != nil || len(route) != 0 {
		t.Fatalf("没有跳板机时应返回空链路: %v %v", route, err)
	}
}

func TestResolveRouteDetectsCycles(t *testing.T) {
	all := []Host{
		{ID: "a", Name: "a", Address: "1", ProxyJump: "b"},
		{ID: "b", Name: "b", Address: "2", ProxyJump: "a"},
	}
	if _, err := ResolveRoute(Host{ID: "t", ProxyJump: "a"}, all, "me"); err == nil || !strings.Contains(err.Error(), "循环") {
		t.Fatalf("err = %v", err)
	}
	self := Host{ID: "s", Name: "s", ProxyJump: "s"}
	if _, err := ResolveRoute(self, []Host{self}, "me"); err == nil {
		t.Fatal("主机把自己当跳板机应报错")
	}
	// 同一台跳板机在链路里出现两次（不成环）是允许的，例如 a,b,a 这种绕行写法。
	if _, err := ResolveRoute(Host{ID: "t", ProxyJump: "x, y, x"}, nil, "me"); err != nil {
		t.Fatalf("重复但不成环的链路应允许: %v", err)
	}
}

func TestResolveRouteLimit(t *testing.T) {
	list := strings.Repeat("h,", maxRoute) + "h"
	if _, err := ResolveRoute(Host{ID: "t", ProxyJump: list}, nil, "me"); err == nil || !strings.Contains(err.Error(), "最多") {
		t.Fatalf("err = %v", err)
	}
}

func TestRenameJumpAndResolveRoute(t *testing.T) {
	got, changed := RenameJump(" bastion , gw:2222, bastion ", "bastion", "edge")
	if !changed || got != "edge,gw:2222,edge" {
		t.Fatalf("got=%q changed=%v", got, changed)
	}
	if got, changed := RenameJump("ops@bastion:22,other", "bastion", "edge"); changed || got != "ops@bastion:22,other" {
		t.Fatalf("不应替换直接写的地址: got=%q changed=%v", got, changed)
	}

	all := []Host{
		{ID: "1", Name: "edge", Address: "10.0.0.1", User: "u1", Port: 22},
		{ID: "2", Name: "app", Address: "10.0.0.2", User: "u2", Port: 22, ProxyJump: "bastion"},
	}
	all[1].ProxyJump, _ = RenameJump(all[1].ProxyJump, "bastion", "edge")
	route, err := ResolveRoute(all[1], all, "me")
	if err != nil || len(route) != 1 || route[0].ID != "1" || route[0].Name != "edge" {
		t.Fatalf("改名后应仍能解析链路: route=%+v err=%v", route, err)
	}
}
