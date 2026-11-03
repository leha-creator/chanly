// Package jobs — обработчики заданий модуля маркировки (07 §2): sync_ord_object и
// poll_ord_status. Сценарии выполняет app.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leha-creator/chanly/internal/marking/app"
	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
)

// Register регистрирует виды заданий модуля.
//
// sync_ord_object — max_attempts 10 (ставит app), после исчерпания — объект в error и
// оповещение (OnExhausted). Ожидание зависимостей попытки не тратит (job.Postpone).
//
// poll_ord_status — без предела: срок задаёт обработчик (24 ч, R-10); после простоя
// выполняется немедленно и один раз (политика Overdue по умолчанию).
func Register(w *job.Worker, svc *app.Service) {
	w.Register(app.KindSyncOrdObject, func(ctx context.Context, r job.Running) error {
		ref, err := decode(r)
		if err != nil {
			return err
		}
		return svc.Sync(ctx, ref, r.Attempts+1)
	}, job.HandlerOptions{
		ObjectRef: objectRef,
		OnExhausted: func(ctx context.Context, r job.Running, err error) {
			if ref, derr := decode(r); derr == nil {
				svc.SyncExhausted(ctx, ref, err)
			}
		},
	})
	w.Register(app.KindPollOrdStatus, func(ctx context.Context, r job.Running) error {
		ref, err := decode(r)
		if err != nil {
			return err
		}
		return svc.Poll(ctx, ref)
	}, job.HandlerOptions{ObjectRef: objectRef})
}

// objectRef — объект задания для списка затронутых после простоя: ord:<type>:<id>.
func objectRef(payload []byte) string {
	var p app.SyncPayload
	if err := json.Unmarshal(payload, &p); err != nil || p.Ref == "" {
		return ""
	}
	return "ord:" + p.Ref
}

func decode(r job.Running) (domain.ObjectRef, error) {
	var p app.SyncPayload
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		return domain.ObjectRef{}, job.Permanent(fmt.Errorf("decode %s payload: %w", r.Kind, err))
	}
	ref, err := domain.ParseObjectRef(p.Ref)
	if err != nil {
		return domain.ObjectRef{}, job.Permanent(err)
	}
	return ref, nil
}

// RegisterReports регистрирует периодические задания отчётности (этап «API администратора и
// отчётность ОРД», 07 §2): ord_monthly_report 1-го числа в 00:05 по Москве — снимок, формирование,
// отправка при ORD_REPORT_AUTO_SEND (одна попытка; после простоя — один запуск, снимок позже);
// ord_report_check ежедневно в 10:00 — срок сдачи (R-23).
func RegisterReports(w *job.Worker, svc *app.Service) {
	w.RegisterPeriodic(app.KindMonthlyReport, job.MonthlyAt(1, 0, 5), func(ctx context.Context, _ job.Running) error {
		return svc.MonthlyReport(ctx)
	}, job.HandlerOptions{})
	w.RegisterPeriodic(app.KindReportCheck, job.DailyAt(10, 0), func(ctx context.Context, _ job.Running) error {
		return svc.CheckReport(ctx)
	}, job.HandlerOptions{})
}
