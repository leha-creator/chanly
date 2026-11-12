package postgres

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/account/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/apperr"
)

func (s *Store) AccountSettings() *AccountSettings { return &AccountSettings{s} }

type AccountSettings struct{ s *Store }

func (r *AccountSettings) LockEmails(ctx context.Context, emails []string) error {
	emails = slices.Clone(emails)
	slices.Sort(emails)
	for _, email := range slices.Compact(emails) {
		if err := r.s.q(ctx).LockAuthEmail(ctx, email); err != nil {
			return err
		}
	}
	return nil
}

func (r *AccountSettings) Access(ctx context.Context, id uuid.UUID) (domain.SettingsAccess, error) {
	row, err := r.s.q(ctx).SettingsAccess(ctx, id)
	return domain.SettingsAccess{AdminRole: row.AdminRole, Advertiser: row.Advertiser, Blogger: row.Blogger, MaxBot: row.MaxBot}, err
}
func (r *AccountSettings) ListSessions(ctx context.Context, user uuid.UUID, now time.Time) ([]domain.Session, error) {
	rows, err := r.s.q(ctx).ListOwnSessions(ctx, sqlc.ListOwnSessionsParams{UserID: user, Now: now})
	out := make([]domain.Session, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.Session{ID: row.ID, UserID: row.UserID, ExpiresAt: row.ExpiresAt, UserAgent: row.UserAgent, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
	}
	return out, err
}
func (r *AccountSettings) RevokeOtherSession(ctx context.Context, user, current, id uuid.UUID, now time.Time) (bool, error) {
	n, err := r.s.q(ctx).RevokeOwnOtherSession(ctx, sqlc.RevokeOwnOtherSessionParams{UserID: user, CurrentID: current, ID: id, Now: now})
	return n > 0, err
}
func (r *AccountSettings) RevokeOtherSessions(ctx context.Context, user, current uuid.UUID, now time.Time) error {
	_, err := r.s.q(ctx).RevokeOtherOwnSessions(ctx, sqlc.RevokeOtherOwnSessionsParams{UserID: user, CurrentID: current, Now: now})
	return err
}
func (r *AccountSettings) CountEmailChanges(ctx context.Context, user uuid.UUID, since time.Time) (int, error) {
	n, err := r.s.q(ctx).CountEmailChanges(ctx, sqlc.CountEmailChangesParams{UserID: user, Since: since})
	return int(n), err
}
func (r *AccountSettings) ConsumeEmailChanges(ctx context.Context, user uuid.UUID, now time.Time) error {
	return r.s.q(ctx).ConsumeEmailChanges(ctx, sqlc.ConsumeEmailChangesParams{UserID: user, Now: now})
}
func (r *AccountSettings) InsertEmailChange(ctx context.Context, c *domain.EmailChange) error {
	return r.s.q(ctx).InsertEmailChange(ctx, sqlc.InsertEmailChangeParams{ID: c.ID, UserID: c.UserID, CurrentEmail: c.CurrentEmail, NewEmail: c.NewEmail, CurrentHash: c.CurrentHash, NewHash: c.NewHash, SentAt: c.SentAt, ExpiresAt: c.ExpiresAt})
}
func (r *AccountSettings) LockEmailChange(ctx context.Context, user, id uuid.UUID) (*domain.EmailChange, error) {
	row, err := r.s.q(ctx).LockEmailChange(ctx, sqlc.LockEmailChangeParams{ID: id, UserID: user})
	if err != nil {
		return nil, r.s.fail(ctx, "Account.LockEmailChange", err)
	}
	return &domain.EmailChange{ID: row.ID, UserID: row.UserID, CurrentEmail: row.CurrentEmail, NewEmail: row.NewEmail, CurrentHash: row.CurrentHash, NewHash: row.NewHash, Attempts: int(row.Attempts), Sends: int(row.Sends), SentAt: row.SentAt, ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt, ConsumedAt: row.ConsumedAt}, nil
}
func (r *AccountSettings) SaveEmailChange(ctx context.Context, c *domain.EmailChange) error {
	return r.s.q(ctx).SaveEmailChange(ctx, sqlc.SaveEmailChangeParams{ID: c.ID, CurrentHash: c.CurrentHash, NewHash: c.NewHash, Attempts: int32(c.Attempts), Sends: int32(c.Sends), SentAt: c.SentAt, ConsumedAt: c.ConsumedAt})
}
func (r *AccountSettings) ChangeEmail(ctx context.Context, id uuid.UUID, email string, now time.Time) error {
	err := r.s.q(ctx).ChangeUserEmail(ctx, sqlc.ChangeUserEmailParams{ID: id, Email: email, Now: now})
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return apperr.Conflict("email is already registered").WithDetail("reason", "email_taken")
	}
	return err
}
