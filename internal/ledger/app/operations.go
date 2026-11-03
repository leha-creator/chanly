package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Вид операции с точки зрения рекламодателя (R-9, BalanceTransaction.kind).
const (
	OperationDeposit       = "deposit"
	OperationHold          = "hold"
	OperationHoldRelease   = "hold_release"
	OperationRefund        = "refund"
	OperationBalanceReturn = "balance_return"
	OperationManual        = "manual"
)

const (
	defaultPageLimit = 20
	maxPageLimit     = 100
)

// OrderRef — заказ размещения для описания операции.
type OrderRef struct {
	OrderID     uuid.UUID
	OrderNumber int64
}

// References — номера заказов и счетов для описаний операций. Реализация — переходник
// в bootstrap/bridge поверх модулей placement и billing.
type References interface {
	Orders(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID]OrderRef, error)
	InvoiceNumbers(ctx context.Context, invoiceIDs []uuid.UUID) (map[uuid.UUID]string, error)
}

// Operation — движение свободного баланса рекламодателя: кредит — «+», дебет — «−».
type Operation struct {
	ID          uuid.UUID
	Kind        string
	Amount      money.Kopecks
	Description string
	OrderID     uuid.UUID
	PlacementID uuid.UUID
	InvoiceID   uuid.UUID
	CreatedAt   time.Time
}

// OperationPage — страница операций; NextCursor пуст на последней странице.
type OperationPage struct {
	Items      []Operation
	NextCursor string
}

type cursorJSON struct {
	T time.Time `json:"t"`
	I uuid.UUID `json:"i"`
}

func encodeCursor(c domain.EntryCursor) string {
	raw, _ := json.Marshal(cursorJSON{T: c.At, I: c.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s string) (*domain.EntryCursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	var c cursorJSON
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	if err != nil || c.I == uuid.Nil || c.T.IsZero() {
		return nil, apperr.Validation("cursor", "invalid")
	}
	return &domain.EntryCursor{At: c.T, ID: c.I}, nil
}

func pageLimit(n int) int {
	switch {
	case n <= 0:
		return defaultPageLimit
	case n > maxPageLimit:
		return maxPageLimit
	}
	return n
}

// operationKind — вид для кабинета: возврат из распределения — refund (R-9).
func operationKind(k domain.Kind) (string, error) {
	switch k {
	case domain.KindDeposit:
		return OperationDeposit, nil
	case domain.KindHold:
		return OperationHold, nil
	case domain.KindHoldRelease:
		return OperationHoldRelease, nil
	case domain.KindSettlement:
		return OperationRefund, nil
	case domain.KindBalanceReturn:
		return OperationBalanceReturn, nil
	case domain.KindManual:
		return OperationManual, nil
	}
	return "", fmt.Errorf("%w: %s touches advertiser balance", domain.ErrInvariant, k)
}

func describe(kind string, order OrderRef, invoice string) string {
	switch kind {
	case OperationDeposit:
		if invoice != "" {
			return "Пополнение по счёту " + invoice
		}
		return "Пополнение баланса"
	case OperationHold:
		return fmt.Sprintf("Удержание по заказу № %d", order.OrderNumber)
	case OperationHoldRelease:
		return fmt.Sprintf("Снятие удержания по заказу № %d", order.OrderNumber)
	case OperationRefund:
		return fmt.Sprintf("Возврат по заказу № %d", order.OrderNumber)
	case OperationBalanceReturn:
		return "Возврат остатка баланса"
	default:
		return "Корректировка баланса"
	}
}

// Operations — движения свободного баланса организации, новые сверху (R-9).
func (s *Service) Operations(ctx context.Context, orgID uuid.UUID, cursorStr string, limit int) (OperationPage, error) {
	const op = "Ledger.Operations"
	after, err := decodeCursor(cursorStr)
	if err != nil {
		return OperationPage{}, err
	}
	limit = pageLimit(limit)
	entries, err := s.Reports.Entries(ctx, domain.AdvertiserBalance(orgID), after, limit+1)
	if err != nil {
		return OperationPage{}, err
	}
	page := OperationPage{}
	if len(entries) > limit {
		entries = entries[:limit]
		last := entries[limit-1]
		page.NextCursor = encodeCursor(domain.EntryCursor{At: last.CreatedAt, ID: last.ID})
	}
	var placements, invoices []uuid.UUID
	for _, e := range entries {
		if e.PlacementID != uuid.Nil {
			placements = append(placements, e.PlacementID)
		}
		if e.InvoiceID != uuid.Nil {
			invoices = append(invoices, e.InvoiceID)
		}
	}
	orders, numbers := map[uuid.UUID]OrderRef{}, map[uuid.UUID]string{}
	if s.References != nil {
		if orders, err = s.References.Orders(ctx, placements); err != nil {
			return OperationPage{}, err
		}
		if numbers, err = s.References.InvoiceNumbers(ctx, invoices); err != nil {
			return OperationPage{}, err
		}
	}
	page.Items = make([]Operation, 0, len(entries))
	for _, e := range entries {
		kind, err := operationKind(e.Kind)
		if err != nil {
			s.log.ErrorContext(ctx, "ledger invariant violated", logx.KeyOp, op, KeyTransactionID, e.TransactionID,
				logx.KeyError, err.Error())
			return OperationPage{}, err
		}
		amount := e.Amount
		if !e.Credit {
			amount = -amount
		}
		order := orders[e.PlacementID]
		page.Items = append(page.Items, Operation{
			ID: e.ID, Kind: kind, Amount: amount, Description: describe(kind, order, numbers[e.InvoiceID]),
			OrderID: order.OrderID, PlacementID: e.PlacementID, InvoiceID: e.InvoiceID, CreatedAt: e.CreatedAt,
		})
	}
	s.log.DebugContext(ctx, "balance read", logx.KeyOp, op, logx.KeyOrganizationID, orgID, "operations", len(page.Items))
	return page, nil
}
