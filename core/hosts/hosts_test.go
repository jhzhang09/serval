package hosts

import "testing"

func TestTarget(t *testing.T) {
	if got := (Host{Address: "a", User: "u", Port: 22}).Target(); got != "u@a" {
		t.Errorf("Target = %q", got)
	}
	if got := (Host{Address: "a", User: "u", Port: 2222}).Target(); got != "u@a:2222" {
		t.Errorf("Target = %q", got)
	}
	if got := (Proxy{Address: "127.0.0.1", Port: 1080}).Target(); got != "socks5://127.0.0.1:1080" {
		t.Errorf("Proxy.Target = %q", got)
	}
	if got := (Proxy{Kind: ProxyHTTP, Address: "::1", Port: 7890, User: "ops", Password: "secret"}).Target(); got != "http://ops@[::1]:7890" {
		t.Errorf("Proxy.Target = %q", got)
	}
}
