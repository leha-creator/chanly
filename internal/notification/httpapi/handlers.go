// Package httpapi — операции OpenAPI уведомлений кабинета (тег notifications, R-12):
// список своих уведомлений и отметка прочитанными. Нужна только сессия: уведомления —
// пользователя, не кабинета.
package httpapi

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/notification/app"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Handlers — операции OpenAPI уведомлений.
type Handlers struct {
	svc *app.Service
	log *slog.Logger
}

// New создаёт обработчики.
func New(svc *app.Service, log *slog.Logger) *Handlers {
	return &Handlers{svc: svc, log: log.With(logx.KeyModule, "notification")}
}

func user(ctx context.Context) (uuid.UUID, error) {
	p, ok := httpx.PrincipalFrom(ctx)
	if !ok {
		return uuid.Nil, apperr.Unauthorized("session is missing")
	}
	return p.UserID, nil
}

func (h *Handlers) ListNotifications(ctx context.Context, req apigen.ListNotificationsRequestObject) (apigen.ListNotificationsResponseObject, error) {
	uid, err := user(ctx)
	if err != nil {
		return nil, err
	}
	var cursor string
	if req.Params.Cursor != nil {
		cursor = *req.Params.Cursor
	}
	var limit int
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	unread := req.Params.Unread != nil && *req.Params.Unread
	page, err := h.svc.List(ctx, uid, unread, cursor, limit)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.Notification, 0, len(page.Items))
	for _, it := range page.Items {
		items = append(items, toAPI(it))
	}
	total := page.Total
	resp := apigen.ListNotifications200JSONResponse{Items: items, Total: &total}
	if page.NextCursor != "" {
		next := page.NextCursor
		resp.NextCursor = &next
	}
	return resp, nil
}

func (h *Handlers) MarkNotificationRead(ctx context.Context, req apigen.MarkNotificationReadRequestObject) (apigen.MarkNotificationReadResponseObject, error) {
	uid, err := user(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.MarkRead(ctx, uid, req.Id); err != nil {
		return nil, err
	}
	return apigen.MarkNotificationRead204Response{}, nil
}

func (h *Handlers) MarkAllNotificationsRead(ctx context.Context, _ apigen.MarkAllNotificationsReadRequestObject) (apigen.MarkAllNotificationsReadResponseObject, error) {
	uid, err := user(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := h.svc.MarkAllRead(ctx, uid); err != nil {
		return nil, err
	}
	return apigen.MarkAllNotificationsRead204Response{}, nil
}

func toAPI(it app.CabinetItem) apigen.Notification {
	out := apigen.Notification{Id: it.ID, Template: it.Template, Title: it.Title, Text: it.Text,
		ObjectType: it.ObjectType, IsRead: it.ReadAt != nil, CreatedAt: it.CreatedAt, OrderId: it.OrderID()}
	if it.ObjectID != nil {
		// Объект без UUID (не бывает у текущих событий) в ответ не попадает: схема требует UUID.
		if id, err := uuid.Parse(*it.ObjectID); err == nil {
			out.ObjectId = &id
		}
	}
	if link := it.Link(); link != "" {
		out.Link = &link
	}
	return out
}
