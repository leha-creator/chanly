package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/account/postgres/sqlc"
)

type MAXBot struct{ s *Store }

func (s *Store) MAXBot() *MAXBot { return &MAXBot{s} }

var _ domain.MAXBotRepository = (*MAXBot)(nil)

func (r *Users) NotificationMAXRecipient(ctx context.Context, user uuid.UUID) (int64, error) {
	return r.s.MAXBot().Recipient(ctx, user)
}
func (r *MAXBot) Recipient(ctx context.Context, user uuid.UUID) (int64, error) {
	id, err := r.s.q(ctx).NotificationMAXRecipient(ctx, user)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}
func (r *MAXBot) CountConnections(ctx context.Context, user uuid.UUID, since time.Time) (int, error) {
	count, err := r.s.q(ctx).CountMAXBotConnections(ctx, sqlc.CountMAXBotConnectionsParams{UserID: user, Since: since})
	return int(count), err
}
func (r *MAXBot) ExpireConnections(ctx context.Context, user uuid.UUID) error {
	return r.s.q(ctx).ExpireMAXBotConnections(ctx, user)
}
func (r *MAXBot) InsertConnection(ctx context.Context, c *domain.MAXBotConnection) error {
	return r.s.q(ctx).InsertMAXBotConnection(ctx, sqlc.InsertMAXBotConnectionParams{
		ID: c.ID, UserID: c.UserID, TokenHash: c.TokenHash, Status: c.Status, CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt,
	})
}
func maxBotConnection(row sqlc.NotificationMaxBotConnection, err error) (*domain.MAXBotConnection, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &domain.MAXBotConnection{
		ID: row.ID, UserID: row.UserID, TokenHash: row.TokenHash, Status: row.Status, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
	}, nil
}
func (r *MAXBot) Connection(ctx context.Context, user, id uuid.UUID, lock bool) (*domain.MAXBotConnection, error) {
	if lock {
		return maxBotConnection(r.s.q(ctx).LockMAXBotConnection(ctx, sqlc.LockMAXBotConnectionParams{UserID: user, ID: id}))
	}
	return maxBotConnection(r.s.q(ctx).GetMAXBotConnection(ctx, sqlc.GetMAXBotConnectionParams{UserID: user, ID: id}))
}
func (r *MAXBot) ConnectionByHash(ctx context.Context, hash string) (*domain.MAXBotConnection, error) {
	return maxBotConnection(r.s.q(ctx).GetMAXBotConnectionByHash(ctx, hash))
}
func (r *MAXBot) Connect(ctx context.Context, c *domain.MAXBotConnection, maxID int64, now time.Time) error {
	queries := r.s.q(ctx)
	if err := queries.SetNotificationMAXRecipient(ctx, sqlc.SetNotificationMAXRecipientParams{UserID: c.UserID, MaxUserID: maxID, ConnectedAt: now}); err != nil {
		return err
	}
	return queries.CompleteMAXBotConnection(ctx, c.ID)
}
func (r *MAXBot) Disconnect(ctx context.Context, user uuid.UUID) error {
	return r.s.q(ctx).DisconnectNotificationMAXBot(ctx, user)
}
