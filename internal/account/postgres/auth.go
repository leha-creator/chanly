package postgres

import (
	"context"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/account/postgres/sqlc"
)

// AuthCodes — коды входа.
type AuthCodes struct{ s *Store }

func (r *AuthCodes) Create(ctx context.Context, c *domain.LoginCode) error {
	const op = "Account.AuthCodes.Create"
	r.s.debug(ctx, op, "auth_code_id", c.ID)
	err := r.s.q(ctx).InsertLoginCode(ctx, sqlc.InsertLoginCodeParams{
		ID: c.ID, Email: ptr(c.Email), CodeHash: c.CodeHash, ExpiresAt: c.ExpiresAt,
		RequestedIp: c.RequestedIP, CreatedAt: c.CreatedAt,
	})
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

func (r *AuthCodes) LatestActiveForUpdate(ctx context.Context, email string) (*domain.LoginCode, error) {
	const op = "Account.AuthCodes.LatestActiveForUpdate"
	r.s.debug(ctx, op)
	row, err := r.s.q(ctx).LatestActiveLoginCodeForUpdate(ctx, ptr(domain.NormalizeEmail(email)))
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	c := &domain.LoginCode{
		ID: row.ID, CodeHash: row.CodeHash, ExpiresAt: row.ExpiresAt, Attempts: int(row.Attempts),
		ConsumedAt: row.ConsumedAt, RequestedIP: row.RequestedIp, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.Email != nil {
		c.Email = *row.Email
	}
	return c, nil
}

func (r *AuthCodes) ConsumeActive(ctx context.Context, email string, now time.Time) error {
	const op = "Account.AuthCodes.ConsumeActive"
	r.s.debug(ctx, op)
	err := r.s.q(ctx).ConsumeActiveLoginCodes(ctx, sqlc.ConsumeActiveLoginCodesParams{Email: ptr(domain.NormalizeEmail(email)), Now: now})
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

func (r *AuthCodes) Save(ctx context.Context, c *domain.LoginCode) error {
	const op = "Account.AuthCodes.Save"
	r.s.debug(ctx, op, "auth_code_id", c.ID, "attempts", c.Attempts, "consumed", c.ConsumedAt != nil)
	err := r.s.q(ctx).SaveLoginCode(ctx, sqlc.SaveLoginCodeParams{
		ID: c.ID, Attempts: int32(c.Attempts), ConsumedAt: c.ConsumedAt, UpdatedAt: c.UpdatedAt,
	})
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

func (r *AuthCodes) CountByEmailSince(ctx context.Context, email string, since time.Time) (domain.WindowCount, error) {
	const op = "Account.AuthCodes.CountByEmailSince"
	row, err := r.s.q(ctx).CountLoginCodesByEmailSince(ctx, sqlc.CountLoginCodesByEmailSinceParams{
		Email: ptr(domain.NormalizeEmail(email)), Since: since,
	})
	if err != nil {
		return domain.WindowCount{}, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "count", row.Count)
	return domain.WindowCount{Count: int(row.Count), Oldest: row.Oldest}, nil
}

func (r *AuthCodes) CountByIPSince(ctx context.Context, ip netip.Addr, since time.Time) (domain.WindowCount, error) {
	const op = "Account.AuthCodes.CountByIPSince"
	row, err := r.s.q(ctx).CountLoginCodesByIPSince(ctx, sqlc.CountLoginCodesByIPSinceParams{Ip: ip, Since: since})
	if err != nil {
		return domain.WindowCount{}, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "count", row.Count)
	return domain.WindowCount{Count: int(row.Count), Oldest: row.Oldest}, nil
}

// Sessions — сессии.
type Sessions struct{ s *Store }

func (r *Sessions) Create(ctx context.Context, x *domain.Session) error {
	const op = "Account.Sessions.Create"
	r.s.debug(ctx, op, "session_id", x.ID, "user_id", x.UserID)
	err := r.s.q(ctx).InsertSession(ctx, sqlc.InsertSessionParams{
		ID: x.ID, UserID: x.UserID, TokenHash: x.TokenHash, ExpiresAt: x.ExpiresAt,
		Ip: x.IP, UserAgent: x.UserAgent, CreatedAt: x.CreatedAt,
	})
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

func (r *Sessions) GetByTokenHash(ctx context.Context, hash string) (*domain.Session, error) {
	const op = "Account.Sessions.GetByTokenHash"
	row, err := r.s.q(ctx).GetSessionByTokenHash(ctx, hash)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "session_id", row.ID)
	return &domain.Session{
		ID: row.ID, UserID: row.UserID, TokenHash: row.TokenHash, ExpiresAt: row.ExpiresAt, IP: row.Ip,
		UserAgent: row.UserAgent, RevokedAt: row.RevokedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func (r *Sessions) SetExpiresAt(ctx context.Context, id uuid.UUID, expiresAt, now time.Time) error {
	const op = "Account.Sessions.SetExpiresAt"
	r.s.debug(ctx, op, "session_id", id)
	if err := r.s.q(ctx).SetSessionExpiresAt(ctx, sqlc.SetSessionExpiresAtParams{ID: id, ExpiresAt: expiresAt, Now: now}); err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

func (r *Sessions) Revoke(ctx context.Context, id uuid.UUID, now time.Time) error {
	const op = "Account.Sessions.Revoke"
	r.s.debug(ctx, op, "session_id", id)
	if err := r.s.q(ctx).RevokeSession(ctx, sqlc.RevokeSessionParams{ID: id, Now: now}); err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

func (r *Sessions) RevokeAllForUser(ctx context.Context, userID uuid.UUID, now time.Time) (int64, error) {
	const op = "Account.Sessions.RevokeAllForUser"
	r.s.debug(ctx, op, "user_id", userID)
	n, err := r.s.q(ctx).RevokeUserSessions(ctx, sqlc.RevokeUserSessionsParams{UserID: userID, Now: now})
	if err != nil {
		return 0, r.s.fail(ctx, op, err)
	}
	return n, nil
}
