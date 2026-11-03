package domain_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/leha-creator/chanly/internal/post/domain"
)

// fixedMeasurer — каждый символ шириной в половину кегля.
type fixedMeasurer struct{}

func (fixedMeasurer) Width(text string, size float64) float64 {
	return float64(utf8.RuneCountInString(text)) * size * 0.5
}

func TestLayoutPlate(t *testing.T) {
	m := fixedMeasurer{}
	const inn = "7707083893"

	t.Run("bar height", func(t *testing.T) {
		for _, tt := range []struct{ h, bar int }{{160, 40}, {300, 40}, {500, 40}, {1000, 80}, {7680, 614}} {
			l := domain.LayoutPlate(2000, tt.h, "А", inn, m)
			if l.BarHeight != tt.bar || l.BarY != tt.h-tt.bar {
				t.Errorf("height %d: bar %d at %d, want %d", tt.h, l.BarHeight, l.BarY, tt.bar)
			}
		}
	})

	t.Run("short name keeps initial size", func(t *testing.T) {
		l := domain.LayoutPlate(2000, 1000, "ООО Ромашка", inn, m)
		if l.FontSize != 80*0.55 || l.Truncated || l.Text != "Реклама. ООО Ромашка, ИНН 7707083893" {
			t.Fatalf("%+v", l)
		}
		if l.Margin != 40 {
			t.Fatalf("margin %d", l.Margin)
		}
	})

	t.Run("long name shrinks font", func(t *testing.T) {
		// Ширина 1000, поля по 20: доступно 960. Начальный кегль 44 → 22 px на символ, помещается 43 символа.
		name := strings.Repeat("н", 40) // текст 65 символов
		l := domain.LayoutPlate(1000, 1000, name, inn, m)
		if l.Truncated || l.FontSize >= 44 || l.FontSize < 44*0.6 {
			t.Fatalf("%+v", l)
		}
		if m.Width(l.Text, l.FontSize) > 960 {
			t.Fatalf("text does not fit: %+v", l)
		}
	})

	t.Run("very long name truncated, inn whole", func(t *testing.T) {
		name := strings.Repeat("н", 300)
		l := domain.LayoutPlate(1000, 1000, name, inn, m)
		if !l.Truncated || l.FontSize != 44*0.6 {
			t.Fatalf("%+v", l)
		}
		if !strings.HasSuffix(l.Text, "…, ИНН "+inn) || !strings.HasPrefix(l.Text, "Реклама. н") {
			t.Fatalf("text %q", l.Text)
		}
		if m.Width(l.Text, l.FontSize) > 960 {
			t.Fatalf("text does not fit: %q", l.Text)
		}
		// Ещё один символ названия уже не помещается.
		longer := strings.Replace(l.Text, "…", "н…", 1)
		if m.Width(longer, l.FontSize) <= 960 {
			t.Fatalf("name cut too short: %q", l.Text)
		}
	})

	t.Run("narrow image keeps inn", func(t *testing.T) {
		l := domain.LayoutPlate(160, 160, "Очень длинное название организации", inn, m)
		if !strings.HasSuffix(l.Text, "ИНН "+inn) {
			t.Fatalf("text %q", l.Text)
		}
	})
}
