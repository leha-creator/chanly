package domain

import (
	"errors"
	"math"
	"strings"
	"unicode"
)

// Plate v2 geometry is part of PlateTemplateVersion, not runtime configuration.
const PlateEllipsis = "…"

// ErrPlateLayout prevents a successful derivative with missing/clipped marking.
var ErrPlateLayout = errors.New("advertising plate does not fit the image")

func PlateText(name, inn string) string { return "Реклама. " + name + ", ИНН " + inn }

type FontMetrics struct{ Ascent, Descent float64 }

// TextMeasurer uses the same embedded font as the renderer.
type TextMeasurer interface {
	Width(text string, size float64) float64
	Metrics(size float64) FontMetrics
}

// PlateLayout describes an inset card in image coordinates.
type PlateLayout struct {
	X, Y, Width, Height int
	Padding, Radius     int
	FontSize            float64
	LineHeight          int
	Ascent              float64
	Lines               []string
	Truncated           bool
}

// LayoutPlate wraps the advertiser name onto at most two lines. Only the name
// may be ellipsized; the advertising label and complete INN must always fit.
func LayoutPlate(width, height int, name, inn string, m TextMeasurer) (PlateLayout, error) {
	// About 10 px when an ordinary photo is displayed at 320 px wide. The
	// height bound keeps panoramic images usable; tiny sources retain 12 px.
	size := max(12, math.Round(min(float64(width)/32, float64(height)/8)))
	margin := max(8, int(math.Round(min(float64(width)*0.02, float64(height)*0.05))))
	padding := max(8, int(math.Round(size/2)))
	available := float64(width - 2*margin - 2*padding)
	metrics := m.Metrics(size)
	if available <= 0 || math.IsInf(metrics.Ascent+metrics.Descent, 0) || math.IsNaN(metrics.Ascent+metrics.Descent) {
		return PlateLayout{}, ErrPlateLayout
	}
	lineHeight := int(math.Ceil(max(size*1.2, metrics.Ascent+metrics.Descent)))
	l := PlateLayout{X: margin, Padding: padding, Radius: max(6, int(math.Round(size*0.3))), FontSize: size,
		LineHeight: lineHeight, Ascent: metrics.Ascent}
	name = strings.Join(strings.Fields(name), " ")
	full := PlateText(name, inn)
	if m.Width(full, size) <= available {
		l.Lines = []string{full}
	} else {
		label, tax := "Реклама", "ИНН "+inn
		if m.Width(label, size) > available || m.Width(tax, size) > available {
			return PlateLayout{}, ErrPlateLayout
		}
		l.Lines = []string{label}
		remaining := name
		for i := 0; i < 2 && remaining != ""; i++ {
			line, rest := plateNameLine(remaining, available, size, m)
			if i == 1 && rest != "" {
				line, _ = plateNameLine(remaining, available-m.Width(PlateEllipsis, size), size, m)
				line = strings.TrimSpace(line) + PlateEllipsis
				l.Truncated = true
			}
			if line == "" || m.Width(line, size) > available {
				return PlateLayout{}, ErrPlateLayout
			}
			l.Lines = append(l.Lines, line)
			remaining = rest
		}
		l.Lines = append(l.Lines, tax)
	}
	maxWidth := 0.0
	for _, line := range l.Lines {
		maxWidth = max(maxWidth, m.Width(line, size))
	}
	l.Width = int(math.Ceil(maxWidth)) + 2*padding
	l.Height = len(l.Lines)*lineHeight + 2*padding
	l.Y = height - margin - l.Height
	if l.Y < margin || l.X+l.Width > width-margin {
		return PlateLayout{}, ErrPlateLayout
	}
	l.Radius = min(l.Radius, l.Width/2, l.Height/2)
	return l, nil
}

// Prefer a word boundary, or a Unicode boundary for an unbroken word.
func plateNameLine(text string, available, size float64, m TextMeasurer) (string, string) {
	runes := []rune(text)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if m.Width(string(runes[:mid]), size) <= available {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo < len(runes) && !unicode.IsSpace(runes[lo]) {
		for i := lo - 1; i > 0; i-- {
			if unicode.IsSpace(runes[i]) {
				lo = i
				break
			}
		}
	}
	return strings.TrimSpace(string(runes[:lo])), strings.TrimSpace(string(runes[lo:]))
}
