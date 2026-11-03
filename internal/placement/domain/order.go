package domain

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// Order — заказ: оформленная корзина с одним постом (03 §1, 04 §5). Собственных
// переходов нет: состояние производное (DeriveOrderStatus).
type Order struct {
	ID                   uuid.UUID
	Number               int64
	OrganizationID       uuid.UUID
	CampaignID           uuid.UUID
	PostID               uuid.UUID
	CurrentPostVersionID uuid.UUID
	Status               OrderStatus
	TotalAmount          money.Kopecks
	// IdempotencyKey — ключ оформления (R-18); пусто — без ключа.
	IdempotencyKey string
	// VersionIdempotencyKey — ключ последней правки поста после оформления (R-18, S-29).
	VersionIdempotencyKey string
	CreatedBy             uuid.UUID
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// DeriveOrderStatus — состояние заказа по правилу 03 §1.2: все отменены → cancelled;
// все в конечных → completed; иначе по состоянию текущей версии поста.
func DeriveOrderStatus(statuses []Status, version VersionStatus) OrderStatus {
	if len(statuses) > 0 {
		allCancelled, allTerminal := true, true
		for _, s := range statuses {
			allCancelled = allCancelled && s == StatusCancelled
			allTerminal = allTerminal && s.Terminal()
		}
		switch {
		case allCancelled:
			return OrderCancelled
		case allTerminal:
			return OrderCompleted
		}
	}
	switch version {
	case VersionRejected:
		return OrderRejected
	case VersionApproved:
		return OrderMarking
	case VersionReady:
		return OrderInProgress
	}
	return OrderModeration
}

// Действия в ответах кабинетов (R-22).
const (
	ActionCancel     = "cancel"
	ActionAccept     = "accept"
	ActionDecline    = "decline"
	ActionSchedule   = "schedule"
	ActionReschedule = "reschedule"
	// ActionClaim — подать претензию (S-08).
	ActionClaim = "claim"
)

// AdvertiserActions — действия рекламодателя, допустимые сейчас (R-22, R-15 этапа приёмки): cancel —
// из created и awaiting_blogger; accept — из removed; claim — из published и из removed до
// claim_deadline_at, если претензии ещё нет.
func AdvertiserActions(p *Placement, hasClaim bool, now time.Time) []string {
	out := []string{}
	if Allowed(P06Cancel, ActorAdvertiser, p.Status) {
		out = append(out, ActionCancel)
	}
	if Allowed(P12AcceptByAdvertiser, ActorAdvertiser, p.Status) {
		out = append(out, ActionAccept)
	}
	if !hasClaim && ClaimWindowOpen(p, now) {
		out = append(out, ActionClaim)
	}
	return out
}

// BloggerActions — действия блогера, допустимые сейчас (R-22).
func BloggerActions(p *Placement, now time.Time, t Timing) []string {
	switch p.Status {
	case StatusAwaitingBlogger:
		return []string{ActionAccept, ActionDecline}
	case StatusAwaitingSchedule:
		return []string{ActionSchedule}
	case StatusScheduled:
		if p.CanReschedule(now, t) {
			return []string{ActionReschedule}
		}
	}
	return []string{}
}

// HistoryEntry — запись истории состояний размещения (03 §12). Неизменяема.
type HistoryEntry struct {
	ID          uuid.UUID
	PlacementID uuid.UUID
	Transition  TransitionCode
	From        Status
	To          Status
	ActorType   ActorType
	ActorUserID *uuid.UUID
	ReasonCode  string
	ReasonText  string
	JobID       *uuid.UUID
	// InboundEventID — входящее событие, вызвавшее переход (03 §12).
	InboundEventID *uuid.UUID
	// IdempotencyKey — ключ действия пользователя (R-18); пусто — без ключа.
	IdempotencyKey string
	CreatedAt      time.Time
}

// NewHistoryEntry — запись истории по результату перехода.
func NewHistoryEntry(id uuid.UUID, c StateChange, jobID *uuid.UUID, idemKey string) HistoryEntry {
	return HistoryEntry{
		ID: id, PlacementID: c.PlacementID, Transition: c.Code, From: c.From, To: c.To, ActorType: c.Actor,
		ActorUserID: c.UserID, ReasonCode: c.Reason, ReasonText: c.ReasonText, JobID: jobID,
		IdempotencyKey: idemKey, CreatedAt: c.At,
	}
}

// PageCursor — ключевой курсор списков «новые сверху»: (created_at, id) последней строки.
type PageCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// ErrInvalidCursor — курсор испорчен или от другого списка.
var ErrInvalidCursor = &RuleError{Field: "cursor", Reason: "invalid"}

type cursorJSON struct {
	T time.Time `json:"t"`
	I uuid.UUID `json:"i"`
}

// Encode — непрозрачная строка курсора для next_cursor.
func (c PageCursor) Encode() string {
	raw, _ := json.Marshal(cursorJSON{T: c.CreatedAt, I: c.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodePageCursor разбирает курсор; пустая строка — первая страница.
func DecodePageCursor(s string) (*PageCursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	var c cursorJSON
	if err := json.Unmarshal(raw, &c); err != nil || c.I == uuid.Nil || c.T.IsZero() {
		return nil, errors.Join(ErrInvalidCursor, err)
	}
	return &PageCursor{CreatedAt: c.T, ID: c.I}, nil
}
