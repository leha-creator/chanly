package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// Format — формат размещения (03 §2): снимок в размещении.
type Format struct {
	ID            string
	TopHours      int
	DurationHours int
}

// FormatPrice — цена блогера за формат канала без комиссии.
type FormatPrice struct {
	Format  Format
	Amount  money.Kopecks
	Enabled bool
	// FormatActive — формат включён в справочнике.
	FormatActive bool
}

// Offered — цену видит рекламодатель: она включена, формат активен.
func (p FormatPrice) Offered() bool { return p.Enabled && p.FormatActive }

// ChannelRef — канал для ответов кабинетов (ChannelRef спецификации).
type ChannelRef struct {
	ID      uuid.UUID
	Title   string
	IconURL string
	Link    string
}

// ChannelOffer — то, что модулю размещений нужно знать о канале: состояние, видимость
// в каталоге (D-11, R-7 этапа «Каталог»), владелец и цены. Реализация — модуль каналов.
type ChannelOffer struct {
	Ref ChannelRef
	// OwnerOrgID — организация-владелец; nil у канала без владельца.
	OwnerOrgID *uuid.UUID
	// MaxChatID — идентификатор чата MAX (публикация, контроль).
	MaxChatID int64
	// Active — канал в active (I-08).
	Active bool
	// Visible — канал виден в каталоге: active, номер РКН подтверждён или не нужен, есть цена.
	Visible bool
	Prices  []FormatPrice
}

// Price — предложенная цена формата; false — формат не активен или цена выключена.
func (o ChannelOffer) Price(formatID string) (FormatPrice, bool) {
	for _, p := range o.Prices {
		if p.Format.ID == formatID && p.Offered() {
			return p, true
		}
	}
	return FormatPrice{}, false
}

// TotalAmount — цена для рекламодателя: цена блогера и комиссия сверху, округление
// комиссии вниз (06 §3, A-30).
func TotalAmount(amount money.Kopecks, feeRateBP int) money.Kopecks {
	return amount + amount.ShareBP(feeRateBP)
}

// CartItem — позиция корзины (04 §5, cart_item).
type CartItem struct {
	ID             uuid.UUID
	OrganizationID uuid.UUID
	ChannelID      uuid.UUID
	FormatID       string
	// ShownTotal — итог с комиссией, показанный рекламодателю при добавлении или подтверждении.
	ShownTotal money.Kopecks
	CreatedBy  uuid.UUID
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// CartLine — позиция корзины с текущим состоянием канала и цены (S-03).
type CartLine struct {
	Item    CartItem
	Channel ChannelRef
	// Current — текущий итог; nil — формат канала выключен.
	Current *money.Kopecks
	// PriceChanged — текущий итог отличается от показанного.
	PriceChanged bool
	// Available — канал виден в каталоге и формат включён.
	Available bool
}

// EvaluateCart сверяет позиции с текущими каналами (R-8): текущий итог, признак смены
// цены и доступность. Позиции недоступных каналов не удаляются.
func EvaluateCart(items []CartItem, offers map[uuid.UUID]ChannelOffer, feeRateBP int) []CartLine {
	out := make([]CartLine, 0, len(items))
	for _, it := range items {
		line := CartLine{Item: it, Channel: ChannelRef{ID: it.ChannelID}}
		if o, ok := offers[it.ChannelID]; ok {
			line.Channel = o.Ref
			if price, ok := o.Price(it.FormatID); ok {
				total := TotalAmount(price.Amount, feeRateBP)
				line.Current = &total
				line.PriceChanged = total != it.ShownTotal
				line.Available = o.Active && o.Visible
			}
		}
		out = append(out, line)
	}
	return out
}

// CartTotal — сумма текущих итогов доступных позиций.
func CartTotal(lines []CartLine) money.Kopecks {
	var sum money.Kopecks
	for _, l := range lines {
		if l.Available && l.Current != nil {
			sum += *l.Current
		}
	}
	return sum
}

// CartRules — пределы корзины (07 §8).
type CartRules struct {
	// MaxItems — CART_MAX_ITEMS (R-8: 50).
	MaxItems int
}

// CheckAdd проверяет добавление канала в корзину организации (R-8): канал виден в каталоге
// (иначе ErrChannelNotInCatalog — 404), его ещё нет в корзине (ErrAlreadyInCart — 409),
// канал не свой, формат предложен, позиций меньше предела. Возвращает цену формата.
func CheckAdd(orgID uuid.UUID, offer *ChannelOffer, formatID string, inCart bool, count int, r CartRules) (FormatPrice, error) {
	if offer == nil || !offer.Visible {
		return FormatPrice{}, ErrChannelNotInCatalog
	}
	if inCart {
		return FormatPrice{}, ErrAlreadyInCart
	}
	if offer.OwnerOrgID != nil && *offer.OwnerOrgID == orgID {
		return FormatPrice{}, ErrOwnChannel
	}
	price, ok := offer.Price(formatID)
	if !ok {
		return FormatPrice{}, ErrFormatUnavailable
	}
	if count >= r.MaxItems {
		return FormatPrice{}, ErrCartFull
	}
	return price, nil
}

// CheckFormat — смена формата позиции или подтверждение цены (R-7): формат предложен.
func CheckFormat(offer *ChannelOffer, formatID string) (FormatPrice, error) {
	if offer == nil {
		return FormatPrice{}, ErrFormatUnavailable
	}
	price, ok := offer.Price(formatID)
	if !ok {
		return FormatPrice{}, ErrFormatUnavailable
	}
	return price, nil
}
