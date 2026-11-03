package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/account/postgres/sqlc"
)

// Users — пользователи.
type Users struct{ s *Store }

func (r *Users) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	const op = "Account.Users.GetByEmail"
	r.s.debug(ctx, op)
	row, err := r.s.q(ctx).GetUserByEmail(ctx, domain.NormalizeEmail(email))
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	return userFromRow(row), nil
}

func (r *Users) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	const op = "Account.Users.GetByID"
	r.s.debug(ctx, op, "user_id", id)
	row, err := r.s.q(ctx).GetUserByID(ctx, id)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	return userFromRow(row), nil
}

func (r *Users) Create(ctx context.Context, u *domain.User) error {
	const op = "Account.Users.Create"
	r.s.debug(ctx, op, "user_id", u.ID)
	err := r.s.q(ctx).InsertUser(ctx, sqlc.InsertUserParams{
		ID: u.ID, Email: u.Email, AdminRole: strPtr(u.AdminRole), Status: string(u.Status),
		LastLoginAt: u.LastLoginAt, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	})
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

func (r *Users) SetLastLogin(ctx context.Context, id uuid.UUID, at time.Time) error {
	const op = "Account.Users.SetLastLogin"
	r.s.debug(ctx, op, "user_id", id)
	if err := r.s.q(ctx).SetUserLastLogin(ctx, sqlc.SetUserLastLoginParams{ID: id, At: at}); err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

func userFromRow(row sqlc.User) *domain.User {
	return &domain.User{
		ID: row.ID, Email: row.Email, AdminRole: enumPtr[domain.AdminRole](row.AdminRole),
		Status: domain.Status(row.Status), LastLoginAt: row.LastLoginAt, BlockedAt: row.BlockedAt, BlockedReason: row.BlockedReason,
		AnonymizedAt: row.AnonymizedAt,
		CreatedAt:    row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func (r *Users) SetAdminRole(ctx context.Context, id uuid.UUID, role *domain.AdminRole, at time.Time) error {
	const op = "Account.Users.SetAdminRole"
	r.s.debug(ctx, op, "user_id", id, "admin_role", strPtr(role))
	if err := r.s.q(ctx).SetUserAdminRole(ctx, sqlc.SetUserAdminRoleParams{ID: id, AdminRole: strPtr(role), At: at}); err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

func (r *Users) GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	const op = "Account.Users.GetForUpdate"
	r.s.debug(ctx, op, "user_id", id)
	row, err := r.s.q(ctx).GetUserForUpdate(ctx, id)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	return userFromRow(row), nil
}

func (r *Users) SetBlocked(ctx context.Context, u *domain.User) error {
	const op = "Account.Users.SetBlocked"
	r.s.debug(ctx, op, "user_id", u.ID, "status", u.Status)
	err := r.s.q(ctx).SetUserBlocked(ctx, sqlc.SetUserBlockedParams{
		ID: u.ID, Status: string(u.Status), BlockedAt: u.BlockedAt, BlockedReason: u.BlockedReason, At: u.UpdatedAt,
	})
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}
