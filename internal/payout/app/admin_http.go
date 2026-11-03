package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Реестр выплат через HTTP (этап «API администратора», R-2, R-14).

// ActionExported — выгрузка реестра в CSV (R-14).
const ActionExported = "payout.exported"

// ExportLimit — строк в выгрузке CSV не больше (exportAdminPayouts).
const ExportLimit = 10_000

// NeedsApproval — отметка выплаты на сумму amount требует второго senior (R-2).
func (s *Service) NeedsApproval(amount money.Kopecks) bool {
	return s.Config.SecondApproval > 0 && amount > s.Config.SecondApproval
}

// MarkPaidPayload — описание отметки выплаты в запросе подтверждения.
type MarkPaidPayload struct {
	PayoutID      uuid.UUID `json:"payout_id"`
	BankReference string    `json:"bank_reference"`
	Reason        string    `json:"reason"`
}

// AdminPage — страница реестра с заказом и каналом и общее число строк.
func (s *Service) AdminPage(ctx context.Context, f domain.AdminFilter) ([]PayoutItem, int, error) {
	rows, total, err := s.Store.AdminPage(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	items, err := s.withBriefs(ctx, rows)
	return items, total, err
}

// PlacementPayouts — попытки размещения для карточки администратора, новые сверху; retryable —
// последнюю можно повторить (failed, размещение в accepted).
func (s *Service) PlacementPayouts(ctx context.Context, placementID uuid.UUID) (items []PayoutItem, retryable bool, err error) {
	rows, err := s.Store.PlacementPayouts(ctx, placementID)
	if err != nil {
		return nil, false, err
	}
	if items, err = s.withBriefs(ctx, rows); err != nil {
		return nil, false, err
	}
	if len(rows) > 0 && rows[0].Status == domain.StatusFailed {
		info, err := s.Placements.Info(ctx, placementID)
		if err != nil {
			return nil, false, err
		}
		retryable = info.Status == domain.PlacementStatusAccepted
	}
	return items, retryable, nil
}

func (s *Service) withBriefs(ctx context.Context, rows []domain.Payout) ([]PayoutItem, error) {
	ids := make([]uuid.UUID, 0, len(rows))
	for _, p := range rows {
		ids = append(ids, p.PlacementID)
	}
	briefs, err := s.Placements.Brief(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("payout admin briefs: %w", err)
	}
	out := make([]PayoutItem, 0, len(rows))
	for _, p := range rows {
		out = append(out, PayoutItem{Payout: p, Brief: briefs[p.PlacementID]})
	}
	return out, nil
}

// AdminSummary — сводка adm/payouts: по состояниям и депозит по журналу (без внешних вызовов).
type AdminSummary struct {
	ByStatus      []domain.StatusTotal
	DepositLedger money.Kopecks
	Payable       money.Kopecks
	InProgress    money.Kopecks
	Shortage      money.Kopecks
}

// AdminSummary собирает сводку реестра выплат.
func (s *Service) AdminSummary(ctx context.Context) (AdminSummary, error) {
	var out AdminSummary
	var err error
	if out.ByStatus, err = s.Store.AdminTotals(ctx); err != nil {
		return AdminSummary{}, err
	}
	for _, t := range out.ByStatus {
		if t.Status == domain.StatusCreated || t.Status == domain.StatusSent {
			out.InProgress += t.Amount
		}
	}
	if out.DepositLedger, err = s.Ledger.DepositBalance(ctx); err != nil {
		return AdminSummary{}, err
	}
	if out.Payable, err = s.Ledger.PayableBalance(ctx); err != nil {
		return AdminSummary{}, err
	}
	rwPayable, err := s.Store.RocketworkPayable(ctx)
	if err != nil {
		return AdminSummary{}, err
	}
	inFlight, err := s.Store.RocketworkInFlight(ctx)
	if err != nil {
		return AdminSummary{}, err
	}
	out.Shortage = max(domain.DepositNeed(rwPayable, inFlight)-out.DepositLedger, 0)
	return out, nil
}

// ExportRows — строки выгрузки реестра в CSV (до ExportLimit); выгрузка пишется в журнал
// действий (R-14). Формирует файл слой HTTP.
func (s *Service) ExportRows(ctx context.Context, f domain.AdminFilter, adminID uuid.UUID) ([]PayoutItem, error) {
	const op = "Payout.Export"
	f.Limit, f.Offset = ExportLimit, 0
	rows, err := s.Store.AdminList(ctx, f)
	if err != nil {
		return nil, err
	}
	items, err := s.withBriefs(ctx, rows)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"count": len(items), "status": string(f.Status)}
	if f.OrganizationID != uuid.Nil {
		data["organization_id"] = f.OrganizationID.String()
	}
	if !f.From.IsZero() {
		data["from"] = f.From.Format("2006-01-02")
	}
	if !f.To.IsZero() {
		data["to"] = f.To.Format("2006-01-02")
	}
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		return s.Audit.Record(ctx, audit.Entry{Action: ActionExported, ObjectType: "payout", ObjectID: "registry",
			ActorType: audit.ActorAdmin, Data: data})
	})
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "payouts exported", logx.KeyOp, op, "count", len(items), "admin_user_id", adminID)
	return items, nil
}

// PlacementPayoutItem — одна попытка с заказом и каналом (ответ повтора и отметки).
func (s *Service) PlacementPayoutItem(ctx context.Context, p domain.Payout) ([]PayoutItem, error) {
	return s.withBriefs(ctx, []domain.Payout{p})
}
