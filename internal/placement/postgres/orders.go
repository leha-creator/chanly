package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/placement/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Orders — заказы.
type Orders struct{ s *Store }

func toOrder(r sqlc.Order) domain.Order {
	return domain.Order{
		ID: r.ID, Number: r.Number, OrganizationID: r.OrganizationID, CampaignID: r.CampaignID, PostID: r.PostID,
		CurrentPostVersionID: r.CurrentPostVersionID, Status: domain.OrderStatus(r.Status),
		TotalAmount: money.Kopecks(r.TotalAmount), IdempotencyKey: str(r.IdempotencyKey),
		VersionIdempotencyKey: str(r.PostVersionIdempotencyKey), CreatedBy: r.CreatedBy,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// found переводит «нет строк» поиска в false без ошибки.
func found(err error) (bool, error) {
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (r *Orders) NextNumber(ctx context.Context) (int64, error) {
	n, err := r.s.q(ctx).NextOrderNumber(ctx)
	if err != nil {
		return 0, r.s.fail(ctx, "Placement.Orders.NextNumber", err)
	}
	return n, nil
}

func (r *Orders) Insert(ctx context.Context, o domain.Order) error {
	const op = "Placement.Orders.Insert"
	if err := r.s.q(ctx).InsertOrder(ctx, sqlc.InsertOrderParams{
		ID: o.ID, Number: o.Number, OrganizationID: o.OrganizationID, CampaignID: o.CampaignID, PostID: o.PostID,
		CurrentPostVersionID: o.CurrentPostVersionID, Status: string(o.Status), TotalAmount: int64(o.TotalAmount),
		IdempotencyKey: text(o.IdempotencyKey), CreatedBy: o.CreatedBy, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}); err != nil {
		return r.s.fail(ctx, op, err, "order_id", o.ID)
	}
	r.s.debug(ctx, op, "order_id", o.ID, "order_number", o.Number)
	return nil
}

func (r *Orders) Get(ctx context.Context, orgID, id uuid.UUID) (domain.Order, error) {
	row, err := r.s.q(ctx).GetOrder(ctx, sqlc.GetOrderParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return domain.Order{}, r.s.fail(ctx, "Placement.Orders.Get", err)
	}
	return toOrder(row), nil
}

func (r *Orders) GetForUpdate(ctx context.Context, orgID, id uuid.UUID) (domain.Order, error) {
	row, err := r.s.q(ctx).GetOrderForUpdate(ctx, sqlc.GetOrderForUpdateParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return domain.Order{}, r.s.fail(ctx, "Placement.Orders.GetForUpdate", err)
	}
	return toOrder(row), nil
}

func (r *Orders) GetByID(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	row, err := r.s.q(ctx).GetOrderByID(ctx, id)
	if err != nil {
		return domain.Order{}, r.s.fail(ctx, "Placement.Orders.GetByID", err)
	}
	return toOrder(row), nil
}

// GetByNumber — заказ по номеру без проверки организации: только для команды разработчика (R-23).
func (r *Orders) GetByNumber(ctx context.Context, number int64) (domain.Order, error) {
	row, err := r.s.q(ctx).GetOrderByNumber(ctx, number)
	if err != nil {
		return domain.Order{}, r.s.fail(ctx, "Placement.Orders.GetByNumber", err)
	}
	return toOrder(row), nil
}

func (r *Orders) GetByIDForUpdate(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	row, err := r.s.q(ctx).GetOrderByIDForUpdate(ctx, id)
	if err != nil {
		return domain.Order{}, r.s.fail(ctx, "Placement.Orders.GetByIDForUpdate", err)
	}
	return toOrder(row), nil
}

func (r *Orders) List(ctx context.Context, orgID uuid.UUID, status *domain.OrderStatus, after *domain.PageCursor, limit int) ([]domain.Order, error) {
	const op = "Placement.Orders.List"
	var st *string
	if status != nil {
		v := string(*status)
		st = &v
	}
	at, aid := cursorArgs(after)
	rows, err := r.s.q(ctx).ListOrders(ctx, sqlc.ListOrdersParams{
		OrganizationID: orgID, Status: st, AfterCreatedAt: at, AfterID: aid, Lim: int32(limit),
	})
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "count", len(rows))
	out := make([]domain.Order, 0, len(rows))
	for _, row := range rows {
		out = append(out, toOrder(row))
	}
	return out, nil
}

func (r *Orders) FindByIdempotencyKey(ctx context.Context, orgID uuid.UUID, key string) (domain.Order, bool, error) {
	row, err := r.s.q(ctx).FindOrderByIdempotencyKey(ctx, sqlc.FindOrderByIdempotencyKeyParams{
		OrganizationID: orgID, IdempotencyKey: &key,
	})
	ok, err := found(r.wrap(ctx, "Placement.Orders.FindByIdempotencyKey", err))
	if !ok {
		return domain.Order{}, false, err
	}
	return toOrder(row), true, nil
}

func (r *Orders) FindByCurrentVersion(ctx context.Context, versionID uuid.UUID) (domain.Order, bool, error) {
	row, err := r.s.q(ctx).FindOrderByCurrentVersion(ctx, versionID)
	ok, err := found(r.wrap(ctx, "Placement.Orders.FindByCurrentVersion", err))
	if !ok {
		return domain.Order{}, false, err
	}
	return toOrder(row), true, nil
}

func (r *Orders) Update(ctx context.Context, o domain.Order) error {
	const op = "Placement.Orders.Update"
	if err := r.s.q(ctx).UpdateOrder(ctx, sqlc.UpdateOrderParams{
		ID: o.ID, Status: string(o.Status), CurrentPostVersionID: o.CurrentPostVersionID,
		PostVersionIdempotencyKey: text(o.VersionIdempotencyKey), UpdatedAt: o.UpdatedAt,
	}); err != nil {
		return r.s.fail(ctx, op, err, "order_id", o.ID)
	}
	r.s.debug(ctx, op, "order_id", o.ID, "status", o.Status)
	return nil
}

func (r *Orders) wrap(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}
	return r.s.fail(ctx, op, err)
}

func (r *Orders) FindByPlacementVersion(ctx context.Context, versionID uuid.UUID) (domain.Order, bool, error) {
	row, err := r.s.q(ctx).FindOrderByPlacementVersion(ctx, versionID)
	ok, err := found(r.wrap(ctx, "Placement.Orders.FindByPlacementVersion", err))
	if !ok {
		return domain.Order{}, false, err
	}
	return toOrder(row), true, nil
}
