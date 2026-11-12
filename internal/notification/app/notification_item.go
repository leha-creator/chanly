package app

import (
	"context"

	"github.com/google/uuid"
)

func (s *Service) GetNotification(ctx context.Context, user, id uuid.UUID) (CabinetItem, error) {
	n, err := s.Repo.OwnCabinetNotification(ctx, user, id)
	if err != nil {
		return CabinetItem{}, notFound(err)
	}
	title, text, err := s.Renderer.Cabinet(n)
	if err != nil {
		title, text = fallbackTitle, ""
		s.log.WarnContext(ctx, "notification render failed", "op", "Notification.GetNotification", "notification_id", id)
	}
	return CabinetItem{Notification: n, Title: title, Text: text}, nil
}
