package main

import (
	"testing"

	"serval/core/hosts"
	"serval/core/vault"
)

func TestSortHosts(t *testing.T) {
	// 测试未指定 SortOrder 时按名称不区分大小写排序
	list1 := []hosts.Host{
		{Name: "zebra"},
		{Name: "Alpha"},
		{Name: "beta"},
	}
	sortHosts(list1)
	if list1[0].Name != "Alpha" || list1[1].Name != "beta" || list1[2].Name != "zebra" {
		t.Fatalf("未指定排序权重时应按字母排序: %+v", list1)
	}

	// 测试显式 SortOrder 优先于字母序
	list2 := []hosts.Host{
		{Name: "zebra", SortOrder: 100},
		{Name: "Alpha", SortOrder: 300},
		{Name: "beta", SortOrder: 200},
	}
	sortHosts(list2)
	if list2[0].Name != "zebra" || list2[1].Name != "beta" || list2[2].Name != "Alpha" {
		t.Fatalf("显式排序权重未生效: %+v", list2)
	}

	// 测试混合：已指定权重的排在未指定（0）的前面
	list3 := []hosts.Host{
		{Name: "apple", SortOrder: 0},
		{Name: "zebra", SortOrder: 50},
	}
	sortHosts(list3)
	if list3[0].Name != "zebra" || list3[1].Name != "apple" {
		t.Fatalf("已设置权重的主机应优先于未设置权重的主机: %+v", list3)
	}
}

func TestMoveHostOrderWithinGroup(t *testing.T) {
	h := newHarness(t)
	// 在 harness 中创建三台同组主机
	h1 := hosts.Host{ID: "h1", Name: "Host-A", Address: "10.0.0.1", Group: "Servers"}
	h2 := hosts.Host{ID: "h2", Name: "Host-B", Address: "10.0.0.2", Group: "Servers"}
	h3 := hosts.Host{ID: "h3", Name: "Host-C", Address: "10.0.0.3", Group: "Servers"}
	if err := h.a.apply(hostChange(h1), hostChange(h2), hostChange(h3)); err != nil {
		t.Fatal(err)
	}

	// 初始状态下按字母序应该是 A, B, C
	initHosts := h.a.hostsInGroup("Servers")
	if len(initHosts) != 3 || initHosts[0].ID != "h1" || initHosts[1].ID != "h2" || initHosts[2].ID != "h3" {
		t.Fatalf("初始顺序异常: %+v", initHosts)
	}

	// 将 Host-C 拖到 Host-A 之前
	if err := h.a.moveHostOrder("h3", "h1", "Servers"); err != nil {
		t.Fatal(err)
	}

	reordered := h.a.hostsInGroup("Servers")
	if len(reordered) != 3 || reordered[0].ID != "h3" || reordered[1].ID != "h1" || reordered[2].ID != "h2" {
		t.Fatalf("同组拖动重排后顺序错误: %+v", reordered)
	}
	if reordered[0].SortOrder >= reordered[1].SortOrder || reordered[1].SortOrder >= reordered[2].SortOrder {
		t.Fatalf("重排后 SortOrder 未严格递增: %+v", reordered)
	}

	// 再次测试拖拽自身不做任何改动
	if err := h.a.moveHostOrder("h3", "h3", "Servers"); err != nil {
		t.Fatal(err)
	}
}

func TestMoveHostOrderAcrossGroups(t *testing.T) {
	h := newHarness(t)
	h1 := hosts.Host{ID: "h1", Name: "Web-1", Address: "10.0.0.1", Group: "Web"}
	h2 := hosts.Host{ID: "h2", Name: "DB-1", Address: "10.0.0.2", Group: "Database"}
	h3 := hosts.Host{ID: "h3", Name: "DB-2", Address: "10.0.0.3", Group: "Database"}
	if err := h.a.apply(hostChange(h1), hostChange(h2), hostChange(h3)); err != nil {
		t.Fatal(err)
	}

	// 将 Web-1 拖到 DB-1 之前（跨组移动并插入）
	if err := h.a.moveHostOrder("h1", "h2", "Database"); err != nil {
		t.Fatal(err)
	}

	dbHosts := h.a.hostsInGroup("Database")
	if len(dbHosts) != 3 || dbHosts[0].ID != "h1" || dbHosts[1].ID != "h2" || dbHosts[2].ID != "h3" {
		t.Fatalf("跨组插入后目标组主机顺序错误: %+v", dbHosts)
	}
	moved, ok := h.a.hostByID("h1")
	if !ok || moved.Group != "Database" {
		t.Fatalf("被移动主机的 Group 未更新: %+v", moved)
	}

	// 将 DB-2 拖到 Web 分组的标题行（targetHostID 为空）
	if err := h.a.moveHostOrder("h3", "", "Web"); err != nil {
		t.Fatal(err)
	}
	webHosts := h.a.hostsInGroup("Web")
	if len(webHosts) != 1 || webHosts[0].ID != "h3" || webHosts[0].Group != "Web" {
		t.Fatalf("拖到空分组标题行后未正确归组: %+v", webHosts)
	}
}

