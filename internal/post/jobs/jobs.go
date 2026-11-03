// Package jobs — обработчики заданий модуля post (07 §2, R-19); сценарии выполняет app.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/post/app"
)

// Register регистрирует виды заданий модуля.
func Register(w *job.Worker, svc *app.Service) {
	w.Register(app.KindBuildMediaDerivative, func(ctx context.Context, r job.Running) error {
		var p app.BuildPayload
		if err := decode(r, &p); err != nil {
			return err
		}
		return svc.BuildDerivative(ctx, p)
	}, job.HandlerOptions{})
	w.Register(app.KindPublishMediaDerivative, func(ctx context.Context, r job.Running) error {
		var p app.PublishPayload
		if err := decode(r, &p); err != nil {
			return err
		}
		return svc.PublishDerivative(ctx, p.DerivativeID)
	}, job.HandlerOptions{})
	// Раз в сутки по Москве в 04:30 (R-19).
	w.RegisterPeriodic(app.KindPurgeMedia, job.DailyAt(4, 30), func(ctx context.Context, _ job.Running) error {
		return svc.PurgeMedia(ctx)
	}, job.HandlerOptions{})
}

func decode(r job.Running, v any) error {
	if err := json.Unmarshal(r.Payload, v); err != nil {
		return job.Permanent(fmt.Errorf("decode %s payload: %w", r.Kind, err))
	}
	return nil
}
