//go:build !windows

package sshconn

import (
	"syscall"
	"testing"
	"time"
)

func requireDevShell(*testing.T) {}

func makeFIFO(t *testing.T, path string) bool {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
	return true
}

func waitProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return // 进程已退出
		}
		time.Sleep(20 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("关闭会话后 shell 进程 %d 仍在运行", pid)
}
