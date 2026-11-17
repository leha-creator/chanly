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

type TelegramBot struct{ s *Store }

func (s *Store) TelegramBot() *TelegramBot { return &TelegramBot{s} }

var _ domain.TelegramBotRepository = (*TelegramBot)(nil)

func (r *Users) NotificationTelegramRecipient(ctx context.Context, user uuid.UUID) (int64, error) {
	return r.s.TelegramBot().Recipient(ctx, user)
}
func (r *TelegramBot) Recipient(ctx context.Context, user uuid.UUID) (int64, error) {
	id, err := r.s.q(ctx).NotificationTelegramRecipient(ctx, user)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}
func (r *TelegramBot) CountConnections(ctx context.Context, user uuid.UUID, since time.Time) (int, error) {
	count, err := r.s.q(ctx).CountTelegramBotConnections(ctx, sqlc.CountTelegramBotConnectionsParams{UserID: user, Since: since})
	return int(count), err
}
func (r *TelegramBot) ExpireConnections(ctx context.Context, user uuid.UUID) error {
	return r.s.q(ctx).ExpireTelegramBotConnections(ctx, user)
}
func (r *TelegramBot) InsertConnection(ctx context.Context, c *domain.TelegramBotConnection) error {
	return r.s.q(ctx).InsertTelegramBotConnection(ctx, sqlc.InsertTelegramBotConnectionParams{
		ID: c.ID, UserID: c.UserID, TokenHash: c.TokenHash, Status: c.Status, CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt,
	})
}
func telegramBotConnection(row sqlc.NotificationTelegramBotConnection, err error) (*domain.TelegramBotConnection, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &domain.TelegramBotConnection{
		ID: row.ID, UserID: row.UserID, TokenHash: row.TokenHash, Status: row.Status, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
	}, nil
}
func (r *TelegramBot) Connection(ctx context.Context, user, id uuid.UUID, lock bool) (*domain.TelegramBotConnection, error) {
	if lock {
		return telegramBotConnection(r.s.q(ctx).LockTelegramBotConnection(ctx, sqlc.LockTelegramBotConnectionParams{UserID: user, ID: id}))
	}
	return telegramBotConnection(r.s.q(ctx).GetTelegramBotConnection(ctx, sqlc.GetTelegramBotConnectionParams{UserID: user, ID: id}))
}
func (r *TelegramBot) ConnectionByHash(ctx context.Context, hash string) (*domain.TelegramBotConnection, error) {
	return telegramBotConnection(r.s.q(ctx).GetTelegramBotConnectionByHash(ctx, hash))
}
func (r *TelegramBot) Connect(ctx context.Context, c *domain.TelegramBotConnection, telegramID int64, now time.Time) error {
	queries := r.s.q(ctx)
	if err := queries.SetNotificationTelegramRecipient(ctx, sqlc.SetNotificationTelegramRecipientParams{UserID: c.UserID, TelegramUserID: telegramID, ConnectedAt: now}); err != nil {
		return err
	}
	return queries.CompleteTelegramBotConnection(ctx, c.ID)
}
func (r *TelegramBot) Disconnect(ctx context.Context, user uuid.UUID) error {
	return r.s.q(ctx).DisconnectNotificationTelegramBot(ctx, user)
}
