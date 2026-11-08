package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"serval/core/sshconn"
	"serval/internal/devsshd"
)

// 只有网络类错误值得自动重试；认证和主机密钥类错误重试只会重复失败。
func TestReconnectRetryable(t *testing.T) {
	permanent := []error{
		&sshconn.HostKeyChangedError{Host: "x"},
		fmt.Errorf("跳板机：%w", sshconn.ErrHostKeyRejected),
		sshconn.ErrAuthFailed,
		&sshconn.HopError{Hop: 0, Host: "b", Err: sshconn.ErrAuthFailed},
		sshconn.ErrNoAuthMethod,
		sshconn.ErrKeyEncrypted,
		sshconn.ErrBadPassphrase,
	}
	for _, err := range permanent {
		if reconnectRetryable(err) {
			t.Errorf("%v 不应自动重试", err)
		}
	}
	for _, err := range []error{io.EOF, errors.New("dial tcp: connection refused"), errors.New("连接超时")} {
		if !reconnectRetryable(err) {
			t.Errorf("%v 应自动重试", err)
		}
	}
}

// 服务器换了主机密钥（可能被冒充）时，自动重连立即停止并说明原因，不会一直重试。
func TestAutoReconnectStopsOnHostKeyChange(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	h.a.pingInterval = 50 * time.Millisecond
	h.a.reconnectBaseDelay = 50 * time.Millisecond

	addr := h.server.Addr().String()
	h.server.Close()
	h.pump("开始重连", func() bool { return tb.reconnecting })

	impostor, err := devsshd.Start(devsshd.Options{Addr: addr, Password: testPassword}) // 新的主机密钥
	if err != nil {
		t.Skipf("端口被占用: %v", err)
	}
	defer impostor.Close()
	h.pump("停止重连", func() bool { return !tb.reconnecting })
	if !strings.Contains(h.a.notice, "主机密钥和上次不同") || !strings.Contains(h.a.notice, "已停止自动重连") {
		t.Fatalf("应说明停止原因: %q", h.a.notice)
	}
	attempt := tb.reconnectAttempt
	time.Sleep(300 * time.Millisecond) // 重试间隔是 50 ms，停止后不应再有新的尝试
	h.pump("确认没有新的尝试", func() bool { return true })
	if tb.reconnecting || tb.reconnectAttempt != attempt {
		t.Fatalf("停止后不应继续重试: reconnecting=%v attempt=%d→%d", tb.reconnecting, attempt, tb.reconnectAttempt)
	}
}

// 用户在 shell 里输入 exit 是会话结束，不是断线，不应自动重连。
func TestExitDoesNotAutoReconnect(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	h.a.pingInterval = 50 * time.Millisecond
	tb.focus.term.Send([]byte("exit\n"))
	h.pump("会话结束提示", func() bool { return h.tt.HasText(tb.host.Name + " 的会话已结束") })
	if tb.reconnecting {
		t.Fatal("exit 不应触发自动重连")
	}
}

// 恢复会话时连接并行完成，先连上的不一定在前：标签按上次的位置排列，
// 只切换到上次激活的标签，不抢走用户在恢复期间打开的标签。
func TestRestoredTabsKeepOrderAndActive(t *testing.T) {
	a := newApp()
	a.restoreActive = 2
	second := &tab{id: 2, restoreSlot: 2}
	third := &tab{id: 3, restoreSlot: 3}
	first := &tab{id: 1, restoreSlot: 1}
	a.insertRestoredTab(third) // 第三个最先连上
	if a.current() != third {
		t.Fatal("还没有标签时，第一个连上的成为当前标签")
	}
	a.insertRestoredTab(second) // 上次激活的是第二个
	if a.current() != second {
		t.Fatal("上次激活的标签连上后应切换过去")
	}
	user := &tab{id: 9} // 恢复过程中用户自己打开的标签
	a.insertRestoredTab(user)
	if a.current() != user {
		t.Fatal("用户打开的标签应成为当前标签")
	}
	a.insertRestoredTab(first)
	if a.current() != user {
		t.Fatal("后连上的恢复标签不应抢走用户的当前标签")
	}
	var order []int
	for _, t := range a.tabs {
		order = append(order, t.id)
	}
	if fmt.Sprint(order) != "[1 2 3 9]" {
		t.Fatalf("标签顺序 = %v，期望 [1 2 3 9]", order)
	}
}
