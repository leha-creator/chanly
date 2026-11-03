// Package jobs — периодические задания журнала проводок (07 §2): check_ledger_balance.
// Сценарий выполняет app.
package jobs

import (
	"context"
	"time"

	"github.com/leha-creator/chanly/internal/ledger/app"
	"github.com/leha-creator/chanly/internal/platform/job"
)

// Register регистрирует проверку сходимости с периодом interval (LEDGER_CHECK_INTERVAL, R-15);
// одна попытка, после простоя — один запуск. Расхождение не ошибка задания: оно в оповещениях.
func Register(w *job.Worker, svc *app.Service, interval time.Duration) {
	w.RegisterPeriodic(app.KindCheckBalance, job.Every(interval), func(ctx context.Context, _ job.Running) error {
		_, err := svc.CheckBalance(ctx)
		return err
	}, job.HandlerOptions{})
}