func TestRenameAndDeleteGroup(t *testing.T) {
	h := newHarness(t)
	h1 := hosts.Host{ID: "h1", Name: "Web-1", Address: "10.0.0.1", Group: "Web"}
	h2 := hosts.Host{ID: "h2", Name: "DB-1", Address: "10.0.0.2", Group: "Database"}
	if err := h.a.apply(hostChange(h1), hostChange(h2)); err != nil {
		t.Fatal(err)
	}

	// 新建两个空自定义分组并折叠 Web 分组
	h.a.openNewGroup()
	h.a.groupSheet.name = "Staging"
	h.a.submitGroupDialog()
	h.a.openNewGroup()
	h.a.groupSheet.name = "TempEmpty"
	h.a.submitGroupDialog()
	h.a.toggleGroup("Web")
	if !h.a.groupCollapsed("Web") {
		t.Fatal("Web 分组应处于折叠状态")
	}

	// 重命名包含主机的分组，同时检查折叠状态随分组名更新
	if err := h.a.renameGroup("Web", "Frontend"); err != nil {
		t.Fatal(err)
	}
	if len(h.a.allHostsInGroup("Web")) != 0 {
		t.Fatal("重命名后旧分组不应保留主机")
	}
	if got := h.a.allHostsInGroup("Frontend"); len(got) != 1 || got[0].ID != "h1" {
		t.Fatalf("重命名后新分组主机异常: %+v", got)
	}
	if !h.a.groupCollapsed("Frontend") || h.a.groupCollapsed("Web") {
		t.Fatalf("分组折叠状态未随重命名更新: %+v", h.a.local.CollapsedGroups)
	}

	// 将空分组重命名为已存在的自定义分组时应去重
	if err := h.a.renameGroup("TempEmpty", "Staging"); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, g := range h.a.customGroups {
		if g == "Staging" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("合并重命名后 customGroups 存在重复项: %+v", h.a.customGroups)
	}

	// 删除空分组
	h.a.deleteEmptyGroup("Staging")
	for _, g := range h.a.customGroups {
		if g == "Staging" {
			t.Fatalf("空分组未删除: %+v", h.a.customGroups)
		}
	}
}

func TestCustomGroupsPersistAcrossReopenAndLock(t *testing.T) {
	h := newHarness(t)
	h.a.rememberOnDevice(true)

	h.a.openNewGroup()
	h.a.groupSheet.name = "EmptyGroup"
	h.a.submitGroupDialog()
	h.tt.Frame()
	if !h.tt.HasText("EmptyGroup") {
		t.Fatalf("新建空分组后应在侧栏显示: %q", h.tt.Texts())
	}

	// 重启后空分组应仍在侧栏。
	h.reopen()
	if !h.tt.HasText("EmptyGroup") {
		t.Fatalf("重启后空分组应保留: %q", h.tt.Texts())
	}

	// 锁定后应清空内存中的分组列表，再次解锁后恢复。
	h.a.lockVault()
	h.tt.Frame()
	if len(h.a.customGroups) != 0 {
		t.Fatalf("锁库后不应保留 customGroups: %+v", h.a.customGroups)
	}
	h.a.unlock.password = masterPassword
	h.a.submitUnlock()
	h.pump("重新解锁", func() bool { return !h.a.unlock.busy && h.a.vault.State() == vault.Unlocked })
	h.tt.Frame()
	if !h.tt.HasText("EmptyGroup") {
		t.Fatalf("重新解锁后空分组应恢复: %q", h.tt.Texts())
	}
}

func TestMoveHostToEndOfOwnGroup(t *testing.T) {
	h := newHarness(t)
	h1 := hosts.Host{ID: "h1", Name: "Host-A", Address: "10.0.0.1", Group: "Servers"}
	h2 := hosts.Host{ID: "h2", Name: "Host-B", Address: "10.0.0.2", Group: "Servers"}
	h3 := hosts.Host{ID: "h3", Name: "Host-C", Address: "10.0.0.3", Group: "Servers"}
	if err := h.a.apply(hostChange(h1), hostChange(h2), hostChange(h3)); err != nil {
		t.Fatal(err)
	}

	// 将首位主机拖到同组末尾（targetHostID 为空）。
	if err := h.a.moveHostOrder("h1", "", "Servers"); err != nil {
		t.Fatal(err)
	}
	reordered := h.a.hostsInGroup("Servers")
	if len(reordered) != 3 || reordered[0].ID != "h2" || reordered[1].ID != "h3" || reordered[2].ID != "h1" {
		t.Fatalf("移到同组末尾后顺序错误: %+v", reordered)
	}
}
