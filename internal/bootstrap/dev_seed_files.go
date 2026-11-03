package bootstrap

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
)

// seedINN дополняет prefix контрольными цифрами до корректного ИНН: 9 цифр у юрлица,
// 10 у остальных (R-8 плана dev seed). Постоянный ИНН — повтор находит ту же организацию.
func seedINN(prefix string, t accountdomain.LegalType) string {
	digits, limit := 2, 100
	if t == accountdomain.LegalEntity {
		digits, limit = 1, 10
	}
	for tail := range limit {
		inn := prefix + fmt.Sprintf("%0*d", digits, tail)
		if accountdomain.CheckINN(inn, t) == "" {
			return inn
		}
	}
	panic("dev seed: no valid INN for prefix " + prefix)
}

// seedPDF — минимальный корректный PDF в одну страницу A4 со строкой text (ASCII:
// стандартный шрифт Helvetica без кириллицы).
func seedPDF(text string) []byte {
	text = strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`).Replace(text)
	stream := fmt.Sprintf("BT /F1 18 Tf 72 760 Td (%s) Tj ET", text)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objects))
	for i, o := range objects {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}

// seedPNG — изображение 800×600: фон и диагональные полосы цвета accent.
func seedPNG(accent color.RGBA) []byte {
	const w, h = 800, 600
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.RGBA{R: 245, G: 242, B: 236, A: 255}
	for y := range h {
		for x := range w {
			c := bg
			if (x+y)/40%3 == 0 {
				c = accent
			}
			img.SetRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err) // запись в память не отказывает
	}
	return b.Bytes()
}
