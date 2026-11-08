package main

import (
	"context"
	"errors"
	"testing"

	"serval/core/hosts"
	"serval/core/sshconn"
)

// 跳板机和目标主机先后都要验证码时依次询问：后来的不覆盖前面的；锁定保险库时全部取消。
func TestChallengesQueueAndCancelOnLock(t *testing.T) {
	h := newHarness(t)
	type result struct {
		host    string
		answers []string
		err     error
	}
	done := make(chan result, 2)
	ask := func(name string) {
		answers, err := h.a.askChallenge(context.Background(), hosts.Host{ID: name, Name: name}, "", "",
			[]sshconn.Question{{Text: "Code: "}})
		done <- result{name, answers, err}
	}
	go ask("bastion")
	h.pump("第一个询问", func() bool { return len(h.a.challenges) == 1 })
	go ask("target")
	h.pump("第二个询问排队", func() bool { return len(h.a.challenges) == 2 })

	if got := h.a.currentChallenge().host.Name; got != "bastion" {
		t.Fatalf("先显示先到的询问，实际是 %q", got)
	}
	h.a.currentChallenge().answers[0] = "111111"
	h.a.submitChallenge()
	if r := <-done; r.host != "bastion" || r.err != nil || r.answers[0] != "111111" {
		t.Fatalf("第一个询问的回答 = %+v", r)
	}
	if got := h.a.currentChallenge(); got == nil || got.host.Name != "target" {
		t.Fatalf("回答后应显示下一个询问: %+v", got)
	}

	h.a.currentChallenge().answers[0] = "22" // 输了一半就锁定
	h.a.lockVault()
	if r := <-done; r.host != "target" || !errors.Is(r.err, errChallengeCancelled) {
		t.Fatalf("锁定后等待的询问应被取消: %+v", r)
	}
	if len(h.a.challenges) != 0 {
		t.Fatal("锁定后不应留下验证码询问")
	}
}

// 命令面板没有输入时，最近连接过的主机排在前面；有输入时按匹配程度排序。
func TestPaletteListsRecentHostsFirst(t *testing.T) {
	a := newApp()
	a.hosts = []hosts.Host{{ID: "x", Name: "alpha"}, {ID: "y", Name: "beta"}, {ID: "z", Name: "gamma"}}
	a.local.RecentHosts = []string{"z", "x"}
	names := func(query string) []string {
		var out []string
		for _, it := range a.paletteItems(query) {
			if it.host != nil {
				out = append(out, it.host.Name)
			}
		}
		return out
	}
	if got := names(""); len(got) != 3 || got[0] != "gamma" || got[1] != "alpha" || got[2] != "beta" {
		t.Fatalf("空查询的主机顺序 = %v，期望 [gamma alpha beta]", got)
	}
	if got := names("beta"); len(got) == 0 || got[0] != "beta" {
		t.Fatalf("有输入时按匹配排序: %v", got)
	}
}
