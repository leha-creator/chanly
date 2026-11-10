package app

import (
	"context"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/audit"
)

// PrepareWithdrawal accepts only the configured own-account destination. HTTP
// and CLI must authenticate both administrators before invoking this command.
func (s *Service) PrepareWithdrawal(ctx context.Context, id uuid.UUID, terms domain.WithdrawalTerms) (domain.Withdrawal, error) {
	store, ok := s.Store.(domain.WithdrawalStore)
	if !ok || !s.Config.ProviderEnabled || s.Config.ProviderScope == nil || s.Config.WithdrawalDestination == nil {
		return domain.Withdrawal{}, domain.ErrProviderDisabled
	}
	if id == uuid.Nil || terms.Destination != *s.Config.WithdrawalDestination {
		return domain.Withdrawal{}, domain.ErrInvalidPayout
	}
	var w domain.Withdrawal
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		w, err = store.PrepareWithdrawal(ctx, domain.Withdrawal{ID: id, Scope: *s.Config.ProviderScope, Terms: terms})
		if err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{Action: "rocketwork.withdrawal_prepared", ObjectType: "rocketwork_withdrawal", ObjectID: id.String(), ActorType: audit.ActorAdmin, Reason: &terms.Reason, Data: map[string]any{"admin_id": terms.AdminID, "approver_id": terms.ApproverID, "amount_kopecks": int64(terms.Amount), "terms_hash": w.Hash}})
	})
	return w, err
}

func (s *Service) ReconcileWithdrawal(ctx context.Context, id uuid.UUID) error {
	store, ok := s.Store.(domain.WithdrawalStore)
	api, capable := s.Provider.(domain.WithdrawalProvider)
	reader, readable := s.Provider.(domain.TaskProvider)
	ledger, bookable := s.Ledger.(domain.WithdrawalLedger)
	if !ok || !capable || !readable || !bookable || !s.Config.ProviderEnabled || s.Config.ProviderScope == nil {
		return domain.ErrProviderDisabled
	}
	w, err := store.Withdrawal(ctx, id, false)
	if err != nil {
		return err
	}
	if w.Scope != *s.Config.ProviderScope {
		return domain.ErrInvalidPayout
	}
	if w.State == "settled" {
		return nil
	}
	externalID := "withdrawal:" + id.String()
	var t domain.ProviderTask
	if w.TaskID == nil {
		found := false
		t, found, err = reader.FindTask(ctx, externalID)
		if err != nil {
			return err
		}
		if !found {
			if w.State != "prepared" {
				return s.workflowWait("withdrawal outcome unknown; lookup only")
			}
			if s.Config.WithdrawalDestination == nil || *s.Config.WithdrawalDestination != w.Terms.Destination {
				return domain.ErrInvalidPayout
			}
			claimed, err := store.ClaimWithdrawal(ctx, id)
			if err != nil {
				return err
			}
			if !claimed {
				return s.workflowWait("withdrawal already claimed")
			}
			s.log.InfoContext(ctx, "rocketwork withdrawal started", "withdrawal_id", id)
			t, err = api.CreateWithdrawal(ctx, externalID, w.Terms)
			if err != nil {
				return s.workflowWait("withdrawal write not confirmed")
			}
		} else if w.State == "prepared" {
			claimed, err := store.ClaimWithdrawal(ctx, id)
			if err != nil {
				return err
			}
			if !claimed {
				return s.workflowWait("withdrawal claimed elsewhere")
			}
		}
	} else {
		t, err = reader.ReadTask(ctx, *w.TaskID)
		if err != nil {
			return err
		}
	}
	if t.ID <= 0 || (w.TaskID != nil && *w.TaskID != t.ID) || t.ExternalID != externalID || t.Type != "Withdrawal" || t.ExecutorID != w.Terms.Destination.ExecutorID || t.ExecutorINN != w.Terms.Destination.CompanyINN || t.EmploymentKind != "self" || t.Amount != w.Terms.Amount {
		return domain.ErrInvalidPayout
	}
	if w.TaskID == nil {
		if err := store.AttachWithdrawal(ctx, id, t.ID); err != nil {
			return err
		}
	}
	if t.Status != "paid" {
		return s.workflowWait("withdrawal awaiting provider payment")
	}
	if t.Commission == nil {
		return s.workflowWait("withdrawal paid; final commission pending")
	}
	if *t.Commission < 0 || *t.Commission > w.Terms.MaxCommission {
		return domain.ErrInvalidPayout
	}
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		current, err := store.Withdrawal(ctx, id, true)
		if err != nil {
			return err
		}
		if current.State == "settled" {
			return nil
		}
		if err := ledger.Withdrawal(ctx, current, *t.Commission); err != nil {
			return err
		}
		if err := store.SettleWithdrawal(ctx, id); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "rocketwork withdrawal reconciled", "withdrawal_id", id)
		return nil
	})
}
