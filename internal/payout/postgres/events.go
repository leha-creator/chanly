package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/payout/postgres/sqlc"
)

func (s *Store) ContractorIDsAfter(ctx context.Context, after uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.q(ctx).ContractorIDsAfter(ctx, after)
	if err != nil {
		return nil, s.workflowError(ctx, "ContractorIDsAfter", err)
	}
	return rows, nil
}

func (s *Store) PrepareEventCommand(ctx context.Context, c domain.EventCommand) (domain.EventCommand, error) {
	r, err := s.q(ctx).PrepareEventCommand(ctx, sqlc.PrepareEventCommandParams{ID: c.ID, Environment: c.Scope.Environment, AccountKey: c.Scope.Account, Action: c.Action, PayloadHash: c.Hash, SubscriptionID: c.SubscriptionID})
	if err != nil {
		return domain.EventCommand{}, s.workflowError(ctx, "PrepareEventCommand", err)
	}
	return domain.EventCommand{ID: r.ID, Scope: c.Scope, Action: r.Action, Hash: r.PayloadHash, State: r.State, SubscriptionID: r.SubscriptionID}, nil
}
func (s *Store) ClaimEventCommand(ctx context.Context, id uuid.UUID) (bool, error) {
	n, err := s.q(ctx).ClaimEventCommand(ctx, id)
	if err != nil {
		return false, s.workflowError(ctx, "ClaimEventCommand", err)
	}
	return n == 1, nil
}
func (s *Store) ResolveEventCommand(ctx context.Context, id uuid.UUID, subscriptionID int64) error {
	if err := s.q(ctx).ResolveEventCommand(ctx, sqlc.ResolveEventCommandParams{ID: id, SubscriptionID: subscriptionID}); err != nil {
		return s.workflowError(ctx, "ResolveEventCommand", err)
	}
	return nil
}
func (s *Store) EventCursor(ctx context.Context, scope domain.ProviderScope) (time.Time, error) {
	t, err := s.q(ctx).EventCursor(ctx, sqlc.EventCursorParams{Environment: scope.Environment, AccountKey: scope.Account})
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, s.workflowError(ctx, "EventCursor", err)
	}
	return t, nil
}
func (s *Store) AdvanceEventCursor(ctx context.Context, scope domain.ProviderScope, through time.Time) error {
	if err := s.q(ctx).AdvanceEventCursor(ctx, sqlc.AdvanceEventCursorParams{Environment: scope.Environment, AccountKey: scope.Account, ThroughAt: through}); err != nil {
		return s.workflowError(ctx, "AdvanceEventCursor", err)
	}
	return nil
}
