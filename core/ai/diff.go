package ai

import (
	"fmt"
	"strings"
)

type diffOpKind uint8

const (
	diffEqual diffOpKind = iota
	diffDel
	diffAdd
)

type diffOp struct {
	kind diffOpKind
	text string
}

// UnifiedDiff 生成纯 Go 实现的 Unified Diff（用于 sftp_write / sftp_edit 审批预览）。
// 当 before 为空时视作新建文件（--- /dev/null）。最多输出约 400 行差异，超出部分附带截断摘要。
func UnifiedDiff(filePath, before, after string, contextLines int) string {
	if contextLines <= 0 {
		contextLines = 3
	}
	if before == after {
		return ""
	}
	aLines := splitLinesForDiff(before)
	bLines := splitLinesForDiff(after)

	var out strings.Builder
	if before == "" {
		out.WriteString("--- /dev/null\n")
	} else {
		out.WriteString("--- a/" + filePath + "\n")
	}
	out.WriteString("+++ b/" + filePath + "\n")

	ops := computeLineDiff(aLines, bLines)
	type hunkRange struct {
		startOp, endOp int
		aStart, bStart int
	}

	// 找到所有变更区间并向外扩展 contextLines，再合并相邻 hunk
	var changeIndices []int
	for i, op := range ops {
		if op.kind != diffEqual {
			changeIndices = append(changeIndices, i)
		}
	}
	if len(changeIndices) == 0 {
		return ""
	}

	type rawSpan struct{ l, r int }
	var spans []rawSpan
	for _, idx := range changeIndices {
		l := max(0, idx-contextLines)
		r := min(len(ops)-1, idx+contextLines)
		if len(spans) > 0 && l <= spans[len(spans)-1].r+1 {
			spans[len(spans)-1].r = max(spans[len(spans)-1].r, r)
		} else {
			spans = append(spans, rawSpan{l: l, r: r})
		}
	}

	// 预计算每个 op 对应的 1-based 行号
	aPos := make([]int, len(ops)+1)
	bPos := make([]int, len(ops)+1)
	curA, curB := 1, 1
	for i, op := range ops {
		aPos[i] = curA
		bPos[i] = curB
		switch op.kind {
		case diffEqual:
			curA++
			curB++
		case diffDel:
			curA++
		case diffAdd:
			curB++
		}
	}
	aPos[len(ops)] = curA
	bPos[len(ops)] = curB

	const maxOutLines = 400
	emitted := 2
	truncated := false
	for _, sp := range spans {
		aCount, bCount := 0, 0
		for i := sp.l; i <= sp.r; i++ {
			switch ops[i].kind {
			case diffEqual:
				aCount++
				bCount++
			case diffDel:
				aCount++
			case diffAdd:
				bCount++
			}
		}
		aStart := aPos[sp.l]
		bStart := bPos[sp.l]
		if before == "" {
			aStart = 0
		}
		if emitted >= maxOutLines {
			truncated = true
			break
		}
		out.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", aStart, aCount, bStart, bCount))
		emitted++
		for i := sp.l; i <= sp.r; i++ {
			if emitted >= maxOutLines {
				truncated = true
				break
			}
			op := ops[i]
			switch op.kind {
			case diffEqual:
				out.WriteString(" " + op.text + "\n")
			case diffDel:
				out.WriteString("-" + op.text + "\n")
			case diffAdd:
				out.WriteString("+" + op.text + "\n")
			}
			emitted++
		}
		if truncated {
			break
		}
	}
	if truncated {
		out.WriteString("... [Diff 预览已截断，请检查关键变更]\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

func splitLinesForDiff(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return []string{""}
	}
	return strings.Split(s, "\n")
}

func computeLineDiff(a, b []string) []diffOp {
	n, m := len(a), len(b)
	// 若文本极大则退化为首尾公共前缀/后缀 + 整块替换，避免 O(N*M) 内存爆炸
	if n*m > 250000 {
		pref := 0
		for pref < n && pref < m && a[pref] == b[pref] {
			pref++
		}
		suf := 0
		for suf < n-pref && suf < m-pref && a[n-1-suf] == b[m-1-suf] {
			suf++
		}
		var ops []diffOp
		for i := 0; i < pref; i++ {
			ops = append(ops, diffOp{diffEqual, a[i]})
		}
		for i := pref; i < n-suf; i++ {
			ops = append(ops, diffOp{diffDel, a[i]})
		}
		for i := pref; i < m-suf; i++ {
			ops = append(ops, diffOp{diffAdd, b[i]})
		}
		for i := n - suf; i < n; i++ {
			ops = append(ops, diffOp{diffEqual, a[i]})
		}
		return ops
	}

	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			ops = append(ops, diffOp{diffEqual, a[i]})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			ops = append(ops, diffOp{diffDel, a[i]})
			i++
		} else {
			ops = append(ops, diffOp{diffAdd, b[j]})
			j++
		}
	}
	for i < n {
		ops = append(ops, diffOp{diffDel, a[i]})
		i++
	}
	for j < m {
		ops = append(ops, diffOp{diffAdd, b[j]})
		j++
	}
	return ops
}
