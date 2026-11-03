// Package httpapi — операции OpenAPI модуля размещений: корзина, оформление и заказы
// рекламодателя, запросы и размещения блогера (R-1, R-22).
package httpapi

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/app"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Кабинеты операций модуля (07 §4).
const (
	cabinetAdvertiser = "advertiser"
	cabinetBlogger    = "blogger"
)

// Handlers — операции OpenAPI модуля размещений.
type Handlers struct {
	svc *app.Service
	log *slog.Logger
	// admin — порты ответов администратора (SetAdminPorts).
	admin AdminPorts
}

// New создаёт обработчики.
func New(svc *app.Service, log *slog.Logger) *Handlers {
	return &Handlers{svc: svc, log: log.With(logx.KeyModule, "placement")}
}

// cabinet — организация и пользователь с открытым кабинетом.
func cabinet(ctx context.Context, name string) (org, user uuid.UUID, err error) {
	p, err := httpx.RequireCabinet(ctx, name)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if p.OrganizationID == nil {
		return uuid.Nil, uuid.Nil, apperr.Forbidden(httpx.ReasonCabinetRequired).WithDetail("cabinet", name)
	}
	return *p.OrganizationID, p.UserID, nil
}

func advertiser(ctx context.Context) (org, user uuid.UUID, err error) {
	return cabinet(ctx, cabinetAdvertiser)
}

func blogger(ctx context.Context) (org, user uuid.UUID, err error) {
	return cabinet(ctx, cabinetBlogger)
}

func opt[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
