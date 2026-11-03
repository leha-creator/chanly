// Package jobs — обработчики заданий и входящих событий модуля каналов.
// События MAX разбирает адаптер (max.ParseUpdate); сценарии выполняет app.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
)

// Register регистрирует обработчики событий MAX в dispatcher и виды заданий модуля (R-16).
func Register(w *job.Worker, d *inbound.Dispatcher, svc *app.Service) {
	d.Handle(inbound.SourceMAX, max.UpdateBotAdded, handle(func(ctx context.Context, ev app.EventRef, u max.Update) error {
		return svc.BotAdded(ctx, ev, u.ChatID, u.UserID(), u.IsChannel)
	}))
	d.Handle(inbound.SourceMAX, max.UpdateBotRemoved, handle(func(ctx context.Context, _ app.EventRef, u max.Update) error {
		return svc.BotRemoved(ctx, u.ChatID)
	}))
	d.Handle(inbound.SourceMAX, max.UpdateBotAdminPermissionsChanged, handle(func(ctx context.Context, ev app.EventRef, u max.Update) error {
		return svc.RightsChanged(ctx, ev, u.ChatID, u.UserID(), u.IsChannel)
	}))
	d.Handle(inbound.SourceMAX, max.UpdateChatTitleChanged, handle(func(ctx context.Context, _ app.EventRef, u max.Update) error {
		return svc.TitleChanged(ctx, u.ChatID, u.Title)
	}))
	d.Handle(inbound.SourceMAX, max.UpdateMessageCreated, handle(func(ctx context.Context, ev app.EventRef, u max.Update) error {
		switch {
		case u.InDialog():
			return svc.DialogMessage(ctx, u.SenderID())
		case u.InChannel():
			return svc.ChannelPostCreated(ctx, ev, app.ChannelPost{
				MaxChatID: u.ChatID, MessageID: u.Message.Body.MID, Text: u.Message.Body.Text,
				SenderID: u.SenderID(), At: u.MessageAt(),
			})
		}
		return app.ErrIgnored
	}))

	w.Register(app.KindRefreshChannelInfo, func(ctx context.Context, r job.Running) error {
		var p app.RefreshPayload
		if err := decode(r, &p); err != nil {
			return err
		}
		return svc.RefreshChannelInfo(ctx, p.ChannelID)
	}, job.HandlerOptions{})
	w.Register(app.KindDeleteMaxMessage, func(ctx context.Context, r job.Running) error {
		var p app.DeletePayload
		if err := decode(r, &p); err != nil {
			return err
		}
		return svc.DeleteMessage(ctx, p)
	}, job.HandlerOptions{})
	w.Register(app.KindLeaveMaxChat, func(ctx context.Context, r job.Running) error {
		var p app.LeavePayload
		if err := decode(r, &p); err != nil {
			return err
		}
		return svc.LeaveChat(ctx, p.MaxChatID)
	}, job.HandlerOptions{})
	// Ответ в диалоге не важен для работы площадки: его сбой не поднимает оповещение.
	w.Register(app.KindReplyBotDialog, func(ctx context.Context, r job.Running) error {
		var p app.ReplyPayload
		if err := decode(r, &p); err != nil {
			return err
		}
		return svc.ReplyDialog(ctx, p.MaxUserID)
	}, job.HandlerOptions{NoFailureAlert: true})
	// Суточные метрики (R-13, R-20): раз в сутки в 00:05 по Москве ставятся задания по
	// каналам, распределённые по суткам. Канал, которого нет в MAX (404), — Permanent:
	// его отключат события бота.
	w.RegisterPeriodic(app.KindScheduleChannelMetrics, job.DailyAt(0, 5), func(ctx context.Context, _ job.Running) error {
		return svc.ScheduleMetrics(ctx)
	}, job.HandlerOptions{})
	w.Register(app.KindRefreshChannelMetrics, func(ctx context.Context, r job.Running) error {
		var p app.MetricsPayload
		if err := decode(r, &p); err != nil {
			return err
		}
		return svc.RefreshMetrics(ctx, p)
	}, job.HandlerOptions{})
	// Раз в сутки по Москве в 05:00, после cleanup (R-12).
	w.RegisterPeriodic(app.KindReleaseUnclaimedChannels, job.DailyAt(5, 0), func(ctx context.Context, _ job.Running) error {
		return svc.ReleaseUnclaimed(ctx)
	}, job.HandlerOptions{})
}

// handle разбирает тело события MAX и переводит ErrIgnored в статус ignored.
func handle(fn func(ctx context.Context, ev app.EventRef, u max.Update) error) inbound.Handler {
	return func(ctx context.Context, e inbound.Event) error {
		u, err := max.ParseUpdate(e.Payload)
		if err != nil {
			return job.Permanent(fmt.Errorf("parse max update %s: %w", e.EventType, err))
		}
		err = fn(ctx, app.EventRef{ID: e.ID, ReceivedAt: e.ReceivedAt}, u)
		if errors.Is(err, app.ErrIgnored) {
			return inbound.ErrIgnore
		}
		return err
	}
}

func decode(r job.Running, v any) error {
	if err := json.Unmarshal(r.Payload, v); err != nil {
		return job.Permanent(fmt.Errorf("decode %s payload: %w", r.Kind, err))
	}
	return nil
}
