package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
)

const KindOnboardExecutor = "onboard_rocketwork_executor"

type OnboardingPayload struct {
	OrganizationID uuid.UUID `json:"organization_id"`
	Action         string    `json:"action"`
}

// RequestOnboarding queues an explicitly selected action. Repeated requests
// retain the same checkpoint; FNS/SMS operations never get a fresh retry key.
func (s *Service) RequestOnboarding(ctx context.Context, org uuid.UUID, action string, names domain.RegistrationNames) error {
	if s.Config.ProviderScope == nil || !s.Config.ProviderEnabled {
		return domain.ErrProviderDisabled
	}
	store, ok := s.Store.(domain.OnboardingStore)
	if !ok {
		return domain.ErrProviderDisabled
	}
	payee, err := s.Organizations.ForPayout(ctx, org)
	if err != nil {
		return err
	}
	if !domain.ContractorRequired(payee.LegalType) {
		return domain.ErrContractorNotRequired
	}
	if (action == "register" || action == "update") && payee.LegalType != domain.SoleProprietor {
		return domain.ErrInvalidPayout
	}
	input := domain.OnboardingInput{INN: payee.INN, Phone: payee.Phone, Names: names}
	if action == "update" || action == "rebind" {
		ws, ok := s.Store.(domain.WorkflowStore)
		if !ok {
			return domain.ErrProviderDisabled
		}
		b, err := ws.ExecutorBinding(ctx, *s.Config.ProviderScope, org)
		if err != nil {
			return err
		}
		input.ExecutorID = b.ExecutorID
	}
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		if _, _, err := s.Store.InsertContractor(ctx, domain.NewContractor(ids.New(), org, s.Clock.Now())); err != nil {
			return err
		}
		_, err := store.PrepareOnboarding(ctx, domain.Onboarding{Scope: *s.Config.ProviderScope, OrganizationID: org, Action: action, Input: input, CorrelationID: ids.New(), CreatedAt: s.Clock.Now()})
		if err != nil {
			return err
		}
		return s.Jobs.Enqueue(ctx, job.Job{Kind: KindOnboardExecutor, DedupeKey: KindOnboardExecutor + ":" + org.String() + ":" + action, Payload: OnboardingPayload{org, action}, MaxAttempts: EnsureAttempts})
	})
}

func (s *Service) RunOnboarding(ctx context.Context, org uuid.UUID, action string) error {
	if !s.Config.ProviderEnabled || s.Config.ProviderScope == nil {
		return domain.ErrProviderDisabled
	}
	store, ok := s.Store.(domain.OnboardingStore)
	if !ok {
		return domain.ErrProviderDisabled
	}
	provider, ok := s.Provider.(domain.OnboardingProvider)
	if !ok {
		return domain.ErrProviderDisabled
	}
	o, err := store.Onboarding(ctx, *s.Config.ProviderScope, org, action)
	if err != nil {
		return err
	}
	c, err := s.Store.ContractorByOrg(ctx, org, false)
	if err != nil {
		return err
	}
	if o.State == domain.OperationConfirmed {
		return s.schedulePoll(ctx, c)
	}
	if o.State == domain.OperationRejected {
		return job.Permanent(domain.ErrInvalidPayoutState)
	}
	if o.State == domain.OperationInFlight || o.State == domain.OperationUncertain {
		// A successful lookup establishes registration/invitation completion,
		// but cannot prove profile edits or a new permissions request succeeded.
		pc, readErr := s.resolveContractor(ctx, c)
		if readErr == nil && (action == "invite" || action == "register" || (action == "rebind" && pc.Status == domain.ContractorActive)) {
			o.Result = "verified_by_lookup"
			o.UpdatedAt = s.Clock.Now()
			if _, err := store.TransitionOnboarding(ctx, o, domain.OperationConfirmed); err != nil {
				return err
			}
			_, err = s.applyContractor(ctx, c.ID, pc)
			return err
		}
		s.log.WarnContext(ctx, "rocketwork onboarding requires reconciliation", "organization_id", org, "action", action)
		return job.Postpone(fmt.Errorf("onboarding outcome uncertain"), time.Hour)
	}
	// Verify the immutable contact snapshot still belongs to this organization.
	payee, err := s.Organizations.ForPayout(ctx, org)
	if err != nil {
		return err
	}
	if payee.INN != o.Input.INN || payee.Phone != o.Input.Phone {
		return domain.ErrInvalidPayout
	}
	o.UpdatedAt = s.Clock.Now()
	claimed := false
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		claimed, err = store.TransitionOnboarding(ctx, o, domain.OperationInFlight)
		return err
	})
	if err != nil {
		return err
	}
	if !claimed {
		return job.Postpone(domain.ErrConcurrentUpdate, s.Config.PollFast)
	}
	result, callErr := provider.ExecuteOnboarding(ctx, action, o.Input)
	o.State = domain.OperationInFlight
	o.UpdatedAt = s.Clock.Now()
	o.Result = result
	next := domain.OperationConfirmed
	if callErr != nil {
		next = domain.OperationUncertain
		o.Result = "unknown"
		if _, rejected := domain.AsRejected(callErr); rejected {
			next = domain.OperationRejected
			o.Result = "rejected"
		}
	}
	// Persist even if the job context was canceled after the provider accepted.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := store.TransitionOnboarding(saveCtx, o, next); err != nil {
		return err
	}
	if callErr != nil {
		if next == domain.OperationRejected {
			return job.Permanent(callErr)
		}
		return job.Postpone(fmt.Errorf("onboarding outcome uncertain"), time.Hour)
	}
	return s.schedulePoll(ctx, c)
}

func (s *Service) ensureVerifiedContractor(ctx context.Context, c domain.Contractor) error {
	pc, err := s.resolveContractor(ctx, c)
	if err == nil {
		_, err = s.applyContractor(ctx, c.ID, pc)
		return err
	}
	if !errors.Is(err, domain.ErrProviderNotFound) {
		return err
	}
	// Self-registration via the provider is valid for both supported legal
	// forms; direct entrepreneur registration uses explicit structured names.
	if err := s.RequestOnboarding(ctx, c.OrganizationID, "invite", domain.RegistrationNames{}); err != nil {
		return err
	}
	return nil
}
