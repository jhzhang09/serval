package design

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// 动效规范。
//
// 原则：动效只用来回应用户的操作、说明发生了什么变化，不做装饰性的循环；时长不超过 250 毫秒
// 左右；空间上的移动用低回弹的弹簧，透明度和颜色用缓动曲线。系统开启“减少动效”时，
// MyGo 会让这些动画直接到达终点。
//
// 有一条硬约束：会改变终端卡片尺寸的动画（比如侧栏和文件抽屉的宽度逐帧变化）一律不做。
// 终端每次尺寸变化都会向服务器发送一次窗口大小变更，远端 shell 随之重绘，十几帧的宽度动画
// 就是十几次重绘，提示符会刷屏。所以侧栏和抽屉的布局一步到位，内容本身再滑入淡入。

// outQuint 是先快后慢的缓动曲线，适合“出现”和“到位”。
var outQuint = ui.CubicBezier(0.22, 1, 0.36, 1)

var (
	// Hover 是悬停和按下时的颜色、阴影渐变。只动颜色和阴影，不动位置和尺寸。
	Hover = ui.ElementTransition{Colors: true, Shadows: true, Duration: 120 * time.Millisecond}

	// Glide 是选中高亮在列表项之间滑动的弹簧，轻微回弹，有“跟手”的感觉。
	Glide = ui.Animation{Spring: ui.SpringOptions{Duration: 320 * time.Millisecond, Bounce: 0.12}}

	// Pop 是新元素弹入的弹簧，比 Glide 稍慢、回弹稍多，让“新出现”更容易被注意到。
	Pop = ui.Animation{Spring: ui.SpringOptions{Duration: 420 * time.Millisecond, Bounce: 0.18}}

	// Fade 用于透明度和薄纱的淡入淡出。
	Fade = ui.Animation{Duration: 160 * time.Millisecond, Ease: outQuint}
)

// SlideIn 返回“从侧面滑入、淡入”的进出场过渡，dx 是起点的水平偏移（负数从左边来）。
// 只动颜色，不动位置和尺寸：元素的布局一步到位，滑动只是绘制上的偏移，不会让终端改尺寸。
func SlideIn(dx float32) ui.ElementTransition {
	return ui.ElementTransition{
		Colors:       true,
		Duration:     220 * time.Millisecond,
		Ease:         outQuint,
		ExitDuration: 140 * time.Millisecond,
		Enter:        &ui.Motion{X: dx, Opacity: 0},
		Exit:         &ui.Motion{X: dx, Opacity: 0},
	}
}

// Arrival 是新元素出现时的起始姿态：略小、半透明，随后用 Pop 弹到正常大小。
var Arrival = ui.Pose{Scale: 0.94, Fade: 0.65}

// Breathe 是“正在连接”时斑纹的呼吸：透明度在 1 和 0.4 之间往返。
// 只改变透明度，不移动任何东西，所以标记为 NonMotion：开启“减少动效”时它照常播放，
// 否则连接中的状态就没有任何提示了。
var Breathe = []ui.Keyframe{
	{Value: 1},
	{At: 650 * time.Millisecond, Value: 0.4, Ease: ui.EaseInOut},
	{At: 1300 * time.Millisecond, Value: 1, Ease: ui.EaseInOut},
}

// BreatheOptions 让呼吸一直循环，直到元素不再显示（连接完成或取消）。
var BreatheOptions = ui.KeyframeOptions{Repeat: ui.RepeatForever, NonMotion: true}

// Pulse 是连接成功时状态圆点的一次放大回弹，只播放一次。
var Pulse = []ui.Keyframe{
	{Value: 1},
	{At: 180 * time.Millisecond, Value: 1.9, Ease: outQuint},
	{At: 520 * time.Millisecond, Value: 1, Ease: ui.EaseInOut},
}
