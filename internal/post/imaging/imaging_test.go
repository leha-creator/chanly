package imaging_test

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	xdraw "golang.org/x/image/draw"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/imaging"
	"github.com/leha-creator/chanly/internal/post/imaging/imagingtest"
)

var update = flag.Bool("update", false, "rewrite golden images in testdata")

const (
	name = "ООО «Ромашка»"
	inn  = "7707083893"
)

func renderer(maxBytes int64) *imaging.Renderer {
	return imaging.New(imaging.Options{PreviewSide: 640, MaxBytes: maxBytes, Concurrency: 2}, clock.System{}, logx.Discard())
}

func render(t *testing.T, r *imaging.Renderer, data []byte, kind domain.DerivativeKind) (domain.Rendered, image.Image) {
	t.Helper()
	out, err := r.Render(context.Background(), data, domain.NewDerivativeParams(kind, name, inn))
	if err != nil {
		t.Fatalf("render %s: %v", kind, err)
	}
	img, _, err := image.Decode(bytes.NewReader(out.Data))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if b := img.Bounds(); b.Dx() != out.Width || b.Dy() != out.Height {
		t.Fatalf("reported %dx%d, actual %v", out.Width, out.Height, b)
	}
	if info := imaging.Probe(out.Data); info.Orientation != 0 || info.ContentType != out.Mime {
		t.Fatalf("result probe %+v, mime %s", info, out.Mime)
	}
	return out, img
}

func near(c color.Color, r, g, b uint8, tol int) bool {
	cr, cg, cb, _ := c.RGBA()
	d := func(a uint32, want uint8) bool { v := int(a>>8) - int(want); return v >= -tol && v <= tol }
	return d(cr, r) && d(cg, g) && d(cb, b)
}

func TestPreview(t *testing.T) {
	r := renderer(50 << 20)
	out, img := render(t, r, imagingtest.JPEG(2000, 1000, 0), domain.KindPreview)
	if out.Mime != domain.MimeJPEG || out.Width != 640 || out.Height != 320 {
		t.Fatalf("%s %dx%d", out.Mime, out.Width, out.Height)
	}
	// Прозрачность превью заливается белым.
	out, img = render(t, r, imagingtest.TransparentPNG(400, 400), domain.KindPreview)
	if out.Mime != domain.MimeJPEG || !near(img.At(200, 390), 255, 255, 255, 8) {
		t.Fatalf("%s, bottom pixel %v", out.Mime, img.At(200, 390))
	}
	// Маленькое изображение не увеличивается.
	out, _ = render(t, r, imagingtest.JPEG(160, 160, 0), domain.KindPreview)
	if out.Width != 160 || out.Height != 160 {
		t.Fatalf("%dx%d", out.Width, out.Height)
	}
}

func TestPlainRotatesAndStripsExif(t *testing.T) {
	// Ориентация 6: поворот на 90° по часовой — левая (красная) половина становится верхней.
	out, img := render(t, renderer(50<<20), imagingtest.JPEG(300, 200, 6), domain.KindPlain)
	if out.Mime != domain.MimeJPEG || out.Width != 200 || out.Height != 300 {
		t.Fatalf("%s %dx%d", out.Mime, out.Width, out.Height)
	}
	if !near(img.At(100, 20), 220, 0, 0, 30) || !near(img.At(100, 280), 0, 0, 220, 30) {
		t.Fatalf("top %v, bottom %v", img.At(100, 20), img.At(100, 280))
	}
}

func TestPlainFormats(t *testing.T) {
	r := renderer(50 << 20)
	tests := []struct {
		name string
		data []byte
		mime string
	}{
		{"png stays png", imagingtest.TransparentPNG(200, 200), domain.MimePNG},
		{"opaque png stays png", imagingtest.OpaquePNG(200, 200), domain.MimePNG},
		{"webp with alpha to png", imagingtest.AlphaWebP(), domain.MimePNG},
		{"opaque webp to jpeg", imagingtest.OpaqueWebP(), domain.MimeJPEG},
		{"jpeg stays jpeg", imagingtest.JPEG(200, 200, 0), domain.MimeJPEG},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if out, _ := render(t, r, tt.data, domain.KindPlain); out.Mime != tt.mime {
				t.Fatalf("mime %s, want %s", out.Mime, tt.mime)
			}
		})
	}
	// Прозрачность PNG сохраняется.
	_, img := render(t, r, imagingtest.TransparentPNG(200, 200), domain.KindPlain)
	if _, _, _, a := img.At(100, 190).RGBA(); a != 0 {
		t.Fatalf("alpha %d", a)
	}
}

func TestLabeled(t *testing.T) {
	src := imagingtest.OpaquePNG(800, 1000)
	out, img := render(t, renderer(50<<20), src, domain.KindLabeled)
	if out.Mime != domain.MimePNG || out.Width != 800 || out.Height != 1000 {
		t.Fatalf("%s %dx%d", out.Mime, out.Width, out.Height)
	}
	layout, err := domain.LayoutPlate(800, 1000, name, inn, imaging.Measurer{})
	if err != nil {
		t.Fatal(err)
	}
	orig, _ := png.Decode(bytes.NewReader(src))
	card := image.Rect(layout.X, layout.Y, layout.X+layout.Width, layout.Y+layout.Height)
	white := 0
	for y := 0; y < 1000; y++ {
		for x := 0; x < 800; x++ {
			if !image.Pt(x, y).In(card) && img.At(x, y) != orig.At(x, y) {
				t.Fatalf("pixel %d,%d changed outside card", x, y)
			}
			if image.Pt(x, y).In(card) && near(img.At(x, y), 255, 255, 255, 10) {
				white++
			}
		}
	}
	if white < 100 {
		t.Fatalf("missing glyphs: %d white pixels", white)
	}
	if !near(img.At(layout.X+layout.Width/2, layout.Y+1), 0x17, 0x1A, 0x1F, 0) {
		t.Fatal("card background is not opaque #171A1F")
	}
}

