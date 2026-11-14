package main

import (
	"cmp"
	"slices"
	"strings"

	"serval/core/hosts"
	"serval/core/vault"
)

// hostDrag 表示在侧栏中被拖动的主机数据。
type hostDrag struct {
	ID string
}

// allHostsInGroup 返回指定分组里的全部主机，按排序权重及名称排序。
func (a *app) allHostsInGroup(group string) []hosts.Host {
	var list []hosts.Host
	for _, h := range a.hosts {
		if h.Group == group {
			list = append(list, h)
		}
	}
	sortHosts(list)
	return list
}

// sortHosts 对主机切片排序：优先按非零 SortOrder 升序；未排序（0）排在后面；相同权重按名称不区分大小写排序。
func sortHosts(list []hosts.Host) {
	slices.SortStableFunc(list, func(x, y hosts.Host) int {
		if x.SortOrder != y.SortOrder {
			if x.SortOrder != 0 && y.SortOrder != 0 {
				return cmp.Compare(x.SortOrder, y.SortOrder)
			}
			if x.SortOrder != 0 {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
}

// moveHostOrder 处理主机拖拽排序与跨分组移动。
// 当 targetHostID 非空时，表示插入到 targetHostID 所在位置；
// 当 targetHostID 为空且 targetGroup 非空时，表示移动到 targetGroup 分组末尾。
func (a *app) moveHostOrder(draggedID, targetHostID, targetGroup string) error {
	dragged, ok := a.hostByID(draggedID)
	if !ok || dragged.ID == "" {
		return nil
	}
	if targetHostID != "" && draggedID == targetHostID {
		return nil
	}

	var targetGroupFinal string
	if targetHostID != "" {
		target, ok := a.hostByID(targetHostID)
		if !ok {
			return nil
		}
		targetGroupFinal = target.Group
	} else {
		targetGroupFinal = targetGroup
	}

	// 目标分组内的既有主机列表（排除掉被拖拽的主机自身）
	existing := a.allHostsInGroup(targetGroupFinal)
	filtered := make([]hosts.Host, 0, len(existing)+1)
	for _, h := range existing {
		if h.ID != dragged.ID {
			filtered = append(filtered, h)
		}
	}

	dragged.Group = targetGroupFinal

	// 计算插入位置
	insertIdx := len(filtered)
	if targetHostID != "" {
		for i, h := range filtered {
			if h.ID == targetHostID {
				insertIdx = i
				break
			}
		}
	}

	reordered := slices.Insert(filtered, insertIdx, dragged)

	// 为重排后的列表依次赋予严格递增的排序权重（步长 1000，方便未来插值）
	var changes []vault.Change
	for i, h := range reordered {
		desiredOrder := (i + 1) * 1000
		desiredGroup := targetGroupFinal
		if h.SortOrder != desiredOrder || h.Group != desiredGroup {
			h.SortOrder = desiredOrder
			h.Group = desiredGroup
			changes = append(changes, hostChange(h))
		}
	}

	if len(changes) == 0 {
		return nil
	}
	return a.apply(changes...)
}

// moveHostStep 在当前分组内将主机向上（delta < 0）或向下（delta > 0）移动一位。
func (a *app) moveHostStep(h hosts.Host, delta int) error {
	list := a.allHostsInGroup(h.Group)
	idx := slices.IndexFunc(list, func(x hosts.Host) bool { return x.ID == h.ID })
	if idx < 0 {
		return nil
	}
	targetIdx := idx + delta
	if targetIdx < 0 || targetIdx >= len(list) {
		return nil
	}
	list[idx], list[targetIdx] = list[targetIdx], list[idx]
	var changes []vault.Change
	for i, item := range list {
		desired := (i + 1) * 1000
		if item.SortOrder != desired {
			item.SortOrder = desired
			changes = append(changes, hostChange(item))
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return a.apply(changes...)
}

// moveHostToGroup 将主机移动到目标分组末尾。
func (a *app) moveHostToGroup(h hosts.Host, targetGroup string) error {
	if targetGroup == "" || h.Group == targetGroup {
		return nil
	}
	return a.moveHostOrder(h.ID, "", targetGroup)
}

// renameGroup 重命名分组，批量更新该分组下所有主机。
func (a *app) renameGroup(oldName, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" || newName == oldName {
		return nil
	}
	var changes []vault.Change
	for _, h := range a.hosts {
		if h.Group == oldName {
			h.Group = newName
			changes = append(changes, hostChange(h))
		}
	}
	if len(changes) > 0 {
		if err := a.apply(changes...); err != nil {
			return err
		}
	}
	var updated []string
	for _, g := range a.customGroups {
		if g == oldName {
			g = newName
		}
		if g != "" && !slices.Contains(updated, g) {
			updated = append(updated, g)
		}
	}
	a.customGroups = updated
	if slices.Contains(a.local.CollapsedGroups, oldName) {
		next := a.local
		var collapsed []string
		for _, g := range a.local.CollapsedGroups {
			if g == oldName {
				g = newName
			}
			if g != "" && !slices.Contains(collapsed, g) {
				collapsed = append(collapsed, g)
			}
		}
		next.CollapsedGroups = collapsed
		_ = a.saveLocal(next)
	}
	return nil
}

// deleteEmptyGroup 删除没有主机的空分组。
func (a *app) deleteEmptyGroup(name string) {
	a.customGroups = slices.DeleteFunc(a.customGroups, func(g string) bool { return g == name })
}

type tabDrag struct {
	ID int
}

// moveTab 拖动标签调整顺序，并保持当前激活标签索引同步。
func (a *app) moveTab(draggedID, targetID int) {
	if draggedID == targetID {
		return
	}
	from := slices.IndexFunc(a.tabs, func(t *tab) bool { return t.id == draggedID })
	to := slices.IndexFunc(a.tabs, func(t *tab) bool { return t.id == targetID })
	if from < 0 || to < 0 || from == to {
		return
	}
	var activeTab *tab
	if a.active >= 0 && a.active < len(a.tabs) {
		activeTab = a.tabs[a.active]
	}
	moved := a.tabs[from]
	a.tabs = slices.Delete(a.tabs, from, from+1)
	if from < to {
		to--
	}
	a.tabs = slices.Insert(a.tabs, to, moved)
	if activeTab != nil {
		if idx := slices.Index(a.tabs, activeTab); idx >= 0 {
			a.active = idx
		}
	}
	a.saveSessionLayout()
}

// moveTabStep 将当前激活标签页向左（delta < 0）或向右（delta > 0）移动一位。
func (a *app) moveTabStep(delta int) {
	if len(a.tabs) < 2 || a.active < 0 || a.active >= len(a.tabs) {
		return
	}
	target := a.active + delta
	if target < 0 || target >= len(a.tabs) {
		return
	}
	next := slices.Clone(a.tabs)
	next[a.active], next[target] = next[target], next[a.active]
	a.tabs = next
	a.active = target
	a.saveSessionLayout()
}

// moveTabItem 将指定标签页向左（delta < 0）或向右（delta > 0）移动一位，并保持当前激活索引同步。
func (a *app) moveTabItem(t *tab, delta int) {
	idx := slices.Index(a.tabs, t)
	if idx < 0 {
		return
	}
	target := idx + delta
	if target < 0 || target >= len(a.tabs) {
		return
	}
	var activeTab *tab
	if a.active >= 0 && a.active < len(a.tabs) {
		activeTab = a.tabs[a.active]
	}
	next := slices.Clone(a.tabs)
	next[idx], next[target] = next[target], next[idx]
	a.tabs = next
	if activeTab != nil {
		if cur := slices.Index(a.tabs, activeTab); cur >= 0 {
			a.active = cur
		}
	}
	a.saveSessionLayout()
}

// switchTabStep 循环切换到上一个（delta < 0）或下一个（delta > 0）标签页。
func (a *app) switchTabStep(delta int) {
	if len(a.tabs) < 2 {
		return
	}
	cur := a.active
	if cur < 0 || cur >= len(a.tabs) {
		cur = 0
	}
	a.active = (cur + delta + len(a.tabs)) % len(a.tabs)
	a.saveSessionLayout()
}

type groupDialogState struct {
	open, fresh, rename bool
	oldName, name, err  string
}

func (a *app) openNewGroup() {
	a.groupSheet = groupDialogState{open: true, fresh: true}
}

func (a *app) openRenameGroup(oldName string) {
	a.groupSheet = groupDialogState{open: true, fresh: true, rename: true, oldName: oldName, name: oldName}
}

func (a *app) submitGroupDialog() {
	name := strings.TrimSpace(a.groupSheet.name)
	if name == "" {
		a.groupSheet.err = "请填写分组名称"
		return
	}
	if a.groupSheet.rename {
		if err := a.renameGroup(a.groupSheet.oldName, name); err != nil {
			a.groupSheet.err = err.Error()
			return
		}
	} else {
		if !slices.Contains(a.groups(), name) {
			a.customGroups = append(a.customGroups, name)
		}
	}
	a.groupSheet = groupDialogState{}
}
