package app

import (
	"context"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
)

// RefreshExecutors schedules authoritative lookups using our own payee data.
// Callback INN/phone never enter the general inbound journal. Scheduling is
// atomic, so an early event is retained until all current contractors are queued.
func (s *Service) RefreshExecutors(ctx context.Context) error {
	store, ok := s.Store.(interface {
		ContractorIDsAfter(context.Context, uuid.UUID) ([]uuid.UUID, error)
	})
	if !ok || s.Config.ProviderScope == nil {
		return domain.ErrProviderDisabled
	}
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		after := uuid.Nil
		for {
			ids, err := store.ContractorIDsAfter(ctx, after)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if err := s.schedule(ctx, job.Job{Kind: KindPollContractor, DedupeKey: PollContractorKey(id), Payload: ContractorPayload{ContractorID: id}, RunAt: s.Clock.Now(), MaxAttempts: PollAttempts}); err != nil {
					return err
				}
			}
			if len(ids) < 100 {
				return nil
			}
			after = ids[len(ids)-1]
		}
	})
}
