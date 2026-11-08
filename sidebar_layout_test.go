package main

import (
	"testing"
	"time"

	"serval/core/hosts"
)

// 侧栏主机名和地址太长时以“…”结尾，不能画出自己的布局框、压到行尾的“更多操作”按钮上。
// 布局框本身一直是对的，出错的是绘制：只用 NoWrap 时文字不会被截断，所以这里检查像素。
func TestHostRowLongTextIsCutWithEllipsis(t *testing.T) {
	long := hosts.Host{ID: "long", Name: "build-server-with-a-very-long-name", Address: "very-long-hostname.internal.example.com",
		Port: 22, User: "deploy-automation", Group: defaultGroup}
	h := newHarness(t, long)
	h.tt.Frame()
	name, ok := h.tt.Find(long.Name)
	if !ok {
		t.Fatalf("找不到主机名: %q", h.tt.Texts())
	}
	h.tt.Move(name.X+4, name.Y+name.H/2) // 悬停，显示“更多操作”
	for range 5 {                        // 等悬停背景的过渡动画结束
		h.tt.Advance(100 * time.Millisecond)
		h.tt.Frame()
	}
	if _, ok := h.tt.Find("更多操作"); !ok {
		t.Fatalf("悬停后应显示“更多操作”: %q", h.tt.Texts())
	}
	sub, ok := h.tt.Find(long.Target())
	if !ok {
		t.Fatalf("找不到地址: %q", h.tt.Texts())
	}

	// 测试窗口缩放比例是 1，图像像素和布局坐标一致。
	img := h.tt.Image()
	// 地址布局框右侧、同一行文字高度内只应是行背景。参考色取自布局框右侧、文字上方的行背景。
	ref := img.RGBAAt(int(sub.X+sub.W+4), int(name.Y+3))
	for x := int(sub.X + sub.W + 2); x < int(sub.X+sub.W+40); x++ {
		for y := int(sub.Y + 3); y < int(sub.Y+sub.H-2); y++ {
			c := img.RGBAAt(x, y)
			if absDiff(c.R, ref.R)+absDiff(c.G, ref.G)+absDiff(c.B, ref.B) > 90 {
				t.Fatalf("地址文字画出了布局框（%d,%d 处有文字像素），应以省略号截断", x, y)
			}
		}
	}
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}
