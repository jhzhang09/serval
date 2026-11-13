package ai

import (
	"strings"
)

// BlockKind 表示解析后的 Markdown 块类型。
type BlockKind uint8

const (
	BlockParagraph BlockKind = iota
	BlockHeading
	BlockList
	BlockCode
	BlockTable
	BlockQuote
	BlockDivider
)

// MarkdownBlock 表示单个可渲染的 Markdown 结构块。
type MarkdownBlock struct {
	Kind    BlockKind
	Level   int        // BlockHeading: 1-4
	Text    string     // BlockParagraph, BlockHeading, BlockQuote
	Ordered bool       // BlockList
	Items   []string   // BlockList
	Lang    string     // BlockCode
	Code    string     // BlockCode
	Headers []string   // BlockTable
	Rows    [][]string // BlockTable
}

// InlineKind 表示行内富文本片段类型。
type InlineKind uint8

const (
	SpanPlain InlineKind = iota
	SpanBold
	SpanCode
)

// InlineSpan 表示行内加粗或行内代码片段。
type InlineSpan struct {
	Kind InlineKind
	Text string
}

// ParseMarkdown 将模型输出的 Markdown 文本解析为结构化块，支持标题、列表、代码块、表格、引用与分割线。
func ParseMarkdown(input string) []MarkdownBlock {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	var blocks []MarkdownBlock
	var paraLines []string

	flushPara := func() {
		if len(paraLines) == 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(paraLines, "\n"))
		paraLines = nil
		if text != "" {
			blocks = append(blocks, MarkdownBlock{Kind: BlockParagraph, Text: text})
		}
	}

	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// 1. 围栏代码块
		if strings.HasPrefix(trimmed, "```") {
			flushPara()
			lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			i++
			var codeLines []string
			for i < len(lines) {
				if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
					i++
					break
				}
				codeLines = append(codeLines, lines[i])
				i++
			}
			blocks = append(blocks, MarkdownBlock{
				Kind: BlockCode,
				Lang: lang,
				Code: strings.TrimRight(strings.Join(codeLines, "\n"), "\n"),
			})
			continue
		}

		// 2. 空行分段
		if trimmed == "" {
			flushPara()
			i++
			continue
		}

		// 3. 分割线
		if isDividerLine(trimmed) {
			flushPara()
			blocks = append(blocks, MarkdownBlock{Kind: BlockDivider})
			i++
			continue
		}

		// 4. 标题 (# ~ ####)
		if level, heading, ok := parseHeadingLine(trimmed); ok {
			flushPara()
			blocks = append(blocks, MarkdownBlock{Kind: BlockHeading, Level: level, Text: heading})
			i++
			continue
		}

		// 5. GFM 表格 (表头行 + 分隔行)
		if i+1 < len(lines) && looksLikeTableRow(trimmed) && isTableSeparator(lines[i+1]) {
			flushPara()
			headers := splitTableRow(trimmed)
			i += 2
			var rows [][]string
			for i < len(lines) {
				rTrim := strings.TrimSpace(lines[i])
				if rTrim == "" || !looksLikeTableRow(rTrim) {
					break
				}
				cells := splitTableRow(rTrim)
				for len(cells) < len(headers) {
					cells = append(cells, "")
				}
				if len(cells) > len(headers) && len(headers) > 0 {
					cells = cells[:len(headers)]
				}
				rows = append(rows, cells)
				i++
			}
			blocks = append(blocks, MarkdownBlock{
				Kind:    BlockTable,
				Headers: headers,
				Rows:    rows,
			})
			continue
		}

		// 6. 引用块
		if strings.HasPrefix(trimmed, ">") {
			flushPara()
			var quotes []string
			for i < len(lines) {
				qTrim := strings.TrimSpace(lines[i])
				if !strings.HasPrefix(qTrim, ">") {
					break
				}
				quotes = append(quotes, strings.TrimSpace(strings.TrimPrefix(qTrim, ">")))
				i++
			}
			blocks = append(blocks, MarkdownBlock{
				Kind: BlockQuote,
				Text: strings.Join(quotes, "\n"),
			})
			continue
		}

		// 7. 无序/有序列表
		if ordered, item, ok := parseListItemLine(trimmed); ok {
			flushPara()
			items := []string{item}
			i++
			for i < len(lines) {
				nextTrim := strings.TrimSpace(lines[i])
				if nextTrim == "" {
					break
				}
				nextOrd, nextItem, ok := parseListItemLine(nextTrim)
				if !ok || nextOrd != ordered {
					break
				}
				items = append(items, nextItem)
				i++
			}
			blocks = append(blocks, MarkdownBlock{
				Kind:    BlockList,
				Ordered: ordered,
				Items:   items,
			})
			continue
		}

		paraLines = append(paraLines, line)
		i++
	}
	flushPara()
	return blocks
}

