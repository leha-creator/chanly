// Package jobs — обработчики заданий и входящих событий модуля размещений (07 §2); сценарии
// выполняет app. События MAX разбирает адаптер (max.ParseUpdate).
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/placement/app"
	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
)

// Options — расписание и сроки заданий модуля (07 §8).
type Options struct {
	// PublishRetryWindow — T-03: публикация, взятая позже, завершается P-09 (07 §2, «После простоя»).
	PublishRetryWindow time.Duration
	// ReconcileInterval — T-09: период сверки публикаций.
	ReconcileInterval time.Duration
}

// Register регистрирует виды заданий модуля. Таймеры, публикация и снятие — высокий приоритет
// (07 §2): берутся первыми и попадают в список затронутых после простоя.
func Register(w *job.Worker, svc *app.Service, o Options) {
	if o.PublishRetryWindow <= 0 {
		o.PublishRetryWindow = domain.DefaultControlRules.PublishRetryWindow
	}
	if o.ReconcileInterval <= 0 {
		o.ReconcileInterval = domain.DefaultControlRules.ReconcileInterval
	}
	w.Register(domain.KindPlacementTimer, func(ctx context.Context, r job.Running) error {
		var p domain.TimerPayload
		if err := decode(r, &p); err != nil {
			return err
		}
		return svc.HandleTimer(ctx, p, r.ID)
	}, job.HandlerOptions{Priority: job.PriorityHigh, ObjectRef: objectRef})

	w.Register(domain.KindPublishPlacement, placementHandler(svc.Publish), job.HandlerOptions{
		Priority: job.PriorityHigh, ObjectRef: objectRef,
		Overdue: job.Overdue{After: o.PublishRetryWindow, OnExpired: placementHandler(svc.ExpirePublish)},
	})
	w.Register(domain.KindRemovePlacement, placementHandler(svc.Remove),
		job.HandlerOptions{Priority: job.PriorityHigh, ObjectRef: objectRef})
	// Напоминание о T-01 (R-8 этапа уведомлений): устаревшее завершается без действий.
	w.Register(domain.KindPlacementReminder, placementHandler(svc.Remind), job.HandlerOptions{ObjectRef: objectRef})

	// Сверка (T-09, R-12): следующий запуск создаётся независимо от исхода.
	w.RegisterPeriodic(domain.KindReconcilePublications, job.Every(o.ReconcileInterval), func(ctx context.Context, _ job.Running) error {
		return svc.ReconcilePublications(ctx)
	}, job.HandlerOptions{})
	w.Register(domain.KindCheckPublication, func(ctx context.Context, r job.Running) error {
		var p domain.CheckPublicationPayload
		if err := decode(r, &p); err != nil {
			return err
		}
		return svc.CheckPublication(ctx, p.PublicationID, app.JobRef{ID: r.ID, Attempts: r.Attempts})
	}, job.HandlerOptions{})
}

// RegisterEvents регистрирует обработчики событий MAX о публикациях (R-9): message_edited и
// message_removed. message_created разбирает модуль каналов и передаёт через порт.
func RegisterEvents(d *inbound.Dispatcher, svc *app.Service) {
	d.Handle(inbound.SourceMAX, max.UpdateMessageEdited, handle(func(ctx context.Context, e inbound.Event, u max.Update) error {
		return svc.MessageEdited(ctx, e.ID, u.MessageID(), u.ContentHash(), u.MessageAt())
	}))
	d.Handle(inbound.SourceMAX, max.UpdateMessageRemoved, handle(func(ctx context.Context, e inbound.Event, u max.Update) error {
		return svc.MessageRemoved(ctx, e.ID, u.MessageID(), u.MessageAt())
	}))
}

// placementHandler — обработчик задания с полезной нагрузкой PlacementPayload.
func placementHandler(fn func(ctx context.Context, id uuid.UUID, j app.JobRef) error) job.Handler {
	return func(ctx context.Context, r job.Running) error {
		var p domain.PlacementPayload
		if err := decode(r, &p); err != nil {
			return err
		}
		return fn(ctx, p.PlacementID, app.JobRef{ID: r.ID, Attempts: r.Attempts, Reason: p.Reason})
	}
}

// handle разбирает тело события MAX и переводит app.ErrIgnored в статус ignored.
func handle(fn func(ctx context.Context, e inbound.Event, u max.Update) error) inbound.Handler {
	return func(ctx context.Context, e inbound.Event) error {
		u, err := max.ParseUpdate(e.Payload)
		if err != nil {
			return job.Permanent(fmt.Errorf("parse max update %s: %w", e.EventType, err))
		}
		if u.MessageID() == "" {
			return inbound.ErrIgnore
		}
		err = fn(ctx, e, u)
		if errors.Is(err, app.ErrIgnored) {
			return inbound.ErrIgnore
		}
		return err
	}
}

// objectRef — объект задания для списка затронутых после простоя: placement:<id>.
func objectRef(payload []byte) string {
	var p domain.PlacementPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return ""
	}
	return "placement:" + p.PlacementID.String()
}

func decode(r job.Running, v any) error {
	if err := json.Unmarshal(r.Payload, v); err != nil {
		return job.Permanent(fmt.Errorf("decode %s payload: %w", r.Kind, err))
	}
	return nil
}
