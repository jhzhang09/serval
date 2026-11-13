package ai

import "testing"

func TestParseMarkdown(t *testing.T) {
	src := `# 服务器配置概览

> CentOS Linux 7

| 项目 | 配置 |
| --- | --- |
| 系统 | CentOS 7 |
| CPU | **4 vCPU** |

1. 运行正常
2. 无明显瓶颈

` + "```bash\nlscpu | head -n 5\n```" + `

---

检查完成，可运行 ` + "`free -h`" + ` 复核。`

	blocks := ParseMarkdown(src)
	if len(blocks) != 7 {
		t.Fatalf("expected 7 blocks, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Kind != BlockHeading || blocks[0].Level != 1 || blocks[0].Text != "服务器配置概览" {
		t.Fatalf("unexpected heading: %+v", blocks[0])
	}
	if blocks[1].Kind != BlockQuote || blocks[1].Text != "CentOS Linux 7" {
		t.Fatalf("unexpected quote: %+v", blocks[1])
	}
	if blocks[2].Kind != BlockTable || len(blocks[2].Headers) != 2 || len(blocks[2].Rows) != 2 || blocks[2].Rows[1][1] != "**4 vCPU**" {
		t.Fatalf("unexpected table: %+v", blocks[2])
	}
	if blocks[3].Kind != BlockList || !blocks[3].Ordered || len(blocks[3].Items) != 2 {
		t.Fatalf("unexpected list: %+v", blocks[3])
	}
	if blocks[4].Kind != BlockCode || blocks[4].Lang != "bash" || blocks[4].Code != "lscpu | head -n 5" {
		t.Fatalf("unexpected code block: %+v", blocks[4])
	}
	if blocks[5].Kind != BlockDivider {
		t.Fatalf("unexpected divider: %+v", blocks[5])
	}
	if blocks[6].Kind != BlockParagraph {
		t.Fatalf("unexpected paragraph: %+v", blocks[6])
	}

	spans := ParseInlineMarkdown("CPU **4 vCPU** 运行 `free -h`")
	if len(spans) != 4 || spans[1].Kind != SpanBold || spans[1].Text != "4 vCPU" || spans[3].Kind != SpanCode || spans[3].Text != "free -h" {
		t.Fatalf("unexpected inline spans: %+v", spans)
	}
}
