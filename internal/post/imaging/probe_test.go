package imaging_test

import (
	"testing"

	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/imaging"
	"github.com/leha-creator/chanly/internal/post/imaging/imagingtest"
)

func TestProbe(t *testing.T) {
	tests := []struct {
		name        string
		data        []byte
		contentType string
		format      string
		w, h        int
		orientation int
		animated    bool
	}{
		{"jpeg", imagingtest.JPEG(300, 200, 0), domain.MimeJPEG, domain.FormatJPEG, 300, 200, 0, false},
		{"jpeg exif 6", imagingtest.JPEG(300, 200, 6), domain.MimeJPEG, domain.FormatJPEG, 300, 200, 6, false},
		{"png alpha", imagingtest.TransparentPNG(200, 160), domain.MimePNG, domain.FormatPNG, 200, 160, 0, false},
		{"apng", imagingtest.AnimatedPNG(200, 160), domain.MimePNG, domain.FormatPNG, 200, 160, 0, true},
		{"webp opaque", imagingtest.OpaqueWebP(), domain.MimeWEBP, domain.FormatWEBP, 400, 300, 0, false},
		{"webp alpha", imagingtest.AlphaWebP(), domain.MimeWEBP, domain.FormatWEBP, 300, 200, 0, false},
		{"webp animated", imagingtest.AnimatedWebP(), domain.MimeWEBP, domain.FormatWEBP, 200, 200, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := imaging.Probe(tt.data)
			if info.ContentType != tt.contentType || info.Format != tt.format || info.Width != tt.w || info.Height != tt.h ||
				info.Orientation != tt.orientation || info.Animated != tt.animated || info.Size != int64(len(tt.data)) {
				t.Fatalf("%+v", info)
			}
		})
	}
}

func TestProbeAnimatedRejected(t *testing.T) {
	for name, data := range map[string][]byte{"webp": imagingtest.AnimatedWebP(), "png": imagingtest.AnimatedPNG(200, 200)} {
		if err := domain.CheckUpload(imaging.Probe(data), domain.DefaultUploadLimits); err != domain.ErrUploadAnimated {
			t.Errorf("animated %s: %v", name, err)
		}
	}
}

func TestProbeNotImage(t *testing.T) {
	info := imaging.Probe([]byte("просто текст, а не картинка"))
	if info.Format != "" || domain.CheckUpload(info, domain.DefaultUploadLimits) != domain.ErrUploadUnsupportedType {
		t.Fatalf("%+v", info)
	}
	// PNG с расширением .jpg определяется по содержимому.
	if info := imaging.Probe(imagingtest.OpaquePNG(200, 200)); info.ContentType != domain.MimePNG {
		t.Fatalf("%+v", info)
	}
}
