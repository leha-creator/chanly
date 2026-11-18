package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/support/app"
	"time"
)

func Register(w *job.Worker, s *app.Service) {
	if !s.Enabled {
		return
	}
	w.Register(app.MediaKind, func(ctx context.Context, j job.Running) error {
		var p app.Payload
		if json.Unmarshal(j.Payload, &p) != nil {
			return job.Permanent(errors.New("invalid support media payload"))
		}
		return s.PrepareMedia(ctx, p.ID)
	}, job.HandlerOptions{})
	w.Register(app.SendKind, func(ctx context.Context, j job.Running) error {
		var p app.Payload
		if json.Unmarshal(j.Payload, &p) != nil {
			return job.Permanent(errors.New("invalid support send payload"))
		}
		return s.Send(ctx, p.ID, j.Attempts)
	}, job.HandlerOptions{})
	w.RegisterPeriodic("recover_support_sends", job.Every(time.Minute), func(ctx context.Context, _ job.Running) error { return s.Repo.Recover(ctx, s.Clock.Now()) }, job.HandlerOptions{})
	w.RegisterPeriodic("cleanup_support", job.DailyAt(4, 30), func(ctx context.Context, _ job.Running) error { return s.Cleanup(ctx) }, job.HandlerOptions{})
}
