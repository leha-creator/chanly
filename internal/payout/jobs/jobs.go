// Package jobs — обработчики заданий и входящих событий модуля выплат (07 §2):
// ensure_contractor, poll_contractor, send_payout, poll_payout, check_rocketwork_deposit и
// события Рокет Ворк (contractor_status, payout_status). Сценарии выполняет app.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/payout/app"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Options — расписание периодических заданий.
type Options struct {
	// DepositCheckInterval — check_rocketwork_deposit (ROCKETWORK_DEPOSIT_CHECK_INTERVAL, R-12).
	DepositCheckInterval time.Duration
}

// Register регистрирует виды заданий модуля и обработчики событий Рокет Ворк.
//
// send_payout — max_attempts 10 (ставит app), после исчерпания — failed provider_error и
// оповещение. Нехватка депозита и режим disabled откладывают задание без траты попытки.
// poll_payout и poll_contractor — без предела, расписание — job.Postpone из app.
func Register(w *job.Worker, d *inbound.Dispatcher, svc *app.Service, opts Options) {
	w.Register(app.KindSendPayout, func(ctx context.Context, r job.Running) error {
		p, err := decodePayout(r)
		if err != nil {
			return err
		}
		return svc.Send(ctx, p.PayoutID, r.Attempts+1)
	}, job.HandlerOptions{
		ObjectRef: payoutRef,
		OnExhausted: func(ctx context.Context, r job.Running, err error) {
			if p, derr := decodePayout(r); derr == nil {
				svc.SendExhausted(ctx, p.PayoutID, err)
			}
		},
	})
	w.Register(app.KindPollPayout, func(ctx context.Context, r job.Running) error {
		p, err := decodePayout(r)
		if err != nil {
			return err
		}
		return svc.PollPayout(ctx, p.PayoutID)
	}, job.HandlerOptions{ObjectRef: payoutRef})
	w.Register(app.KindPollContractor, func(ctx context.Context, r job.Running) error {
		var p app.ContractorPayload
		if err := decode(r, &p); err != nil {
			return err
		}
		return svc.PollContractor(ctx, p.ContractorID)
	}, job.HandlerOptions{ObjectRef: func(raw []byte) string {
		var p app.ContractorPayload
		if json.Unmarshal(raw, &p) != nil || p.ContractorID == uuid.Nil {
			return ""
		}
		return "rocketwork_contractor:" + p.ContractorID.String()
	}})
	w.Register(app.KindEnsureContractor, func(ctx context.Context, r job.Running) error {
		var p app.OrgPayload
		if err := decode(r, &p); err != nil {
			return err
		}
		return svc.EnsureContractor(ctx, p.OrganizationID)
	}, job.HandlerOptions{
		OnExhausted: func(ctx context.Context, r job.Running, err error) {
			var p app.OrgPayload
			if decode(r, &p) == nil {
				svc.EnsureExhausted(ctx, p.OrganizationID, err)
			}
		},
	})
	interval := opts.DepositCheckInterval
	if interval <= 0 {
		interval = time.Hour
	}
	w.RegisterPeriodic(app.KindCheckDeposit, job.Every(interval), func(ctx context.Context, _ job.Running) error {
		if !svc.Config.ProviderEnabled {
			return nil // режим disabled: сверять нечего (R-1)
		}
		_, err := svc.CheckDeposit(ctx)
		return err
	}, job.HandlerOptions{})

	if d != nil {
		RegisterEvents(d, svc)
	}
}

// RegisterEvents — события Рокет Ворк в общем диспетчере входящих событий.
func RegisterEvents(d *inbound.Dispatcher, svc *app.Service) {
	d.Handle(inbound.SourceRocketWork, string(domain.WebhookContractorStatus), event(svc, svc.ApplyContractor))
	d.Handle(inbound.SourceRocketWork, string(domain.WebhookPayoutStatus), event(svc, svc.ApplyPayout))
}

// event разбирает тело события; неизвестный объект — ignored (app.ErrIgnored).
func event(svc *app.Service, apply func(ctx context.Context, externalID string) error) inbound.Handler {
	const op = "Payout.RocketWorkEvent"
	return func(ctx context.Context, e inbound.Event) error {
		var p app.EventPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil || p.ExternalID == "" {
			return job.Permanent(fmt.Errorf("decode rocketwork event: %v", err))
		}
		err := apply(ctx, p.ExternalID)
		if errors.Is(err, domain.ErrNotFound) {
			svc.Log.DebugContext(ctx, "rocketwork event ignored", logx.KeyOp, op, "event_type", e.EventType,
				"external_id", p.ExternalID)
			return inbound.ErrIgnore
		}
		if err == nil {
			svc.Log.InfoContext(ctx, "rocketwork event processed", logx.KeyOp, op, "event_type", e.EventType,
				"external_id", p.ExternalID, "event_id", p.EventID)
		}
		return err
	}
}

func payoutRef(raw []byte) string {
	var p app.PayoutPayload
	if json.Unmarshal(raw, &p) != nil || p.PayoutID == uuid.Nil {
		return ""
	}
	return "payout:" + p.PayoutID.String()
}

func decodePayout(r job.Running) (app.PayoutPayload, error) {
	var p app.PayoutPayload
	if err := decode(r, &p); err != nil {
		return p, err
	}
	if p.PayoutID == uuid.Nil {
		return p, job.Permanent(fmt.Errorf("%s: empty payout id", r.Kind))
	}
	return p, nil
}

func decode(r job.Running, v any) error {
	if err := json.Unmarshal(r.Payload, v); err != nil {
		return job.Permanent(fmt.Errorf("decode %s payload: %w", r.Kind, err))
	}
	return nil
}