func isDividerLine(s string) bool {
	if len(s) < 3 {
		return false
	}
	ch := s[0]
	if ch != '-' && ch != '*' && ch != '_' {
		return false
	}
	count := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			continue
		}
		if s[i] != ch {
			return false
		}
		count++
	}
	return count >= 3
}

func parseHeadingLine(s string) (int, string, bool) {
	level := 0
	for level < len(s) && s[level] == '#' {
		level++
	}
	if level == 0 || level > 4 || level >= len(s) || s[level] != ' ' {
		return 0, "", false
	}
	return level, strings.TrimSpace(s[level+1:]), true
}

func parseListItemLine(s string) (ordered bool, text string, ok bool) {
	if len(s) >= 2 && (s[0] == '-' || s[0] == '*' || s[0] == '+') && s[1] == ' ' {
		return false, strings.TrimSpace(s[2:]), true
	}
	dot := strings.IndexByte(s, '.')
	if dot > 0 && dot <= 3 && dot+1 < len(s) && s[dot+1] == ' ' {
		for i := 0; i < dot; i++ {
			if s[i] < '0' || s[i] > '9' {
				return false, "", false
			}
		}
		return true, strings.TrimSpace(s[dot+2:]), true
	}
	return false, "", false
}

func looksLikeTableRow(s string) bool {
	return strings.Contains(s, "|")
}

func isTableSeparator(s string) bool {
	trimmed := strings.TrimSpace(s)
	if !strings.Contains(trimmed, "|") || !strings.Contains(trimmed, "-") {
		return false
	}
	cells := splitTableRow(trimmed)
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		c = strings.TrimSpace(c)
		c = strings.TrimPrefix(c, ":")
		c = strings.TrimSuffix(c, ":")
		if len(c) < 1 {
			return false
		}
		for i := 0; i < len(c); i++ {
			if c[i] != '-' {
				return false
			}
		}
	}
	return true
}

func splitTableRow(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	parts := strings.Split(s, "|")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.TrimSpace(p)
	}
	return out
}

// ParseInlineMarkdown 解析行内 **加粗** 与 `代码` 标记。
func ParseInlineMarkdown(s string) []InlineSpan {
	if s == "" {
		return nil
	}
	var out []InlineSpan
	i := 0
	plainStart := 0
	flushPlain := func(end int) {
		if end > plainStart {
			out = append(out, InlineSpan{Kind: SpanPlain, Text: s[plainStart:end]})
		}
	}

	for i < len(s) {
		if s[i] == '`' {
			if closeIdx := strings.IndexByte(s[i+1:], '`'); closeIdx > 0 {
				flushPlain(i)
				out = append(out, InlineSpan{Kind: SpanCode, Text: s[i+1 : i+1+closeIdx]})
				i = i + 1 + closeIdx + 1
				plainStart = i
				continue
			}
		}
		if i+1 < len(s) && s[i] == '*' && s[i+1] == '*' {
			if closeIdx := strings.Index(s[i+2:], "**"); closeIdx > 0 {
				flushPlain(i)
				out = append(out, InlineSpan{Kind: SpanBold, Text: s[i+2 : i+2+closeIdx]})
				i = i + 2 + closeIdx + 2
				plainStart = i
				continue
			}
		}
		i++
	}
	flushPlain(len(s))
	return out
}
