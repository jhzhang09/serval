package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"serval/core/hosts"
)

func textIndex(texts []string, exact string) int {
	for i, s := range texts {
		if s == exact {
			return i
		}
	}
	return -1
}

// 侧栏过滤只留下名称、地址、用户或分组匹配的主机，不打开命令面板。
func TestSidebarFilterHidesOtherHosts(t *testing.T) {
	h := newHarness(t,
		hosts.Host{ID: "web", Name: "web-01", Address: "10.0.0.3", Port: 22, User: "deploy", Group: "站点"},
		hosts.Host{ID: "db", Name: "prod-db", Address: "10.9.9.9", Port: 22, User: "admin", Group: defaultGroup},
	)
	h.tt.Frame()

	h.a.hostFilter = "WEB"
	h.tt.Frame()
	texts := h.tt.Texts()
	if textIndex(texts, "web-01") < 0 || textIndex(texts, "prod-db") >= 0 || textIndex(texts, "dev") >= 0 {
		t.Fatalf("应按名称过滤（不区分大小写）: %q", texts)
	}
	if h.a.pal.open {
		t.Fatal("过滤主机不应打开命令面板")
	}

	h.a.hostFilter = "10.9.9.9"
	h.tt.Frame()
	texts = h.tt.Texts()
	if textIndex(texts, "prod-db") < 0 || textIndex(texts, "web-01") >= 0 {
		t.Fatalf("应按地址过滤: %q", texts)
	}

	h.a.hostFilter = "admin"
	h.tt.Frame()
	if textIndex(h.tt.Texts(), "prod-db") < 0 {
		t.Fatalf("应按用户名过滤: %q", h.tt.Texts())
	}

	h.a.hostFilter = "站点"
	h.tt.Frame()
	texts = h.tt.Texts()
	if textIndex(texts, "web-01") < 0 || textIndex(texts, "prod-db") >= 0 {
		t.Fatalf("应按分组过滤: %q", texts)
	}

	h.a.hostFilter = "没有这台"
	h.tt.Frame()
	if !h.tt.HasText("没有匹配的主机") || textIndex(h.tt.Texts(), "web-01") >= 0 {
		t.Fatalf("无匹配时应提示: %q", h.tt.Texts())
	}
}

// 分组内按名称排序，和保险库里的写入顺序无关。
func TestSidebarSortsHostsByNameWithinGroup(t *testing.T) {
	h := newHarness(t,
		hosts.Host{ID: "z", Name: "zzz-host", Address: "10.0.0.2", Port: 22, User: "u"},
		hosts.Host{ID: "a", Name: "aaa-host", Address: "10.0.0.1", Port: 22, User: "u"},
	)
	h.tt.Frame()
	texts := h.tt.Texts()
	ia, id, iz := textIndex(texts, "aaa-host"), textIndex(texts, "dev"), textIndex(texts, "zzz-host")
	if ia < 0 || id < 0 || iz < 0 || !(ia < id && id < iz) {
		t.Fatalf("组内应按名称排序，实际下标 aaa=%d dev=%d zzz=%d，文字 %q", ia, id, iz, texts)
	}
}

// 折叠分组后主机不可见，状态写进 device.json，不进保险库；重启后仍然折叠。过滤时暂时展开。
func TestSidebarGroupCollapseIsLocal(t *testing.T) {
	h := newHarness(t, hosts.Host{ID: "p", Name: "prod-db", Address: "10.0.0.9", Port: 22, User: "admin"})
	h.tt.Frame()
	h.click("折叠 " + defaultGroup)
	h.tt.Frame()
	if textIndex(h.tt.Texts(), "prod-db") >= 0 || textIndex(h.tt.Texts(), "dev") >= 0 {
		t.Fatalf("折叠后不应看到主机: %q", h.tt.Texts())
	}
	if !h.tt.HasText(defaultGroup) {
		t.Fatalf("折叠后分组标题应还在: %q", h.tt.Texts())
	}

	raw, err := os.ReadFile(filepath.Join(h.dir, "device.json"))
	if err != nil || !strings.Contains(string(raw), defaultGroup) {
		t.Fatalf("折叠状态应写入 device.json: err=%v raw=%q", err, string(raw))
	}
	vaultRaw, err := os.ReadFile(filepath.Join(h.dir, "vault.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(vaultRaw), "collapsedGroups") || strings.Contains(string(vaultRaw), defaultGroup) {
		t.Fatal("折叠状态不应出现在保险库里")
	}

	h.a.rememberOnDevice(true)
	h.reopen()
	if textIndex(h.tt.Texts(), "dev") >= 0 {
		t.Fatalf("重启后分组应仍然折叠: %q", h.tt.Texts())
	}

	h.a.hostFilter = "prod-db"
	h.tt.Frame()
	if textIndex(h.tt.Texts(), "prod-db") < 0 {
		t.Fatalf("过滤时应暂时展开折叠的分组: %q", h.tt.Texts())
	}

	h.a.hostFilter = ""
	h.tt.Frame()
	h.click("展开 " + defaultGroup)
	h.tt.Frame()
	if textIndex(h.tt.Texts(), "dev") < 0 || textIndex(h.tt.Texts(), "prod-db") < 0 {
		t.Fatalf("展开后应看到主机: %q", h.tt.Texts())
	}
}

// 侧栏支持按主机标签胶囊点击筛选，以及通过搜索框匹配标签。
func TestSidebarTagFiltering(t *testing.T) {
	h := newHarness(t,
		hosts.Host{ID: "web", Name: "web-01", Address: "10.0.0.3", Port: 22, User: "deploy", Tags: []string{"prod", "web"}},
		hosts.Host{ID: "db", Name: "stage-db", Address: "10.0.0.9", Port: 22, User: "admin", Tags: []string{"staging"}},
	)
	h.tt.Frame()

	// 标签筛选行应展示去重后的标签胶囊
	h.click("筛选标签 prod")
	h.tt.Frame()
	texts := h.tt.Texts()
	if textIndex(texts, "web-01") < 0 || textIndex(texts, "stage-db") >= 0 || textIndex(texts, "dev") >= 0 {
		t.Fatalf("按标签 prod 筛选后应只显示 web-01: %q", texts)
	}

	// 点“清空”后同时清除关键字和标签筛选
	h.click("清空")
	h.tt.Frame()
	texts = h.tt.Texts()
	if h.a.activeTag != "" || textIndex(texts, "web-01") < 0 || textIndex(texts, "stage-db") < 0 {
		t.Fatalf("清空后应恢复展示全部主机: activeTag=%q texts=%q", h.a.activeTag, texts)
	}

	// 搜索框也应能匹配主机标签
	h.a.hostFilter = "staging"
	h.tt.Frame()
	texts = h.tt.Texts()
	if textIndex(texts, "stage-db") < 0 || textIndex(texts, "web-01") >= 0 {
		t.Fatalf("搜索框应能按标签过滤: %q", texts)
	}
}
