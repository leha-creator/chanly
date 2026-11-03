// Package jobs — обработчики заданий модуля аккаунтов.
package jobs

import (
	"context"

	"github.com/leha-creator/chanly/internal/account/app"
	"github.com/leha-creator/chanly/internal/platform/job"
)

// Register регистрирует виды заданий модуля.
func Register(w *job.Worker, svc *app.Service) {
	// Полезная нагрузка содержит зашифрованный код: затирается после завершения (R-1).
	w.Register(app.JobSendLoginCode, func(ctx context.Context, r job.Running) error {
		return svc.SendLoginCode(ctx, r.Payload)
	}, job.HandlerOptions{WipePayload: true})
}
