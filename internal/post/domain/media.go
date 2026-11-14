package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Типы изображений медиатеки (04 §4).
const (
	MimeJPEG = "image/jpeg"
	MimePNG  = "image/png"
	MimeWEBP = "image/webp"
)

// Форматы декодера (image.DecodeConfig).
const (
	FormatJPEG = "jpeg"
	FormatPNG  = "png"
	FormatWEBP = "webp"
)

var formatMime = map[string]string{FormatJPEG: MimeJPEG, FormatPNG: MimePNG, FormatWEBP: MimeWEBP}

// Ext — расширение имени объекта для типа.
func Ext(mime string) string {
	switch mime {
	case MimeJPEG:
		return ".jpg"
	case MimePNG:
		return ".png"
	case MimeWEBP:
		return ".webp"
	}
	return ""
}

// Причины отказа в загрузке (R-2, details.reason).
var (
	ErrUploadEmpty            = &RuleError{Field: "file", Reason: "empty"}
	ErrUploadTooLarge         = &RuleError{Field: "file", Reason: "too_large"}
	ErrUploadUnsupportedType  = &RuleError{Field: "file", Reason: "unsupported_type"}
	ErrUploadAnimated         = &RuleError{Field: "file", Reason: "animated"}
	ErrUploadTooSmall         = &RuleError{Field: "file", Reason: "too_small"}
	ErrUploadTooBigDimensions = &RuleError{Field: "file", Reason: "too_big_dimensions"}
	ErrUploadMalformed        = &RuleError{Field: "file", Reason: "malformed"}
)

// UploadLimits — пределы загрузки (R-2).
type UploadLimits struct {
	MaxBytes int64
	MinSide  int
	MaxSide  int
}

// DefaultUploadLimits — 50 МБ, стороны от 160 до 7680 (R-2, 05 §2.6).
var DefaultUploadLimits = UploadLimits{MaxBytes: 50 << 20, MinSide: 160, MaxSide: 7680}

// UploadInfo — сведения о файле без полного декодирования (R-3).
type UploadInfo struct {
	// ContentType — тип по содержимому (http.DetectContentType).
	ContentType string
	// Format — формат по image.DecodeConfig; пусто — заголовок не разобран.
	Format string
	// Width, Height — размеры до поворота.
	Width, Height int
	// Orientation — ориентация EXIF (1–8); 0 — нет.
	Orientation int
	Animated    bool
	Size        int64
}

// Dimensions — размеры после поворота по EXIF: ориентации 5–8 меняют стороны местами.
func (u UploadInfo) Dimensions() (width, height int) {
	if u.Orientation >= 5 && u.Orientation <= 8 {
		return u.Height, u.Width
	}
	return u.Width, u.Height
}

// Mime — тип файла по формату декодера.
func (u UploadInfo) Mime() string { return formatMime[u.Format] }

// CheckUpload проверяет файл по R-2. Тип по содержимому должен совпасть с форматом декодера.
func CheckUpload(u UploadInfo, lim UploadLimits) error {
	switch {
	case u.Size == 0:
		return ErrUploadEmpty
	case u.Size > lim.MaxBytes:
		return ErrUploadTooLarge
	}
	switch u.ContentType {
	case MimeJPEG, MimePNG, MimeWEBP:
	default:
		return ErrUploadUnsupportedType
	}
	if u.Format == "" || u.Width <= 0 || u.Height <= 0 {
		return ErrUploadMalformed
	}
	if formatMime[u.Format] != u.ContentType {
		return ErrUploadUnsupportedType
	}
	if u.Animated {
		return ErrUploadAnimated
	}
	w, h := u.Dimensions()
	switch {
	case w < lim.MinSide || h < lim.MinSide:
		return ErrUploadTooSmall
	case w > lim.MaxSide || h > lim.MaxSide:
		return ErrUploadTooBigDimensions
	}
	return nil
}

// MaxOriginalNameLength — предел имени файла в символах (R-2).
const MaxOriginalNameLength = 255

