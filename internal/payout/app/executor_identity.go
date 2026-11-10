package app

import (
	"context"
	"errors"
	"strconv"

	"github.com/leha-creator/chanly/internal/payout/domain"
)

// resolveContractor performs only a read. A missing executor never sends an
// invitation or updates provider data; onboarding owns those separate actions.
func (s *Service) resolveContractor(ctx context.Context, c domain.Contractor) (domain.ProviderContractor, error) {
	store, ok := s.Store.(domain.WorkflowStore)
	if !ok || s.Config.ProviderScope == nil {
		return domain.ProviderContractor{}, domain.ErrProviderDisabled
	}
	reader, ok := s.Provider.(domain.ExecutorReader)
	if !ok {
		return domain.ProviderContractor{}, domain.ErrProviderDisabled
	}
	payee, err := s.Organizations.ForPayout(ctx, c.OrganizationID)
	if err != nil {
		return domain.ProviderContractor{}, err
	}
	kind := "selfemployed"
	if payee.LegalType == domain.SoleProprietor {
		kind = "entrepreneur"
	}
	r := domain.ExecutorLookup{OrganizationID: c.OrganizationID, LookupKind: "inn", LookupValue: payee.INN, INN: payee.INN, Phone: payee.Phone, EmploymentKind: kind}
	b, err := store.ExecutorBinding(ctx, *s.Config.ProviderScope, c.OrganizationID)
	if err == nil {
		if b.LookupKind != "inn" || b.LookupValue != payee.INN || b.EmploymentKind != kind {
			return domain.ProviderContractor{}, domain.ErrInvalidPayout
		}
		r.ExpectedID = b.ExecutorID
	} else if !errors.Is(err, domain.ErrNotFound) {
		return domain.ProviderContractor{}, err
	}
	resolved, err := reader.ResolveExecutor(ctx, r)
	if err != nil {
		return domain.ProviderContractor{}, err
	}
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		_, err := store.BindExecutor(ctx, domain.ExecutorBinding{Scope: *s.Config.ProviderScope, OrganizationID: c.OrganizationID, ExecutorID: resolved.ExecutorID, LookupKind: r.LookupKind, LookupValue: r.LookupValue, EmploymentKind: kind, VerifiedAt: s.Clock.Now()})
		return err
	})
	if err != nil {
		return domain.ProviderContractor{}, err
	}
	s.log.InfoContext(ctx, "rocketwork executor resolved", "organization_id", c.OrganizationID, "status", resolved.Status, "reason", resolved.Reason)
	return domain.ProviderContractor{ExternalID: strconv.FormatInt(resolved.ExecutorID, 10), Status: resolved.Status, BlockedReason: resolved.Reason}, nil
}
