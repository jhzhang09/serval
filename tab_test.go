package main

import (
	"testing"
)

func TestTabManagement(t *testing.T) {
	h := newHarness(t)
	firstTab := h.connectDev()

	// 初始状态：连上 dev 主机（1 个标签）
	if len(h.a.tabs) != 1 {
		t.Fatalf("初始应有 1 个标签，实际 %d", len(h.a.tabs))
	}

	// 1. 复制标签页：使用 duplicateTab 开启同一个主机的第二个连接
	h.a.duplicateTab(firstTab)
	h.pump("第 2 个标签建立连接", func() bool {
		return len(h.a.tabs) == 2 && h.a.tabs[1].client != nil
	})
	secondTab := h.a.tabs[1]

	// 2. 复制本地终端
	h.a.openLocalTerminal()
	h.tt.Frame()
	if len(h.a.tabs) != 3 {
		t.Fatalf("打开本地终端后应有 3 个标签，实际 %d", len(h.a.tabs))
	}
	localTab := h.a.tabs[2]

	// 3. 复制本地终端标签
	h.a.duplicateTab(localTab)
	h.tt.Frame()
	if len(h.a.tabs) != 4 {
		t.Fatalf("复制本地终端后应有 4 个标签，实际 %d", len(h.a.tabs))
	}

	// 4. 测试关闭右侧标签页：关闭 secondTab (index 1) 右侧的所有标签页
	h.a.closeTabsToRight(secondTab)
	h.tt.Frame()
	if len(h.a.tabs) != 2 {
		t.Fatalf("关闭右侧标签页后应剩余 2 个标签，实际 %d", len(h.a.tabs))
	}
	if h.a.tabs[0] != firstTab || h.a.tabs[1] != secondTab {
		t.Fatal("剩余标签页不匹配")
	}

	// 5. 打开新的本地终端，测试关闭其他标签页
	h.a.openLocalTerminal()
	h.tt.Frame()
	if len(h.a.tabs) != 3 {
		t.Fatalf("应有 3 个标签，实际 %d", len(h.a.tabs))
	}
	h.a.closeOtherTabs(secondTab)
	h.tt.Frame()
	if len(h.a.tabs) != 1 || h.a.tabs[0] != secondTab {
		t.Fatalf("关闭其他标签页后应只保留 secondTab，实际 len=%d", len(h.a.tabs))
	}

	// 6. 测试关闭全部标签页
	h.a.closeAllTabs()
	h.tt.Frame()
	if len(h.a.tabs) != 0 {
		t.Fatalf("关闭全部标签页后应为空，实际 %d", len(h.a.tabs))
	}
}
