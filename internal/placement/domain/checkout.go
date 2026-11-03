package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// CampaignStatus — состояние кампании (04 §9) на языке модуля размещений.
type CampaignStatus string

const (
	CampaignDraft    CampaignStatus = "draft"
	CampaignOrdered  CampaignStatus = "ordered"
	CampaignArchived CampaignStatus = "archived"
)

// CampaignSnapshot — поля кампании, нужные оформлению и кабинетам (R-1). Реализация — модуль post.
type CampaignSnapshot struct {
	ID                uuid.UUID
	OrganizationID    uuid.UUID
	PostID            uuid.UUID
	Name              string
	Status            CampaignStatus
	DateFrom, DateTo  *time.Time
	TimeFrom, TimeTo  *TimeOfDay
	ExtraRequirements string
	OrderID           *uuid.UUID
}

// CheckCampaign — кампания готова к оплате (R-6, A-39): draft, заданы даты и время, период
// не в прошлом по Москве (D-20), до конца окна больше WindowGuard. Возвращает окно размещения.
// Не draft — ErrCampaignNotDraft; не хватает полей — IncompleteError.
func CheckCampaign(c CampaignSnapshot, now time.Time, t Timing) (Window, error) {
	if c.Status != CampaignDraft {
		return Window{}, ErrCampaignNotDraft
	}
	var missing []string
	if c.DateFrom == nil {
		missing = append(missing, "date_from")
	}
	if c.DateTo == nil {
		missing = append(missing, "date_to")
	}
	if c.TimeFrom == nil {
		missing = append(missing, "time_from")
	}
	if c.TimeTo == nil {
		missing = append(missing, "time_to")
	}
	if len(missing) > 0 {
		return Window{}, &IncompleteError{MissingFields: missing}
	}
	if c.DateFrom.Before(TodayMSK(now)) {
		return Window{}, &IncompleteError{MissingFields: []string{"date_from"}}
	}
	w := NewWindow(*c.DateFrom, *c.DateTo, *c.TimeFrom, *c.TimeTo)
	if w.End.Sub(now) <= t.WindowGuard {
		return Window{}, &IncompleteError{MissingFields: []string{"date_to"}}
	}
	return w, nil
}

// CheckCartForCheckout — корзина совпадает с показанной (R-7): не пуста, каждая позиция
// доступна, формат включён, цена не изменилась. Иначе CartChangedError с позициями.
func CheckCartForCheckout(lines []CartLine) error {
	if len(lines) == 0 {
		return &CartChangedError{Items: []uuid.UUID{}}
	}
	var bad []uuid.UUID
	for _, l := range lines {
		if !l.Available || l.Current == nil || l.PriceChanged {
			bad = append(bad, l.Item.ID)
		}
	}
	if len(bad) > 0 {
		return &CartChangedError{Items: bad}
	}
	return nil
}

// errOfferWithoutOwner — канал без владельца не попадает в заказ.
var errOfferWithoutOwner = errors.New("placement: channel has no owner")

// NewPlacement — размещение в created (P-01): суммы по 06 §3 (комиссия от цены блогера,
// округление вниз), снимок формата и окна, версия поста; таймер окна T-14 (R-11).
func NewPlacement(id, orderID uuid.UUID, offer ChannelOffer, price FormatPrice, w Window, versionID uuid.UUID,
	feeRateBP int, userID uuid.UUID, now time.Time, t Timing) (Placement, StateChange, error) {
	if offer.OwnerOrgID == nil {
		return Placement{}, StateChange{}, errOfferWithoutOwner
	}
	fee := price.Amount.ShareBP(feeRateBP)
	p := Placement{
		ID: id, OrderID: orderID, ChannelID: offer.Ref.ID, BloggerOrgID: *offer.OwnerOrgID,
		FormatID: price.Format.ID, TopHours: price.Format.TopHours, DurationHours: price.Format.DurationHours,
		WindowStart: w.Start, WindowEnd: w.End, TimeFrom: w.TimeFrom, TimeTo: w.TimeTo,
		PostVersionID: versionID, Status: StatusNone, FeeRateBP: feeRateBP,
		BloggerAmount: price.Amount, FeeAmount: fee, TotalAmount: price.Amount + fee,
		CreatedAt: now, UpdatedAt: now,
	}
	change, err := p.Apply(P01Create, ActorAdvertiser, "", now)
	if err != nil {
		return Placement{}, StateChange{}, err
	}
	change.UserID = &userID
	change.Jobs = []Job{TimerJob(id, TimerWindow, WindowDeadline(w, t), StatusCreated)}
	return p, change, nil
}

// OrderTotal — сумма total_amount размещений заказа.
func OrderTotal(ps []Placement) money.Kopecks {
	var sum money.Kopecks
	for _, p := range ps {
		sum += p.TotalAmount
	}
	return sum
}
