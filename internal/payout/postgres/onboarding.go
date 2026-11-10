package postgres

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/payout/postgres/sqlc"
)

var _ domain.OnboardingStore = (*Store)(nil)

func onboardingOf(r sqlc.RocketworkOnboarding) (domain.Onboarding, error) {
	o := domain.Onboarding{Scope: domain.ProviderScope{Environment: r.Environment, Account: r.AccountKey}, OrganizationID: r.OrganizationID, Action: r.Action, State: domain.OperationState(r.State), PayloadHash: r.PayloadHash, CorrelationID: r.CorrelationID, Result: r.Result, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	if err := json.Unmarshal(r.Payload, &o.Input); err != nil {
		return domain.Onboarding{}, domain.ErrInvalidPayout
	}
	_, hash, err := o.Input.Snapshot(o.Action)
	if err != nil || hash != o.PayloadHash {
		return domain.Onboarding{}, domain.ErrInvalidPayout
	}
	return o, nil
}
func (s *Store) PrepareOnboarding(ctx context.Context, o domain.Onboarding) (domain.Onboarding, error) {
	if err := o.Scope.Validate(); err != nil {
		return domain.Onboarding{}, err
	}
	body, hash, err := o.Input.Snapshot(o.Action)
	if err != nil {
		return domain.Onboarding{}, err
	}
	r, err := s.q(ctx).PrepareRocketworkOnboarding(ctx, sqlc.PrepareRocketworkOnboardingParams{Environment: o.Scope.Environment, AccountKey: o.Scope.Account, OrganizationID: o.OrganizationID, Action: o.Action, Payload: body, PayloadHash: hash, CorrelationID: o.CorrelationID, CreatedAt: o.CreatedAt})
	if err != nil {
		return domain.Onboarding{}, s.workflowError(ctx, "prepare_onboarding", err)
	}
	s.log.InfoContext(ctx, "rocketwork onboarding prepared", "organization_id", o.OrganizationID, "action", o.Action)
	return onboardingOf(r)
}
func (s *Store) Onboarding(ctx context.Context, scope domain.ProviderScope, org uuid.UUID, action string) (domain.Onboarding, error) {
	r, err := s.q(ctx).GetRocketworkOnboarding(ctx, sqlc.GetRocketworkOnboardingParams{Environment: scope.Environment, AccountKey: scope.Account, OrganizationID: org, Action: action})
	if err != nil {
		return domain.Onboarding{}, s.workflowError(ctx, "get_onboarding", err)
	}
	return onboardingOf(r)
}
func (s *Store) TransitionOnboarding(ctx context.Context, o domain.Onboarding, next domain.OperationState) (bool, error) {
	if !domain.CanTransitionOperation(o.State, next) {
		return false, domain.ErrInvalidPayoutState
	}
	n, err := s.q(ctx).TransitionRocketworkOnboarding(ctx, sqlc.TransitionRocketworkOnboardingParams{Environment: o.Scope.Environment, AccountKey: o.Scope.Account, OrganizationID: o.OrganizationID, Action: o.Action, PreviousState: string(o.State), NextState: string(next), Result: o.Result, CorrelationID: o.CorrelationID, UpdatedAt: o.UpdatedAt})
	if err != nil {
		return false, s.workflowError(ctx, "transition_onboarding", err)
	}
	if n == 1 {
		s.log.InfoContext(ctx, "rocketwork onboarding transitioned", "organization_id", o.OrganizationID, "action", o.Action, "from", o.State, "to", next)
	}
	return n == 1, nil
}
