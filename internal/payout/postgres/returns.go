package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/payout/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/money"
)

func (s *Store) ReturnEvidence(ctx context.Context, id uuid.UUID) (domain.ReturnEvidence, error) {
	r, err := s.q(ctx).GetReturnEvidence(ctx, id)
	if err != nil {
		return domain.ReturnEvidence{}, s.workflowError(ctx, "ReturnEvidence", err)
	}
	return domain.ReturnEvidence{ID: r.ID, PayoutID: r.PayoutID, DocumentID: r.DocumentID, AdminID: r.AdminID, ApproverID: r.ApproverID, Amount: money.Kopecks(r.Amount), Destination: r.Destination, Reason: r.Reason, ReceivedAt: r.ReceivedAt}, nil
}
func (s *Store) ReturnedAmount(ctx context.Context, id uuid.UUID) (money.Kopecks, error) {
	n, err := s.q(ctx).ReturnedAmount(ctx, id)
	if err != nil {
		return 0, s.workflowError(ctx, "ReturnedAmount", err)
	}
	return money.Kopecks(n), nil
}
func (s *Store) InsertReturnEvidence(ctx context.Context, e domain.ReturnEvidence) error {
	if err := s.q(ctx).InsertReturnEvidence(ctx, sqlc.InsertReturnEvidenceParams{ID: e.ID, PayoutID: e.PayoutID, DocumentID: e.DocumentID, AdminID: e.AdminID, ApproverID: e.ApproverID, Amount: int64(e.Amount), Destination: e.Destination, Reason: e.Reason, ReceivedAt: e.ReceivedAt}); err != nil {
		return s.workflowError(ctx, "InsertReturnEvidence", err)
	}
	return nil
}
