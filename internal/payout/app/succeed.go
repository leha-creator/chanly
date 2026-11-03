package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// apply переносит ответ сервиса о выплате: sent — отметка отправки, succeeded — complete,
// failed — fail. Возвращает итоговое состояние.
func (s *Service) apply(ctx context.Context, id uuid.UUID, pp domain.ProviderPayout) (domain.Status, error) {
	switch pp.Status {
	case domain.StatusSucceeded:
		return domain.StatusSucceeded, s.complete(ctx, id, pp)
	case domain.StatusFailed:
		code := pp.FailureCode
		if !code.Valid() {
			code = domain.FailureProviderError
		}
		return domain.StatusFailed, s.fail(ctx, id, code, pp.FailureMessage)
	}
	return domain.StatusSent, nil
}

// complete — успех выплаты (R-5): в одной транзакции попытка succeeded, проводка payout и P-16.
// Повтор после успеха — no-op. Размещение не accepted — откат и оповещение.
func (s *Service) complete(ctx context.Context, id uuid.UUID, pp domain.ProviderPayout) error {
	const op = "Payout.Complete"
	var (
		p       domain.Payout
		changed bool
	)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		if p, err = s.Store.PayoutByID(ctx, id, true); err != nil {
			return err
		}
		if changed, err = p.MarkSucceeded(pp.Fee, pp.ReceiptURL, p.BankReference, s.Clock.Now()); err != nil || !changed {
			return err
		}
		if err := s.Store.UpdatePayout(ctx, &p); err != nil {
			return err
		}
		if err := s.Ledger.Payout(ctx, p); err != nil {
			return fmt.Errorf("%s: ledger: %w", op, err)
		}
		return s.Placements.MarkPaid(ctx, p.PlacementID, p.ID)
	})
	if errors.Is(err, domain.ErrPlacementState) {
		s.log.ErrorContext(ctx, "payout placement state mismatch", logx.KeyOp, op, KeyPayoutID, id, KeyPlacementID, p.PlacementID)
		s.raise(ctx, alert.LevelError, AlertStateMismatch, payoutObject(id), "payout succeeded but placement is not accepted",
			map[string]any{"payout_id": id.String(), "placement_id": p.PlacementID.String()})
		return job.Permanent(err)
	}
	if errors.Is(err, domain.ErrInvalidPayoutState) && p.Status == domain.StatusFailed {
		// Сервис выплатил то, что у нас отмечено failed: деньги ушли без проводки (R-5).
		s.log.ErrorContext(ctx, "payout state mismatch", logx.KeyOp, op, KeyPayoutID, id, KeyPlacementID, p.PlacementID,
			KeyFailureCode, p.FailureCode, KeyExternalID, p.ExternalID)
		s.raise(ctx, alert.LevelError, AlertStateMismatch, payoutObject(id), "rocketwork paid a payout marked failed",
			map[string]any{"payout_id": id.String(), "placement_id": p.PlacementID.String()})
		return job.Permanent(err)
	}
	if err != nil {
		return err
	}
	if !changed {
		s.log.DebugContext(ctx, "payout already succeeded", logx.KeyOp, op, KeyPayoutID, id)
		return nil
	}
	s.Metrics.PayoutFinished(p.Status, p.Method, p.Amount)
	s.log.InfoContext(ctx, "payout succeeded", logx.KeyOp, op, KeyPayoutID, p.ID, KeyPlacementID, p.PlacementID,
		KeyAttempt, p.Attempt, KeyAmount, int64(p.Amount), "provider_fee", int64(p.ProviderFee), KeyExternalID, p.ExternalID,
		"method", p.Method)
	s.notify(ctx, domain.Notice{Template: domain.NoticePayoutSucceeded, OrganizationID: p.BloggerOrganizationID, ObjectID: p.ID, PlacementID: p.PlacementID,
		Data: map[string]any{"amount": int64(p.Amount), "receipt_url": p.ReceiptURL}})
	s.resolve(ctx, AlertDepositInsufficient, payoutObject(p.ID))
	return nil
}

// fail — окончательный отказ попытки (R-8): размещение остаётся accepted, деньги — на
// blogger_payable. Отказы, требующие решения, — ERROR и оповещение.
func (s *Service) fail(ctx context.Context, id uuid.UUID, code domain.FailureCode, msg string) error {
	const op = "Payout.Fail"
	var (
		p       domain.Payout
		changed bool
	)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		if p, err = s.Store.PayoutByID(ctx, id, true); err != nil {
			return err
		}
		if changed, err = p.MarkFailed(code, msg, s.Clock.Now()); err != nil || !changed {
			return err
		}
		return s.Store.UpdatePayout(ctx, &p)
	})
	if err != nil || !changed {
		return err
	}
	s.Metrics.PayoutFinished(p.Status, p.Method, p.Amount)
	args := []any{logx.KeyOp, op, KeyPayoutID, p.ID, KeyPlacementID, p.PlacementID, KeyAttempt, p.Attempt,
		KeyAmount, int64(p.Amount), KeyFailureCode, code, KeyExternalID, p.ExternalID}
	if code.NeedsAdmin() {
		s.log.ErrorContext(ctx, "payout failed", args...)
		s.raise(ctx, alert.LevelError, AlertPayoutFailed, payoutObject(p.ID), "payout failed: "+string(code),
			map[string]any{"payout_id": p.ID.String(), "placement_id": p.PlacementID.String(), "failure_code": string(code)})
	} else {
		s.log.WarnContext(ctx, "payout failed", args...)
	}
	s.notify(ctx, domain.Notice{Template: domain.NoticePayoutFailed, OrganizationID: p.BloggerOrganizationID, ObjectID: p.ID, PlacementID: p.PlacementID,
		Data: map[string]any{"reason": string(code)}})
	return nil
}
