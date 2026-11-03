package imaging

import (
	"fmt"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"

	"github.com/leha-creator/chanly/internal/post/domain"
)

// Шрифт плашки — Go Bold (лицензия BSD, есть кириллица), встроен в бинарник (R-5).
var plateFont = mustParseFont()

func mustParseFont() *opentype.Font {
	f, err := opentype.Parse(gobold.TTF)
	if err != nil {
		panic(fmt.Sprintf("imaging: parse Go Bold: %v", err))
	}
	return f
}

// newFace — начертание кегля size пикселей (72 dpi: пункт равен пикселю).
func newFace(size float64) (font.Face, error) {
	return opentype.NewFace(plateFont, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
}

// Measurer — ширина строки шрифтом плашки; реализует domain.TextMeasurer.
type Measurer struct{}

var _ domain.TextMeasurer = Measurer{}

// Width — ширина text при кегле size в пикселях, с округлением вверх.
func (Measurer) Width(text string, size float64) float64 {
	face, err := newFace(size)
	if err != nil {
		return math.Inf(1)
	}
	defer face.Close()
	return math.Ceil(float64(font.MeasureString(face, text)) / 64)
}
