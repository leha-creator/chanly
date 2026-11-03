package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/post/domain"
)

func TestCheckUpload(t *testing.T) {
	ok := domain.UploadInfo{ContentType: domain.MimeJPEG, Format: domain.FormatJPEG, Width: 800, Height: 600, Size: 1000}
	with := func(f func(*domain.UploadInfo)) domain.UploadInfo { u := ok; f(&u); return u }
	lim := domain.DefaultUploadLimits
	tests := []struct {
		name string
		info domain.UploadInfo
		want error
	}{
		{"ok", ok, nil},
		{"png", with(func(u *domain.UploadInfo) { u.ContentType, u.Format = domain.MimePNG, domain.FormatPNG }), nil},
		{"webp", with(func(u *domain.UploadInfo) { u.ContentType, u.Format = domain.MimeWEBP, domain.FormatWEBP }), nil},
		{"empty", with(func(u *domain.UploadInfo) { u.Size = 0 }), domain.ErrUploadEmpty},
		{"exactly 50 MB", with(func(u *domain.UploadInfo) { u.Size = 50 << 20 }), nil},
		{"50 MB + 1 byte", with(func(u *domain.UploadInfo) { u.Size = 50<<20 + 1 }), domain.ErrUploadTooLarge},
		{"text", with(func(u *domain.UploadInfo) { u.ContentType = "text/plain; charset=utf-8" }), domain.ErrUploadUnsupportedType},
		{"gif", with(func(u *domain.UploadInfo) { u.ContentType, u.Format = "image/gif", "gif" }), domain.ErrUploadUnsupportedType},
		{"header not parsed", with(func(u *domain.UploadInfo) { u.Format = "" }), domain.ErrUploadMalformed},
		{"type and format differ", with(func(u *domain.UploadInfo) { u.Format = domain.FormatPNG }), domain.ErrUploadUnsupportedType},
		{"animated", with(func(u *domain.UploadInfo) { u.Animated = true }), domain.ErrUploadAnimated},
		{"width 159", with(func(u *domain.UploadInfo) { u.Width = 159 }), domain.ErrUploadTooSmall},
		{"height 159", with(func(u *domain.UploadInfo) { u.Height = 159 }), domain.ErrUploadTooSmall},
		{"160x160", with(func(u *domain.UploadInfo) { u.Width, u.Height = 160, 160 }), nil},
		{"7680x7680", with(func(u *domain.UploadInfo) { u.Width, u.Height = 7680, 7680 }), nil},
		{"width 7681", with(func(u *domain.UploadInfo) { u.Width = 7681 }), domain.ErrUploadTooBigDimensions},
		{"height 7681", with(func(u *domain.UploadInfo) { u.Height = 7681 }), domain.ErrUploadTooBigDimensions},
		// Поворот 90° (ориентация 6) меняет стороны местами: 7681×200 до поворота — 200×7681 после.
		{"rotated too tall", with(func(u *domain.UploadInfo) { u.Width, u.Height, u.Orientation = 7681, 200, 6 }), domain.ErrUploadTooBigDimensions},
		{"rotated too narrow", with(func(u *domain.UploadInfo) { u.Width, u.Height, u.Orientation = 800, 159, 8 }), domain.ErrUploadTooSmall},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := domain.CheckUpload(tt.info, lim); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDimensions(t *testing.T) {
	for o := 0; o <= 8; o++ {
		w, h := domain.UploadInfo{Width: 100, Height: 200, Orientation: o}.Dimensions()
		swapped := o >= 5
		if swapped != (w == 200 && h == 100) {
			t.Errorf("orientation %d: %dx%d", o, w, h)
		}
	}
}

func TestCleanOriginalName(t *testing.T) {
	for in, want := range map[string]string{
		"photo.jpg":              "photo.jpg",
		"../../a.png":            "a.png",
		`C:\Users\me\b.webp`:     "b.webp",
		"a\x00b\x1fc\u007f.jpg":  "abc.jpg",
		"  name.jpg  ":           "name.jpg",
		"":                       "image",
		"dir/":                   "image",
		"..":                     "image",
		strings.Repeat("я", 300): strings.Repeat("я", 255),
		"\xff\xfeбитое.jpg":      "битое.jpg",
	} {
		if got := domain.CleanOriginalName(in); got != want {
			t.Errorf("CleanOriginalName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParamsHash(t *testing.T) {
	base := domain.ParamsHash(domain.KindLabeled, 1, "ООО Ромашка", "7707083893")
	for name, other := range map[string]string{
		"name":    domain.ParamsHash(domain.KindLabeled, 1, "ООО Василёк", "7707083893"),
		"inn":     domain.ParamsHash(domain.KindLabeled, 1, "ООО Ромашка", "7707083894"),
		"version": domain.ParamsHash(domain.KindLabeled, 2, "ООО Ромашка", "7707083893"),
		"kind":    domain.ParamsHash(domain.KindPlain, 1, "ООО Ромашка", "7707083893"),
	} {
		if other == base {
			t.Errorf("hash does not change with %s", name)
		}
	}
	// Без плашки реквизиты в хеш не входят.
	if domain.ParamsHash(domain.KindPlain, 1, "a", "1") != domain.ParamsHash(domain.KindPlain, 1, "b", "2") {
		t.Error("plain hash depends on requisites")
	}
	if p := domain.NewDerivativeParams(domain.KindPreview, "a", "1"); p.Name != "" || p.Hash() != domain.NewDerivativeParams(domain.KindPreview, "", "").Hash() {
		t.Error("preview params keep requisites")
	}
	if len(base) != 64 {
		t.Errorf("hash %q", base)
	}
}

func TestNeededKind(t *testing.T) {
	if domain.NeededKind(domain.CarrierMedia) != domain.KindLabeled || domain.NeededKind(domain.CarrierText) != domain.KindPlain {
		t.Fatal("NeededKind")
	}
}
