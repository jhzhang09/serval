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
