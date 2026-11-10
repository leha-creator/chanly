package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Retry — повтор выплаты администратором (S-19, R-9): только из последней попытки failed
// размещения в accepted; новая попытка attempt+1 с новым ключом и send_payout.
func (s *Service) Retry(ctx context.Context, payoutID, adminID uuid.UUID, reason string) (domain.Payout, error) {
	const op = "Payout.Retry"
	if strings.TrimSpace(reason) == "" {
		return domain.Payout{}, fmt.Errorf("%w: reason is required", domain.ErrInvalidPayout)
	}
	var next domain.Payout
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		p, err := s.Store.PayoutByID(ctx, payoutID, true)
		if err != nil {
			return err
		}
		if store, ok := s.Store.(domain.WorkflowStore); ok {
			w, werr := store.Workflow(ctx, p.ID, false)
			if werr == nil && w.Phase != "canceled" {
				next = p
				if err := s.schedule(ctx, job.Job{Kind: KindPollPayout, DedupeKey: PollPayoutKey(p.ID), Payload: PayoutPayload{p.ID}, RunAt: s.Clock.Now(), MaxAttempts: PollAttempts}); err != nil {
					return err
				}
				return s.Audit.Record(ctx, audit.Entry{Action: "payout.reconcile", ObjectType: "payout", ObjectID: p.ID.String(), Reason: &reason, ActorType: audit.ActorAdmin, OrganizationID: &p.BloggerOrganizationID, Data: map[string]any{"identity_preserved": true}})
			}
			if werr != nil && !errors.Is(werr, domain.ErrNotFound) {
				return werr
			}
		}
		latest, err := s.Store.LatestPayout(ctx, p.PlacementID, true)
		if err != nil {
			return err
		}
		if latest.ID != p.ID {
			return fmt.Errorf("%w: attempt %d is not the latest (%d)", domain.ErrPayoutNotRetryable, p.Attempt, latest.Attempt)
		}
		info, err := s.Placements.Info(ctx, p.PlacementID)
		if err != nil {
			return err
		}
		if info.Status != domain.PlacementStatusAccepted {
			return fmt.Errorf("%w: placement is %s", domain.ErrPayoutNotRetryable, info.Status)
		}
		c, err := s.Store.ContractorByOrg(ctx, p.BloggerOrganizationID, false)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if next, err = p.NextAttempt(ids.New(), c.ID, adminID, s.Clock.Now()); err != nil {
			return err
		}
		if err := s.Store.InsertPayout(ctx, next); err != nil {
			return err
		}
		if err := s.Jobs.Enqueue(ctx, job.Job{Kind: KindSendPayout, DedupeKey: SendKey(next.ID),
			Payload: PayoutPayload{PayoutID: next.ID}, MaxAttempts: SendAttempts}); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{Action: ActionRetry, ObjectType: "payout", ObjectID: next.ID.String(),
			Reason: &reason, ActorType: audit.ActorAdmin, OrganizationID: &p.BloggerOrganizationID,
			Data: map[string]any{"previous_payout_id": p.ID.String(), "attempt": next.Attempt, "failure_code": string(p.FailureCode)}})
	})
	if err != nil {
		return domain.Payout{}, err
	}
	s.log.WarnContext(ctx, "admin payout retry", logx.KeyOp, op, KeyPayoutID, next.ID, "previous_payout_id", payoutID,
		KeyPlacementID, next.PlacementID, KeyAttempt, next.Attempt, "admin_user_id", adminID)
	return next, nil
}

// MarkLegalPaid — администратор перевёл деньги юрлицу (R-3, A-36): succeeded, проводка
// blogger_payable → bank и P-16 в одной транзакции, запись в журнал действий.
func (s *Service) MarkLegalPaid(ctx context.Context, payoutID, adminID, approverID uuid.UUID, bankRef, reason string) (domain.Payout, error) {
	const op = "Payout.MarkLegalPaid"
	bankRef, reason = strings.TrimSpace(bankRef), strings.TrimSpace(reason)
	if bankRef == "" || reason == "" {
		return domain.Payout{}, fmt.Errorf("%w: bank reference and reason are required", domain.ErrInvalidPayout)
	}
	var (
		p       domain.Payout
		changed bool
	)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		if p, err = s.Store.PayoutByID(ctx, payoutID, true); err != nil {
			return err
		}
		if p.Method != domain.MethodBankTransfer {
			return fmt.Errorf("%w: %s payout is paid by rocketwork", domain.ErrInvalidPayoutState, p.Method)
		}
		if s.NeedsApproval(p.Amount) && (approverID == uuid.Nil || approverID == adminID) {
			s.log.WarnContext(ctx, "admin payout mark paid refused", logx.KeyOp, op, KeyPayoutID, p.ID, KeyAmount, int64(p.Amount),
				"admin_user_id", adminID, "reason", "second_approval")
			return domain.ErrSecondApproval
		}
		if changed, err = p.MarkSucceeded(0, "", bankRef, s.Clock.Now()); err != nil || !changed {
			return err
		}
		if err := s.Store.UpdatePayout(ctx, &p); err != nil {
			return err
		}
		if err := s.Ledger.LegalPayout(ctx, p, adminID, reason+" (п/п "+bankRef+")"); err != nil {
			return fmt.Errorf("%s: ledger: %w", op, err)
		}
		if err := s.Placements.MarkPaid(ctx, p.PlacementID, p.ID); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{Action: ActionMarkPaid, ObjectType: "payout", ObjectID: p.ID.String(),
			Reason: &reason, ActorType: audit.ActorAdmin, OrganizationID: &p.BloggerOrganizationID,
			Data: legalPaidData(bankRef, p.Amount, approverID)})
	})
	if err != nil {
		return domain.Payout{}, err
	}
	if changed {
		s.Metrics.PayoutFinished(p.Status, p.Method, p.Amount)
		s.notify(ctx, domain.Notice{Template: domain.NoticePayoutSucceeded, OrganizationID: p.BloggerOrganizationID, ObjectID: p.ID, PlacementID: p.PlacementID,
			Data: map[string]any{"amount": int64(p.Amount)}})
	}
	s.log.WarnContext(ctx, "admin payout marked paid", logx.KeyOp, op, KeyPayoutID, p.ID, KeyPlacementID, p.PlacementID,
		KeyAmount, int64(p.Amount), "admin_user_id", adminID, "replayed", !changed)
	return p, nil
}

func legalPaidData(bankRef string, amount money.Kopecks, approverID uuid.UUID) map[string]any {
	data := map[string]any{"bank_reference": bankRef, "amount": int64(amount)}
	if approverID != uuid.Nil {
		data["approver_id"] = approverID.String()
	}
	return data
}

// AdminList — реестр выплат для команды администратора.
func (s *Service) AdminList(ctx context.Context, f domain.AdminFilter) ([]domain.Payout, error) {
	return s.Store.AdminList(ctx, f)
}

// Payout — попытка по идентификатору.
func (s *Service) Payout(ctx context.Context, id uuid.UUID) (domain.Payout, error) {
	return s.Store.PayoutByID(ctx, id, false)
}
