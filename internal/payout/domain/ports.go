package domain

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// ContractorRequest — данные исполнителя для Рокет Ворк (06 §8: только ИНН, ФИО, телефон, тип лица).
type ContractorRequest struct {
	OrganizationID uuid.UUID
	LegalType      LegalType
	INN            string
	FullName       string
	Phone          string
}

// PayoutOrder — команда на выплату (05 §4.2, CreatePayout).
type PayoutOrder struct {
	IdempotencyKey     string
	PayoutID           uuid.UUID
	ContractorExternal string
	Amount             money.Kopecks
	// Purpose — назначение: номер заказа и канал.
	Purpose string
}

// WebhookKind — вид события Рокет Ворк (R-10): событие несёт только внешний идентификатор.
type WebhookKind string

const (
	WebhookContractorStatus WebhookKind = "contractor_status"
	WebhookPayoutStatus     WebhookKind = "payout_status"
)

// WebhookEvent — проверенное и разобранное событие вебхука.
type WebhookEvent struct {
	ScopeKey   string
	Kind       WebhookKind
	ExternalID string
	// EventID — идентификатор события сервиса; пусто — ключ из тела (inbound.KeyFromBody).
	EventID string
}

// Provider — Рокет Ворк за интерфейсом (R-1, 05 §4.2). Реализации — integration/rocketwork
// (Client, Disabled) и rocketworkfake через bootstrap/bridge. Вызывается только вне транзакции
// и не в обработчике запроса (I-13), кроме ParseWebhook.
//
// Ошибки: ErrProviderDisabled — режим disabled; ErrProviderUnavailable — временный сбой;
// ErrProviderNotFound — объекта нет; *RejectedError — окончательный отказ с нашим кодом.
type Provider interface {
	// EnsureContractor создаёт исполнителя или возвращает существующего (по ИНН).
	EnsureContractor(ctx context.Context, r ContractorRequest) (ProviderContractor, error)
	GetContractor(ctx context.Context, externalID string) (ProviderContractor, error)
	GetDepositBalance(ctx context.Context) (money.Kopecks, error)
	// CreatePayout идемпотентен по IdempotencyKey: повтор возвращает ту же выплату.
	CreatePayout(ctx context.Context, o PayoutOrder) (ProviderPayout, error)
	GetPayout(ctx context.Context, externalID string) (ProviderPayout, error)
	// FindPayout ищет выплату по нашему ключу после неизвестного исхода CreatePayout (R-6).
	FindPayout(ctx context.Context, idempotencyKey string) (ProviderPayout, bool, error)
	// ParseWebhook проверяет подлинность (ErrWebhookAuth) и разбирает тело (ErrWebhookFormat).
	ParseWebhook(header map[string][]string, body []byte) (WebhookEvent, error)
}

// Cursor — позиция курсора списка (created_at, id).
type Cursor struct {
	At time.Time
	ID uuid.UUID
}

// Summary — итоги выплат организации для blg/payouts.
type Summary struct {
	Received   money.Kopecks
	InProgress money.Kopecks
	Count      int
}

// AdminFilter — отбор реестра выплат для maxim admin payout list.
type AdminFilter struct {
	Status         Status
	OrganizationID uuid.UUID
	From, To       time.Time
	Limit          int
	Offset         int
}

// StatusTotal — число и сумма выплат в состоянии.
type StatusTotal struct {
	Status Status
	Count  int
	Amount money.Kopecks
}

// StuckItem — объект проверки зависших (07 §3).
type StuckItem struct {
	ObjectKey string
	Since     time.Time
}

