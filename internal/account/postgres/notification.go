package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
)

// MemberIDs — активные пользователи организации, владелец первым (адресаты уведомлений, R-1
// этапа «Уведомления и отчёт по кампании»).
func (r *Users) MemberIDs(ctx context.Context, orgID uuid.UUID) ([]uuid.UUID, error) {
	const op = "Account.Users.MemberIDs"
	ids, err := r.s.q(ctx).ListOrganizationMemberIDs(ctx, orgID)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "organization_id", orgID, "count", len(ids))
	return ids, nil
}

// AdminIDs — активные администраторы.
func (r *Users) AdminIDs(ctx context.Context) ([]uuid.UUID, error) {
	const op = "Account.Users.AdminIDs"
	ids, err := r.s.q(ctx).ListAdminUserIDs(ctx)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	return ids, nil
}

// ActiveEmail — почта активного пользователя; заблокирован или нет — пусто.
func (r *Users) ActiveEmail(ctx context.Context, id uuid.UUID) (string, error) {
	u, err := r.GetByID(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if u.Blocked() {
		return "", nil
	}
	return u.Email, nil
}