// CleanOriginalName очищает имя от пользователя (R-2): убирает путь и управляющие
// символы, обрезает до 255 символов; пустое заменяется на «image».
func CleanOriginalName(s string) string {
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > MaxOriginalNameLength {
		s = strings.TrimSpace(string(r[:MaxOriginalNameLength]))
	}
	if s == "" || s == "." || s == ".." {
		return "image"
	}
	return s
}

// MediaFile — оригинал в медиатеке (04 §4). Содержимое не меняется (D-04).
type MediaFile struct {
	ID             uuid.UUID
	OrganizationID uuid.UUID
	StorageKey     string
	Mime           string
	SizeBytes      int64
	Width, Height  int
	SHA256         string
	OriginalName   string
	CreatedBy      uuid.UUID
	CreatedAt      time.Time
	DeletedAt      *time.Time
	PurgedAt       *time.Time
}

// DerivativeKind — вид производной (04 §4).
type DerivativeKind string

const (
	KindPreview DerivativeKind = "preview"
	KindPlain   DerivativeKind = "plain"
	KindLabeled DerivativeKind = "labeled"
)

// Valid — значение из перечня.
func (k DerivativeKind) Valid() bool { return k == KindPreview || k == KindPlain || k == KindLabeled }

// DerivativeStatus — готовность производной (R-6).
type DerivativeStatus string

const (
	DerivativePending DerivativeStatus = "pending"
	DerivativeReady   DerivativeStatus = "ready"
)

// Derivative — производная изображения: превью, копия или копия с плашкой.
type Derivative struct {
	ID          uuid.UUID
	MediaFileID uuid.UUID
	Kind        DerivativeKind
	ParamsHash  string
	Status      DerivativeStatus
	StorageKey  string
	Mime        string
	Width       int
	Height      int
	SizeBytes   int64
	// PublicStorageKey, PublicURL, PublishedAt — копия в публичной области (R-7).
	PublicStorageKey string
	PublicURL        string
	PublishedAt      *time.Time
	CreatedAt        time.Time
	PurgedAt         *time.Time
}

// Ready — файл построен и не удалён из хранилища.
func (d Derivative) Ready() bool { return d.Status == DerivativeReady && d.PurgedAt == nil }

// Версии построения (R-5). Меняются вместе с кодом построения, а не конфигурацией:
// новая версия даёт новый params_hash, уже зафиксированные версии поста не перестраиваются.
const (
	PlateTemplateVersion = 2
	RenderVersion        = 1
)

// ParamsHash — sha256 параметров построения (R-5): вид и версия; для labeled — ещё
// название и ИНН рекламодателя.
func ParamsHash(kind DerivativeKind, version int, name, inn string) string {
	s := fmt.Sprintf("%s\x00%d", kind, version)
	if kind == KindLabeled {
		s += "\x00" + name + "\x00" + inn
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// DerivativeParams — параметры производной вида kind по текущим версиям построения.
// Name и INN нужны только плашке.
type DerivativeParams struct {
	Kind DerivativeKind
	Name string
	INN  string
}

// NewDerivativeParams собирает параметры; для видов без плашки реквизиты отбрасываются.
func NewDerivativeParams(kind DerivativeKind, name, inn string) DerivativeParams {
	if kind != KindLabeled {
		name, inn = "", ""
	}
	return DerivativeParams{Kind: kind, Name: name, INN: inn}
}

// Hash — params_hash производной.
func (p DerivativeParams) Hash() string {
	return ParamsHash(p.Kind, p.Version(), p.Name, p.INN)
}

// Version is the rendering algorithm used for this kind, independent of album position.
func (p DerivativeParams) Version() int {
	if p.Kind == KindLabeled {
		return PlateTemplateVersion
	}
	return RenderVersion
}

// NeededKindAt selects media for a new snapshot by its saved, zero-based album
// position. Read existing snapshots by DerivativeID; never apply this policy again.
func NeededKindAt(c TokenCarrier, position int) DerivativeKind {
	if c == CarrierMedia && position == 0 {
		return KindLabeled
	}
	return KindPlain
}
