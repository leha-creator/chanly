package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// PayoutRequest — создать выплату по принятому размещению (R-4).
type PayoutRequest struct {
	PlacementID  uuid.UUID
	BloggerOrgID uuid.UUID
	Amount       money.Kopecks
}

// Create создаёт выплату в транзакции вызывающего (I-10): переход в accepted, проводка
// settlement и выплата фиксируются вместе. Способ — по типу лица (R-3); для rocketwork —
// задание send_payout (I-13). Живая выплата уже есть — её идентификатор без новой строки.
func (s *Service) Create(ctx context.Context, r PayoutRequest) (uuid.UUID, error) {
	const op = "Payout.Create"
	if r.Amount <= 0 {
		return uuid.Nil, fmt.Errorf("%w: amount %d", domain.ErrInvalidPayout, r.Amount)
	}
	latest, err := s.Store.LatestPayout(ctx, r.PlacementID, true)
	switch {
	case err == nil && latest.Status != domain.StatusFailed:
		s.log.DebugContext(ctx, "payout already exists", logx.KeyOp, op, KeyPlacementID, r.PlacementID, KeyPayoutID, latest.ID,
			"status", latest.Status)
		return latest.ID, nil
	case err != nil && !errors.Is(err, domain.ErrNotFound):
		return uuid.Nil, err
	}
	payee, err := s.Organizations.ForPayout(ctx, r.BloggerOrgID)
	if err != nil {
		return uuid.Nil, err
	}
	method := domain.MethodFor(payee.LegalType)
	contractorID := uuid.Nil
	if method == domain.MethodRocketwork {
		c, err := s.Store.ContractorByOrg(ctx, r.BloggerOrgID, false)
		if err != nil {
			return uuid.Nil, fmt.Errorf("%s: contractor of %s: %w", op, r.BloggerOrgID, err)
		}
		contractorID = c.ID
	}
	now := s.Clock.Now()
	p, err := domain.NewPayout(ids.New(), r.PlacementID, r.BloggerOrgID, method, contractorID, r.Amount, now)
	if err != nil {
		return uuid.Nil, err
	}
	if latest.ID != uuid.Nil {
		p.Attempt = latest.Attempt + 1
	}
	if err := s.Store.InsertPayout(ctx, p); err != nil {
		return uuid.Nil, err
	}
	if method == domain.MethodRocketwork {
		if err := s.Jobs.Enqueue(ctx, job.Job{Kind: KindSendPayout, DedupeKey: SendKey(p.ID),
			Payload: PayoutPayload{PayoutID: p.ID}, MaxAttempts: SendAttempts}); err != nil {
			return uuid.Nil, err
		}
	}
	s.log.InfoContext(ctx, "payout created", logx.KeyOp, op, KeyPayoutID, p.ID, KeyPlacementID, p.PlacementID,
		logx.KeyOrganizationID, p.BloggerOrganizationID, "method", p.Method, KeyAmount, int64(p.Amount), KeyAttempt, p.Attempt,
		KeyIdempotencyKey, p.IdempotencyKey)
	if payee.LegalType == domain.SelfEmployed {
		s.checkNPD(ctx, p)
	}
	return p.ID, nil
}

// checkNPD — предупреждение о годовом лимите самозанятого (R-13). Не блокирует выплату.
func (s *Service) checkNPD(ctx context.Context, p domain.Payout) {
	from, to := domain.YearBounds(s.Clock.Now())
	sum, err := s.Store.SucceededSum(ctx, p.BloggerOrganizationID, from, to)
	if err != nil {
		s.log.WarnContext(ctx, "npd income check failed", logx.KeyOp, "Payout.CheckNPD", logx.KeyError, err.Error())
		return
	}
	if !domain.NPDWarning(sum, p.Amount, s.Config.NPDAnnualLimit) {
		return
	}
	s.log.WarnContext(ctx, "npd income limit approaching", logx.KeyOp, "Payout.CheckNPD", logx.KeyOrganizationID,
		p.BloggerOrganizationID, "year_sum", int64(sum), KeyAmount, int64(p.Amount), "limit", int64(s.Config.NPDAnnualLimit))
	s.raise(ctx, alert.LevelWarn, AlertNPDLimit, organizationObject(p.BloggerOrganizationID),
		"self-employed blogger is close to the annual income limit",
		map[string]any{"organization_id": p.BloggerOrganizationID.String(), "year_sum": int64(sum), "amount": int64(p.Amount)})
}
