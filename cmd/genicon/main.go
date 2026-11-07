// genicon 用 Serval 的品牌斑纹生成应用图标 resources/icon.png。
//
//	go run ./cmd/genicon
//
// 图标按 macOS 的规格留出边距：1024 画布里放 824 的圆角方块。
// 画面由 MyGo 的无窗口渲染器绘制，圆角外的像素再按距离设为透明，保证边缘平滑。
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"

	"serval/design"

	"github.com/egoist/mygo/ui"
)

const (
	canvas = 1024
	tile   = 824
	radius = 185 // 和 macOS 应用图标的圆角接近
)

func main() {
	coat := design.Brand()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Size(canvas, canvas).Draw(func(p *ui.Painter, r ui.Rect) {
			offset := float32(canvas-tile) / 2
			coat.Paint(p, ui.Rect{X: r.X + offset, Y: r.Y + offset, W: tile, H: tile}, radius)
		})
	}, canvas, canvas)
	tt.Frame()
	src := tt.Image()

	// 圆角方块之外设为透明；边缘一个像素内按距离做抗锯齿。
	out := image.NewNRGBA(image.Rect(0, 0, canvas, canvas))
	half := float64(tile) / 2
	for y := range canvas {
		for x := range canvas {
			dx := math.Abs(float64(x)+0.5-canvas/2) - (half - radius)
			dy := math.Abs(float64(y)+0.5-canvas/2) - (half - radius)
			outside := math.Hypot(math.Max(dx, 0), math.Max(dy, 0)) + math.Min(math.Max(dx, dy), 0) - radius
			alpha := math.Min(math.Max(0.5-outside, 0), 1)
			if alpha == 0 {
				continue
			}
			c := src.RGBAAt(x, y)
			out.SetNRGBA(x, y, color.NRGBA{R: c.R, G: c.G, B: c.B, A: uint8(alpha * 255)})
		}
	}

	f, err := os.Create("resources/icon.png")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, out); err != nil {
		log.Fatal(err)
	}
}
