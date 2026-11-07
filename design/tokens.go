// Package design 定义 Serval 的视觉规范：配色、字体、图标和主机斑纹。
//
// 风格是冷调、通透的玻璃感：窗口底是一层从上到下的冷色渐变，右上角有一圈靛蓝色的光，
// 侧栏透出系统的毛玻璃；终端放在白色（深色模式下是石墨色）的圆角卡片里，靠细边和
// 柔和的阴影浮在底上。全界面只有靛蓝一种强调色，红色只留给生产环境和危险操作。
//
// 做法参考了 Kero（github.com/egoist/kero，MIT）的“底色渐变加卡片”，具体数值是 Serval 自己的。
package design

import (
	"embed"
	"sync"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// 字号。用系统字体（SF Pro、苹方等），中英文的字形和字重都更统一。
const (
	TextSmall   float32 = 11.5
	TextBody    float32 = 13
	TextHeading float32 = 15
	TextDisplay float32 = 26
)

// 圆角和间距。层级越高圆角越大：卡片 > 行和按钮 > 小标签。
const (
	RadiusCard float32 = 12
	RadiusRow  float32 = 8
	RadiusChip float32 = 6
	// Gap 是卡片之间、卡片和窗口边缘之间的空隙。
	Gap float32 = 8
)

// Colors 是 ui.Theme 之外的界面颜色。
type Colors struct {
	Dark bool
	// Wash 是窗口的底色渐变，Glow 是叠在上面、从右上角散开的光。
	Wash, Glow ui.LinearGradient
	// Card 是终端、文件抽屉这类内容卡片的底，CardBorder 和 CardShadow 让它浮起来。
	Card, CardBorder, CardShadow ui.Color
	// Hairline 是卡片内部的分隔线。
	Hairline ui.Color
	// Hover 是列表行悬停时的底；Selected 和 SelectedShadow 是选中行“浮起”的样子。
	Hover, Selected, SelectedShadow ui.Color
	// TextFaint 用于计数、快捷键提示这类最次要的文字。
	TextFaint ui.Color
	// Chip 是小标签的底。
	Chip ui.Color
	// Danger 标记生产环境和危险操作，DangerSoft 是它的浅底。
	Danger, DangerSoft ui.Color
	// Live 表示连接正常。
	Live ui.Color
	// Veil 盖在未激活的窗格上。
	Veil ui.Color
	// Scrim 是弹窗背后压暗整个窗口的一层，玻璃面板透出的就是它和下面的界面。
	Scrim ui.Color
}

func wash(top, bottom string) ui.LinearGradient {
	return ui.LinearGradient{Angle: 180, Oklab: true, Stops: []ui.GradientStop{
		{Color: ui.Hex(top), At: 0}, {Color: ui.Hex(bottom), At: 1},
	}}
}

func glow(c ui.Color) ui.LinearGradient {
	return ui.LinearGradient{Angle: 225, Oklab: true, Stops: []ui.GradientStop{
		{Color: c, At: 0}, {Color: c.Alpha(0), At: 0.55},
	}}
}

// Of 返回指定外观的界面颜色。
func Of(dark bool) Colors {
	if dark {
		return Colors{
			Dark:           true,
			Wash:           wash("#0E1018", "#12141C"),
			Glow:           glow(ui.RGBA(108, 99, 255, 0.24)),
			Card:           ui.Hex("#181B24"),
			CardBorder:     ui.RGBA(255, 255, 255, 0.07),
			CardShadow:     ui.RGBA(0, 0, 0, 0.45),
			Hairline:       ui.RGBA(255, 255, 255, 0.07),
			Hover:          ui.RGBA(255, 255, 255, 0.05),
			Selected:       ui.RGBA(255, 255, 255, 0.09),
			SelectedShadow: ui.RGBA(0, 0, 0, 0.3),
			TextFaint:      ui.Hex("#737888"),
			Chip:           ui.RGBA(255, 255, 255, 0.07),
			Danger:         ui.Hex("#FF6B6F"),
			DangerSoft:     ui.RGBA(255, 107, 111, 0.14),
			Live:           ui.Hex("#3DD68C"),
			Veil:           ui.RGBA(14, 16, 24, 0.45),
			Scrim:          ui.RGBA(0, 0, 0, 0.38),
		}
	}
	return Colors{
		Wash:           wash("#ECEFF6", "#F4F5F9"),
		Glow:           glow(ui.RGBA(122, 138, 255, 0.22)),
		Card:           ui.Hex("#FFFFFF"),
		CardBorder:     ui.RGBA(16, 20, 40, 0.07),
		CardShadow:     ui.RGBA(20, 24, 60, 0.08),
		Hairline:       ui.RGBA(16, 20, 40, 0.07),
		Hover:          ui.RGBA(16, 20, 40, 0.045),
		Selected:       ui.Hex("#FFFFFF"),
		SelectedShadow: ui.RGBA(20, 24, 60, 0.10),
		TextFaint:      ui.Hex("#8E92A2"),
		Chip:           ui.RGBA(16, 20, 40, 0.05),
		Danger:         ui.Hex("#C22A30"),
		DangerSoft:     ui.RGBA(217, 58, 63, 0.09),
		Live:           ui.Hex("#1F9D5C"),
		Veil:           ui.RGBA(255, 255, 255, 0.42),
		Scrim:          ui.RGBA(20, 24, 50, 0.18),
	}
}

// Theme 返回 MyGo 控件使用的主题。
func Theme(dark bool) *ui.Theme {
	var t *ui.Theme
	colors := Of(dark)
	if dark {
		t = ui.DarkTheme()
		t.Background = ui.Hex("#12141C")
		t.Surface = ui.Hex("#232733")
		t.SurfaceHover = ui.Hex("#2B303D")
		t.SurfacePressed = ui.Hex("#333847")
		t.Border = ui.RGBA(255, 255, 255, 0.10)
		t.Text = ui.Hex("#E8EAF0")
		t.TextMuted = ui.Hex("#9CA1B2")
		t.Accent = ui.Hex("#7C8CFF")
		t.AccentHover = ui.Hex("#909EFF")
		t.AccentPressed = ui.Hex("#6A7BF2")
		t.AccentText = ui.Hex("#0E1018")
		t.Selection = ui.Hex("#2E3A7A")
		t.Warning = ui.Hex("#F5B544")
		t.Scrollbar = ui.RGBA(255, 255, 255, 0.2)
	} else {
		t = ui.LightTheme()
		t.Background = ui.Hex("#F4F5F9")
		t.Surface = ui.Hex("#FFFFFF")
		t.SurfaceHover = ui.Hex("#F6F7FA")
		t.SurfacePressed = ui.Hex("#EDEFF4")
		t.Border = ui.RGBA(16, 20, 40, 0.12)
		t.Text = ui.Hex("#1B1D24")
		t.TextMuted = ui.Hex("#5F6373")
		t.Accent = ui.Hex("#4361EE")
		t.AccentHover = ui.Hex("#3752DC")
		t.AccentPressed = ui.Hex("#2E47C8")
		t.AccentText = ui.Hex("#FFFFFF")
		t.Selection = ui.Hex("#D4DCFF")
		t.Warning = ui.Hex("#B7791F")
		t.Scrollbar = ui.RGBA(16, 20, 40, 0.22)
	}
	t.Danger = colors.Danger
	t.Success = colors.Live
	t.Focus = t.Accent
	t.InactiveAccent = t.Accent
	t.Radius = RadiusRow
	t.FontSize = TextBody
	return t
}

// Terminal 返回终端配色。终端的底就是卡片的底，所以终端和卡片是一个整体。
// 16 色都按卡片底色校验过对比度（见 tokens_test.go）。
func Terminal(dark bool) *terminal.Theme {
	colors := Of(dark)
	t := Theme(dark)
	theme := &terminal.Theme{
		Foreground: t.Text,
		Background: colors.Card,
		Cursor:     t.Accent,
		CursorText: colors.Card,
		Selection:  t.Selection,
	}
	if dark {
		theme.Palette = hexes(
			"#3A3F4E", "#FF7A7E", "#4FD69A", "#F5C35B", "#7C9BFF", "#C792FF", "#4CD3E8", "#C9CDD8",
			"#6B7182", "#FF9A9D", "#74E4B0", "#FFD47E", "#9DB4FF", "#D8AEFF", "#7FE2F0", "#F4F5F8",
		)
	} else {
		theme.Palette = hexes(
			"#1B1D24", "#D12F35", "#14834B", "#9A6200", "#2F55D4", "#8E3CCB", "#0A7C93", "#5F6373",
			"#454956", "#B8252B", "#0F6E3F", "#7E5000", "#2545B3", "#7631AB", "#086679", "#3F4250",
		)
	}
	return theme
}

func hexes(values ...string) (palette [16]ui.Color) {
	for i, v := range values {
		palette[i] = ui.Hex(v)
	}
	return palette
}

// FontTerminal 优先使用用户已安装的 Nerd Font，提示符图标才能正常显示，
// 最后回退到应用自带的 JetBrains Mono。
const FontTerminal = "JetBrainsMono Nerd Font Mono, JetBrainsMono Nerd Font, Hack Nerd Font Mono, MesloLGS NF, JetBrains Mono"

//go:embed fonts/JetBrainsMono.ttf
var jetBrainsMono []byte

var fontsOnce sync.Once

// RegisterFonts 注册应用自带的字体，可重复调用。
func RegisterFonts() {
	fontsOnce.Do(func() {
		ui.RegisterFont(jetBrainsMono, "JetBrains Mono")
	})
}

// TerminalFontWith 根据指定的字体族、字号和行高生成终端字体配置。
// 未指定（零值）的参数使用规范默认值。
func TerminalFontWith(family string, size, lineHeight float32) terminal.Font {
	if family == "" {
		family = FontTerminal
	}
	if size <= 0 {
		size = 13
	}
	if lineHeight <= 0 {
		lineHeight = 1.15
	}
	return terminal.Font{Family: family, Size: size, LineHeight: lineHeight}
}

// TerminalFont 返回终端字体设置。
func TerminalFont() terminal.Font {
	return TerminalFontWith("", 13, 1.15)
}

//go:embed icons/*.svg
var iconFiles embed.FS

// Icon 按名称返回 Lucide 图标。名称写错属于编程错误，会直接 panic。
func Icon(name string) *ui.SVG {
	iconsMu.Lock()
	defer iconsMu.Unlock()
	if svg, ok := icons[name]; ok {
		return svg
	}
	data, err := iconFiles.ReadFile("icons/" + name + ".svg")
	if err != nil {
		panic(err)
	}
	svg := ui.MustParseSVG(data)
	icons[name] = svg
	return svg
}

var (
	iconsMu sync.Mutex
	icons   = map[string]*ui.SVG{}
)
