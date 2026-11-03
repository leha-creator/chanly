package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/notification/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// CabinetItem — уведомление кабинета с собранным текстом.
type CabinetItem struct {
	domain.Notification
	Title string
	Text  string
}

// CabinetPage — страница кабинета; NextCursor пуст на последней.
type CabinetPage struct {
	Items      []CabinetItem
	NextCursor string
	Total      int
}

// List — уведомления кабинета пользователя сессии, новые сверху (R-12). Текст собирается
// при чтении (R-5); шаблон не собрался — запасной заголовок, WARN.
func (s *Service) List(ctx context.Context, userID uuid.UUID, unread bool, cursorStr string, limit int) (CabinetPage, error) {
	const op = "Notification.List"
	after, err := domain.DecodeCursor(cursorStr)
	if err != nil {
		return CabinetPage{}, apperr.Validation("cursor", "invalid")
	}
	limit = pageLimit(limit)
	list, err := s.Repo.ListCabinet(ctx, userID, unread, after, limit+1)
	if err != nil {
		return CabinetPage{}, err
	}
	page := CabinetPage{Items: make([]CabinetItem, 0, len(list))}
	if len(list) > limit {
		list = list[:limit]
		last := list[limit-1]
		page.NextCursor = domain.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}.Encode()
	}
	if page.Total, err = s.Repo.CountCabinet(ctx, userID, unread); err != nil {
		return CabinetPage{}, err
	}
	for _, n := range list {
		title, text, err := s.Renderer.Cabinet(n)
		if err != nil {
			s.log.WarnContext(ctx, "notification render failed", logx.KeyOp, op, KeyNotificationID, n.ID, KeyTemplate, n.Template,
				logx.KeyError, err.Error())
			title, text = fallbackTitle, ""
		}
		page.Items = append(page.Items, CabinetItem{Notification: n, Title: title, Text: text})
	}
	s.log.DebugContext(ctx, "notifications listed", logx.KeyOp, op, logx.KeyUserID, userID, "count", len(page.Items),
		"unread", unread)
	return page, nil
}

// fallbackTitle — заголовок, если шаблон не собрался.
const fallbackTitle = "Уведомление"

// MarkRead отмечает своё уведомление кабинета прочитанным; повтор безопасен; чужое — 404.
func (s *Service) MarkRead(ctx context.Context, userID, id uuid.UUID) error {
	const op = "Notification.MarkRead"
	if err := s.Repo.MarkRead(ctx, userID, id, s.Clock.Now()); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.log.DebugContext(ctx, "notification to read not found", logx.KeyOp, op, KeyNotificationID, id)
		}
		return notFound(err)
	}
	s.log.InfoContext(ctx, "notifications marked read", logx.KeyOp, op, logx.KeyUserID, userID, KeyNotificationID, id, "count", 1)
	return nil
}

// MarkAllRead отмечает все непрочитанные уведомления кабинета пользователя; повтор безопасен.
func (s *Service) MarkAllRead(ctx context.Context, userID uuid.UUID) (int64, error) {
	n, err := s.Repo.MarkAllRead(ctx, userID, s.Clock.Now())
	if err != nil {
		return 0, err
	}
	s.log.InfoContext(ctx, "notifications marked read", logx.KeyOp, "Notification.MarkAllRead", logx.KeyUserID, userID, "count", n)
	return n, nil
}
