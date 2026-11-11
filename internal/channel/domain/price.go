package domain

import (
	"slices"
	"time"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// RuleError — нарушение правила с машинной причиной: app передаёт её в
// details.reason ответа 422. Сравнивается по указателю (errors.Is).
type RuleError struct {
	// Field — поле запроса, к которому относится причина.
	Field  string
	Reason string
}

func (e *RuleError) Error() string { return "channel: " + e.Field + ": " + e.Reason }

// Причины отказа в ценах (R-4, R-5).
var (
	ErrActiveRequiresPrice = &RuleError{Field: "prices", Reason: "active_requires_price"}
	ErrDuplicateFormat     = &RuleError{Field: "prices", Reason: "duplicate_format"}
	ErrUnknownFormat       = &RuleError{Field: "prices", Reason: "unknown_format"}
	ErrPriceOutOfRange     = &RuleError{Field: "prices", Reason: "out_of_range"}
)

// Пределы цены блогера (04 §3): от 100 до 10 000 000 ₽.
const (
	MinPriceAmount money.Kopecks = 10_000
	MaxPriceAmount money.Kopecks = 1_000_000_000
)

// Format — формат размещения (03 §2, placement_format).
type Format struct {
	ID            string
	Name          string
	TopHours      int
	DurationHours int
	IsActive      bool
	Position      int
}

// Price — цена блогера за формат без комиссии (channel_price).
type Price struct {
	FormatID  string
	Amount    money.Kopecks
	IsEnabled bool
	// FormatActive — формат включён в справочнике; заполняет репозиторий.
	FormatActive bool
}

// Offered — цену видит рекламодатель: она включена, а формат активен.
func (p Price) Offered() bool { return p.IsEnabled && p.FormatActive }

// TotalAmount — цена для рекламодателя: цена блогера и комиссия платформы сверху,
// комиссия округляется вниз (06 §3, A-30).
func TotalAmount(amount money.Kopecks, feeRateBP int) money.Kopecks {
	return amount + amount.ShareBP(feeRateBP)
}

// catalogEditable — состояния, в которых блогер правит цены и недоступность (R-5).
func (c *Channel) catalogEditable() bool {
	switch c.Status {
	case StatusUnclaimed, StatusDisconnected, StatusBlocked:
		return false
	}
	return true
}

// CheckCatalogEditable — ErrNotEditable, если блогер не правит цены и интервалы канала.
func (c *Channel) CheckCatalogEditable() error {
	if !c.catalogEditable() {
		return ErrNotEditable
	}
	return nil
}

// HasOfferedPrice — у канала есть включённая цена активного формата (04 §3).
func (c *Channel) HasOfferedPrice() bool {
	return slices.ContainsFunc(c.Prices, Price.Offered)
}

// SetPrices заменяет набор цен целиком (R-4, R-5): формат, которого нет в наборе,
// удаляется. active — активные форматы справочника по порядку; цены сохраняются
// в том же порядке.
func (c *Channel) SetPrices(prices []Price, active []Format, now time.Time) error {
	return c.SetPricesWithMinimum(prices, active, now, MinPriceAmount)
}

// SetPricesWithMinimum applies the environment floor; zero retains the production floor.
func (c *Channel) SetPricesWithMinimum(prices []Price, active []Format, now time.Time, minimum money.Kopecks) error {
	if minimum <= 0 {
		minimum = MinPriceAmount
	}
	if !c.catalogEditable() {
		return ErrNotEditable
	}
	seen := map[string]bool{}
	byFormat := map[string]Price{}
	for _, p := range prices {
		if seen[p.FormatID] {
			return ErrDuplicateFormat
		}
		seen[p.FormatID] = true
		if !slices.ContainsFunc(active, func(f Format) bool { return f.ID == p.FormatID && f.IsActive }) {
			return ErrUnknownFormat
		}
		if p.Amount < minimum || p.Amount > MaxPriceAmount {
			return ErrPriceOutOfRange
		}
		p.FormatActive = true
		byFormat[p.FormatID] = p
	}
	out := make([]Price, 0, len(byFormat))
	for _, f := range active {
		if p, ok := byFormat[f.ID]; ok {
			out = append(out, p)
		}
	}
	// S-13: без цены канал не выходит в каталог — у активного должна остаться хотя бы одна.
	if c.Status == StatusActive && !slices.ContainsFunc(out, Price.Offered) {
		return ErrActiveRequiresPrice
	}
	c.Prices = out
	c.touch(now)
	return nil
}

// EnabledFormats — форматы включённых цен, которые видит рекламодатель.
func (c *Channel) EnabledFormats() []string {
	out := []string{}
	for _, p := range c.Prices {
		if p.Offered() {
			out = append(out, p.FormatID)
		}
	}
	return out
}
