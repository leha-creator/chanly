package app

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/job"
)

// RequestCancellation freezes forward payment actions before scheduling cancel.
// Only a fresh provider read can establish whether cancellation is permitted.
func (s *Service) RequestCancellation(ctx context.Context, id, adminID uuid.UUID, reason string) error {
	store, ok := s.Store.(domain.WorkflowStore)
	if !ok || s.Config.ProviderScope == nil {
		return domain.ErrProviderDisabled
	}
	if adminID == uuid.Nil || strings.TrimSpace(reason) == "" {
		return domain.ErrInvalidPayout
	}
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		p, err := s.Store.PayoutByID(ctx, id, true)
		if err != nil {
			return err
		}
		w, err := store.Workflow(ctx, id, true)
		if err != nil {
			return err
		}
		if w.Phase == "canceled" {
			return nil
		}
		if p.Status.Final() || w.TaskID == nil || w.Scope != *s.Config.ProviderScope {
			return domain.ErrInvalidPayoutState
		}
		w.Phase, w.UpdatedAt = "cancel_requested", s.Clock.Now()
		if err := store.UpdateWorkflow(ctx, w); err != nil {
			return err
		}
		if err := s.schedule(ctx, job.Job{Kind: KindPollPayout, DedupeKey: PollPayoutKey(id), Payload: PayoutPayload{PayoutID: id}, RunAt: s.Clock.Now(), MaxAttempts: PollAttempts}); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{Action: "payout.cancel_requested", ObjectType: "payout", ObjectID: id.String(), ActorType: audit.ActorAdmin, Reason: &reason, Data: map[string]any{"admin_id": adminID.String()}})
	})
}
