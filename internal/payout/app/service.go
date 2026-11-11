// Package app — сценарии модуля выплат (05 §4, S-11, S-14, S-15, S-19): онбординг
// исполнителя Рокет Ворк, проверка при принятии размещения, создание, отправка и опрос
// выплат, их исход (проводка payout и P-16 в одной транзакции), контроль депозита, повтор и
// выплата юрлицу вручную. Вызовы Рокет Ворк — только из обработчиков заданий вне транзакции
// (I-13).
package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// Виды заданий (07 §2).
const (
	KindEnsureContractor = "ensure_contractor"
	KindPollContractor   = "poll_contractor"
	KindSendPayout       = "send_payout"
	KindPollPayout       = "poll_payout"
	KindCheckDeposit     = "check_rocketwork_deposit"

	// SendAttempts — max_attempts send_payout (R-8).
	SendAttempts = 10
	// EnsureAttempts — max_attempts ensure_contractor.
	EnsureAttempts = 5
	// PollAttempts — max_attempts опросов: ожидание идёт через job.Postpone и попыток не тратит,
	// попытки расходуют только ошибки сервиса и базы.
	PollAttempts = 20
)

// Поля журнала (06 §11, 07 §5.3).
const (
	KeyPayoutID       = "payout_id"
	KeyPlacementID    = "placement_id"
	KeyContractorID   = "contractor_id"
	KeyExternalID     = "external_id"
	KeyAttempt        = "attempt"
	KeyAmount         = "amount"
	KeyIdempotencyKey = "idempotency_key"
	KeyFailureCode    = "failure_code"
)

// Причины оповещений (05 §4.3, R-7 … R-14).
const (
	AlertDepositInsufficient = "rocketwork_deposit_insufficient"
	AlertDepositLow          = "rocketwork_deposit_low"
	AlertDepositMismatch     = "rocketwork_deposit_mismatch"
	AlertPayoutFailed        = "payout_failed"
	AlertStateMismatch       = "payout_state_mismatch"
	AlertNPDLimit            = "npd_income_limit"
)

// Действия журнала действий.
const (
	ActionRetry      = "payout.retry"
	ActionMarkPaid   = "payout.mark_paid"
	ActionInvite     = "rocketwork.invite"
	ActionRefreshAdm = "rocketwork.refresh"
)

// PayoutPayload — полезная нагрузка send_payout и poll_payout.
type PayoutPayload struct {
	PayoutID uuid.UUID `json:"payout_id"`
}

// ContractorPayload — полезная нагрузка poll_contractor.
type ContractorPayload struct {
	ContractorID uuid.UUID `json:"contractor_id"`
}

// OrgPayload — полезная нагрузка ensure_contractor.
type OrgPayload struct {
	OrganizationID uuid.UUID `json:"organization_id"`
}

// Dedupe-ключи заданий.
func SendKey(payoutID uuid.UUID) string          { return KindSendPayout + ":" + payoutID.String() }
func PollPayoutKey(payoutID uuid.UUID) string    { return KindPollPayout + ":" + payoutID.String() }
func PollContractorKey(id uuid.UUID) string      { return KindPollContractor + ":" + id.String() }
func EnsureContractorKey(orgID uuid.UUID) string { return KindEnsureContractor + ":" + orgID.String() }
func payoutObject(payoutID uuid.UUID) string     { return "payout:" + payoutID.String() }
func organizationObject(orgID uuid.UUID) string  { return "organization:" + orgID.String() }