// Store — исполнители и выплаты. Реализация — payout/postgres; методы работают в транзакции
// из context. ...ForUpdate блокируют строку.
type Store interface {
	ContractorByOrg(ctx context.Context, orgID uuid.UUID, forUpdate bool) (Contractor, error)
	ContractorByID(ctx context.Context, id uuid.UUID, forUpdate bool) (Contractor, error)
	ContractorByExternalID(ctx context.Context, externalID string, forUpdate bool) (Contractor, error)
	// InsertContractor вставляет строку; у организации уже есть — возвращает её и false.
	InsertContractor(ctx context.Context, c Contractor) (Contractor, bool, error)
	UpdateContractor(ctx context.Context, c Contractor) error
	ContractorCounts(ctx context.Context) (map[ContractorStatus]int, error)

	// InsertPayout — новая попытка; живая выплата размещения уже есть — ErrPayoutExists.
	InsertPayout(ctx context.Context, p Payout) error
	PayoutByID(ctx context.Context, id uuid.UUID, forUpdate bool) (Payout, error)
	PayoutByExternalID(ctx context.Context, externalID string, forUpdate bool) (Payout, error)
	// LatestPayout — последняя попытка размещения; нет — ErrNotFound.
	LatestPayout(ctx context.Context, placementID uuid.UUID, forUpdate bool) (Payout, error)
	// UpdatePayout сохраняет попытку при совпадении lock_version и увеличивает его;
	// иначе ErrConcurrentUpdate.
	UpdatePayout(ctx context.Context, p *Payout) error
	List(ctx context.Context, orgID uuid.UUID, after *Cursor, limit int) ([]Payout, error)
	Summary(ctx context.Context, orgID uuid.UUID) (Summary, error)
	// SucceededSum — сумма succeeded выплат организации с completed_at в [from, to).
	SucceededSum(ctx context.Context, orgID uuid.UUID, from, to time.Time) (money.Kopecks, error)
	AdminList(ctx context.Context, f AdminFilter) ([]Payout, error)
	// AdminPage — страница реестра со смещением f.Offset и общим числом (adm/payouts).
	AdminPage(ctx context.Context, f AdminFilter) ([]Payout, int, error)
	// AdminTotals — число и сумма выплат по состояниям.
	AdminTotals(ctx context.Context) ([]StatusTotal, error)
	// PlacementPayouts — попытки размещения, новые сверху.
	PlacementPayouts(ctx context.Context, placementID uuid.UUID) ([]Payout, error)

	// RocketworkPayable — остаток blogger_payable организаций с исполнителем Рокет Ворк (не юрлиц).
	RocketworkPayable(ctx context.Context) (money.Kopecks, error)
	// RocketworkInFlight — blogger_amount размещений в published и removed у таких организаций.
	RocketworkInFlight(ctx context.Context) (money.Kopecks, error)

	// AcceptedUnpaid — размещения в accepted со status_changed_at раньше before (T-12).
	AcceptedUnpaid(ctx context.Context, before time.Time) ([]StuckItem, error)
	// PayoutsInStatusBefore — попытки в status с updated_at раньше before; methods — пусто: все.
	PayoutsInStatusBefore(ctx context.Context, status Status, method Method, before time.Time) ([]StuckItem, error)
}

// PlacementInfo — размещение для выплаты.
type PlacementInfo struct {
	ID           uuid.UUID
	Status       string
	BloggerOrgID uuid.UUID
	PayoutAmount money.Kopecks
}

// PlacementBrief — заказ и канал размещения для списка выплат.
type PlacementBrief struct {
	OrderNumber  int64
	ChannelID    uuid.UUID
	ChannelTitle string
}

// PlacementStatusAccepted — размещение принято и ждёт выплаты (03 §3).
const PlacementStatusAccepted = "accepted"

// Placements — размещения. Реализация — placement через bootstrap/bridge.
type Placements interface {
	// MarkPaid — P-16 в транзакции вызывающего (R-5); не accepted — ErrPlacementState.
	MarkPaid(ctx context.Context, placementID, payoutID uuid.UUID) error
	Info(ctx context.Context, placementID uuid.UUID) (PlacementInfo, error)
	Brief(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]PlacementBrief, error)
}

// Ledger — проводки выплат (06 §2.3). Реализация — ledger через bootstrap/bridge;
// Payout и LegalPayout — в транзакции вызывающего.
type Ledger interface {
	// Payout — blogger_payable → rocketwork_deposit и комиссия payout_fees → rocketwork_deposit.
	Payout(ctx context.Context, p Payout) error
	// LegalPayout — юрлицу переводом: blogger_payable → bank (R-3).
	LegalPayout(ctx context.Context, p Payout, adminID uuid.UUID, reason string) error
	// DepositBalance — остаток rocketwork_deposit в журнале.
	DepositBalance(ctx context.Context) (money.Kopecks, error)
	// PayableBalance — остаток blogger_payable всех блогеров (сводка adm/payouts).
	PayableBalance(ctx context.Context) (money.Kopecks, error)
}

// Payee — получатель выплаты (06 §8).
type Payee struct {
	OrganizationID uuid.UUID
	LegalType      LegalType
	INN            string
	FullName       string
	Phone          string
}

// Organizations — данные организации-блогера. Реализация — account через bootstrap/bridge.
type Organizations interface {
	ForPayout(ctx context.Context, orgID uuid.UUID) (Payee, error)
}

// Notice — уведомление блогеру (R-15). PlacementID — размещение выплаты: заказ и канал для
// текста подставляет сервис; у статуса исполнителя пусто.
type Notice struct {
	Template       string
	OrganizationID uuid.UUID
	ObjectID       uuid.UUID
	PlacementID    uuid.UUID
	Data           map[string]any
}

// Шаблоны уведомлений (R-15).
const (
	NoticePayoutSent        = "payout.sent"
	NoticePayoutSucceeded   = "payout.succeeded"
	NoticePayoutFailed      = "payout.failed"
	NoticeRocketworkActive  = "rocketwork.active"
	NoticeRocketworkBlocked = "rocketwork.blocked"
)

// Notifier — уведомления. Реализация — модуль уведомлений через bootstrap/bridge.
type Notifier interface {
	Notify(ctx context.Context, n Notice) error
}
