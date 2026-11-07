package design

import (
	"hash/fnv"
	"math"

	"github.com/egoist/mygo/ui"
)

// Spot 是斑纹上的一个斑点，坐标和半径都是相对图块边长的比例（0 到 1）。
type Spot struct {
	X, Y, RX, RY float32
	// Angle 是旋转角度（弧度）。
	Angle float32
}

// Coat 是一台主机的斑纹。
//
// 真实薮猫的斑点每只都不一样，Serval 借这个特点给每台主机一块专属图案：
// 同一个名字永远生成同一块图案，开很多标签时不用读字也能认出机器。
// 图块是一段鲜明的渐变，斑点是半透明的白色，像一枚有光泽的小徽章。
type Coat struct {
	From, To ui.Color
	Ink      ui.Color
	Spots    []Spot
}

// coatHues 是渐变起点的色相（Oklch），在色环上拉开距离，几台主机放在一起也不会撞色。
// 刻意避开红色和粉色（色相 330～40），那一段只留给生产环境。
var coatHues = []float32{250, 275, 200, 225, 165, 140, 80, 110}

// productionCoat 是生产环境固定使用的红色渐变，这个信号比“认出是哪台”更重要。
var productionFrom, productionTo = ui.Oklch(0.64, 0.2, 25), ui.Oklch(0.58, 0.21, 10)

// NewCoat 根据种子（通常是主机标识）生成斑纹。
func NewCoat(seed string, production bool) Coat {
	h := fnv.New64a()
	h.Write([]byte(seed))
	state := h.Sum64() | 1
	next := func() float32 {
		// xorshift64*：只需要一个确定、分布足够好的序列，不用于任何安全用途。
		state ^= state >> 12
		state ^= state << 25
		state ^= state >> 27
		return float32((state*2685821657736338717)>>40) / float32(1<<24)
	}

	hue := coatHues[int(next()*float32(len(coatHues)))%len(coatHues)]
	coat := Coat{
		From: ui.Oklch(0.66, 0.17, hue),
		To:   ui.Oklch(0.56, 0.19, hue+38),
		Ink:  ui.RGBA(255, 255, 255, 0.86),
	}
	if production {
		coat.From, coat.To = productionFrom, productionTo
	}

	want := 5 + int(next()*4)
	// 用拒绝采样保证斑点不粘连，尝试次数有上限，保证一定能结束。
	for tries := 0; tries < 80 && len(coat.Spots) < want; tries++ {
		spot := Spot{
			X:     0.14 + next()*0.72,
			Y:     0.14 + next()*0.72,
			RX:    0.06 + next()*0.07,
			Angle: next() * math.Pi,
		}
		spot.RY = spot.RX * (0.55 + next()*0.35)
		clear := true
		for _, other := range coat.Spots {
			dx, dy := spot.X-other.X, spot.Y-other.Y
			if min := spot.RX + other.RX + 0.04; dx*dx+dy*dy < min*min {
				clear = false
				break
			}
		}
		if clear {
			coat.Spots = append(coat.Spots, spot)
		}
	}
	return coat
}

// Paint 把斑纹画到矩形里。
func (coat Coat) Paint(p *ui.Painter, r ui.Rect, radius float32) {
	p.FillGradient(r, ui.LinearGradient{Angle: 135, Oklab: true, Stops: []ui.GradientStop{
		{Color: coat.From, At: 0}, {Color: coat.To, At: 1},
	}}, radius)
	var path ui.Path
	for _, s := range coat.Spots {
		ellipse(&path, r.X+s.X*r.W, r.Y+s.Y*r.H, s.RX*r.W, s.RY*r.H, s.Angle)
	}
	p.FillPath(&path, coat.Ink)
}

// ellipse 用四段三次贝塞尔曲线画旋转椭圆。
func ellipse(path *ui.Path, cx, cy, rx, ry, angle float32) {
	const k = 0.5522848 // 四分之一圆的贝塞尔近似系数
	sin, cos := float32(math.Sin(float64(angle))), float32(math.Cos(float64(angle)))
	at := func(x, y float32) (float32, float32) {
		return cx + x*cos - y*sin, cy + x*sin + y*cos
	}
	x0, y0 := at(rx, 0)
	path.MoveTo(x0, y0)
	segments := [4][6]float32{
		{rx, ry * k, rx * k, ry, 0, ry},
		{-rx * k, ry, -rx, ry * k, -rx, 0},
		{-rx, -ry * k, -rx * k, -ry, 0, -ry},
		{rx * k, -ry, rx, -ry * k, rx, 0},
	}
	for _, s := range segments {
		ax, ay := at(s[0], s[1])
		bx, by := at(s[2], s[3])
		ex, ey := at(s[4], s[5])
		path.CubeTo(ax, ay, bx, by, ex, ey)
	}
	path.Close()
}

// Brand 是 Serval 自己的斑纹，用在应用图标和欢迎页上：从靛蓝到青色的渐变，和强调色同源。
func Brand() Coat {
	coat := NewCoat("serval", false)
	coat.From, coat.To = ui.Oklch(0.58, 0.2, 275), ui.Oklch(0.7, 0.14, 210)
	return coat
}

// BrandView 返回 Serval 的标志。
func BrandView(c *ui.Context, size float32) ui.Element {
	return coatBox(c, Brand(), size)
}

// CoatView 返回一个显示斑纹的方块。
func CoatView(c *ui.Context, seed string, production bool, size float32) ui.Element {
	return coatBox(c, NewCoat(seed, production), size)
}

func coatBox(c *ui.Context, coat Coat, size float32) ui.Element {
	radius := size * 0.28
	// 元素本身也要有圆角，否则加在它上面的阴影会是直角的方块。
	return ui.Box(c).Size(size, size).Radius(radius).Shrink(0).AccessHidden(true).Draw(func(p *ui.Painter, r ui.Rect) {
		coat.Paint(p, r, radius)
	})
}
