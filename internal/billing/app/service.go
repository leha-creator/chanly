// Package app — сценарии модуля расчётов (05 §5, 06 §2.4–2.5): счета на пополнение баланса,
// загрузка выписки и зачисление поступлений, закрывающие документы. Деньги пишет только
// журнал проводок через порт Ledger; правила — в domain.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/storage"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// Поля журнала модуля (07 §5.1).
const (
	KeyInvoiceID         = "invoice_id"
	KeyBankStatementID   = "bank_statement_id"
	KeyBankTransactionID = "bank_transaction_id"
	KeyClosingActID      = "closing_act_id"
	KeyAmount            = "amount"
	KeyAdminID           = "admin_id"
)

// Виды заданий модуля (07 §2).
const (
	KindExpireInvoices      = "expire_invoices"
	KindGenerateClosingActs = "generate_closing_acts"
)

// Действия журнала действий (07 §4).
const (
	ActionInvoiceIssued       = "invoice.issued"
	ActionInvoiceCancelled    = "invoice.cancelled"
	ActionStatementImported   = "bank_statement.imported"
	ActionPaymentMatched      = "bank_transaction.matched"
	ActionPaymentIgnored      = "bank_transaction.ignored"
	ActionClosingActGenerated = "closing_act.generated"
)

const (
	defaultPageLimit = 20
	maxPageLimit     = 100
	// expireBatch — счетов за одну транзакцию истечения.
	expireBatch = 100
)

// Limits — счётчики действий для лимитов частоты, общие для всех экземпляров api (07 §4).
type Limits interface {
	CountByUserSince(ctx context.Context, action string, userID uuid.UUID, since time.Time) (int, error)
	CountByIPSince(ctx context.Context, action string, ip netip.Addr, since time.Time) (int, error)
}

// Alerter — оповещения администратору (07 §3).
type Alerter interface {
	Raise(ctx context.Context, a alert.Alert) error
	Resolve(ctx context.Context, reason, objectKey string) error
}

// NopNotifier — уведомления до модуля уведомлений (R-17).
type NopNotifier struct{ Log *slog.Logger }

func (n NopNotifier) Notify(ctx context.Context, notice domain.Notice) error {
	n.Log.DebugContext(ctx, "notification skipped", logx.KeyOp, "Billing.Notify", "template", notice.Template,
		"object_id", notice.ObjectID)
	return nil
}

// Config — параметры модуля.
type Config struct {
	// InvoiceTTL — срок действия счёта (14 суток).
	InvoiceTTL time.Duration
	// InvoicesPerUserHour, InvoicesPerIPHour — лимиты выставления (R-11).
	InvoicesPerUserHour int
	InvoicesPerIPHour   int
	// Supplier — реквизиты платформы для документов (R-10); его расчётный счёт — счёт выписки (R-12).
	Supplier domain.Supplier
	// VATNote — строка НДС в счёте и акте (Q-32).
	VATNote string
}

// Deps — зависимости сервиса; связывает bootstrap.
type Deps struct {
	BankSource       domain.BankSource
	BankSync         domain.BankSyncStore
	BankIdentity     domain.BankIdentity
	BankConfig       BankConfig
	BankBudget       domain.BankCreditBudget
	Invoices         domain.Invoices
	Statements       domain.Statements
	BankTransactions domain.BankTransactions
	ClosingActs      domain.ClosingActs
	Ledger           domain.Ledger
	Organizations    domain.Organizations
	Placements       domain.Placements
	Renderer         domain.Renderer
	Parser           domain.StatementParser
	Storage          storage.ObjectStorage
	Notifier         domain.Notifier
	Alerts           Alerter
	Limits           Limits
	Tx               tx.Runner
	Audit            audit.Recorder
	Clock            clock.Clock
	Log              *slog.Logger
	Config           Config
}

// Service — модуль расчётов.
type Service struct {
	Deps
	log *slog.Logger
}

// New создаёт сервис.
func New(d Deps) *Service {
	if d.Notifier == nil {
		d.Notifier = NopNotifier{Log: d.Log}
	}
	return &Service{Deps: d, log: d.Log.With(logx.KeyModule, "billing")}
}

// pageLimit приводит размер страницы к допустимому.
func pageLimit(n int) int {
	switch {
	case n <= 0:
		return defaultPageLimit
	case n > maxPageLimit:
		return maxPageLimit
	}
	return n
}

// cursor разбирает курсор страницы; испорченный — 422.
func cursor(s string) (*domain.Cursor, error) {
	c, err := domain.DecodeCursor(s)
	if err != nil {
		return nil, apperr.Validation("cursor", "invalid")
	}
	return c, nil
}

func notFound(err error, what string) error {
	if errors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound(what + " not found")
	}
	return err
}

// raise поднимает оповещение; ошибка оповещения не отменяет сценарий — она в журнале.
func (s *Service) raise(ctx context.Context, op string, a alert.Alert) {
	if s.Alerts == nil {
		return
	}
	if err := s.Alerts.Raise(ctx, a); err != nil {
		s.log.ErrorContext(ctx, "alert raise failed", logx.KeyOp, op, "reason", a.Reason, logx.KeyError, err.Error())
	}
}

func ptr[T any](v T) *T { return &v }