// Config — параметры модуля (07 §8).
type Config struct {
	WithdrawalDestination *domain.WithdrawalDestination
	AgreementTemplate     *domain.AgreementTerms
	// ProviderScope enables verified OpenAPI identity/workflow handling. Nil
	// retains the legacy fake port while bootstrap migrates its configuration.
	ProviderScope *domain.ProviderScope
	// ProviderEnabled — Рокет Ворк подключён (ROCKETWORK_MODE ≠ disabled, R-1).
	ProviderEnabled bool
	// PollFast, PollSlow — опрос: первый час и дальше (PAYOUT_POLL_FAST, PAYOUT_POLL_SLOW, R-10).
	PollFast, PollSlow time.Duration
	// ContractorPollMaxAge — не опрашивать invited/pending дольше (ROCKETWORK_CONTRACTOR_POLL_MAX_AGE).
	ContractorPollMaxAge time.Duration
	// ActiveRecheck — опрос active-исполнителя: лишение статуса до выплаты (R-10).
	ActiveRecheck time.Duration
	// StatusMaxAge — статус исполнителя старше — перед выплатой GetContractor (R-7).
	StatusMaxAge time.Duration
	// DepositRetry — повтор при нехватке депозита (05 §4.3: каждый час).
	DepositRetry time.Duration
	// DisabledRetry — откладывание заданий в режиме disabled.
	DisabledRetry time.Duration
	// DepositTolerance — допуск сверки депозита (ROCKETWORK_DEPOSIT_TOLERANCE, 06 §2.4).
	DepositTolerance money.Kopecks
	// NPDAnnualLimit — годовой лимит дохода самозанятого (PAYOUT_NPD_ANNUAL_LIMIT, 06 §4).
	NPDAnnualLimit money.Kopecks
	// StuckAcceptedAge, StuckSentAge, StuckCreatedAge — пороги проверок зависших (R-14).
	StuckAcceptedAge, StuckSentAge, StuckCreatedAge time.Duration
	// SecondApproval — сумма отметки выплаты юрлицу, выше которой нужен второй senior
	// (LEDGER_MANUAL_SECOND_APPROVAL_THRESHOLD, R-2); 0 — без порога.
	SecondApproval money.Kopecks
}

func (c *Config) defaults() {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&c.PollFast, 5*time.Minute)
	def(&c.PollSlow, time.Hour)
	def(&c.ContractorPollMaxAge, 30*24*time.Hour)
	def(&c.ActiveRecheck, 24*time.Hour)
	def(&c.StatusMaxAge, time.Hour)
	def(&c.DepositRetry, time.Hour)
	def(&c.DisabledRetry, time.Hour)
	def(&c.StuckAcceptedAge, 72*time.Hour)
	def(&c.StuckSentAge, 24*time.Hour)
	def(&c.StuckCreatedAge, 2*time.Hour)
	if c.DepositTolerance < 0 {
		c.DepositTolerance = 0
	}
	if c.NPDAnnualLimit == 0 {
		c.NPDAnnualLimit = 240000000
	}
}

// Alerter — оповещения администратору (07 §3).
type Alerter interface {
	Raise(ctx context.Context, a alert.Alert) error
	Resolve(ctx context.Context, reason, objectKey string) error
}

type nopAlerter struct{}

func (nopAlerter) Raise(context.Context, alert.Alert) error      { return nil }
func (nopAlerter) Resolve(context.Context, string, string) error { return nil }

// Observer — метрики модуля (07 §5.5).
type Observer interface {
	// PayoutFinished — попытка завершилась (succeeded, failed).
	PayoutFinished(status domain.Status, method domain.Method, amount money.Kopecks)
	// Deposit — остаток депозита: source api или ledger.
	Deposit(source string, v money.Kopecks)
}

// NopObserver — без метрик.
type NopObserver struct{}

func (NopObserver) PayoutFinished(domain.Status, domain.Method, money.Kopecks) {}
func (NopObserver) Deposit(string, money.Kopecks)                              {}

// NopNotifier — без уведомлений (сервис без модуля уведомлений, тесты).
type NopNotifier struct{ Log *slog.Logger }

func (n NopNotifier) Notify(ctx context.Context, x domain.Notice) error {
	if n.Log != nil {
		n.Log.DebugContext(ctx, "payout notice skipped", logx.KeyOp, "Payout.Notify", "template", x.Template,
			logx.KeyOrganizationID, x.OrganizationID)
	}
	return nil
}

