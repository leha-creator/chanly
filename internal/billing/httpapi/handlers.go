// Package httpapi — операции OpenAPI модуля расчётов (тег balance): счета на пополнение и
// закрывающие документы. Баланс и операции по нему — модуль ledger.
package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/app"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// cabinetAdvertiser — операции модуля доступны кабинету рекламодателя (07 §4).
const cabinetAdvertiser = "advertiser"

// Handlers — операции OpenAPI модуля расчётов.
type Handlers struct {
	svc   *app.Service
	log   *slog.Logger
	admin AdminPorts
}

// New создаёт обработчики.
func New(svc *app.Service, log *slog.Logger) *Handlers {
	return &Handlers{svc: svc, log: log.With(logx.KeyModule, "billing")}
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

// pdfFile — PDF с именем файла для сохранения (Content-Disposition).
type pdfFile struct {
	name string
	body io.ReadCloser
	size int64
}

func (f pdfFile) write(w http.ResponseWriter) error {
	defer f.body.Close()
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.pdf"`, f.name))
	if f.size > 0 {
		w.Header().Set("Content-Length", fmt.Sprint(f.size))
	}
	w.WriteHeader(http.StatusOK)
	_, err := io.Copy(w, f.body)
	return err
}
