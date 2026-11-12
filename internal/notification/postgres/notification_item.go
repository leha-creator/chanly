package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/notification/domain"
	"github.com/leha-creator/chanly/internal/notification/postgres/sqlc"
)

func (s *Store) OwnCabinetNotification(ctx context.Context, user, id uuid.UUID) (domain.Notification, error) {
	row, err := s.q(ctx).GetOwnCabinetNotification(ctx, sqlc.GetOwnCabinetNotificationParams{UserID: user, ID: id})
	if err != nil {
		return domain.Notification{}, s.fail(ctx, "Notification.OwnCabinetNotification", err)
	}
	return toDomain(row)
}
