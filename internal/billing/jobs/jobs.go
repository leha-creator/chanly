// Package jobs — периодические задания модуля расчётов (07 §2): expire_invoices и
// generate_closing_acts. Сценарии выполняет app.
package jobs

import (
	"context"
	"time"

	"github.com/leha-creator/chanly/internal/billing/app"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
)

// Options — расписание заданий.
type Options struct {
	// ExpireInterval — период expire_invoices (INVOICE_EXPIRE_INTERVAL, 1 ч).
	ExpireInterval time.Duration
	// ClosingDay, ClosingHour, ClosingMinute — запуск generate_closing_acts по Москве (CLOSING_ACTS_DAY,
	// CLOSING_ACTS_TIME; 1-е число, 03:00).
	ClosingDay, ClosingHour, ClosingMinute int
}

// Register регистрирует периодические задания модуля; после простоя — один запуск.
func Register(w *job.Worker, svc *app.Service, o Options) {
	w.RegisterPeriodic(app.KindExpireInvoices, job.Every(o.ExpireInterval), func(ctx context.Context, _ job.Running) error {
		_, err := svc.ExpireInvoices(ctx)
		return err
	}, job.HandlerOptions{})
	// Документы за прошедший месяц; повтор за тот же месяц пропускает сформированные (R-16).
	w.RegisterPeriodic(app.KindGenerateClosingActs, job.MonthlyAt(o.ClosingDay, o.ClosingHour, o.ClosingMinute),
		func(ctx context.Context, _ job.Running) error {
			_, err := svc.GenerateClosingActs(ctx, domain.PreviousPeriod(svc.Clock.Now()))
			return err
		}, job.HandlerOptions{})
}
