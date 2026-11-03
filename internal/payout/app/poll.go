package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// PollPayout — задание poll_payout (R-10): до окончательного статуса каждые PollFast первый
// час после отправки, затем раз в PollSlow. Опрос делается всегда, вебхук только ускоряет.
func (s *Service) PollPayout(ctx context.Context, payoutID uuid.UUID) error {
	const op = "Payout.PollPayout"
	p, err := s.Store.PayoutByID(ctx, payoutID, false)
	if errors.Is(err, domain.ErrNotFound) {
		return job.Permanent(err)
	}
	if err != nil {
		return err
	}
	if p.Status.Final() || p.ExternalID == "" {
		s.log.DebugContext(ctx, "payout poll stopped", logx.KeyOp, op, KeyPayoutID, p.ID, "status", p.Status)
		return nil
	}
	pp, err := s.Provider.GetPayout(ctx, p.ExternalID)
	if err != nil {
		return s.providerWait(ctx, op, err)
	}
	status, err := s.apply(ctx, p.ID, pp)
	if err != nil || status.Final() {
		return err
	}
	now := s.Clock.Now()
	since := p.CreatedAt
	if p.SentAt != nil {
		since = *p.SentAt
	}
	next := domain.PollDelay(since, now, s.Config.PollFast, s.Config.PollSlow)
	s.log.DebugContext(ctx, "payout poll", logx.KeyOp, op, KeyPayoutID, p.ID, KeyExternalID, p.ExternalID,
		"next_run_at", now.Add(next))
	return job.Postpone(fmt.Errorf("payout %s is still sent", p.ID), next)
}

// ApplyPayout — свежее состояние выплаты из вебхука (R-10). Неизвестная выплата — ErrNotFound.
func (s *Service) ApplyPayout(ctx context.Context, externalID string) error {
	const op = "Payout.ApplyPayout"
	p, err := s.Store.PayoutByExternalID(ctx, externalID, false)
	if err != nil {
		return err
	}
	if p.Status.Final() {
		s.log.DebugContext(ctx, "payout event for final payout", logx.KeyOp, op, KeyPayoutID, p.ID, "status", p.Status)
		return nil
	}
	pp, err := s.Provider.GetPayout(ctx, externalID)
	if err != nil {
		return s.providerWait(ctx, op, err)
	}
	_, err = s.apply(ctx, p.ID, pp)
	return err
}
