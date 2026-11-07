package design

import (
	"math"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
)

func luminance(c ui.Color) float64 {
	channel := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(c.R) + 0.7152*channel(c.G) + 0.0722*channel(c.B)
}

func contrast(a, b ui.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// over 把半透明颜色叠到不透明底色上，得到实际看到的颜色。
func over(top, base ui.Color) ui.Color {
	a := float64(top.A) / 255
	mix := func(t, b uint8) uint8 { return uint8(math.Round(float64(t)*a + float64(b)*(1-a))) }
	return ui.Color{R: mix(top.R, base.R), G: mix(top.G, base.G), B: mix(top.B, base.B), A: 255}
}

// 审美不能以牺牲可读性为代价：文字对比度按 WCAG AA（4.5:1）校验，
// 底色包括窗口渐变的两端、卡片，以及悬停和选中的行。
func TestTextContrast(t *testing.T) {
	for _, dark := range []bool{false, true} {
		theme, colors := Theme(dark), Of(dark)
		top, bottom := colors.Wash.Stops[0].Color, colors.Wash.Stops[len(colors.Wash.Stops)-1].Color
		hover := over(colors.Hover, top)
		selected := over(colors.Selected, top)
		cases := []struct {
			name   string
			fg, bg ui.Color
		}{
			{"正文/窗口顶部", theme.Text, top},
			{"正文/窗口底部", theme.Text, bottom},
			{"正文/卡片", theme.Text, colors.Card},
			{"次要文字/窗口顶部", theme.TextMuted, top},
			{"次要文字/卡片", theme.TextMuted, colors.Card},
			{"次要文字/悬停行", theme.TextMuted, hover},
			{"次要文字/选中行", theme.TextMuted, selected},
			{"强调按钮文字", theme.AccentText, theme.Accent},
			{"强调色/卡片", theme.Accent, colors.Card},
			{"危险色/卡片", colors.Danger, colors.Card},
			{"危险色/浅红底", colors.Danger, over(colors.DangerSoft, colors.Card)},
		}
		for _, tc := range cases {
			if got := contrast(tc.fg, tc.bg); got < 4.5 {
				t.Errorf("dark=%v %s 对比度 %.2f，低于 4.5", dark, tc.name, got)
			}
		}
		// 最次要的文字（计数、快捷键提示）放宽到 3:1，相当于大字号的 AA 标准。
		if got := contrast(colors.TextFaint, colors.Card); got < 3 {
			t.Errorf("dark=%v 最次要文字对比度 %.2f，低于 3", dark, got)
		}
	}
}

// 终端程序常用的前景色（红绿黄蓝紫青和白）必须在卡片底色上看得清。
// 下标 0 和 8 是黑与亮黑，按惯例常被用作背景或暗淡文字，要求放宽。
func TestTerminalPaletteContrast(t *testing.T) {
	for _, dark := range []bool{false, true} {
		theme := Terminal(dark)
		for i, color := range theme.Palette {
			min := 4.5
			if i == 0 || i == 8 {
				min = 1.5
			}
			if got := contrast(color, theme.Background); got < min {
				t.Errorf("dark=%v 调色板 %d 对比度 %.2f，低于 %.1f", dark, i, got, min)
			}
		}
		if theme.Background != Of(dark).Card {
			t.Errorf("dark=%v 终端底色应和卡片一致", dark)
		}
	}
}

func TestCoatIsDeterministicAndDistinct(t *testing.T) {
	first, second := NewCoat("prod-web-01", false), NewCoat("prod-web-01", false)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("同一个名字必须生成同一块斑纹")
	}

	seen := map[string]bool{}
	for _, name := range []string{"web-01", "web-02", "web-03", "db-01", "db-02", "cache", "bastion", "staging"} {
		coat := NewCoat(name, false)
		if len(coat.Spots) < 4 {
			t.Errorf("%s 只有 %d 个斑点，图案太单薄", name, len(coat.Spots))
		}
		for _, s := range coat.Spots {
			if s.X-s.RX < 0 || s.X+s.RX > 1 || s.Y-s.RX < 0 || s.Y+s.RX > 1 {
				t.Errorf("%s 的斑点超出图块: %+v", name, s)
			}
		}
		key := ""
		for _, s := range coat.Spots {
			key += string(rune(int(s.X*100))) + string(rune(int(s.Y*100)))
		}
		if seen[key] {
			t.Errorf("%s 和其他主机的斑纹相同", name)
		}
		seen[key] = true
	}
}

// 生产环境固定用红色，其他主机不能撞上这个颜色，否则信号就失效了。
func TestProductionCoatIsReservedRed(t *testing.T) {
	for _, name := range []string{"a", "b", "c", "d"} {
		if got := NewCoat(name, true); got.From != productionFrom || got.To != productionTo {
			t.Errorf("%s 的生产环境斑纹不是红色", name)
		}
	}
	for _, hue := range coatHues {
		// 渐变终点会再转 38 度，起点和终点都不能落进红色和粉色的区间。
		for _, h := range []float32{hue, hue + 38} {
			if h := float32(int(h) % 360); h < 40 || h > 330 {
				t.Errorf("普通主机的色相 %.0f 太接近生产环境的红色", h)
			}
		}
	}
}

func TestAllIconsParse(t *testing.T) {
	entries, err := iconFiles.ReadDir("icons")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		Icon(e.Name()[:len(e.Name())-len(".svg")])
	}
}

// 侧栏和抽屉的滑入不能改变布局尺寸：终端每次改尺寸都会通知服务器重绘，
// 逐帧的宽度动画会让远端提示符刷屏。
func TestSlideInDoesNotAnimateLayout(t *testing.T) {
	tr := SlideIn(16)
	if tr.Position || tr.Size || tr.Width || tr.Height {
		t.Fatalf("滑入不应动画位置或尺寸: %+v", tr)
	}
	if !tr.Colors {
		t.Fatal("应只选择颜色，否则 MyGo 默认会同时动画位置和尺寸")
	}
	for name, m := range map[string]*ui.Motion{"Enter": tr.Enter, "Exit": tr.Exit} {
		if m == nil || m.Collapse {
			t.Errorf("%s 不能用 Collapse，它会让宽度从 0 逐帧增长", name)
		}
	}
}
