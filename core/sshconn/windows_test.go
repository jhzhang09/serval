//go:build windows

package sshconn

import "testing"

func requireDevShell(t *testing.T) {
	t.Helper()
	t.Skip("Windows 不支持 devsshd PTY")
}

func makeFIFO(*testing.T, string) bool {
	return false
}

func waitProcessGone(t *testing.T, _ int) {
	t.Helper()
	t.Skip("Windows 不支持 devsshd PTY")
}
