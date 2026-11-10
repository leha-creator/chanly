package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/audit"
)

// ReversePayout explicitly rejects the acquiring refund route for blogger payouts.
func (s *Service) ReversePayout(context.Context, uuid.UUID) error { return domain.ErrUnsupported }
func (s *Service) AnnulReceipt(context.Context, uuid.UUID) error  { return domain.ErrUnsupported }

// RecordPayoutReturn records two operators' evidence of funds already received.
// It restores the corresponding blogger liability through ledger; it neither
// calls a provider refund endpoint nor changes the historical payout status.
func (s *Service) RecordPayoutReturn(ctx context.Context, e domain.ReturnEvidence) error {
	e.ReceivedAt = e.ReceivedAt.Truncate(time.Microsecond)
	store, ok := s.Store.(domain.ReturnStore)
	ledger, capable := s.Ledger.(domain.ReturnLedger)
	if !ok || !capable {
		return domain.ErrUnsupported
	}
	if e.ID == uuid.Nil || e.DocumentID == uuid.Nil || e.PayoutID == uuid.Nil || e.AdminID == uuid.Nil || e.ApproverID == uuid.Nil || e.AdminID == e.ApproverID || e.Amount <= 0 || e.ReceivedAt.IsZero() || e.ReceivedAt.After(s.Clock.Now()) || strings.TrimSpace(e.Reason) == "" || len([]rune(e.Reason)) > 2000 || (e.Destination != "bank" && e.Destination != "rocketwork_deposit") {
		return domain.ErrInvalidPayout
	}
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		p, err := s.Store.PayoutByID(ctx, e.PayoutID, true)
		if err != nil {
			return err
		}
		if p.Method != domain.MethodRocketwork || p.Status != domain.StatusSucceeded {
			return domain.ErrInvalidPayoutState
		}
		old, err := store.ReturnEvidence(ctx, e.ID)
		if err == nil {
			if old.ID == e.ID && old.PayoutID == e.PayoutID && old.DocumentID == e.DocumentID && old.Amount == e.Amount && old.AdminID == e.AdminID && old.ApproverID == e.ApproverID && old.Destination == e.Destination && old.Reason == e.Reason && old.ReceivedAt.Equal(e.ReceivedAt) {
				return nil
			}
			return domain.ErrInvalidPayout
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		total, err := store.ReturnedAmount(ctx, e.PayoutID)
		if err != nil {
			return err
		}
		if total > p.Amount || e.Amount > p.Amount-total {
			return domain.ErrInvalidPayout
		}
		if err := store.InsertReturnEvidence(ctx, e); err != nil {
			return err
		}
		if err := ledger.RecordPayoutReturn(ctx, p, e); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{Action: "payout.return_evidence", ObjectType: "payout", ObjectID: p.ID.String(), ActorType: audit.ActorAdmin, Reason: &e.Reason, Data: map[string]any{"evidence_id": e.ID, "document_id": e.DocumentID, "admin_id": e.AdminID, "approver_id": e.ApproverID, "amount_kopecks": int64(e.Amount)}})
	})
}
