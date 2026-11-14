package domain_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/leha-creator/chanly/internal/post/domain"
)

type fixedMeasurer struct{}

func (fixedMeasurer) Width(text string, size float64) float64 {
	return float64(utf8.RuneCountInString(text)) * size * 0.5
}
func (fixedMeasurer) Metrics(size float64) domain.FontMetrics {
	return domain.FontMetrics{Ascent: size * 0.8, Descent: size * 0.2}
}

func TestLayoutPlate(t *testing.T) {
	m := fixedMeasurer{}
	for _, size := range [][2]int{{160, 160}, {640, 480}, {800, 1000}, {7680, 160}, {160, 7680}, {7680, 7680}} {
		for _, name := range []string{"ООО «Василёк»", strings.Repeat("Очень длинное название ", 20), strings.Repeat("я", 300)} {
			for _, inn := range []string{"7707083893", "123456789012"} {
				l, err := domain.LayoutPlate(size[0], size[1], name, inn, m)
				if err != nil {
					t.Fatalf("%v: %v", size, err)
				}
				if l.X < 8 || l.Y < 8 || l.X+l.Width > size[0]-8 || l.Y+l.Height > size[1]-8 || l.FontSize < 12 || len(l.Lines) > 4 {
					t.Fatalf("%v: %+v", size, l)
				}
				for _, line := range l.Lines {
					if !utf8.ValidString(line) || m.Width(line, l.FontSize) > float64(l.Width-2*l.Padding) {
						t.Fatalf("line outside card: %+v", l)
					}
				}
				text := strings.Join(l.Lines, " ")
				if !strings.HasPrefix(text, "Реклама") || !strings.HasSuffix(text, "ИНН "+inn) {
					t.Fatalf("marking lost: %q", text)
				}
				if l.Truncated && (len(l.Lines) < 3 || !strings.HasSuffix(l.Lines[len(l.Lines)-2], "…")) {
					t.Fatalf("bad ellipsis: %+v", l)
				}
			}
		}
	}
}

func TestLayoutPlateWrapAndErrors(t *testing.T) {
	l, err := domain.LayoutPlate(160, 160, "ООО «Василёк»", "7707083893", fixedMeasurer{})
	if err != nil || l.Truncated || len(l.Lines) < 3 {
		t.Fatalf("short Cyrillic name: %+v %v", l, err)
	}
	for _, size := range [][2]int{{0, 0}, {20, 20}, {160, 20}} {
		if _, err := domain.LayoutPlate(size[0], size[1], "Имя", "7707083893", fixedMeasurer{}); !errors.Is(err, domain.ErrPlateLayout) {
			t.Fatalf("%v: %v", size, err)
		}
	}
}

func TestLayoutPlateScalesSpacingWithText(t *testing.T) {
	for _, dimensions := range [][2]int{{640, 480}, {1536, 1152}, {7680, 7680}} {
		l, err := domain.LayoutPlate(dimensions[0], dimensions[1], "ООО Василёк", "7707083893", fixedMeasurer{})
		if err != nil {
			t.Fatal(err)
		}
		scale := 320 / float64(dimensions[0])
		if l.FontSize*scale < 9.5 || l.FontSize*scale > 10.5 || float64(l.Padding)*scale < 4 || float64(l.X)*scale < 6 || float64(l.Radius)*scale < 2.5 {
			t.Fatalf("spacing is lost when %v is displayed at 320px: %+v", dimensions, l)
		}
	}
}