func TestLabeledV2Golden(t *testing.T) {
	long := "Общество с ограниченной ответственностью «Очень длинное название рекламодателя Василёк»"
	var white bytes.Buffer
	whiteImage := image.NewNRGBA(image.Rect(0, 0, 640, 480))
	for y := range 480 {
		for x := range 640 {
			whiteImage.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
		}
	}
	if err := png.Encode(&white, whiteImage); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, advertiser string
		data             []byte
	}{
		{"normal", name, imagingtest.OpaquePNG(640, 480)},
		{"normal_320", name, imagingtest.OpaquePNG(640, 480)},
		{"long", long, imagingtest.OpaquePNG(640, 480)},
		{"minimum", long, imagingtest.OpaquePNG(160, 160)},
		{"transparent", name, imagingtest.TransparentPNG(400, 400)},
		{"white", name, white.Bytes()},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			out, err := renderer(50<<20).Render(context.Background(), tt.data, domain.NewDerivativeParams(domain.KindLabeled, tt.advertiser, inn))
			if err != nil {
				t.Fatal(err)
			}
			layout, err := domain.LayoutPlate(out.Width, out.Height, tt.advertiser, inn, imaging.Measurer{})
			if err != nil || !strings.Contains(strings.Join(layout.Lines, " "), "ИНН "+inn) {
				t.Fatalf("layout %+v %v", layout, err)
			}
			path := filepath.Join("testdata", "labeled_v2_"+tt.name+".png")
			if tt.name == "normal_320" {
				src, err := png.Decode(bytes.NewReader(out.Data))
				if err != nil {
					t.Fatal(err)
				}
				dst := image.NewNRGBA(image.Rect(0, 0, 320, 240))
				xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
				var buf bytes.Buffer
				if err := png.Encode(&buf, dst); err != nil {
					t.Fatal(err)
				}
				out.Data = buf.Bytes()
			}
			if *update {
				if err := os.WriteFile(path, out.Data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("golden: %v (run -run TestLabeledV2Golden -update)", err)
			}
			// PNG output and the embedded font are deterministic. Do not allow a broad
			// pixel tolerance to hide a missing short line such as the INN.
			if !bytes.Equal(out.Data, want) {
				t.Fatal("v2 golden differs")
			}
		})
	}
}

func TestLabeledV2FormatsAndAspectRatios(t *testing.T) {
	inputs := [][]byte{imagingtest.JPEG(160, 160, 0), imagingtest.OpaquePNG(7680, 160), imagingtest.OpaquePNG(160, 7680), imagingtest.AlphaWebP(), imagingtest.OpaqueWebP(), imagingtest.JPEG(800, 600, 6)}
	for _, src := range inputs {
		out, err := renderer(50<<20).Render(context.Background(), src, domain.NewDerivativeParams(domain.KindLabeled, strings.Repeat("Василёк", 70), "123456789012"))
		if err != nil {
			t.Fatal(err)
		}
		layout, err := domain.LayoutPlate(out.Width, out.Height, strings.Repeat("Василёк", 70), "123456789012", imaging.Measurer{})
		if err != nil || !strings.HasSuffix(strings.Join(layout.Lines, " "), "ИНН 123456789012") {
			t.Fatalf("layout %+v %v", layout, err)
		}
		for _, line := range layout.Lines {
			if (imaging.Measurer{}).Width(line, layout.FontSize) > float64(layout.Width-2*layout.Padding) {
				t.Fatal("text overflow")
			}
		}
	}
}
func TestRenderErrors(t *testing.T) {
	r := renderer(50 << 20)
	ctx := context.Background()
	if _, err := r.Render(ctx, []byte("not an image"), domain.NewDerivativeParams(domain.KindPlain, "", "")); !errors.Is(err, domain.ErrUndecodable) {
		t.Fatalf("garbage: %v", err)
	}
	// Предел меньше любого результата: PNG перекодируется в JPEG и всё равно не помещается.
	small := renderer(100)
	if _, err := small.Render(ctx, imagingtest.OpaquePNG(400, 400), domain.NewDerivativeParams(domain.KindPlain, "", "")); !errors.Is(err, domain.ErrDerivativeTooLarge) {
		t.Fatalf("too large: %v", err)
	}
}

// TestConcurrencyLimit: третий одновременный вызов ждёт свободного места (R-20).
func TestConcurrencyLimit(t *testing.T) {
	r := imaging.New(imaging.Options{PreviewSide: 640, MaxBytes: 50 << 20, Concurrency: 2}, clock.System{}, logx.Discard())
	data := imagingtest.JPEG(4000, 4000, 0)
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started <- struct{}{}
			_, _ = r.Render(ctx, data, domain.NewDerivativeParams(domain.KindLabeled, name, inn))
		}()
	}
	<-started
	<-started
	time.Sleep(50 * time.Millisecond) // оба построения заняли места
	short, cancelShort := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancelShort()
	if _, err := r.Render(short, data, domain.NewDerivativeParams(domain.KindPlain, "", "")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third render: want deadline exceeded, got %v", err)
	}
	wg.Wait()
	if _, err := r.Render(context.Background(), imagingtest.JPEG(200, 200, 0), domain.NewDerivativeParams(domain.KindPlain, "", "")); err != nil {
		t.Fatalf("render after slots freed: %v", err)
	}
}
