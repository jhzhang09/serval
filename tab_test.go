package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
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

// 回归：所有标签都关闭后，继续按 Cmd+W / Ctrl+Shift+W 必须被应用层拦截，不能触发系统关闭窗口。
func TestEmptyStateKeyWDoesNotCloseWindow(t *testing.T) {
	h := newHarness(t)
	h.a.openLocalTerminal()
	h.tt.Frame()
	if len(h.a.tabs) != 1 {
		t.Fatalf("打开本地终端失败: tabs=%d", len(h.a.tabs))
	}

	// 确认按 KeyW 能关闭当前唯一标签页回到空状态，且随后在空状态再按 KeyW 仍稳定留在空状态。
	h.tt.Key(chromeShortcut(), ui.KeyW)
	h.tt.Frame()
	if len(h.a.tabs) != 0 {
		t.Fatalf("关闭唯一标签后应为 0 个标签，实际 %d", len(h.a.tabs))
	}

	// 验证快捷键处理器仍然已注册（即使 a.current() == nil），不会透传到底层默认关闭窗口。
	h.tt.Key(chromeShortcut(), ui.KeyW)
	h.tt.Frame()
	if len(h.a.tabs) != 0 || !h.tt.HasText("连接一台主机") {
		t.Fatalf("空状态下按 KeyW 后界面异常: tabs=%d texts=%q", len(h.a.tabs), h.tt.Texts())
	}
}

func TestTabReorderShortcutsAndMenu(t *testing.T) {
	h := newHarness(t)
	first := h.connectDev()
	h.a.openLocalTerminal()
	h.tt.Frame()
	second := h.a.tabs[1]
	if h.a.active != 1 {
		t.Fatalf("新打开标签应激活在下标 1，实际 %d", h.a.active)
	}

	// 快捷键切换上一/下一标签页
	h.tt.Key(chromeShortcut(), ui.KeyBracketLeft)
	h.tt.Frame()
	if h.a.active != 0 || h.a.current() != first {
		t.Fatalf("Cmd+[ 后应切到第 0 个标签，实际 active=%d", h.a.active)
	}
	h.tt.Key(chromeShortcut(), ui.KeyBracketRight)
	h.tt.Frame()
	if h.a.active != 1 || h.a.current() != second {
		t.Fatalf("Cmd+] 后应切到第 1 个标签，实际 active=%d", h.a.active)
	}

	// 快捷键左移标签页（Cmd+Alt+[）：[first, second] -> [second, first]，active 变为 0
	h.tt.Key(chromeShortcut()|ui.Alt, ui.KeyBracketLeft)
	h.tt.Frame()
	if h.a.tabs[0] != second || h.a.tabs[1] != first || h.a.active != 0 {
		t.Fatalf("左移标签后顺序或激活索引错误: tabs=[%d,%d] active=%d", h.a.tabs[0].id, h.a.tabs[1].id, h.a.active)
	}

	// 快捷键右移标签页（Cmd+Alt+]）：[second, first] -> [first, second]，active 变为 1
	h.tt.Key(chromeShortcut()|ui.Alt, ui.KeyBracketRight)
	h.tt.Frame()
	if h.a.tabs[0] != first || h.a.tabs[1] != second || h.a.active != 1 {
		t.Fatalf("右移标签后顺序或激活索引错误: tabs=[%d,%d] active=%d", h.a.tabs[0].id, h.a.tabs[1].id, h.a.active)
	}

	// 右键标签页菜单“左移标签页”
	if err := h.tt.RightClick("本机"); err != nil {
		t.Fatalf("右键标签页失败: %v", err)
	}
	if err := h.tt.ChooseMenuItem("左移标签页"); err != nil {
		t.Fatalf("选择左移标签页失败: %v，菜单=%q", err, h.tt.Menu())
	}
	h.tt.Frame()
	if h.a.tabs[0] != second || h.a.tabs[1] != first || h.a.active != 0 {
		t.Fatalf("菜单左移后顺序或激活索引错误: tabs=[%d,%d] active=%d", h.a.tabs[0].id, h.a.tabs[1].id, h.a.active)
	}

	// 右键标签页菜单“右移标签页”
	if err := h.tt.RightClick("本机"); err != nil {
		t.Fatalf("右键标签页失败: %v", err)
	}
	if err := h.tt.ChooseMenuItem("右移标签页"); err != nil {
		t.Fatalf("选择右移标签页失败: %v，菜单=%q", err, h.tt.Menu())
	}
	h.tt.Frame()
	if h.a.tabs[0] != first || h.a.tabs[1] != second || h.a.active != 1 {
		t.Fatalf("菜单右移后顺序或激活索引错误: tabs=[%d,%d] active=%d", h.a.tabs[0].id, h.a.tabs[1].id, h.a.active)
	}
}

func TestCloseAllStopsTabMonitor(t *testing.T) {
	h := newHarness(t)
	tb := h.connectDev()
	stopCh := tb.monitorStop
	if stopCh == nil {
		t.Fatal("连接 SSH 主机后应自动启动服务器监控")
	}
	h.a.closeAll()
	if tb.monitorStop != nil {
		t.Fatal("closeAll 后应清空 monitorStop")
	}
	select {
	case <-stopCh:
	default:
		t.Fatal("closeAll 后应关闭监控停止通道")
	}
}
