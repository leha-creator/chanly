package domain

import (
	"encoding/base64"
	"encoding/json"
	"slices"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// CatalogVisible — канал виден рекламодателю в каталоге и карточке (R-7): active,
// номер РКН подтверждён или он не нужен, есть включённая цена активного формата,
// площадка канала зарегистрирована в ОРД (этап «Маркировка», R-14). То же условие — в SQL выборки каталога.
func CatalogVisible(c *Channel, rknThreshold int) bool {
	return c.Status == StatusActive && !c.RknRequired(rknThreshold) && c.HasOfferedPrice() && c.OrdPlatformReadyAt != nil
}

// CatalogSort — сортировка каталога (S-02, R-9).
type CatalogSort string

const (
	SortSubscribersDesc CatalogSort = "subscribers_desc"
	SortPriceAsc        CatalogSort = "price_asc"
	SortPriceDesc       CatalogSort = "price_desc"
	SortEngagementDesc  CatalogSort = "engagement_desc"
	SortRatingDesc      CatalogSort = "rating_desc"
	SortAvgViewsDesc    CatalogSort = "avg_views_desc"
	SortCPVAsc          CatalogSort = "cpv_asc"
)

// CatalogSorts — все сортировки каталога; первая — по умолчанию.
var CatalogSorts = []CatalogSort{
	SortSubscribersDesc, SortPriceAsc, SortPriceDesc, SortEngagementDesc, SortRatingDesc, SortAvgViewsDesc, SortCPVAsc,
}

// Valid — сортировка из перечня.
func (s CatalogSort) Valid() bool { return slices.Contains(CatalogSorts, s) }

// Причины отказа в запросе каталога (R-10).
var (
	ErrInvalidCursor = &RuleError{Field: "cursor", Reason: "invalid_cursor"}
	ErrPriceRange    = &RuleError{Field: "price_from", Reason: "range"}
	ErrSubscribers   = &RuleError{Field: "subscribers_from", Reason: "range"}
	ErrCatalogFormat = &RuleError{Field: "format_id", Reason: "unknown_format"}
)

// CatalogFilter — фильтры каталога. Пустые поля не применяются.
type CatalogFilter struct {
	// Query ищется в названии и описании буквально, без учёта регистра (R-11).
	Query      string
	CategoryID *uuid.UUID
	// FormatID — только каналы с включённой ценой формата; к ней применяется фильтр цены.
	FormatID        string
	PriceFrom       *money.Kopecks
	PriceTo         *money.Kopecks
	SubscribersFrom *int
	SubscribersTo   *int
}

// Validate проверяет согласованность границ.
func (f CatalogFilter) Validate() error {
	if f.PriceFrom != nil && f.PriceTo != nil && *f.PriceFrom > *f.PriceTo {
		return ErrPriceRange
	}
	if f.SubscribersFrom != nil && f.SubscribersTo != nil && *f.SubscribersFrom > *f.SubscribersTo {
		return ErrSubscribers
	}
	return nil
}

// CatalogCursor — позиция в выдаче: значение ключа сортировки и id последнего канала
// страницы (R-10). Value nil — у канала ключа нет (он в хвосте выдачи).
type CatalogCursor struct {
	Sort  CatalogSort `json:"s"`
	Value *int64      `json:"v"`
	ID    uuid.UUID   `json:"id"`
}

// Encode — base64url от JSON.
func (c CatalogCursor) Encode() string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeCatalogCursor разбирает курсор сортировки sort; курсор другой сортировки
// или испорченный — ErrInvalidCursor.
func DecodeCatalogCursor(s string, sort CatalogSort) (*CatalogCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	var c CatalogCursor
	if err := json.Unmarshal(raw, &c); err != nil || c.Sort != sort || c.ID == uuid.Nil {
		return nil, ErrInvalidCursor
	}
	return &c, nil
}

// CatalogQuery — запрос страницы каталога к репозиторию.
type CatalogQuery struct {
	Filter CatalogFilter
	Sort   CatalogSort
	After  *CatalogCursor
	// Limit — сколько строк вернуть (app просит на одну больше, чтобы узнать о следующей странице).
	Limit        int
	FeeRateBP    int
	RknThreshold int
}

// CatalogPrice — цена формата для рекламодателя, с комиссией.
type CatalogPrice struct {
	FormatID string
	Total    money.Kopecks
}

// CatalogItem — канал в выдаче каталога с последним снимком метрик.
type CatalogItem struct {
	ID               uuid.UUID
	Title            string
	IconURL          string
	Link             string
	Description      string
	MaxDescription   string
	Category         Category
	SubscribersCount int
	Rating           int
	RknNumber        string
	// Metrics — последний снимок; nil, если снимков нет.
	Metrics *MetricsSnapshot
	Prices  []CatalogPrice
	// CPV — цена выбранного формата (или минимальная включённая) за просмотр (R-9).
	CPV *money.Kopecks
	// SortValue — значение ключа сортировки запроса; nil — ключа нет.
	SortValue *int64
}

// CPV — цена за просмотр в копейках, округление вниз (R-9). Нет просмотров — nil.
func CPV(price money.Kopecks, avgViews *int) *money.Kopecks {
	if avgViews == nil || *avgViews <= 0 {
		return nil
	}
	v := price / money.Kopecks(*avgViews)
	return &v
}

// CatalogCard — карточка канала (S-02): канал, история метрик, недоступность.
type CatalogCard struct {
	CatalogItem
	// History — последние снимки, старые первыми.
	History []MetricsSnapshot
	// Unavailability — живые интервалы блогера и занятые размещениями (R-8).
	Unavailability []Unavailability
}
