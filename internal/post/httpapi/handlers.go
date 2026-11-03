// Package httpapi — операции OpenAPI модуля post: медиатека, кампании, черновик
// поста и предпросмотр (кабинет рекламодателя, R-16).
package httpapi

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/post/app"
)

// cabinetAdvertiser — кабинет, которому доступны операции модуля (R-16).
const cabinetAdvertiser = "advertiser"

// Options — параметры обработчиков.
type Options struct {
	// MaxUploadBytes — предел файла (MEDIA_MAX_BYTES); часть file читается до предела + 1 байт.
	MaxUploadBytes int64
	// UploadConcurrency — одновременных загрузок на экземпляр (MEDIA_UPLOAD_CONCURRENCY, R-2).
	UploadConcurrency int
}

// Handlers — операции OpenAPI модуля post.
type Handlers struct {
	svc     *app.Service
	log     *slog.Logger
	opts    Options
	uploads chan struct{}
}

// New создаёт обработчики.
func New(svc *app.Service, log *slog.Logger, opts Options) *Handlers {
	if opts.UploadConcurrency < 1 {
		opts.UploadConcurrency = 4
	}
	if opts.MaxUploadBytes <= 0 {
		opts.MaxUploadBytes = 50 << 20
	}
	return &Handlers{svc: svc, log: log.With(logx.KeyModule, "post"), opts: opts,
		uploads: make(chan struct{}, opts.UploadConcurrency)}
}

// advertiser — организация и пользователь с открытым кабинетом рекламодателя.
func advertiser(ctx context.Context) (org, user uuid.UUID, err error) {
	p, err := httpx.RequireCabinet(ctx, cabinetAdvertiser)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if p.OrganizationID == nil {
		return uuid.Nil, uuid.Nil, apperr.Forbidden(httpx.ReasonCabinetRequired).WithDetail("cabinet", cabinetAdvertiser)
	}
	return *p.OrganizationID, p.UserID, nil
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
