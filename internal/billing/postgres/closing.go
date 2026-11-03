package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/billing/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

type closingActs struct{ s *Store }

var _ domain.ClosingActs = closingActs{}

func closingActFromRow(r sqlc.ClosingAct) domain.ClosingAct {
	return domain.ClosingAct{
		ID: r.ID, OrganizationID: r.OrganizationID, Period: domain.PeriodFromDate(r.Period.Time), Number: r.Number,
		PlacementsAmount: money.Kopecks(r.PlacementsAmount), FeeAmount: money.Kopecks(r.FeeAmount),
		ReportKey: r.ReportPdfStorageKey, ActKey: r.ActPdfStorageKey, CreatedAt: r.CreatedAt.UTC(),
	}
}

func (r closingActs) NextNumber(ctx context.Context) (int64, error) {
	n, err := r.s.q(ctx).NextClosingActNumber(ctx)
	if err != nil {
		return 0, r.s.fail(ctx, "ClosingAct.NextNumber", err)
	}
	return n, nil
}

func (r closingActs) Insert(ctx context.Context, a domain.ClosingAct) error {
	if err := r.s.q(ctx).InsertClosingAct(ctx, sqlc.InsertClosingActParams{
		ID: a.ID, OrganizationID: a.OrganizationID, Period: pgDate(a.Period.Date()), Number: a.Number,
		PlacementsAmount: int64(a.PlacementsAmount), FeeAmount: int64(a.FeeAmount),
		ReportPdfStorageKey: a.ReportKey, ActPdfStorageKey: a.ActKey, CreatedAt: a.CreatedAt,
	}); err != nil {
		return r.s.fail(ctx, "ClosingAct.Insert", err)
	}
	r.s.log.DebugContext(ctx, "closing act saved", logx.KeyOp, "ClosingAct.Insert", "closing_act_id", a.ID)
	return nil
}

func (r closingActs) Exists(ctx context.Context, orgID uuid.UUID, p domain.Period) (bool, error) {
	found, err := r.s.q(ctx).ClosingActExists(ctx, sqlc.ClosingActExistsParams{OrganizationID: orgID, Period: pgDate(p.Date())})
	if err != nil {
		return false, r.s.fail(ctx, "ClosingAct.Exists", err)
	}
	return found, nil
}

// List — документы организации; курсор — первое число периода (At) и id.
func (r closingActs) List(ctx context.Context, orgID uuid.UUID, after *domain.Cursor, limit int) ([]domain.ClosingAct, error) {
	params := sqlc.ListClosingActsParams{OrganizationID: orgID, Lim: int32(limit)}
	if after != nil {
		params.HasCursor, params.CursorPeriod, params.CursorID = true, pgDate(after.At), after.ID
	}
	rows, err := r.s.q(ctx).ListClosingActs(ctx, params)
	if err != nil {
		return nil, r.s.fail(ctx, "ClosingAct.List", err)
	}
	out := make([]domain.ClosingAct, 0, len(rows))
	for _, row := range rows {
		out = append(out, closingActFromRow(row))
	}
	return out, nil
}

func (r closingActs) Get(ctx context.Context, orgID, id uuid.UUID) (domain.ClosingAct, error) {
	row, err := r.s.q(ctx).GetClosingAct(ctx, sqlc.GetClosingActParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return domain.ClosingAct{}, r.s.fail(ctx, "ClosingAct.Get", err)
	}
	return closingActFromRow(row), nil
}
