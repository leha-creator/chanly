package imaging

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"github.com/leha-creator/chanly/internal/post/domain"
)

var plateBackground = color.NRGBA{R: 0x17, G: 0x1A, B: 0x1F, A: 255}

func drawPlate(img *image.NRGBA, name, inn string) error {
	b := img.Bounds()
	l, err := domain.LayoutPlate(b.Dx(), b.Dy(), name, inn, Measurer{})
	if err != nil {
		return err
	}
	face, err := newFace(l.FontSize)
	if err != nil {
		return err
	}
	defer face.Close()
	card := image.Rect(b.Min.X+l.X, b.Min.Y+l.Y, b.Min.X+l.X+l.Width, b.Min.Y+l.Y+l.Height)
	drawRoundedCard(img, card, l.Radius)
	for i, line := range l.Lines {
		baseline := float64(card.Min.Y+l.Padding+i*l.LineHeight) + l.Ascent
		d := font.Drawer{Dst: img, Src: image.NewUniform(color.White), Face: face,
			Dot: fixed.Point26_6{X: fixed.I(card.Min.X + l.Padding), Y: fixed.Int26_6(math.Round(baseline * 64))}}
		d.DrawString(line)
	}
	return nil
}

// Horizontal spans avoid a full-size mask for large source images.
func drawRoundedCard(dst *image.NRGBA, b image.Rectangle, radius int) {
	for y := 0; y < b.Dy(); y++ {
		inset := 0
		dy := min(y, b.Dy()-1-y)
		if dy < radius {
			d := float64(radius-dy) - 0.5
			inset = int(math.Ceil(float64(radius) - math.Sqrt(float64(radius*radius)-d*d)))
		}
		row := image.Rect(b.Min.X+inset, b.Min.Y+y, b.Max.X-inset, b.Min.Y+y+1)
		draw.Draw(dst, row, image.NewUniform(plateBackground), image.Point{}, draw.Src)
	}
}
