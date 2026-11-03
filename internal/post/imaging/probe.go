package imaging

import (
	"bytes"
	"encoding/binary"
	"image"
	"net/http"

	"github.com/leha-creator/chanly/internal/post/domain"
)

// Probe читает сведения о файле без полного декодирования (R-3): тип по содержимому,
// формат и размеры по заголовку, ориентацию EXIF у JPEG, признак анимации у PNG и WebP.
// Неразобранный заголовок даёт пустой Format — его отклоняет domain.CheckUpload.
func Probe(data []byte) domain.UploadInfo {
	info := domain.UploadInfo{ContentType: http.DetectContentType(data), Size: int64(len(data))}
	if webpAnimated(data) {
		// Декодер x/image/webp анимацию не разбирает; размеры холста — из VP8X.
		info.Format, info.Animated = domain.FormatWEBP, true
		info.Width = 1 + (int(data[24]) | int(data[25])<<8 | int(data[26])<<16)
		info.Height = 1 + (int(data[27]) | int(data[28])<<8 | int(data[29])<<16)
		return info
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return info
	}
	info.Format, info.Width, info.Height = format, cfg.Width, cfg.Height
	switch format {
	case domain.FormatJPEG:
		info.Orientation = jpegOrientation(data)
	case domain.FormatPNG:
		info.Animated = pngAnimated(data)
	case domain.FormatWEBP:
		info.Animated = webpAnimated(data)
	}
	return info
}

// pngAnimated — в PNG есть чанк acTL до первого IDAT (APNG).
func pngAnimated(data []byte) bool {
	const signature = 8
	for i := signature; i+8 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[i:]))
		switch string(data[i+4 : i+8]) {
		case "acTL":
			return true
		case "IDAT", "IEND":
			return false
		}
		if n < 0 || i+12+n > len(data) {
			return false
		}
		i += 12 + n
	}
	return false
}

// webpAnimated — у WebP есть чанк VP8X с флагом анимации.
func webpAnimated(data []byte) bool {
	const flagAnimation = 0x02
	if len(data) < 30 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" || string(data[12:16]) != "VP8X" {
		return false
	}
	return data[20]&flagAnimation != 0
}

// jpegOrientation — значение тега Orientation (0x0112) из APP1 Exif; 0 — нет.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 0
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return 0
		}
		marker := data[i+1]
		if marker == 0xD9 || marker == 0xDA { // EOI, SOS — дальше данные изображения
			return 0
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if n < 2 || i+2+n > len(data) {
			return 0
		}
		seg := data[i+4 : i+2+n]
		if marker == 0xE1 && len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 0
}

// tiffOrientation ищет Orientation в IFD0 заголовка TIFF.
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 0
	}
	count := int(bo.Uint16(t[off:]))
	for k := range count {
		e := off + 2 + k*12
		if e+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[e:]) == 0x0112 && bo.Uint16(t[e+2:]) == 3 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 0
		}
	}
	return 0
}
