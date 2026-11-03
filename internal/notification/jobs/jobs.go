// Package jobs — обработчики заданий модуля уведомлений.
package jobs

import (
	"context"

	"github.com/leha-creator/chanly/internal/notification/app"
	"github.com/leha-creator/chanly/internal/platform/job"
)

// Register регистрирует send_notification (07 §2). Исчерпание попыток помечает уведомление
// failed и оповещения не поднимает: сбой доставки не требует человека (07 §6, R-7).
func Register(w *job.Worker, svc *app.Service) {
	w.Register(app.KindSendNotification, func(ctx context.Context, r job.Running) error {
		id, err := app.DecodeSendPayload(r.Payload)
		if err != nil {
			return err
		}
		return svc.Send(ctx, id)
	}, job.HandlerOptions{
		NoFailureAlert: true,
		OnExhausted: func(ctx context.Context, r job.Running, err error) {
			if id, derr := app.DecodeSendPayload(r.Payload); derr == nil {
				svc.Exhausted(ctx, id, err)
			}
		},
	})
}
