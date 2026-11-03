package imaging

import (
	"image"
	"image/color"
	"image/draw"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"github.com/leha-creator/chanly/internal/post/domain"
)

// plateBackground — чёрная подложка с непрозрачностью 60% (R-5).
var plateBackground = color.NRGBA{A: 153}

// drawPlate рисует плашку по нижнему краю изображения (06 §6.2): полоса во всю
// ширину, белый текст по центру полосы по вертикали, слева — поле.
func drawPlate(img *image.NRGBA, name, inn string) {
	b := img.Bounds()
	l := domain.LayoutPlate(b.Dx(), b.Dy(), name, inn, Measurer{})
	bar := image.Rect(b.Min.X, b.Min.Y+l.BarY, b.Max.X, b.Max.Y)
	draw.Draw(img, bar, image.NewUniform(plateBackground), image.Point{}, draw.Over)

	face, err := newFace(l.FontSize)
	if err != nil {
		return
	}
	defer face.Close()
	m := face.Metrics()
	// Базовая линия: строка (подъём + спуск) по центру полосы.
	textHeight := m.Ascent + m.Descent
	baseline := fixed.I(bar.Min.Y) + (fixed.I(l.BarHeight)-textHeight)/2 + m.Ascent
	d := font.Drawer{Dst: img, Src: image.NewUniform(color.White), Face: face,
		Dot: fixed.Point26_6{X: fixed.I(b.Min.X + l.Margin), Y: baseline}}
	d.DrawString(l.Text)
}
