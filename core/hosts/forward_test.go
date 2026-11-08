package hosts

import "testing"

func TestForwardListenAndSummary(t *testing.T) {
	local := Forward{Kind: ForwardLocal, BindPort: 8080, TargetHost: "db", TargetPort: 5432}
	if local.ListenHost() != "127.0.0.1" {
		t.Fatalf("空监听地址应是回环，得到 %q", local.ListenHost())
	}
	if local.Summary() != "本地 127.0.0.1:8080 → db:5432" {
		t.Fatalf("Summary = %q", local.Summary())
	}
	remote := Forward{Kind: ForwardRemote, BindHost: "127.0.0.1", BindPort: 9, TargetHost: "::1", TargetPort: 22}
	if remote.Summary() != "远程 127.0.0.1:9 → [::1]:22" {
		t.Fatalf("Summary = %q", remote.Summary())
	}
	dynamic := Forward{Kind: ForwardDynamic, BindHost: "127.0.0.1", BindPort: 1080}
	if dynamic.Summary() != "动态 127.0.0.1:1080" {
		t.Fatalf("Summary = %q", dynamic.Summary())
	}
}
