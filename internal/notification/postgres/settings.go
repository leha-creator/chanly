package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/leha-creator/chanly/internal/notification/domain"
	"github.com/leha-creator/chanly/internal/notification/postgres/sqlc"
)

func decodeSettings(row sqlc.UserSetting) (domain.Settings, error) {
	s := domain.DefaultSettings()
	s.Version = row.Version
	s.HideNotificationPreview = row.HideNotificationPreview
	if err := json.Unmarshal(row.QuietHours, &s.QuietHours); err != nil {
		return s, err
	}
	err := json.Unmarshal(row.Delivery, &s.Delivery)
	return s, err
}
func (s *Store) Settings(ctx context.Context, user uuid.UUID) (domain.Settings, error) {
	row, err := s.q(ctx).GetUserSettings(ctx, user)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DefaultSettings(), nil
	}
	if err != nil {
		return domain.Settings{}, s.fail(ctx, "Notification.Settings", err)
	}
	return decodeSettings(row)
}
func (s *Store) LockSettings(ctx context.Context, user uuid.UUID, now time.Time) (domain.Settings, error) {
	if err := s.q(ctx).EnsureUserSettings(ctx, sqlc.EnsureUserSettingsParams{UserID: user, Now: now}); err != nil {
		return domain.Settings{}, err
	}
	row, err := s.q(ctx).LockUserSettings(ctx, user)
	if err != nil {
		return domain.Settings{}, err
	}
	return decodeSettings(row)
}
func (s *Store) SaveSettings(ctx context.Context, user uuid.UUID, settings domain.Settings, now time.Time) error {
	q, err := json.Marshal(settings.QuietHours)
	if err != nil {
		return err
	}
	d, err := json.Marshal(settings.Delivery)
	if err != nil {
		return err
	}
	return s.q(ctx).SaveUserSettings(ctx, sqlc.SaveUserSettingsParams{UserID: user, HideNotificationPreview: settings.HideNotificationPreview, QuietHours: q, Delivery: d, Now: now})
}
