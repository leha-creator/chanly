// Package imaging строит производные изображений медиатеки (D-04, 06 §6.2): превью,
// копию для публикации и копию с плашкой «Реклама. {название}, ИНН {ИНН}».
// Реализует порт post/domain.Renderer. Оригинал не меняется; производные строятся
// после поворота по EXIF и кодируются без метаданных (R-3, 07 §4).
package imaging

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"log/slog"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp" // декодер WebP для image.Decode и image.DecodeConfig

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/post/domain"
)

// Качество JPEG (R-4).
const (
	previewQuality = 80
	jpegQuality    = 90
)

// Options — параметры построения.
type Options struct {
	// PreviewSide — длинная сторона превью (MEDIA_PREVIEW_SIDE).
	PreviewSide int
	// MaxBytes — предел размера производной (предел MAX, 50 МБ).
	MaxBytes int64
	// Concurrency — сколько изображений строится одновременно (MEDIA_BUILD_CONCURRENCY, R-20).
	Concurrency int
}

// Renderer строит производные. Безопасен для конкурентного использования.
type Renderer struct {
	opts  Options
	sem   chan struct{}
	clock clock.Clock
	log   *slog.Logger
}

var _ domain.Renderer = (*Renderer)(nil)

// New создаёт построитель.
func New(opts Options, clk clock.Clock, log *slog.Logger) *Renderer {
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	return &Renderer{opts: opts, sem: make(chan struct{}, opts.Concurrency), clock: clk,
		log: log.With(logx.KeyModule, "post")}
}

// Probe — сведения о файле без полного декодирования.
func (r *Renderer) Probe(data []byte) domain.UploadInfo { return Probe(data) }

// acquire занимает место построения; ждёт, пока место освободится или отменится ctx (R-20).
func (r *Renderer) acquire(ctx context.Context) error {
	select {
	case r.sem <- struct{}{}:
		return nil
	default:
	}
	r.log.DebugContext(ctx, "media render waiting", logx.KeyOp, "Post.RenderDerivative", "in_use", len(r.sem))
	select {
	case r.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Renderer) release() { <-r.sem }

// Render строит производную вида p.Kind. Ошибка декодирования — domain.ErrUndecodable,
// производная больше предела и после перекодирования — domain.ErrDerivativeTooLarge.
func (r *Renderer) Render(ctx context.Context, original []byte, p domain.DerivativeParams) (domain.Rendered, error) {
	if err := r.acquire(ctx); err != nil {
		return domain.Rendered{}, err
	}
	defer r.release()
	start := r.clock.Now()

	_, format, err := image.DecodeConfig(bytes.NewReader(original))
	if err != nil {
		return domain.Rendered{}, fmt.Errorf("%w: %v", domain.ErrUndecodable, err)
	}
	src, err := imaging.Decode(bytes.NewReader(original), imaging.AutoOrientation(true))
	if err != nil {
		return domain.Rendered{}, fmt.Errorf("%w: %v", domain.ErrUndecodable, err)
	}

	var out domain.Rendered
	switch p.Kind {
	case domain.KindPreview:
		img := imaging.Fit(src, r.opts.PreviewSide, r.opts.PreviewSide, imaging.Lanczos)
		out, err = encodeJPEG(flatten(img), previewQuality)
	case domain.KindPlain:
		out, err = r.encodeLike(ctx, src, format)
	case domain.KindLabeled:
		img := imaging.Clone(src)
		drawPlate(img, p.Name, p.INN)
		out, err = r.encodeLike(ctx, img, format)
	default:
		return domain.Rendered{}, fmt.Errorf("imaging: unknown derivative kind %q", p.Kind)
	}
	if err != nil {
		return domain.Rendered{}, err
	}
	r.log.DebugContext(ctx, "media derivative rendered", logx.KeyOp, "Post.RenderDerivative", "kind", p.Kind,
		"width", out.Width, "height", out.Height, "size_bytes", len(out.Data),
		"duration_ms", r.clock.Now().Sub(start).Milliseconds())
	return out, nil
}

// encodeLike кодирует копию для публикации (R-4): JPEG → JPEG 90, PNG → PNG, WebP → PNG
// при прозрачности, иначе JPEG 90. Больше предела — перекодирование в JPEG 90.
func (r *Renderer) encodeLike(ctx context.Context, img image.Image, format string) (domain.Rendered, error) {
	var out domain.Rendered
	var err error
	switch {
	case format == domain.FormatPNG, format == domain.FormatWEBP && !opaque(img):
		out, err = encodePNG(img)
	default:
		out, err = encodeJPEG(flatten(img), jpegQuality)
	}
	if err != nil {
		return domain.Rendered{}, err
	}
	if int64(len(out.Data)) <= r.opts.MaxBytes {
		return out, nil
	}
	if out.Mime != domain.MimeJPEG {
		r.log.WarnContext(ctx, "media derivative reencoded", logx.KeyOp, "Post.RenderDerivative", "reason", "too_large",
			"size_bytes", len(out.Data))
		if out, err = encodeJPEG(flatten(img), jpegQuality); err != nil {
			return domain.Rendered{}, err
		}
		if int64(len(out.Data)) <= r.opts.MaxBytes {
			return out, nil
		}
	}
	return domain.Rendered{}, fmt.Errorf("%w: %d bytes", domain.ErrDerivativeTooLarge, len(out.Data))
}

// opaque — в изображении нет прозрачных пикселей.
func opaque(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return o.Opaque()
	}
	return false
}

// flatten заливает прозрачность белым: в JPEG альфа-канала нет.
func flatten(img image.Image) image.Image {
	if opaque(img) {
		return img
	}
	b := img.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, b, img, b.Min, draw.Over)
	return dst
}

func encodeJPEG(img image.Image, quality int) (domain.Rendered, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return domain.Rendered{}, fmt.Errorf("encode jpeg: %w", err)
	}
	b := img.Bounds()
	return domain.Rendered{Data: buf.Bytes(), Mime: domain.MimeJPEG, Width: b.Dx(), Height: b.Dy()}, nil
}

func encodePNG(img image.Image) (domain.Rendered, error) {
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&buf, img); err != nil {
		return domain.Rendered{}, fmt.Errorf("encode png: %w", err)
	}
	b := img.Bounds()
	return domain.Rendered{Data: buf.Bytes(), Mime: domain.MimePNG, Width: b.Dx(), Height: b.Dy()}, nil
}