// Deps — зависимости сервиса; связывает bootstrap.
type Deps struct {
	DiagnosticEvents interface {
		RouteDiagnostic(context.Context, string) (bool, error)
	}
	Store         domain.Store
	Provider      domain.Provider
	Ledger        domain.Ledger
	Placements    domain.Placements
	Organizations domain.Organizations
	Notifier      domain.Notifier
	Alerts        Alerter
	Metrics       Observer
	Tx            tx.Runner
	Jobs          job.Scheduler
	Audit         audit.Recorder
	// Limits — лимит частоты приглашений и проверок статуса; nil — без лимита.
	Limits Limits
	Clock  clock.Clock
	Log    *slog.Logger
	Config Config
}

// Service — сценарии модуля выплат.
type Service struct {
	Deps
	log *slog.Logger
}

// New создаёт сервис.
func New(d Deps) *Service {
	d.Config.defaults()
	log := d.Log.With(logx.KeyModule, "payout")
	if d.Alerts == nil {
		d.Alerts = nopAlerter{}
	}
	if d.Metrics == nil {
		d.Metrics = NopObserver{}
	}
	if d.Notifier == nil {
		d.Notifier = NopNotifier{Log: log}
	}
	return &Service{Deps: d, log: log}
}

// raise поднимает оповещение; ошибка записи только журналируется (Alerter сам пишет ERROR).
func (s *Service) raise(ctx context.Context, level, reason, objectKey, summary string, fields map[string]any) {
	_ = s.Alerts.Raise(ctx, alert.Alert{Reason: reason, ObjectKey: objectKey, Level: level, Summary: summary, Fields: fields})
}

func (s *Service) resolve(ctx context.Context, reason, objectKey string) {
	if err := s.Alerts.Resolve(ctx, reason, objectKey); err != nil {
		s.log.WarnContext(ctx, "alert resolve failed", logx.KeyOp, "Payout.Resolve", "reason", reason, logx.KeyError, err.Error())
	}
}

// notify — уведомление блогеру; сбой не мешает сценарию. У уведомления о выплате в данные
// добавляются размещение, номер заказа и канал (шаблоны payout.*, R-15).
func (s *Service) notify(ctx context.Context, n domain.Notice) {
	if n.PlacementID != uuid.Nil {
		data := map[string]any{"placement_id": n.PlacementID.String()}
		briefs, err := s.Placements.Brief(ctx, []uuid.UUID{n.PlacementID})
		if err != nil {
			s.log.WarnContext(ctx, "payout notice placement lookup failed", logx.KeyOp, "Payout.Notify", "template", n.Template,
				KeyPlacementID, n.PlacementID, logx.KeyError, err.Error())
		} else if b, ok := briefs[n.PlacementID]; ok {
			data["order_number"] = b.OrderNumber
			data["channel_id"] = b.ChannelID.String()
			if b.ChannelTitle != "" {
				data["channel_title"] = b.ChannelTitle
			}
		}
		for k, v := range n.Data {
			data[k] = v
		}
		n.Data = data
	}
	if err := s.Notifier.Notify(ctx, n); err != nil {
		s.log.WarnContext(ctx, "payout notice failed", logx.KeyOp, "Payout.Notify", "template", n.Template, logx.KeyError, err.Error())
	}
}

// schedule ставит задание на runAt или переносит ожидающее с тем же ключом на runAt.
func (s *Service) schedule(ctx context.Context, j job.Job) error {
	moved, err := s.Jobs.Reschedule(ctx, j.DedupeKey, j.RunAt)
	if err != nil || moved {
		return err
	}
	return s.Jobs.Enqueue(ctx, j)
}

// EventPayload — тело inbound_event Рокет Ворк: подпись уже проверена, формат сервиса разобран
// адаптером, хранится только идентификатор объекта (R-10).
type EventPayload struct {
	ScopeKey   string `json:"scope_key,omitempty"`
	ExternalID string `json:"external_id"`
	EventID    string `json:"event_id,omitempty"`
}
